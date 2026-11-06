package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	got, err := Resolve(dir)
	if err != nil || got.Kind != KindDir || got.Path != dir {
		t.Fatalf("dir: %+v %v", got, err)
	}
	got, err = Resolve("frenb/rl-roboracer")
	if err != nil || got.Kind != KindGitHub || got.URL != "https://github.com/frenb/rl-roboracer" {
		t.Fatalf("github: %+v %v", got, err)
	}
	for _, u := range []string{"https://github.com/frenb/rl-roboracer.git", "git@github.com:frenb/rl-roboracer.git"} {
		got, err = Resolve(u)
		if err != nil || got.Kind != KindGit || got.URL != u {
			t.Fatalf("git %s: %+v %v", u, got, err)
		}
	}
	if _, err := Resolve("not a source"); err == nil {
		t.Fatal("accepted a bare word")
	}
	if _, err := Resolve(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("accepted a missing folder")
	}
	nested := filepath.Join(dir, "owner", "repo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = Resolve(nested)
	if err != nil || got.Kind != KindDir {
		t.Fatalf("existing owner/repo path: %+v %v", got, err)
	}
}
