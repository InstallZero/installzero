package catalog

import "testing"

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
