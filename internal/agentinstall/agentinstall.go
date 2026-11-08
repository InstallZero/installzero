// Package agentinstall is installzero install --agent: a model carries out
// an install.md's plan itself, running the plan's blocks, its own commands
// and read-only investigations, under the same tiers, approvals and install
// record as the step runner (runner.Run), which needs no model. Afterwards
// the steps' verify blocks, not the model, decide what is installed.
package agentinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/policy"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/secrets"
	"github.com/InstallZero/installzero/prompts"
)

// Options configure an agentic install.
type Options struct {
	// Runner is how blocks and commands run, as for runner.Run: Exec, Read
	// (quiet runs for the model's investigations and the final checks), Dir,
	// Env, Skip, Tracker and Events are used.
	Runner  runner.Options
	Session *agentloop.Session
	// Remote says the target is not this machine (WSL): files are read
	// there with bash.
	Remote bool
	// Ask asks the user to allow a machine-wide command; Yes allows them
	// all without asking.
	Ask func(question, code string) bool
	Yes bool
	// MaxTurns (120 when 0) and Timeout (4 hours when 0) bound the model;
	// CommandTimeout (60 minutes when 0) bounds one of its commands.
	MaxTurns       int
	Timeout        time.Duration
	CommandTimeout time.Duration
	Transcript     string
	Trail          io.Writer
}

// Command is one command run for the model: a block of the plan or its own.
type Command struct {
	Time     time.Time `json:"time"`
	Origin   string    `json:"origin"` // <step>/<block>, or "model"
	Shell    string    `json:"shell"`
	Command  string    `json:"command"`
	Tier     string    `json:"tier"`
	Reason   string    `json:"reason"`
	Approved bool      `json:"approved"`
	Exit     *int      `json:"exit,omitempty"`
	Seconds  float64   `json:"seconds"`
}

// Result is an agentic install's outcome.
type Result struct {
	// Status, Summary and BlockedOn are what the model said when it
	// finished; Status is "unfinished" when it did not.
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	BlockedOn string `json:"blocked_on,omitempty"`
	// Steps are judged by their verify blocks after the run; each Reason
	// says what the model reported.
	Steps    []runner.StepResult `json:"steps"`
	OK       bool                `json:"ok"`
	Commands []Command           `json:"commands"`
	Asked    int                 `json:"approval_prompts"`
	Run      *agentloop.Run      `json:"run"`
}

var statuses = map[string]runner.Status{"done": runner.Done, "already-done": runner.AlreadyDone, "skipped": runner.Skipped, "failed": runner.Failed}

type agent struct {
	o        Options
	plan     *runner.Plan
	windows  bool
	steps    map[string]*runner.Step
	reported map[string]string
	evidence map[string]string
	allowed  map[string]bool // steps and commands the user allowed
	declined map[string]bool
	current  *runner.Step // the step the model works on
	window   *runner.Step // the step whose changes are being recorded
	outside  *runner.Step // stands for the model's changes before any step
	known    []secrets.Value
	res      *Result
}

func newAgent(plan *runner.Plan, o Options) *agent {
	if o.MaxTurns <= 0 {
		o.MaxTurns = 120
	}
	if o.Timeout <= 0 {
		o.Timeout = 4 * time.Hour
	}
	if o.CommandTimeout <= 0 {
		o.CommandTimeout = time.Hour
	}
	if o.Runner.Read == nil {
		o.Runner.Read = o.Runner.Exec
	}
	a := &agent{
		o: o, plan: plan, windows: strings.EqualFold(o.Runner.Machine.OS, "windows"),
		steps: map[string]*runner.Step{}, reported: map[string]string{}, evidence: map[string]string{},
		allowed: map[string]bool{}, declined: map[string]bool{}, res: &Result{},
		outside: &runner.Step{Step: &installmd.Step{ID: "agent", Title: "Changes the agent made outside the install.md's steps"}},
	}
	for _, s := range plan.Steps {
		a.steps[s.ID] = s
	}
	if !o.Remote {
		a.known = secrets.Known(o.Runner.Dir, os.Environ())
	}
	return a
}

// Prompts are the system prompt, first user message and tools the model
// gets for a plan.
func Prompts(plan *runner.Plan, o Options) (system, user string, tools []json.RawMessage, err error) {
	return newAgent(plan, o).prompts()
}

