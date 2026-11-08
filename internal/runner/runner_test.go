package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/target"
)

// fakeExec answers each block by its first line: "ok", "fail", or
// "fail-once" (fails the first time it runs).
type fakeExec struct {
	ran  []string
	seen map[string]int
}

func (f *fakeExec) Run(_ context.Context, shell, code, dir string, _ time.Duration) target.Result {
	if f.seen == nil {
		f.seen = map[string]int{}
	}
	f.ran = append(f.ran, code)
	f.seen[code]++
	switch first := strings.SplitN(code, "\n", 2)[0]; {
	case strings.HasPrefix(first, "# ok"):
		return target.Result{Code: 0, Out: "fine"}
	case strings.HasPrefix(first, "# fail-once") && f.seen[code] == 1:
		return target.Result{Code: 2, Out: "E: Unable to locate package libfoo"}
	case strings.HasPrefix(first, "# fail-once"):
		return target.Result{Code: 0}
	default:
		return target.Result{Code: 1, Out: "boom"}
	}
}

const doc = `---
name: demo
---
# Demo

## Step 1: tools - Install tools
` + "```detect bash\n# ok already\n```\n```install bash\nsudo apt-get install -y git\n```\n```verify bash\n# ok\ngit --version\n```" + `

## Step 2: gpu - GPU only
` + "```requires\ngpu: nvidia\n```\n```install bash\n# ok\n```\n```verify bash\n# ok\n```" + `

## Step 3: deps - Python deps
` + "```detect bash\n# fail\ntest -d {{INSTALL_DIR}}/.venv\n```\n```install bash\n# fail-once\npython3 -m venv .venv && .venv/bin/pip install -r requirements.txt\n```\n```verify bash\n# ok\n.venv/bin/python -c 'import flask'\n```" + `

## Step 4: service - Service
` + "```install bash\n# ok\nsystemctl enable --now demo\n```\n```verify bash\n# ok\nsystemctl is-active demo\n```"

