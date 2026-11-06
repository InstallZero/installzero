// Package wsl is a Linux target inside a WSL distro, driven from Windows:
// scripts run there as root, through wsl.exe with the script on stdin.
package wsl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/InstallZero/installzero/internal/target"
)

// Distro runs bash in one WSL distro.
type Distro struct {
	Name string
}

// New is the probe's view of a distro.
func New(name string) *target.Unix {
	return &target.Unix{Runner: &Distro{Name: name}, Label: "wsl:" + name}
}

// Bash is Invoke-WslBash.
func (d *Distro) Bash(ctx context.Context, script string, timeout time.Duration, dir string) target.Result {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	body := script
	if dir != "" {
		body = "cd '" + dir + "' 2>/dev/null\n" + script
	}
	body = strings.ReplaceAll(body, "\r\n", "\n") + "\n"
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(run, "wsl.exe", "-d", d.Name, "-u", "root", "--", "bash", "-ls")
	hideWindow(cmd)
	cmd.Stdin = strings.NewReader(body)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	stdout := strings.ToValidUTF8(out.String(), "\uFFFD")
	if errors.Is(run.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return target.Result{Code: 124, Out: fmt.Sprintf("%s\n[timed out after %d s]", stdout, int(timeout/time.Second))}
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return target.Result{Code: 1, Out: "wsl.exe: " + err.Error()}
		}
		code = exit.ExitCode()
	}
	text := stdout
	if e := strings.ToValidUTF8(errOut.String(), "\uFFFD"); strings.TrimSpace(e) != "" {
		text += "\n" + e
	}
	return target.Result{Code: code, Out: strings.TrimRightFunc(text, unicode.IsSpace)}
}
