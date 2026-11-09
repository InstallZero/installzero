package tag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRead(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.exe")
	data := append(append([]byte("MZ...code..."), slot[:]...), []byte("...more")...)
	if err := os.WriteFile(in, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := Read(in); err != nil || p != "" {
		t.Fatalf("untagged: %q %v", p, err)
	}
	out := filepath.Join(dir, "out.exe")
	if err := Write(in, out, "frenb/rl-roboracer@v0.2"); err != nil {
		t.Fatal(err)
	}
	if p, err := Read(out); err != nil || p != "frenb/rl-roboracer@v0.2" {
		t.Fatalf("tagged: %q %v", p, err)
	}
	if err := Write(out, out, "o/r"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Read(out); p != "o/r" {
		t.Fatalf("retagged: %q", p)
	}
	b, _ := os.ReadFile(out)
	if len(b) != len(data) {
		t.Fatalf("size changed: %d -> %d", len(data), len(b))
	}
}

func TestOneSlot(t *testing.T) {
	in := filepath.Join(t.TempDir(), "two.exe")
	os.WriteFile(in, append(slot[:], slot[:]...), 0o755)
	if err := Write(in, in+".out", "o/r"); err == nil {
		t.Fatal("a file with two slots was tagged")
	}
}
