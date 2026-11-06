package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/psrx"
	"github.com/InstallZero/installzero/internal/readonly"
	"github.com/InstallZero/installzero/prompts"
)

// DraftStats is how the last saved draft did: its format problems and its
// detect/verify blocks run on the target.
type DraftStats struct {
	Steps    int `json:"steps"`
	Problems int `json:"problems"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Untested int `json:"untested"`
}

type bestDraft struct {
	score   int
	content string
	stats   *DraftStats
}

var (
	exitCode    = psrx.MustCompile(`^\[exit (\d+)\]`)
	lastSegment = psrx.MustCompile(`/[^/]+$`)
	docLike     = psrx.MustCompile(`(?ms)^name\s*:.*^##\s+Step\s+\d+`)
)

func (p *probe) saveDraft(content string) error {
	if err := os.MkdirAll(filepath.Dir(p.out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.out, []byte(strings.ReplaceAll(content, "\r\n", "\n")), 0o644)
}

func (p *probe) saved() bool {
	_, err := os.Stat(p.out)
	return err == nil
}

// placeholders are what {{INSTALL_DIR}} and {{REPO_DIR}} stand for on the
// target.
func (p *probe) placeholders(name string) map[string]string {
	if p.linuxDir == "" {
		return map[string]string{"INSTALL_DIR": "/opt", "REPO_DIR": "/opt/" + name}
	}
	dir := lastSegment.Replace(p.linuxDir, "")
	if dir == "" {
		dir = "/"
	}
	return map[string]string{"INSTALL_DIR": dir, "REPO_DIR": p.linuxDir}
}

// checkable is Test-ProbeCheck on Linux: read-only bash is run, anything
// else is reported untested.
func checkable(code, shell string) (bool, string) {
	switch strings.ToLower(shell) {
	case "", "bash", "sh":
	default:
		return false, shell + " blocks are not run on this Linux machine"
	}
	if why := readonly.Bash(code); why != "" {
		return false, "not read-only (" + why + "), so not run here (it may still be right)"
	}
	return true, ""
}

// draftCheck is Invoke-DraftCheck: saves a draft, then runs its steps'
// detect and verify checks on the target, where the project is installed,
// so they should pass.
func (p *probe) draftCheck(ctx context.Context, content string) string {
	if err := p.saveDraft(content); err != nil {
		return "error: " + err.Error()
	}
	doc := installmd.Parse(strings.ReplaceAll(content, "\r\n", "\n"))
	problems := doc.Problems()
	name, _ := doc.Meta.Get("name")
	if name == "" {
		name = "project"
	}
	ph := p.placeholders(name)
	var lines []string
	format := " Format OK."
	if len(problems) > 0 {
		format = " Format problems: " + strings.Join(problems, "; ")
	}
	lines = append(lines, fmt.Sprintf("Saved %s: %d steps.%s", p.out, len(doc.Steps), format))
	lines = append(lines, fmt.Sprintf("Checks run here, where the project is installed (detect and verify should both pass; {{INSTALL_DIR}}=%s, {{REPO_DIR}}=%s):", ph["INSTALL_DIR"], ph["REPO_DIR"]))
	stats := &DraftStats{Steps: len(doc.Steps), Problems: len(problems)}
	p.stats = stats
	for _, s := range doc.Steps {
		for _, k := range []string{"detect", "verify"} {
			b := s.Select(k, p.machine)
			if b == nil {
				if k == "verify" {
					lines = append(lines, fmt.Sprintf("  %s %s: no block for this machine", s.ID, k))
				}
				continue
			}
			code := installmd.Expand(b.Code, ph)
			if ok, why := checkable(code, b.Shell); !ok {
				stats.Untested++
				lines = append(lines, fmt.Sprintf("  %s %s: untested - %s", s.ID, k, why))
				continue
			}
			r := p.target.Bash(ctx, code, 60*time.Second, ph["REPO_DIR"]).Format(0)
			exit := 1
			if g := exitCode.Groups(r); g != nil {
				if n, err := strconv.Atoi(g[1]); err == nil {
					exit = n
				}
			}
			var rest []string
			for i, l := range strings.Split(r, "\n") {
				if i > 0 && strings.TrimSpace(l) != "" {
					rest = append(rest, l)
				}
			}
			verdict := fmt.Sprintf("FAIL (exit %d)", exit)
			if exit == 0 {
				stats.Passed++
				verdict = "PASS"
			} else {
				stats.Failed++
			}
			lines = append(lines, fmt.Sprintf("  %s %s: %s - %s", s.ID, k, verdict, agentloop.Brief(strings.Join(rest, " "), 200)))
		}
	}
	// Later drafts can regress; keep the best one seen (format problems weigh most).
	score := stats.Passed - stats.Failed - 5*stats.Problems + stats.Steps
	if p.best == nil || score >= p.best.score {
		p.best = &bestDraft{score: score, content: content, stats: stats}
	}
	return strings.Join(lines, "\n")
}

// documentIn finds an install.md written as a message: from its front
// matter on, when it has a name and a step.
func documentIn(text string) (string, bool) {
	start := strings.Index(text, "---\n")
	if start < 0 {
		start = strings.Index(text, "---\r\n")
	}
	if start < 0 || !docLike.Match(text[start:]) {
		return "", false
	}
	return text[start:], true
}

func message(name string, v prompts.Values) string {
	m, err := prompts.Message(name, v)
	if err != nil {
		panic(err)
	}
	return m
}

// finishGuard sends the model back when it finishes without a saved
// draft, or (once) with format problems in it.
func (p *probe) finishGuard(ctx context.Context, text string) string {
	if p.saved() {
		s := p.stats
		if p.formatPushedBack || s == nil || (s.Steps > 0 && s.Problems == 0) {
			return ""
		}
		p.formatPushedBack = true
		var problems []string
		if data, err := os.ReadFile(p.out); err == nil {
			problems = installmd.Parse(string(data)).Problems()
		}
		return message("probe_format_problems", prompts.Values{"problems": strings.Join(problems, "; ")})
	}
	if doc, ok := documentIn(text); ok {
		p.savedFromText = text
		return message("probe_saved_at_finish", prompts.Values{"checks": p.draftCheck(ctx, doc)})
	}
	return message("probe_not_saved", prompts.Values{})
}

// checkpoint saves a document written as a plain message like a
// write_install_md call, and reminds the model once, halfway, to save a
// draft.
func (p *probe) checkpoint(ctx context.Context, run *agentloop.Run) string {
	if text := run.LastLongText; text != "" && text != p.savedFromText {
		if doc, ok := documentIn(text); ok {
			p.savedFromText = text
			return message("probe_saved_from_message", prompts.Values{"checks": p.draftCheck(ctx, doc)})
		}
	}
	if p.draftReminded || p.saved() || run.Steps < p.maxTurns/2 {
		return ""
	}
	p.draftReminded = true
	return message("probe_halfway", prompts.Values{"turns": run.Steps, "max_turns": p.maxTurns})
}