func (a *agent) prompts() (system, user string, tools []json.RawMessage, err error) {
	m := a.o.Runner.Machine
	machine := fmt.Sprintf("%s, %s, GPU %s (%s)", m.OSVersion, m.Arch, m.GPU, m.GPUs)
	if m.Driver != "" {
		machine += ", NVIDIA driver " + m.Driver
	}
	machine += fmt.Sprintf(", %s GB RAM", prompts.Text(m.RAMGB))
	if m.DiskGB != nil {
		machine += fmt.Sprintf(", %s GB free on the install drive", prompts.Text(*m.DiskGB))
	}
	system, err = prompts.Prompt("agent", prompts.Values{"machine": machine, "install_dir": a.o.Runner.Dir, "repo_dir": a.o.Runner.Dir, "structured": true, "windows": a.windows})
	if err != nil {
		return
	}
	var plan []string
	for _, s := range a.plan.Steps {
		head := fmt.Sprintf("Step %d: %s - %s", s.Number, s.ID, s.Title)
		if s.Why != "" {
			head += "\n  Why: " + s.Why
		}
		switch {
		case a.o.Runner.Skip[s.ID]:
			head += "\n  SKIPPED on request: do not run it; record it with step_result skipped."
		case s.Unmet != nil:
			head += "\n  NOT APPLICABLE here: " + strings.Join(s.Unmet, "; ")
		default:
			for _, b := range s.Blocks() {
				head += fmt.Sprintf("\n  %s [%s, %s]:\n    %s", b.Kind, b.Shell, b.Tier.Tier, strings.ReplaceAll(b.Code, "\n", "\n    "))
			}
		}
		plan = append(plan, head)
	}
	user, err = prompts.Message("agent_user_steps", prompts.Values{"name": a.plan.Name, "plan": strings.Join(plan, "\n\n")})
	if err != nil {
		return
	}
	tools, err = a.tools()
	return
}

