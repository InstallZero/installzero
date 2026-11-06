package target

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fake struct {
	out     string
	scripts []string
}

func (f *fake) Bash(_ context.Context, script string, _ time.Duration, _ string) Result {
	f.scripts = append(f.scripts, script)
	return Result{Out: f.out}
}

func TestFormat(t *testing.T) {
	if got := (Result{Code: 2, Out: "x"}).Format(0); got != "[exit 2]\nx" {
		t.Fatalf("got %q", got)
	}
	if got := (Result{Out: "abcdef"}).Format(3); got != "[exit 0]\nabc\n[... cut at 3 characters]" {
		t.Fatalf("got %q", got)
	}
}

func TestQuote(t *testing.T) {
	if got := Quote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}

func TestProfile(t *testing.T) {
	u := &Unix{Runner: &fake{out: "os_version=Ubuntu 24.04.5 LTS\narch=x86_64\nram_gb=23\ndisk_gb=935\ngpus=NVIDIA GeForce RTX 4090\ndriver=617.42"}}
	m := u.Profile(context.Background())
	if m.OS != "linux" || m.Arch != "x64" || m.GPU != "nvidia" || m.RAMGB != 23 || *m.DiskGB != 935 || m.Driver != "617.42" || m.OSVersion != "Ubuntu 24.04.5 LTS" {
		t.Fatalf("got %+v", m)
	}
	m = (&Unix{Runner: &fake{out: "arch=aarch64\nram_gb=x"}}).Profile(context.Background())
	if m.Arch != "arm64" || m.GPU != "none" || m.RAMGB != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestInventory(t *testing.T) {
	f := &fake{out: "ok"}
	u := &Unix{Runner: f}
	if got := u.Inventory(context.Background(), "nope", ""); got != "error: unknown area 'nope'; use "+strings.Join(InventoryAreas, ", ") {
		t.Fatalf("got %q", got)
	}
	if len(f.scripts) != 0 {
		t.Fatal("an unknown area ran a script")
	}
	for _, a := range InventoryAreas {
		if got := u.Inventory(context.Background(), strings.ToUpper(a), "/srv"); got != "ok" {
			t.Fatalf("%s: got %q", a, got)
		}
	}
	if git := f.scripts[len(f.scripts)-3]; !strings.Contains(git, "find '/srv' -maxdepth 4 -name .git") {
		t.Fatalf("git inventory does not search the path: %s", git)
	}
}
