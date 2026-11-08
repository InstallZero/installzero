package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentinstall"
	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/policy"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
	"github.com/InstallZero/installzero/internal/ui"
)

// installReport is one install run, written the same way by the step
// runner and by --agent so the two can be compared.
type installReport struct {
	Mode    string              `json:"mode"` // deterministic or agent
	Model   string              `json:"model,omitempty"`
	Name    string              `json:"name"`
	Target  string              `json:"target"`
	Dir     string              `json:"dir"`
	Machine installmd.Machine   `json:"machine"`
	Started time.Time           `json:"started"`
	Seconds float64             `json:"seconds"`
	OK      bool                `json:"ok"`
	Steps   []runner.StepResult `json:"steps"`
	// Blocks counts the blocks and agent commands run (detect, install,
	// verify, run_change); Fixes the fixes the step runner applied.
	Blocks          int                  `json:"blocks"`
	ApprovalPrompts int                  `json:"approval_prompts"`
	Fixes           int                  `json:"fixes"`
	Agent           *agentinstall.Result `json:"agent,omitempty"`
	Transcript      string               `json:"transcript,omitempty"`
}

// writeInstallReport writes the report to path, or under the state folder
// when path is empty, and returns where it went.
func writeInstallReport(path string, r installReport) (string, error) {
	if path == "" {
		sd, err := state.Dir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(sd, "reports", fmt.Sprintf("%s-%s-%s.json", r.Started.Format("20060102-150405"), r.Name, r.Mode))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, writeJSON(path, r)
}

// startAgentModel connects to the model --agent names: provider:model-id,
// a .gguf, "local" for the default model, or a server URL.
func startAgentModel(ctx context.Context, spec string, trail io.Writer) (*agentloop.Session, func(), error) {
	o := agentloop.SessionOptions{Trail: trail, Download: true}
	switch {
	case strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://"):
		o.URL = spec
	case spec == "local":
	case strings.HasSuffix(strings.ToLower(spec), ".gguf"):
		o.Model = spec
	case strings.Contains(spec, ":"):
		o.Remote = spec
	default:
		return nil, func() {}, fmt.Errorf("--agent %s: want provider:model-id (e.g. google:gemini-4-argon), a .gguf, local, or a server URL", spec)
	}
	if root, err := findRoot(); err == nil {
		o.Root, o.EnvFiles = root, []string{filepath.Join(root, ".env")}
	} else if o.Remote == "" && o.URL == "" {
		return nil, func() {}, err
	}
	return agentloop.StartSession(ctx, o)
}

// cmdCompare shows install reports side by side.
func cmdCompare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr, "compare <report.json>... (written by install, --report or under the state folder's reports)")
		return 2
	}
	var reps []installReport
	for _, p := range args {
		var r installReport
		data, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(data, &r)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", p, err)
			return 1
		}
		reps = append(reps, r)
	}
	rows := [][]string{{""}}
	add := func(label string, f func(r installReport) string) {
		row := []string{label}
		for _, r := range reps {
			row = append(row, f(r))
		}
		rows = append(rows, row)
	}
	for i := range reps {
		rows[0] = append(rows[0], fmt.Sprintf("run %d", i+1))
	}
	add("mode", func(r installReport) string { return r.Mode })
	add("model", func(r installReport) string { return or(r.Model, "-") })
	add("installed", func(r installReport) string { return map[bool]string{true: "yes", false: "no"}[r.OK] })
	add("time", func(r installReport) string { return ui.Elapsed(time.Duration(r.Seconds * float64(time.Second))) })
	add("blocks and commands run", func(r installReport) string { return fmt.Sprint(r.Blocks) })
	add("approval prompts", func(r installReport) string { return fmt.Sprint(r.ApprovalPrompts) })
	add("runner fixes", func(r installReport) string { return fmt.Sprint(r.Fixes) })
	add("agent said", func(r installReport) string {
		if r.Agent == nil {
			return "-"
		}
		return r.Agent.Status
	})
	add("model turns", func(r installReport) string {
		if r.Agent == nil || r.Agent.Run == nil {
			return "0"
		}
		return fmt.Sprint(r.Agent.Run.Steps)
	})
	add("tokens (prompt/completion)", func(r installReport) string {
		if r.Agent == nil || r.Agent.Run == nil {
			return "0"
		}
		return fmt.Sprintf("%d/%d", r.Agent.Run.PromptTokens, r.Agent.Run.CompletionTokens)
	})
	var ids []string
	seen := map[string]bool{}
	for _, r := range reps {
		for _, s := range r.Steps {
			if !seen[s.ID] {
				seen[s.ID] = true
				ids = append(ids, s.ID)
			}
		}
	}
	for _, id := range ids {
		add("step "+id, func(r installReport) string {
			for _, s := range r.Steps {
				if s.ID == id {
					return string(s.Status)
				}
			}
			return "-"
		})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], len(c))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, c := range row {
			fmt.Fprintf(&b, "%-*s  ", widths[i], c)
		}
		fmt.Fprintln(stdout, strings.TrimRight(b.String(), " "))
	}
	return 0
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func printAgentSummary(w io.Writer, t *ui.Term, r *agentinstall.Result) {
	fmt.Fprintf(w, "\n%s %s\n", t.Paint("The agent says:", ui.Bold), r.Status)
	if r.Summary != "" {
		fmt.Fprintf(w, "  %s\n", r.Summary)
	}
	if r.BlockedOn != "" {
		fmt.Fprintf(w, "  %s %s\n", t.Paint("Needs you:", ui.Yellow), r.BlockedOn)
	}
	sys, refused := 0, 0
	for _, c := range r.Commands {
		switch c.Tier {
		case policy.System:
			sys++
		case policy.Never:
			refused++
		}
	}
	if run := r.Run; run != nil {
		fmt.Fprintf(w, "  %d model turns, %d tool calls, %d prompt and %d completion tokens", run.Steps, run.ToolCalls, run.PromptTokens, run.CompletionTokens)
		if run.StopReason != "" {
			fmt.Fprintf(w, ", stopped: %s", run.StopReason)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  %d commands (%d machine-wide, %d refused as destructive)\n", len(r.Commands), sys, refused)
}
