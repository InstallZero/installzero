package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeProjectFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mongo-auth.json", `{"id": "mongo-auth", "phases": ["container"], "pattern": "Authentication failed", "diagnosis": "d", "advice": "a", "fix": {"action": "none"}, "samples": ["MongoServerError: Authentication failed."]}`)
	write("disk-full.json", `{"id": "disk-full", "order": 30, "phases": ["*"], "pattern": "project disk rule", "diagnosis": "project", "advice": "a", "fix": {"action": "none"}, "samples": ["project disk rule"]}`)
	project, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundled, _ := Load()
	cat := Merge(project, bundled)
	if len(cat.Entries()) != len(bundled.Entries())+1 {
		t.Fatalf("%d entries", len(cat.Entries()))
	}
	if cat.ByID("disk-full").Diagnosis != "project" {
		t.Error("the project's entry should replace the bundled one with its id")
	}
	if e := cat.Match("MongoServerError: Authentication failed.", "container"); e == nil || e.ID != "mongo-auth" {
		t.Errorf("matched %v", e)
	}
	if e := cat.Entries()[len(cat.Entries())-1]; e.ID != "mongo-auth" {
		t.Errorf("an entry without order goes last, got %s", e.ID)
	}
	if line := cat.ByID("mongo-auth").Line("a\nMongoServerError: Authentication failed.\nb"); line != "MongoServerError: Authentication failed." {
		t.Errorf("line %q", line)
	}
}

func TestLoadAndSelfTest(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Entries()) < 10 {
		t.Fatalf("loaded %d entries", len(cat.Entries()))
	}
	for _, e := range cat.Entries() {
		if len(e.Samples) == 0 {
			t.Errorf("%s has no samples", e.ID)
		}
		for _, phase := range e.Phases {
			for _, sample := range e.Samples {
				got := cat.Match(sample, phase)
				if got == nil || got.ID != e.ID {
					who := "nothing"
					if got != nil {
						who = got.ID
					}
					t.Errorf("sample of %s on phase %s matched %s\n  %q", e.ID, phase, who, sample)
				}
			}
		}
	}
}

func TestLookup(t *testing.T) {
	cat, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	sample := "error during connect: open //./pipe/dockerDesktopLinuxEngine: The system cannot find the file specified."
	if got := cat.Lookup(sample, ""); got == nil || got.ID != "docker-not-running" {
		t.Fatalf("error text matched %#v", got)
	}
	if got := cat.Lookup("docker-not-running", ""); got == nil || got.Action != "none" {
		t.Fatalf("id lookup = %#v", got)
	}
	if got := cat.Lookup("docker-not-running", "trainer"); got != nil {
		t.Fatalf("phase filter returned %s", got.ID)
	}
	if cat.Lookup("this error has never been seen", "") != nil {
		t.Fatal("unknown text matched an entry")
	}
}
