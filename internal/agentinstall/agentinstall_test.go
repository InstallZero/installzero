package agentinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/llm"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/target"
)

const doc = "---\nname: app\n---\n\n" +
	"## Step 1: pkg - The tool\n\n```detect bash\ncommand -v tool\n```\n\n```install bash\napt-get install -y tool\n```\n\n```verify bash\ntool --version\n```\n\n" +
	"## Step 2: data - The data folder\n\n```install bash\nmkdir -p /opt/app/data\n```\n\n```verify bash\ntest -d /opt/app/data\n```\n\n" +
	"## Step 3: extra - Skipped\n\n```install bash\ntrue\n```\n"

// fakeExec answers by command text; anything else exits 0.
type fakeExec struct {
	codes map[string]int
	ran   []string
}

func (f *fakeExec) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	f.ran = append(f.ran, code)
	return target.Result{Code: f.codes[code], Out: "output of " + code}
}

type scripted struct{ calls [][2]string }

func (s *scripted) Turn(context.Context, []llm.Message, []json.RawMessage, float64, int, json.RawMessage, bool) (*llm.Reply, error) {
	if len(s.calls) == 0 {
		return nil, fmt.Errorf("no more replies")
	}
	c := s.calls[0]
	s.calls = s.calls[1:]
	return &llm.Reply{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c", Type: "function", Function: llm.FunctionCall{Name: c[0], Arguments: llm.Arguments(c[1])}}}}}, nil
}

type tracker struct{ log []string }

func (t *tracker) Before(_ context.Context, s *runner.Step) { t.log = append(t.log, "before "+s.ID) }
func (t *tracker) After(_ context.Context, s *runner.Step, st runner.Status) {
	t.log = append(t.log, fmt.Sprintf("after %s %s", s.ID, st))
}

func TestAgentInstall(t *testing.T) {
	ctx := context.Background()
	exec := &fakeExec{codes: map[string]int{"command -v tool": 1, "test -d /opt/app/data": 1}}
	tr := &tracker{}
	o := runner.Options{Exec: exec, Machine: installmd.Machine{OS: "linux", Arch: "x64", GPU: "none"}, Dir: "/opt/app", Tracker: tr, Skip: map[string]bool{"extra": true}}
	plan, err := runner.NewPlan(ctx, installmd.Parse(doc), o)
	if err != nil {
		t.Fatal(err)
	}
	model := &scripted{calls: [][2]string{
		{"run_change", `{"command":"echo prepared > /opt/app/note"}`},
		{"run_step", `{"step":"pkg","block":"detect"}`},
		{"run_step", `{"step":"pkg","block":"install"}`},
		{"run_step", `{"step":"pkg","block":"verify"}`},
		{"step_result", `{"step":"pkg","status":"done","evidence":"tool 1.0"}`},
		{"run_step", `{"step":"data","block":"install"}`},
		{"run_change", `{"command":"rm -rf /"}`},
		{"step_result", `{"step":"data","status":"done","evidence":"made it"}`},
		{"finish", `{"status":"installed","summary":"all done"}`},
	}}
	var asked []string
	res, err := Run(ctx, plan, Options{
		Runner: o, Session: &agentloop.Session{Model: model, Hosted: true},
		Ask: func(q, code string) bool { asked = append(asked, code); return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "installed" || res.Summary != "all done" {
		t.Errorf("finish: %q %q", res.Status, res.Summary)
	}
	if len(asked) != 1 || asked[0] != "apt-get install -y tool" || res.Asked != 1 {
		t.Errorf("asked %q (%d)", asked, res.Asked)
	}
	for _, c := range res.Commands {
		if c.Command == "rm -rf /" && (c.Approved || c.Tier != "never") {
			t.Errorf("rm -rf / was %s, approved %v", c.Tier, c.Approved)
		}
	}
	for _, c := range exec.ran {
		if strings.Contains(c, "rm -rf") {
			t.Error("rm -rf / ran")
		}
	}
	want := map[string]runner.Status{"pkg": runner.Done, "data": runner.Failed, "extra": runner.Skipped}
	for _, s := range res.Steps {
		if s.Status != want[s.ID] {
			t.Errorf("step %s: %s (%s), want %s", s.ID, s.Status, s.Reason, want[s.ID])
		}
	}
	if res.OK {
		t.Error("ok although data's verify fails")
	}
	if got, w := strings.Join(tr.log, "; "), "before agent; after agent unreported; before pkg; after pkg done; before data; after data done"; got != w {
		t.Errorf("tracker: %s\nwant      %s", got, w)
	}
}

func TestAgentPrompts(t *testing.T) {
	ctx := context.Background()
	o := runner.Options{Exec: &fakeExec{}, Machine: installmd.Machine{OS: "linux", Arch: "x64", GPU: "none"}, Dir: "/opt/app", Skip: map[string]bool{"extra": true}}
	plan, err := runner.NewPlan(ctx, installmd.Parse(doc), o)
	if err != nil {
		t.Fatal(err)
	}
	system, user, tools, err := Prompts(plan, Options{Runner: o, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "fresh bash shell") || strings.Contains(system, "PowerShell each time") {
		t.Errorf("system prompt is not for bash:\n%s", system)
	}
	if !strings.Contains(user, "Step 3: extra - Skipped\n  SKIPPED on request") || !strings.Contains(user, "install [bash, system]:\n    apt-get install -y tool") {
		t.Errorf("user message:\n%s", user)
	}
	var names []string
	for _, raw := range tools {
		var tl struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		json.Unmarshal(raw, &tl)
		names = append(names, tl.Function.Name)
	}
	if got := strings.Join(names, ","); got != "system_facts,gpu_drivers,compose_ps,service_logs,read_file,list_dir,grep,run_command,run_change,finish,run_step,step_result" {
		t.Errorf("tools %s", got)
	}
}
