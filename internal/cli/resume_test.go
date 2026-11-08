package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/InstallZero/installzero/internal/runner"
)

func TestResumeScript(t *testing.T) {
	got := resumeScript(`C:\tools\installzero.exe`, `C:\My Projects\rl`, []string{"install.md", "--dir", `D:\rl roboracer`, "--env", "NAME=50%", "-y"})
	for _, want := range []string{
		`cd /d "C:\My Projects\rl"`,
		`"C:\tools\installzero.exe" install install.md --dir "D:\rl roboracer" --env NAME=50%% -y`,
		"\r\npause\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestReportResumeWithoutRegistering(t *testing.T) {
	rep := runner.Report{Resume: runner.RestartNeeded, Steps: []runner.StepResult{
		{ID: "wsl", Status: runner.RestartNeeded, Reason: "install exited 3010: Windows must restart to finish it"},
		{ID: "git", Status: runner.NotRun},
	}}
	var out strings.Builder
	code := reportResume(context.Background(), rep, "demo", "lock.json", nil, false, false, nil,
		func(f string, a ...any) { out.WriteString(strings.TrimSpace(fmt.Sprintf(f, a...)) + "\n") })
	if code != exitRestart || !strings.Contains(out.String(), "Step wsl needs you to restart Windows") || !strings.Contains(out.String(), "run installzero install again") {
		t.Fatalf("%d\n%s", code, out.String())
	}
	rep.Resume, rep.Steps[0].Status = runner.SignOutNeeded, runner.SignOutNeeded
	if code := reportResume(context.Background(), rep, "demo", "lock.json", nil, false, false, nil, func(string, ...any) {}); code != exitSignOut {
		t.Fatalf("sign-out exit %d", code)
	}
}
