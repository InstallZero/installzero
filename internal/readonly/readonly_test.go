package readonly

import (
	"context"
	"os/exec"
	"reflect"
	"runtime"
	"testing"

	"github.com/InstallZero/installzero/doctor"
	"github.com/InstallZero/installzero/internal/fixture"
)

func TestBashFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		Code   string
		Reason *string
	}](t, "readonly/bash.json")
	for _, c := range cases {
		want := ""
		if c.Reason != nil {
			want = *c.Reason
		}
		if got := Bash(c.Code); got != want {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Code), got, want)
		}
	}
}

func TestSplitBashFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		Code     string
		Commands []string
	}](t, "readonly/bash-split.json")
	for _, c := range cases {
		if got := SplitBash(c.Code); !reflect.DeepEqual(got, c.Commands) {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Code), got, c.Commands)
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
	cases := fixture.Load[struct {
		Command string
		Reason  *string
	}](t, "readonly/powershell.json")
	commands := make([]string, len(cases))
	for i, c := range cases {
		commands[i] = c.Command
	}
	got, err := PowerShell(context.Background(), commands...)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		want := ""
		if c.Reason != nil {
			want = *c.Reason
		}
		if got[i] != want {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Command), got[i], want)
		}
	}
}

func TestReadOnlyScriptIsASCII(t *testing.T) {
	for i, r := range doctor.ReadOnlyPS {
		if r > 127 {
			t.Fatalf("ReadOnly.ps1 has %q at byte %d; the script reaches PowerShell through stdin, which is read in the console code page", r, i)
		}
	}
}
