package policy

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"github.com/InstallZero/installzero/agent"
	"github.com/InstallZero/installzero/internal/fixture"
)

type tierCase struct {
	Command string
	Shell   string
	Roots   []string
	Tier    string
	Reason  string
}

func TestBashFixtures(t *testing.T) {
	for _, c := range fixture.Load[tierCase](t, "policy/bash.json") {
		if got := Bash(c.Command, c.Roots); got != (Result{c.Tier, c.Reason}) {
			t.Errorf("%q\n  got  %s: %s\n  want %s: %s", fixture.Short(c.Command), got.Tier, got.Reason, c.Tier, c.Reason)
		}
	}
}

func TestPowerShellFixtures(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the fixtures hold Windows PowerShell's parse error messages")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("no powershell.exe")
	}
	byRoots := map[string][]tierCase{}
	for _, c := range fixture.Load[tierCase](t, "policy/tiers.json") {
		if isPowerShell(c.Shell) {
			k := c.Shell + "\x00" + joinRoots(c.Roots)
			byRoots[k] = append(byRoots[k], c)
		}
	}
	for _, cases := range byRoots {
		commands := make([]string, len(cases))
		for i, c := range cases {
			commands[i] = c.Command
		}
		got, err := Classify(context.Background(), cases[0].Shell, cases[0].Roots, commands...)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range cases {
			if got[i] != (Result{c.Tier, c.Reason}) {
				t.Errorf("%q\n  got  %s: %s\n  want %s: %s", fixture.Short(c.Command), got[i].Tier, got[i].Reason, c.Tier, c.Reason)
			}
		}
	}
}

func joinRoots(roots []string) string {
	s := ""
	for _, r := range roots {
		s += r + "\x00"
	}
	return s
}

func TestPolicyScriptIsASCII(t *testing.T) {
	for i, r := range agent.PolicyPS {
		if r > 127 {
			t.Fatalf("Policy.ps1 has %q at byte %d; the script reaches PowerShell through stdin, which is read in the console code page", r, i)
		}
	}
}