func (a *agent) tools() ([]json.RawMessage, error) {
	keys := []string{"doctor/system_facts", "doctor/gpu_drivers", "doctor/compose_ps", "doctor/service_logs", "doctor/read_file", "doctor/list_dir", "doctor/grep", "doctor/run_command"}
	if !a.windows {
		keys = []string{"doctor/system_facts", "doctor/gpu_drivers", "doctor/compose_ps", "doctor/service_logs", "linux/read_file", "linux/list_dir", "linux/grep", "linux/run_command"}
	}
	ids := make([]string, 0, len(a.plan.Steps))
	for _, s := range a.plan.Steps {
		ids = append(ids, s.ID)
	}
	v := prompts.Values{"command_timeout_minutes": int(a.o.CommandTimeout.Minutes()), "step_ids": ids}
	keys = append(keys, "agent/run_change", "agent/finish", "agent/run_step", "agent/step_result")
	var out []json.RawMessage
	for _, k := range keys {
		t, err := prompts.Tool(k, v)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Run lets the model install the plan, then checks every step with its
// verify block.
func Run(ctx context.Context, plan *runner.Plan, o Options) (*Result, error) {
	a := newAgent(plan, o)
	system, user, tools, err := a.prompts()
	if err != nil {
		return nil, err
	}
	run := agentloop.Loop(ctx, a.o.Session, agentloop.Options{
		System: system, User: user, Tools: tools,
		Dispatch:   func(name string, args *agentloop.Args) (string, error) { return a.dispatch(ctx, name, args) },
		MaxTurns:   a.o.MaxTurns,
		Timeout:    a.o.Timeout,
		Transcript: a.o.Transcript,
		Trail:      a.o.Trail,
		FinalTools: []string{"step_result"},
	})
	a.closeWindow(ctx)
	a.res.Run = run
	a.res.Status = "unfinished"
	if run.Finish != nil {
		a.res.Status = run.Finish.String("status")
		a.res.Summary = run.Finish.String("summary")
		a.res.BlockedOn = run.Finish.String("blocked_on")
	}
	a.judge(ctx)
	return a.res, nil
}

func (a *agent) dispatch(ctx context.Context, name string, args *agentloop.Args) (string, error) {
	switch name {
	case "run_step":
		return a.runStep(ctx, args.String("step"), args.String("block")), nil
	case "run_change":
		return a.runChange(ctx, args), nil
	case "step_result":
		return a.stepResult(ctx, args), nil
	}
	return a.readTool(ctx, name, args)
}

func (a *agent) runStep(ctx context.Context, id, kind string) string {
	s := a.steps[id]
	if s == nil {
		return fmt.Sprintf("error: no step '%s'", id)
	}
	if a.o.Runner.Skip[id] {
		return "not run: the user asked to skip this step. Record it with step_result skipped."
	}
	if s.Unmet != nil {
		return "not applicable on this machine: " + strings.Join(s.Unmet, "; ")
	}
	b := map[string]*runner.Block{"detect": s.Detect, "install": s.Install, "verify": s.Verify}[kind]
	if b == nil {
		return fmt.Sprintf("step %s has no %s block for this machine", id, kind)
	}
	a.current = s
	origin := id + "/" + kind
	if msg := a.allow(b.Tier, "step:"+id, origin, b.Shell, b.Code, s.Why); msg != "" {
		return msg
	}
	if kind == "install" {
		a.openWindow(ctx, s)
	}
	started := time.Now()
	res := a.o.Runner.RunBlock(ctx, s, b)
	a.ran(res.Code, started)
	if runner.IsRestartCode(res.Code) {
		return fmt.Sprintf("[exit %d] Windows must restart to finish this step. Call finish with status partial and blocked_on telling the user to restart Windows and run the install again.", res.Code)
	}
	return fmt.Sprintf("[exit %d] (%s: %s)\n%s", res.Code, b.Tier.Tier, b.Tier.Reason, runner.Describe(res))
}

func (a *agent) runChange(ctx context.Context, args *agentloop.Args) string {
	code := args.String("command")
	if strings.TrimSpace(code) == "" {
		return "error: command is empty"
	}
	shell := args.String("shell")
	if shell == "" {
		shell = map[bool]string{true: "powershell", false: "bash"}[a.windows]
	}
	if shell != "powershell" && shell != "bash" {
		return "error: shell must be powershell or bash"
	}
	if shell == "powershell" && !a.windows {
		return "error: this target runs bash, not PowerShell"
	}
	tier, err := a.o.Runner.Classify(ctx, shell, code)
	if err != nil {
		return "error: " + err.Error()
	}
	if msg := a.allow(tier, "command:"+code, "model", shell, code, args.String("purpose")); msg != "" {
		return msg
	}
	timeout := 20 * time.Minute
	if n := intArg(args, "timeout_minutes", 0); n > 0 {
		timeout = time.Duration(n) * time.Minute
	}
	timeout = min(timeout, a.o.CommandTimeout)
	s := a.current
	if s == nil {
		s = a.outside
	}
	a.openWindow(ctx, s)
	ev := a.o.Runner.Events
	if ev != nil {
		ev(runner.Event{Step: s, Kind: "block", Block: "run_change"})
	}
	started := time.Now()
	res := a.o.Runner.RunCode(ctx, shell, code, timeout)
	a.ran(res.Code, started)
	if ev != nil {
		ev(runner.Event{Step: s, Kind: "block-done", Block: "run_change", Code: res.Code})
	}
	if runner.IsRestartCode(res.Code) {
		return fmt.Sprintf("[exit %d] Windows must restart to finish this. Call finish with status partial and blocked_on telling the user to restart Windows and run the install again.", res.Code)
	}
	return fmt.Sprintf("[exit %d] (%s: %s)\n%s", res.Code, tier.Tier, tier.Reason, secrets.Redact(runner.Describe(res), a.known))
}

// allow records a command and decides whether it runs: "" when it may,
// else what the model is told.
func (a *agent) allow(tier policy.Result, key, origin, shell, code, why string) string {
	c := Command{Time: time.Now(), Origin: origin, Shell: shell, Command: code, Tier: tier.Tier, Reason: tier.Reason}
	defer func() { a.res.Commands = append(a.res.Commands, c) }()
	switch tier.Tier {
	case policy.Never:
		return fmt.Sprintf("refused: never allowed (%s). Find another way or tell the user in finish.", tier.Reason)
	case policy.System:
		if a.declined[key] {
			return "refused: the user declined this machine-wide command. Do not ask again for the same command; continue without it or explain in finish."
		}
		if !a.allowed[key] && !a.o.Yes {
			a.res.Asked++
			q := fmt.Sprintf("The agent wants to run a machine-wide command (%s)", tier.Reason)
			if why != "" {
				q += "\nWhy: " + why
			}
			if a.o.Ask == nil || !a.o.Ask(q, code) {
				a.declined[key] = true
				return "refused: the user declined this machine-wide command. Do not ask again for the same command; continue without it or explain in finish."
			}
		}
		a.allowed[key] = true
	}
	c.Approved = true
	return ""
}

// ran completes the last command's record.
func (a *agent) ran(code int, started time.Time) {
	c := &a.res.Commands[len(a.res.Commands)-1]
	c.Exit, c.Seconds = &code, time.Since(started).Seconds()
}

func (a *agent) stepResult(ctx context.Context, args *agentloop.Args) string {
	id, status := args.String("step"), args.String("status")
	s := a.steps[id]
	if s == nil {
		return fmt.Sprintf("error: no step '%s'", id)
	}
	if _, ok := statuses[status]; !ok {
		return fmt.Sprintf("error: status must be done, already-done, skipped or failed, not '%s'", status)
	}
	a.reported[id], a.evidence[id] = status, args.String("evidence")
	if a.window == s {
		a.closeWindow(ctx)
	}
	if ev := a.o.Runner.Events; ev != nil {
		ev(runner.Event{Step: s, Kind: "result", Status: statuses[status], Text: agentloop.Brief(args.String("evidence"), 160)})
	}
	var left []string
	for _, s := range a.plan.Steps {
		if s.Unmet == nil && !a.o.Runner.Skip[s.ID] && a.reported[s.ID] == "" {
			left = append(left, s.ID)
		}
	}
	if len(left) == 0 {
		return "Recorded. Steps still pending: none; call finish"
	}
	return "Recorded. Steps still pending: " + strings.Join(left, ", ")
}

// openWindow starts recording a step's changes for uninstall, ending the
// recording of the step before it: the recorder follows one step at a time.
func (a *agent) openWindow(ctx context.Context, s *runner.Step) {
	if a.o.Runner.Tracker == nil || a.window == s {
		return
	}
	a.closeWindow(ctx)
	a.o.Runner.Tracker.Before(ctx, s)
	a.window = s
}

func (a *agent) closeWindow(ctx context.Context) {
	if a.window == nil {
		return
	}
	st, ok := statuses[a.reported[a.window.ID]]
	if !ok {
		st = "unreported"
	}
	a.o.Runner.Tracker.After(ctx, a.window, st)
	a.window = nil
}

// judge sets each step's outcome from its verify block, run quietly after
// the model is done; steps without one keep what the model reported.
func (a *agent) judge(ctx context.Context) {
	quiet := a.o.Runner
	quiet.Exec, quiet.Events = quiet.Read, nil
	a.res.OK = true
	for _, s := range a.plan.Steps {
		r := runner.StepResult{ID: s.ID, Title: s.Title, Started: time.Now()}
		said := a.reported[s.ID]
		switch {
		case a.o.Runner.Skip[s.ID]:
			r.Status, r.Reason = runner.Skipped, "skipped on request"
		case s.Unmet != nil:
			r.Status, r.Reason = runner.NotApplicable, strings.Join(s.Unmet, "; ")
		case s.Verify != nil && (s.Verify.Tier.Tier == policy.Read || s.Verify.Tier.Tier == policy.Project):
			timeout := s.Verify.Timeout
			if timeout == 0 {
				timeout = 5 * time.Minute
			}
			res := quiet.RunCode(ctx, s.Verify.Shell, s.Verify.Code, timeout)
			switch {
			case res.Code != 0:
				r.Status, r.Output = runner.Failed, runner.Describe(res)
				r.Reason = fmt.Sprintf("verify exited %d after the run", res.Code)
			case said == "already-done":
				r.Status = runner.AlreadyDone
			default:
				r.Status = runner.Done
			}
		case said != "":
			r.Status = statuses[said]
			r.Reason = "no verify block to check it"
		default:
			r.Status, r.Reason = runner.NotRun, "the agent did not report it"
		}
		if said != "" {
			note := "agent: " + said
			if e := strings.TrimSpace(a.evidence[s.ID]); e != "" {
				note += " (" + agentloop.Brief(e, 200) + ")"
			}
			r.Reason = strings.TrimPrefix(strings.Join([]string{r.Reason, note}, "; "), "; ")
		} else if r.Status == runner.Done {
			r.Reason = "verify passed; the agent did not report it"
		}
		r.Seconds = time.Since(r.Started).Seconds()
		switch r.Status {
		case runner.Done, runner.AlreadyDone, runner.NotApplicable, runner.Skipped:
		default:
			a.res.OK = false
		}
		a.res.Steps = append(a.res.Steps, r)
	}
}
