package agentinstall

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/target"
)

const fixDoc = "---\nname: app\n---\n\n" +
	"## Step 1: config - Settings\n\n```install bash\necho port=1 > /opt/app/conf/app.ini\n```\n\n```verify bash\ngrep -q port=1 /opt/app/conf/app.ini\n```\n\n" +
	"## Step 2: tool - The tool\n\n```install bash\napt-get install -y tool-old\n```\n\n```verify bash\ntool --version\n```\n"

// fixExec fails writing into the missing folder until it is made, and the
// tool until the replacement package is installed.
type fixExec struct {
	made, tool bool
	ran        []string
}

func (f *fixExec) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	f.ran = append(f.ran, code)
	switch {
	case code == "mkdir -p /opt/app/conf":
		f.made = true
	case strings.HasPrefix(code, "echo port=1"), strings.HasPrefix(code, "grep -q port=1"):
		if !f.made {
			return target.Result{Code: 1, Out: "No such file or directory"}
		}
	case code == "apt-get install -y tool-old":
		return target.Result{Code: 100, Out: "E: Unable to locate package tool-old"}
	case code == "apt-get install -y tool":
		f.tool = true
	case code == "tool --version" && !f.tool:
		return target.Result{Code: 127, Out: "tool: command not found"}
	}
	return target.Result{Out: "ok"}
}

func TestFixer(t *testing.T) {
	ctx := context.Background()
	exec := &fixExec{}
	o := runner.Options{Exec: exec, Machine: installmd.Machine{OS: "linux", Arch: "x64", GPU: "none"}, Dir: "/opt/app"}
	plan, err := runner.NewPlan(ctx, installmd.Parse(fixDoc), o)
	if err != nil {
		t.Fatal(err)
	}
	model := &scripted{calls: [][2]string{
		{"read_file", `{"path":"conf"}`},
		{"run_change", `{"command":"mkdir -p /opt/app/conf"}`},
		{"fix_result", `{"action":"retry","note":"the conf folder was missing; made it"}`},
		{"run_command", `{"command":"apt-cache search tool"}`},
		{"fix_result", `{"action":"replace","note":"the package is called tool","install":"apt-get install -y tool"}`},
	}}
	connects := 0
	f := &Fixer{Plan: plan, Attempts: 3, Connect: func(context.Context) (*agentloop.Session, error) {
		connects++
		return &agentloop.Session{Model: model, Hosted: true}, nil
	}}
	f.Options = Options{Runner: o, Remote: true, Yes: true}
	o.Fixer = f
	o.Confirm = func(*runner.Step, *runner.Block) bool { return true }
	o.Approve = func([]*runner.Step) bool { return true }
	rep := runner.Run(ctx, plan, o)
	if !rep.OK {
		t.Fatalf("not installed: %+v", rep.Steps)
	}
	if rep.Steps[1].Proposed != "apt-get install -y tool" {
		t.Errorf("proposed %q", rep.Steps[1].Proposed)
	}
	if connects != 1 {
		t.Errorf("model started %d times", connects)
	}
	if len(f.Episodes) != 2 || f.Episodes[0].Action != "retry" || f.Episodes[1].Action != "replace" {
		t.Errorf("episodes %+v", f.Episodes)
	}
	if n := len(f.Commands()); n != 1 || f.Commands()[0].Command != "mkdir -p /opt/app/conf" || f.Commands()[0].Tier != "project" {
		t.Errorf("commands %+v", f.Commands())
	}
}
