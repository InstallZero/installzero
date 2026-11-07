// Package probe works backwards from a running install to an install.md the
// installer agent can replay elsewhere. Read-only: the model inventories
// what runs, works out how it was installed and writes an install.md; each
// draft's detect and verify checks are run on the target, where the
// project is installed, and failures go back to the model. Port of
// agent/Probe.ps1 for Linux targets.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/secrets"
	"github.com/InstallZero/installzero/internal/target"
	"github.com/InstallZero/installzero/prompts"
)

// PromptVersion changes whenever prompts/probe.md does.
const PromptVersion = "probe-5"

// Options are Probe.ps1's parameters.
type Options struct {
	// Root is the InstallZero folder (the install.md spec, model assets,
	// .env with API keys).
	Root   string
	Target target.Target
	// Where is -Target: a folder on the target (the project's checkout), a
	// URL or port it serves on, or a name; "" lets the model look.
	Where string
	Out   string
	// Draft and AgentReport refine a draft with what a replay on a clean
	// machine showed.
	Draft, AgentReport string
	NoDocs             bool
	// Hide are test-harness folders and files the probe must not see
	// (comma-separated lists allowed; %VARS% expanded).
	Hide                        []string
	Model, Backend, Remote, URL string
	Context                     int // largest context for a local model; 32768 when 0
	MaxTurns                    int // 40 when 0
	Timeout                     time.Duration
	ResultFile                  string
	// Guidance is a lesson set {id, text} added to the system prompt.
	Guidance string
	// Download allows fetching a missing llama.cpp build.
	Download bool
	// Seed is sent with every request to a llama-server; 0 for none.
	Seed int
	// Log receives progress lines; nil for none.
	Log io.Writer
}

// Result is what a run wrote and how it went (schemas/probe-result-1.json).
type Result struct {
	RunID         string      `json:"run_id"`
	Target        string      `json:"target"`
	TargetEnv     string      `json:"target_env"`
	NoDocs        bool        `json:"no_docs"`
	Out           string      `json:"out"`
	Model         string      `json:"model"`
	Context       int         `json:"context"`
	PromptVersion string      `json:"prompt_version"`
	Blind         Blind       `json:"blind"`
	Saved         bool        `json:"saved"`
	Finished      bool        `json:"finished"`
	StopReason    string      `json:"stop_reason"`
	Summary       string      `json:"summary"`
	Gaps          string      `json:"gaps"`
	Confidence    string      `json:"confidence"`
	DraftChecks   *DraftStats `json:"draft_checks"`
	Stats         Stats       `json:"stats"`
	Transcript    string      `json:"transcript"`
}

// Blind is how the probe was kept from seeing the original install files.
type Blind struct {
	HiddenPaths    []string `json:"hidden_paths"`
	HiddenAttempts int      `json:"hidden_attempts"`
	WithheldLines  int      `json:"withheld_lines"`
	GuardedLines   int      `json:"guarded_lines"`
}

