package fetch

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in, url, gh, ref, name string
		ok                     bool
	}{
		{"frenb/rl-roboracer", "https://github.com/frenb/rl-roboracer", "frenb/rl-roboracer", "", "rl-roboracer", true},
		{"frenb/rl-roboracer@v0.2", "https://github.com/frenb/rl-roboracer", "frenb/rl-roboracer", "v0.2", "rl-roboracer", true},
		{"https://github.com/o/r.git@main", "https://github.com/o/r.git", "o/r", "main", "r", true},
		{"https://gitlab.com/g/p", "https://gitlab.com/g/p", "", "", "p", true},
		{"git@github.com:o/r.git", "git@github.com:o/r.git", "", "", "r", true},
		{"install.md", "", "", "", "", false},
		{"./docs", "", "", "", "", false},
		{"docs/install.md", "", "", "", "", false},
		{`docs\x`, "", "", "", "", false},
	} {
		r, ok := Parse(c.in)
		if ok != c.ok {
			t.Errorf("Parse(%q) ok = %v", c.in, ok)
			continue
		}
		if ok && (r.URL != c.url || r.GitHub != c.gh || r.Ref != c.ref || r.Name() != c.name) {
			t.Errorf("Parse(%q) = %+v, name %s", c.in, r, r.Name())
		}
	}
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func TestUnzipDropsTopFolder(t *testing.T) {
	z := writeZip(t, map[string]string{"r-main/install.md": "# x\n", "r-main/sub/a.sh": "echo\n"})
	dest := filepath.Join(t.TempDir(), "r")
	if err := unzip(z, dest); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"install.md", "sub/a.sh"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Error(err)
		}
	}
}

func TestUnzipRefusesEscapes(t *testing.T) {
	z := writeZip(t, map[string]string{"r-main/../../evil.txt": "x"})
	if err := unzip(z, filepath.Join(t.TempDir(), "r")); err == nil {
		t.Fatal("an entry outside the folder was extracted")
	}
}