func plan(t *testing.T, o Options) *Plan {
	t.Helper()
	d := installmd.Parse(doc)
	if p := d.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
	p, err := NewPlan(context.Background(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func linux(e Exec) Options {
	return Options{Exec: e, Machine: installmd.Machine{OS: "linux", Arch: "x64", GPU: "none"}, Dir: "/home/u/demo"}
}

func statuses(r Report) string {
	var s []string
	for _, x := range r.Steps {
		s = append(s, x.ID+"="+string(x.Status))
	}
	return strings.Join(s, " ")
}

func TestPlanTiersAndPlaceholders(t *testing.T) {
	p := plan(t, linux(&fakeExec{}))
	tiers := map[string]string{}
	for _, s := range p.Steps {
		tiers[s.ID] = s.Highest()
	}
	want := map[string]string{"tools": "system", "gpu": "", "deps": "project", "service": "system"}
	for k, v := range want {
		if tiers[k] != v {
			t.Errorf("%s: tier %q, want %q", k, tiers[k], v)
		}
	}
	if got := p.Steps[2].Detect.Code; !strings.Contains(got, "test -d /home/u/demo/.venv") {
		t.Errorf("placeholder not filled: %q", got)
	}
	if n := len(p.System()); n != 2 {
		t.Errorf("%d system steps, want 2", n)
	}
}

func TestRunStopsWhenNotApproved(t *testing.T) {
	e := &fakeExec{}
	o := linux(e)
	r := Run(context.Background(), plan(t, o), o)
	if got := statuses(r); got != "tools=not-approved gpu=not-run deps=not-run service=not-run" {
		t.Errorf("got %s", got)
	}
	if len(e.ran) != 0 || r.OK {
		t.Errorf("ran %d blocks, ok=%v", len(e.ran), r.OK)
	}
}

type retryFixer struct{ calls []Failure }

func (f *retryFixer) Fix(_ context.Context, x Failure) Fix {
	f.calls = append(f.calls, x)
	return Fix{Action: "retry", Note: "installed libfoo-dev"}
}

func TestRunWithApprovalAndFix(t *testing.T) {
	e := &fakeExec{}
	o := linux(e)
	o.Approve = func([]*Step) bool { return true }
	fx := &retryFixer{}
	o.Fixer = fx
	o.Lock = filepath.Join(t.TempDir(), ".installzero", "lock.json")
	r := Run(context.Background(), plan(t, o), o)
	if got := statuses(r); got != "tools=already-done gpu=not-applicable deps=done service=done" {
		t.Errorf("got %s", got)
	}
	if !r.OK || len(fx.calls) != 1 || fx.calls[0].Block != "install" || !strings.Contains(fx.calls[0].Output, "Unable to locate") {
		t.Errorf("ok=%v fixes=%+v", r.OK, fx.calls)
	}
	var lock Report
	b, err := os.ReadFile(o.Lock)
	if err != nil || json.Unmarshal(b, &lock) != nil || !lock.OK || len(lock.Steps) != 4 {
		t.Errorf("lock file: %v %s", err, b)
	}
}

func TestRunFailsWithoutFixer(t *testing.T) {
	o := linux(&fakeExec{})
	o.Approve = func([]*Step) bool { return true }
	r := Run(context.Background(), plan(t, o), o)
	if got := statuses(r); got != "tools=already-done gpu=not-applicable deps=failed service=not-run" {
		t.Errorf("got %s", got)
	}
	if r.Steps[2].Reason != "install exited 2" || !strings.Contains(r.Steps[2].Output, "libfoo") {
		t.Errorf("deps: %+v", r.Steps[2])
	}
}

type timedExec struct {
	limits map[string]time.Duration
	builds int
}

func (e *timedExec) Run(_ context.Context, _, code, _ string, timeout time.Duration) target.Result {
	e.limits[strings.SplitN(code, "\n", 2)[0]] = timeout
	if strings.HasPrefix(code, "# build") {
		if e.builds++; e.builds == 1 {
			return target.Result{Code: 1, Out: "failed to solve"}
		}
	}
	return target.Result{}
}

func TestBlockTimeoutAndPhase(t *testing.T) {
	d := installmd.Parse("---\nname: demo\n---\n\n## Step 1: images - Images\n\n" +
		"```install bash timeout=90m phase=build\n# build\ndocker compose build\n```\n\n```verify bash phase=verify\n# check\ndocker image inspect x\n```\n")
	e := &timedExec{limits: map[string]time.Duration{}}
	o := linux(e)
	fx := &retryFixer{}
	o.Fixer, o.Attempts = fx, 1
	p, err := NewPlan(context.Background(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	Run(context.Background(), p, o)
	if e.limits["# build"] != 90*time.Minute || e.limits["# check"] != 5*time.Minute {
		t.Errorf("timeouts %v", e.limits)
	}
	if len(fx.calls) == 0 || fx.calls[0].Phase != "build" {
		t.Errorf("fixes %+v", fx.calls)
	}
}

type replaceFixer struct{ code string }

func (f replaceFixer) Fix(context.Context, Failure) Fix {
	return Fix{Action: "replace", Block: &installmd.Block{Code: f.code}, Note: "use the system package"}
}

func TestReplaceIsClassified(t *testing.T) {
	o := linux(&fakeExec{})
	o.Approve = func([]*Step) bool { return true }
	o.Fixer = replaceFixer{"rm -rf /etc/"}
	r := Run(context.Background(), plan(t, o), o)
	if r.Steps[2].Status != Refused {
		t.Errorf("never-tier fix: %+v", r.Steps[2])
	}
	o.Fixer = replaceFixer{"# ok\napt-get install -y python3-flask"}
	o.Exec = &fakeExec{}
	r = Run(context.Background(), plan(t, o), o)
	if r.Steps[2].Status != NotApproved {
		t.Errorf("system fix without Confirm: %+v", r.Steps[2])
	}
	o.Confirm = func(*Step, *Block) bool { return true }
	o.Exec = &fakeExec{}
	r = Run(context.Background(), plan(t, o), o)
	if r.Steps[2].Status != Done || !strings.Contains(r.Steps[2].Proposed, "python3-flask") {
		t.Errorf("confirmed fix: %+v", r.Steps[2])
	}
}

type echoExec struct{ got string }

func (e *echoExec) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	e.got = code
	return target.Result{Code: 1, Out: "password is s3cret-value"}
}

func TestEnvIsSetRequiredAndHidden(t *testing.T) {
	d := installmd.Parse("---\nname: x\n---\n## Step 1: a - A\n```install bash\n: \"${ADMIN_PASSWORD:?set it}\"\necho ${OTHER:?x} ${ADMIN_PASSWORD:?again}\n```\n```verify bash\ntest -f x\n```\n")
	e := &echoExec{}
	o := Options{Exec: e, Machine: installmd.Machine{OS: "linux"}, Dir: "/srv/x", Approve: func([]*Step) bool { return true }}
	p, err := NewPlan(context.Background(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.RequiredEnv(map[string]string{"OTHER": "1"}), ","); got != "ADMIN_PASSWORD" {
		t.Errorf("required: %q", got)
	}
	o.Env = map[string]string{"ADMIN_PASSWORD": "s3cret-value"}
	r := Run(context.Background(), p, o)
	if !strings.HasPrefix(e.got, "export ADMIN_PASSWORD='s3cret-value'\n") {
		t.Errorf("block: %q", e.got)
	}
	if out := r.Steps[0].Output; strings.Contains(out, "s3cret") || !strings.Contains(out, "[ADMIN_PASSWORD]") {
		t.Errorf("output: %q", out)
	}
}

func TestWrapBashReadsNoInput(t *testing.T) {
	w := wrapBash("read x\necho \"$x\"")
	if !strings.HasPrefix(w, "set -e\n") || !strings.HasSuffix(w, "\n} </dev/null\n") {
		t.Errorf("%q", w)
	}
}