// Stats are the loop's counters.
type Stats struct {
	ToolCalls        int     `json:"tool_calls"`
	ModelTurns       int     `json:"model_turns"`
	InvalidCalls     int     `json:"invalid_calls"`
	Repeats          int     `json:"repeats"`
	Nudges           int     `json:"nudges"`
	Seconds          float64 `json:"seconds"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ServerErrors     int     `json:"server_errors"`
	Error            string  `json:"error"`
}

// Prompt is what the model is sent first (Probe.ps1 -PromptOut).
type Prompt struct {
	PromptVersion string            `json:"prompt_version"`
	System        string            `json:"system"`
	User          string            `json:"user"`
	Tools         []json.RawMessage `json:"tools"`
}

type probe struct {
	o        Options
	target   target.Target
	out      string
	linuxDir string
	maxTurns int
	machine  installmd.Machine
	blind    *blind
	known    []secrets.Value

	stats            *DraftStats
	best             *bestDraft
	savedFromText    string
	draftReminded    bool
	formatPushedBack bool
}

func newProbe(o Options) (*probe, error) {
	if o.Target == nil {
		return nil, errors.New("the probe needs a target (a WSL distro)")
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = 40
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.Context == 0 {
		o.Context = 32768
	}
	if o.Out == "" {
		cwd, _ := os.Getwd()
		o.Out = filepath.Join(cwd, "probe-"+time.Now().Format("20060102-150405")+".install.md")
	}
	out, err := filepath.Abs(o.Out)
	if err != nil {
		return nil, err
	}
	p := &probe{o: o, target: o.Target, out: out, maxTurns: o.MaxTurns}
	if strings.HasPrefix(o.Where, "/") {
		p.linuxDir = strings.TrimRight(o.Where, "/")
	}
	p.blind = newBlind(o.Root, out, o.Hide, true)
	return p, nil
}

// Prompts builds the system prompt, first user message and tools; the
// machine profile is read from the target.
func Prompts(ctx context.Context, o Options) (*Prompt, error) {
	p, err := newProbe(o)
	if err != nil {
		return nil, err
	}
	p.machine = p.target.Profile(ctx)
	return p.prompt()
}

func (p *probe) prompt() (*Prompt, error) {
	tools, err := Tools()
	if err != nil {
		return nil, err
	}
	spec, err := os.ReadFile(filepath.Join(p.o.Root, "agent", "INSTALL-MD.md"))
	if err != nil {
		return nil, err
	}
	m := p.machine
	system, err := prompts.Prompt("probe", prompts.Values{
		"os_word": "Linux", "linux": true, "os_version": m.OSVersion, "arch": m.Arch, "gpu": m.GPU, "gpus": m.GPUs,
		"driver": m.Driver, "ram_gb": prompts.Text(m.RAMGB), "half_turns": p.maxTurns / 2, "max_turns": p.maxTurns, "spec": string(spec),
	})
	if err != nil {
		return nil, err
	}
	version := PromptVersion
	if p.o.Guidance != "" {
		data, err := os.ReadFile(p.o.Guidance)
		if err != nil {
			return nil, err
		}
		var g struct {
			ID   any    `json:"id"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\ufeff")), &g); err != nil {
			return nil, fmt.Errorf("%s: %w", p.o.Guidance, err)
		}
		if strings.TrimSpace(g.Text) != "" {
			extra, err := prompts.Message("probe_guidance", prompts.Values{"text": g.Text})
			if err != nil {
				return nil, err
			}
			system += extra
			version = PromptVersion + "+" + prompts.Text(jsonScalar(g.ID))
		}
	}
	var user string
	if p.o.Draft != "" && p.o.AgentReport != "" {
		user, err = p.refineMessage()
	} else {
		user, err = prompts.Message("probe_user", prompts.Values{"target": p.o.Where, "target_here": p.linuxDir != ""})
	}
	if err != nil {
		return nil, err
	}
	return &Prompt{PromptVersion: version, System: system, User: user, Tools: tools}, nil
}

// jsonScalar turns a decoded JSON number into an int when it is one, so it
// prints as PowerShell would.
func jsonScalar(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}

