package catalogfix

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/catalog"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/target"
)

type fake struct {
	ran  []string
	code int
}

func (f *fake) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	f.ran = append(f.ran, code)
	return target.Result{Code: f.code}
}

func sample(t *testing.T, cat *catalog.Catalog, id string) string {
	t.Helper()
	e := cat.ByID(id)
	if e == nil || len(e.Samples) == 0 {
		t.Fatalf("no samples for %s", id)
	}
	return "Step 6/14 : RUN pip install tensorflow\n" + e.Samples[0] + "\nERROR: failed to solve"
}

func failure(phase, out string) runner.Failure {
	return runner.Failure{Step: &runner.Step{Step: &installmd.Step{ID: "images"}}, Block: "install", Phase: phase, Output: out, Attempt: 1}
}

func TestActions(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	slept := time.Duration(0)
	asked := []string{}
	newFixer := func(e *fake, yes bool) Fixer {
		return Fixer{Catalog: cat, Exec: e, Windows: true, Sleep: func(d time.Duration) { slept += d },
			Confirm: func(q string) bool { asked = append(asked, q); return yes }}
	}

	e := &fake{}
	x := newFixer(e, true).Fix(context.Background(), failure("build", sample(t, cat, "download-truncated")))
	if x.Action != "retry" || !strings.Contains(x.Note, "[download-truncated]") || slept != RetryWait || len(e.ran) != 0 {
		t.Fatalf("retry: %+v slept %v ran %d", x, slept, len(e.ran))
	}

	e = &fake{}
	x = newFixer(e, true).Fix(context.Background(), failure("build", sample(t, cat, "docker-vm-crash")))
	if x.Action != "retry" || len(e.ran) != 1 || !strings.Contains(e.ran[0], "docker desktop restart") {
		t.Fatalf("restart-docker: %+v ran %q", x, e.ran)
	}

	e = &fake{}
	x = newFixer(e, false).Fix(context.Background(), failure("build", sample(t, cat, "docker-vm-crash")))
	if x.Action != "give-up" || len(e.ran) != 0 || !strings.Contains(x.Note, cat.ByID("docker-vm-crash").Advice) {
		t.Fatalf("declined: %+v", x)
	}

	e = &fake{}
	x = newFixer(e, true).Fix(context.Background(), failure("docker-gpu", sample(t, cat, "gpu-not-in-docker")))
	if x.Action != "retry" || len(e.ran) != 2 || !strings.Contains(e.ran[0], "wsl.exe -ArgumentList '--update'") {
		t.Fatalf("wsl-update: %+v ran %q", x, e.ran)
	}

	e = &fake{}
	x = newFixer(e, true).Fix(context.Background(), failure("verify", sample(t, cat, "corrupted-layers")))
	if x.Action != "retry" || len(e.ran) != 1 || !strings.Contains(e.ran[0], "docker builder prune -af") {
		t.Fatalf("rebuild-clean: %+v ran %q", x, e.ran)
	}

	e = &fake{code: 1}
	x = newFixer(e, true).Fix(context.Background(), failure("build", sample(t, cat, "docker-vm-crash")))
	if x.Action != "give-up" || !strings.Contains(x.Note, "restarting Docker Desktop failed") {
		t.Fatalf("restart failed: %+v", x)
	}

	x = newFixer(&fake{}, true).Fix(context.Background(), failure("docker-start", sample(t, cat, "docker-access-denied")))
	if x.Action != "sign-out" || !strings.HasPrefix(x.Note, "[docker-access-denied] ") {
		t.Fatalf("signout: %+v", x)
	}

	// Advice only: the catalog's diagnosis and what to do end the step.
	x = newFixer(&fake{}, true).Fix(context.Background(), failure("docker-start", sample(t, cat, "docker-not-running")))
	if x.Action != "give-up" || !strings.HasPrefix(x.Note, "[docker-not-running] ") {
		t.Fatalf("none: %+v", x)
	}
}

func TestOnlyOnTheWindowsHost(t *testing.T) {
	cat, _ := catalog.Load()
	e := &fake{}
	f := Fixer{Catalog: cat, Exec: e, Confirm: func(string) bool { t.Fatal("asked on a WSL target"); return true }}
	x := f.Fix(context.Background(), failure("build", sample(t, cat, "docker-vm-crash")))
	if x.Action != "give-up" || len(e.ran) != 0 {
		t.Fatalf("%+v", x)
	}
}

type next struct{ called bool }

func (n *next) Fix(context.Context, runner.Failure) runner.Fix {
	n.called = true
	return runner.Fix{Action: "give-up", Note: "next"}
}

func TestHandsOn(t *testing.T) {
	cat, _ := catalog.Load()
	for _, x := range []runner.Failure{
		failure("", "Error response from daemon: driver failed programming external connectivity: Bind for 0.0.0.0:80 failed: port is already allocated"),
		failure("build", "something nobody has catalogued"),
		// A matching entry for another phase does not apply.
		failure("git", "Error response from daemon: Docker Desktop is unable to start"),
	} {
		n := &next{}
		if got := (Fixer{Catalog: cat, Exec: &fake{}, Next: n}).Fix(context.Background(), x); !n.called || got.Note != "next" {
			t.Errorf("phase %q: %+v", x.Phase, got)
		}
	}
}
