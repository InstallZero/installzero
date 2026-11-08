package agentinstall

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/prompts"
)

// Fixer is the step runner's last fixer: for a failure the port fixer and
// the catalog could not fix, a model investigates with read-only tools,
// fixes with run_change under the same tiers and approvals, and says how
// the step continues. Each call is a short episode of its own.
type Fixer struct {
	// Options: Runner, Session, Remote, Ask, Yes, Trail and CommandTimeout
	// are used; MaxTurns (20 when 0) and Timeout (30 minutes when 0) bound
	// one episode. Transcript, when set, names the run's transcript; each
	// episode writes its own beside it.
	Options
	Plan *runner.Plan
	// Connect starts the model at the first failure when Session is nil, so
	// a local model takes no memory while nothing fails.
	Connect func(ctx context.Context) (*agentloop.Session, error)
	// Attempts is the runner's number of fixes per step, told to the model.
	Attempts int
	Episodes []Episode
	a        *agent
}

// Episode is one fix attempt by the model.
type Episode struct {
	Step    string         `json:"step"`
	Attempt int            `json:"attempt"`
	Action  string         `json:"action"`
	Note    string         `json:"note"`
	Install string         `json:"install,omitempty"`
	Run     *agentloop.Run `json:"run"`
}

// Commands are the commands the fixes ran, and Asked the approval prompts
// they needed.
func (f *Fixer) Commands() []Command {
	if f.a == nil {
		return nil
	}
	return f.a.res.Commands
}

func (f *Fixer) Asked() int {
	if f.a == nil {
		return 0
	}
	return f.a.res.Asked
}

func (f *Fixer) Fix(ctx context.Context, x runner.Failure) runner.Fix {
	if f.a == nil {
		o := f.Options
		// The runner records the failing step's changes; fixes happen inside it.
		o.Runner.Tracker = nil
		if o.MaxTurns <= 0 {
			o.MaxTurns = 20
		}
		if o.Timeout <= 0 {
			o.Timeout = 30 * time.Minute
		}
		f.a = newAgent(f.Plan, o)
	}
	a := f.a
	a.current = x.Step
	ep := Episode{Step: x.Step.ID, Attempt: x.Attempt}
	giveUp := func(note string) runner.Fix {
		ep.Action, ep.Note = "give_up", note
		f.Episodes = append(f.Episodes, ep)
		return runner.Fix{Action: "give-up", Note: note}
	}
	if a.o.Session == nil {
		if f.Connect == nil {
			return giveUp("no model to fix it")
		}
		s, err := f.Connect(ctx)
		if err != nil {
			f.Connect = nil
			return giveUp("the fixer model could not be started: " + err.Error())
		}
		a.o.Session = s
	}
	system, err := prompts.Prompt("fix", prompts.Values{"machine": a.machine(), "install_dir": a.o.Runner.Dir, "windows": a.windows, "max_turns": a.o.MaxTurns})
	if err != nil {
		return giveUp(err.Error())
	}
	var blocks []string
	for _, b := range x.Step.Blocks() {
		blocks = append(blocks, fmt.Sprintf("%s [%s, %s]:\n    %s", b.Kind, b.Shell, b.Tier.Tier, strings.ReplaceAll(b.Code, "\n", "\n    ")))
	}
	user, err := prompts.Message("fix_user", prompts.Values{
		"name": f.Plan.Name, "number": x.Step.Number, "total": len(f.Plan.Steps), "id": x.Step.ID, "title": x.Step.Title, "why": x.Step.Why,
		"block": x.Block, "code": x.Code, "attempt": x.Attempt, "attempts": max(f.Attempts, x.Attempt), "history": strings.Join(x.History, "; "),
		"blocks": strings.Join(blocks, "\n"), "output": x.Output,
	})
	if err != nil {
		return giveUp(err.Error())
	}
	tools, err := a.tools("agent/run_change", "fix/fix_result")
	if err != nil {
		return giveUp(err.Error())
	}
	transcript := ""
	if f.Transcript != "" {
		transcript = fmt.Sprintf("%s-fix-%s-%d.jsonl", strings.TrimSuffix(f.Transcript, ".jsonl"), x.Step.ID, x.Attempt)
	}
	run := agentloop.Loop(ctx, a.o.Session, agentloop.Options{
		System: system, User: user, Tools: tools,
		Dispatch: func(name string, args *agentloop.Args) (string, error) {
			if name == "run_step" || name == "step_result" {
				return "error: unknown tool '" + name + "'", nil
			}
			return a.dispatch(ctx, name, args)
		},
		MaxTurns: a.o.MaxTurns, Timeout: a.o.Timeout, Transcript: transcript, Trail: a.o.Trail,
		FinishName: "fix_result",
	})
	ep.Run = run
	if run.Finish == nil {
		return giveUp(fmt.Sprintf("the model did not conclude (%s)", run.StopReason))
	}
	ep.Action, ep.Note = run.Finish.String("action"), run.Finish.String("note")
	note := "model: " + agentloop.Brief(ep.Note, 400)
	var fix runner.Fix
	switch ep.Action {
	case "retry":
		fix = runner.Fix{Action: "retry", Note: note}
	case "replace":
		ep.Install = run.Finish.String("install")
		if strings.TrimSpace(ep.Install) == "" {
			fix = runner.Fix{Action: "give-up", Note: note + " (a replacement was proposed without its block)"}
			break
		}
		shell := a.shell()
		if x.Step.Install != nil {
			shell = x.Step.Install.Shell
		}
		fix = runner.Fix{Action: "replace", Block: &installmd.Block{Kind: "install", Shell: shell, Code: ep.Install}, Note: note}
	case "restart":
		fix = runner.Fix{Action: "restart", Note: note}
	default:
		fix = runner.Fix{Action: "give-up", Note: note}
	}
	f.Episodes = append(f.Episodes, ep)
	return fix
}
