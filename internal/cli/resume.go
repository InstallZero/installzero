package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
)

// Exit codes of a run that stopped to resume after a restart (the Windows
// installer code for "restart required") or a sign-out.
const (
	exitRestart = 3010
	exitSignOut = 3
)

// resumeScript is the .cmd a RunOnce entry starts at the next sign-in: the
// same installzero install, from the same folder. It pauses so the result
// stays on screen.
func resumeScript(exe, cwd string, args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "%", "%%")
		if a == "" || strings.ContainsAny(a, " \t&|<>^()\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `""`) + `"`
		}
		quoted[i] = a
	}
	return strings.Join([]string{
		"@echo off",
		"title InstallZero: continuing the install",
		fmt.Sprintf(`cd /d "%s"`, cwd),
		fmt.Sprintf(`"%s" install %s`, exe, strings.Join(quoted, " ")),
		"pause",
		"",
	}, "\r\n")
}

var notNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// registerResume writes the resume script and a RunOnce entry (current
// user) that starts it at the next sign-in; it returns the script's path.
func registerResume(ctx context.Context, name string, args []string) (string, error) {
	exe := resumeExe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir, err := state.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := notNameChars.ReplaceAllString(name, "-")
	script := filepath.Join(dir, "resume-"+safe+".cmd")
	if err := os.WriteFile(script, []byte(resumeScript(exe, cwd, args)), 0o644); err != nil {
		return "", err
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	ps := fmt.Sprintf(`$key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\RunOnce'
if (-not (Test-Path $key)) { New-Item -Path $key -Force | Out-Null }
Set-ItemProperty -Path $key -Name %s -Value %s`, q("InstallZero "+safe), q(`cmd.exe /c "`+script+`"`))
	res := runner.Local{}.Run(ctx, "powershell", ps, "", time.Minute)
	if res.Code != 0 {
		return "", fmt.Errorf("could not add the sign-in entry: %s", strings.TrimSpace(res.Out))
	}
	return script, nil
}

// reportResume tells the user what the stopped run needs, offers to resume
// at the next sign-in, and returns the exit code.
func reportResume(ctx context.Context, rep runner.Report, name, lock string, args []string, windowsHost, yes bool, confirm func(string) bool, out func(string, ...any)) int {
	var step runner.StepResult
	for _, s := range rep.Steps {
		if s.Status == rep.Resume {
			step = s
		}
	}
	what, code := "restart Windows", exitRestart
	if rep.Resume == runner.SignOutNeeded {
		what, code = "sign out of Windows and back in", exitSignOut
	}
	out("\nStep %s needs you to %s before it can finish (%s).\n", step.ID, what, step.Reason)
	if windowsHost && (yes || confirm("Continue the install automatically when you next sign in? [Y/n] ")) {
		if script, err := registerResume(ctx, name, args); err != nil {
			out("%v\n", err)
		} else {
			out("At your next sign-in, %s runs and the install continues from step %s. Progress is in %s.\n", script, step.ID, lock)
			return code
		}
	}
	out("Then run installzero install again: steps that are done are detected and skipped. Progress is in %s.\n", lock)
	return code
}