// refineMessage asks for a draft to be fixed with what a replay showed.
func (p *probe) refineMessage() (string, error) {
	data, err := os.ReadFile(p.o.AgentReport)
	if err != nil {
		return "", err
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	var rep struct {
		Machine struct {
			OSVersion any `json:"os_version"`
			GPU       any `json:"gpu"`
		} `json:"machine"`
		Status    any             `json:"status"`
		BlockedOn any             `json:"blocked_on"`
		Summary   any             `json:"summary"`
		Steps     json.RawMessage `json:"steps"`
		Commands  []struct {
			Origin  any `json:"origin"`
			Exit    any `json:"exit"`
			Command any `json:"command"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return "", fmt.Errorf("%s: %w", p.o.AgentReport, err)
	}
	var failed []string
	for _, c := range rep.Commands {
		if e, ok := c.Exit.(float64); ok && e != 0 {
			failed = append(failed, fmt.Sprintf("- %s [exit %s]: %s", prompts.Text(c.Origin), prompts.Text(jsonScalar(c.Exit)), agentloop.Brief(prompts.Text(c.Command), 200)))
		}
	}
	if len(failed) > 8 {
		failed = failed[len(failed)-8:]
	}
	var steps []string
	if len(rep.Steps) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rep.Steps))
		if t, err := dec.Token(); err == nil && t == json.Delim('{') {
			for dec.More() {
				k, _ := dec.Token()
				var v any
				if dec.Decode(&v) != nil {
					break
				}
				steps = append(steps, fmt.Sprintf("%v=%s", k, prompts.Text(jsonScalar(v))))
			}
		}
	}
	draft, err := os.ReadFile(p.o.Draft)
	if err != nil {
		return "", err
	}
	return prompts.Message("probe_refine", prompts.Values{
		"os_version": prompts.Text(rep.Machine.OSVersion), "gpu": prompts.Text(rep.Machine.GPU), "status": prompts.Text(rep.Status),
		"steps": strings.Join(steps, ", "), "blocked_on": prompts.Text(rep.BlockedOn), "failed": strings.Join(failed, "\n"),
		"summary": prompts.Text(rep.Summary), "draft": string(draft),
	})
}

func (p *probe) line(format string, args ...any) {
	if p.o.Log != nil {
		fmt.Fprintf(p.o.Log, format+"\n", args...)
	}
}

// LogDir is where transcripts and results go by default.
func LogDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserCacheDir()
	}
	return filepath.Join(base, "installzero", "probe")
}

// Run probes the target and writes the install.md and the result file.
func Run(ctx context.Context, o Options) (*Result, error) {
	p, err := newProbe(o)
	if err != nil {
		return nil, err
	}
	o = p.o
	p.known = secrets.Known(o.Root, os.Environ())
	p.blind.guard(ctx, func(ctx context.Context, script string, timeout time.Duration) string {
		return p.target.Bash(ctx, script, timeout, "").Out
	}, p.linuxDir)
	p.machine = p.target.Profile(ctx)
	pr, err := p.prompt()
	if err != nil {
		return nil, err
	}

	runID := fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid())
	logDir := LogDir()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	transcript := filepath.Join(logDir, runID+".jsonl")
	of := ""
	if o.Where != "" {
		of = " of " + o.Where
	}
	p.line("InstallZero probe%s -> %s", of, p.out)

	session, stop, err := agentloop.StartSession(ctx, agentloop.SessionOptions{
		Model: o.Model, Backend: o.Backend, Remote: o.Remote, URL: o.URL, Context: o.Context,
		Root: o.Root, EnvFiles: []string{filepath.Join(o.Root, ".env")}, Download: o.Download, Trail: o.Log, Seed: o.Seed,
	})
	if err != nil {
		return nil, err
	}
	defer stop()
	run := agentloop.Loop(ctx, session, agentloop.Options{
		System: pr.System, User: pr.User, Tools: pr.Tools,
		Dispatch: func(name string, a *agentloop.Args) (string, error) { return p.dispatch(ctx, name, a) },
		MaxTurns: o.MaxTurns, Timeout: o.Timeout, Transcript: transcript, MaxTokens: 8192,
		FinishGuard: func(_ *agentloop.Args, text string) string { return p.finishGuard(ctx, text) },
		Checkpoint:  func(r *agentloop.Run) string { return p.checkpoint(ctx, r) },
		FinalTools:  []string{"write_install_md"}, ResultSaved: p.saved,
		FinalNudge:         message("probe_final", prompts.Values{}),
		ParseFailureNudge:  message("probe_parse_failure", prompts.Values{}),
		UnchangedWriteHint: message("probe_unchanged_hint", prompts.Values{}),
		Trail:              o.Log,
	})
	// Last resort: a document the model wrote as a message is still the result.
	if !p.saved() && run.LastLongText != "" {
		if doc, ok := documentIn(run.LastLongText); ok {
			p.line("The model wrote the install.md as a message without saving it; saving it now.")
			p.draftCheck(ctx, doc)
		}
	}
	if p.best != nil && p.stats != nil && p.best.stats != p.stats {
		p.line("The last draft was worse than an earlier one; keeping the earlier draft.")
		if err := p.saveDraft(p.best.content); err != nil {
			return nil, err
		}
		p.stats = p.best.stats
	}
	stop()

	res := &Result{
		RunID: runID, Target: o.Where, TargetEnv: p.target.Name(), NoDocs: o.NoDocs, Model: session.Label, Context: session.Context, PromptVersion: pr.PromptVersion,
		Blind:    Blind{HiddenPaths: append([]string{}, p.blind.paths...), HiddenAttempts: p.blind.attempts, WithheldLines: p.blind.withheld, GuardedLines: len(p.blind.leak)},
		Saved:    p.saved(),
		Finished: run.Finished, StopReason: run.StopReason, DraftChecks: p.stats,
		Stats: Stats{ToolCalls: run.ToolCalls, ModelTurns: run.Steps, InvalidCalls: run.InvalidCalls, Repeats: run.Repeats, Nudges: run.Nudges, Seconds: run.Seconds,
			PromptTokens: run.PromptTokens, CompletionTokens: run.CompletionTokens, ServerErrors: run.ServerErrors, Error: run.Error},
		Transcript: transcript,
	}
	if res.Saved {
		res.Out = p.out
	}
	if run.Finish != nil {
		res.Summary, res.Gaps, res.Confidence = run.Finish.String("summary"), run.Finish.String("gaps"), run.Finish.String("confidence")
	}
	resultFile := o.ResultFile
	if resultFile == "" {
		resultFile = filepath.Join(logDir, runID+".result.json")
	}
	body, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(resultFile, append(body, '\n'), 0o644); err != nil {
		return nil, err
	}

	p.line("")
	if res.Saved {
		checks := ""
		if s := p.stats; s != nil {
			checks = fmt.Sprintf("%d steps; checks here: %d passed, %d failed, %d untested", s.Steps, s.Passed, s.Failed, s.Untested)
		}
		p.line("install.md: %s (%s)", p.out, checks)
	} else {
		p.line("No install.md was written.")
	}
	if res.Gaps != "" {
		p.line("Gaps: %s", res.Gaps)
	}
	p.line("Blind to install files: %d attempt(s) to reach hidden files refused, %d line(s) of install-file content withheld (%d guarded lines).", p.blind.attempts, p.blind.withheld, len(p.blind.leak))
	p.line("Result: %s", resultFile)
	return res, nil
}
