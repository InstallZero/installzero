package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Run(args, &out, &err)
	return code, out.String(), err.String()
}

func TestShortName(t *testing.T) {
	prev := Name
	t.Cleanup(func() { Name = prev })
	SetName(`C:\tools\iz.EXE`)
	if Name != "iz" {
		t.Fatalf("name %q", Name)
	}
	code, out, _ := run(t)
	if code != 0 || !strings.Contains(out, "iz <command>") {
		t.Fatalf("help:\n%s", out)
	}
}

func TestHelpListsCommands(t *testing.T) {
	code, out, errText := run(t)
	if code != 0 || errText != "" {
		t.Fatalf("help code %d stderr %q", code, errText)
	}
	for _, cmd := range []string{"add", "check", "list", "update", "remove", "doctor", "find", "failures", "share", "capture", "init"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help missing %s", cmd)
		}
	}
}

func TestFind(t *testing.T) {
	code, out, errText := run(t, "find", "Cannot connect to the Docker daemon")
	if code != 0 || !strings.Contains(out, "docker-not-running") {
		t.Fatalf("code %d\n%s\n%s", code, out, errText)
	}
	code, _, errText = run(t, "find", "nothing in the catalog matches this sentence")
	if code != 1 || !strings.Contains(errText, "no catalog entry matches") {
		t.Fatalf("code %d stderr %q", code, errText)
	}
}

func TestInitAndAddList(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	code, out, errText := run(t, "init", "-name", "demo")
	if code != 0 || !strings.Contains(out, "wrote install.json") {
		t.Fatalf("init code %d\n%s\n%s", code, out, errText)
	}
	code, _, errText = run(t, "init", "-name", "demo")
	if code != 1 || !strings.Contains(errText, "already exists") {
		t.Fatalf("second init code %d stderr %q", code, errText)
	}
	code, out, errText = run(t, "add", "--list", dir)
	if code != 0 || !strings.Contains(out, "demo") || !strings.Contains(out, "preflight") {
		t.Fatalf("add --list code %d\n%s\n%s", code, out, errText)
	}
	code, out, errText = run(t, "add", dir)
	if code != 2 || !strings.Contains(out, "demo") || !strings.Contains(errText, "not in this build yet") {
		t.Fatalf("add code %d\n%s\n%s", code, out, errText)
	}
}

func TestListAndFailures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INSTALLZERO_STATE", dir)
	code, out, _ := run(t, "list")
	if code != 0 || !strings.Contains(out, "No projects installed.") {
		t.Fatalf("list code %d %q", code, out)
	}
	code, out, _ = run(t, "failures")
	if code != 0 || !strings.Contains(out, "No failure records.") {
		t.Fatalf("failures code %d %q", code, out)
	}
	lock := []byte(`{"projects":[{"name":"demo","source":"frenb/rl-roboracer"}]}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "lock.json"), lock, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errText := run(t, "list")
	if code != 0 || !strings.Contains(out, "demo") || !strings.Contains(out, "frenb/rl-roboracer") {
		t.Fatalf("list projects code %d\n%s\n%s", code, out, errText)
	}
	recDir := filepath.Join(dir, "records", "2026-10-24T0000Z-images-aaaa")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := []byte(`{"id":"rec-1","phase":{"id":"images"},"signature":{"lines":["rpc error"]},"match":{"entry":"docker-vm-crash"}}` + "\n")
	if err := os.WriteFile(filepath.Join(recDir, "record.json"), rec, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errText = run(t, "failures")
	if code != 0 || !strings.Contains(out, "rec-1") || !strings.Contains(out, "docker-vm-crash") {
		t.Fatalf("failures code %d\n%s\n%s", code, out, errText)
	}
	code, out, errText = run(t, "failures", "show", "rec-1")
	if code != 0 || !strings.Contains(out, "docker-vm-crash") {
		t.Fatalf("show code %d\n%s\n%s", code, out, errText)
	}
}

func TestRemoteAddResolves(t *testing.T) {
	code, out, errText := run(t, "add", "frenb/rl-roboracer")
	if code != 2 || !strings.Contains(out, "https://github.com/frenb/rl-roboracer") {
		t.Fatalf("code %d\n%s\n%s", code, out, errText)
	}
}
