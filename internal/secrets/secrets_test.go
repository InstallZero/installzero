package secrets

import (
	"testing"

	"github.com/InstallZero/installzero/internal/fixture"
)

func TestRedactFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		Text     fixture.Text
		Values   []struct{ Value, Name string }
		Redacted string
	}](t, "secrets/redact.json")
	for _, c := range cases {
		var known []Value
		for _, v := range c.Values {
			known = append(known, Value{Value: v.Value, Name: v.Name})
		}
		if got := Redact(string(c.Text), known); got != c.Redacted {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(string(c.Text)), got, c.Redacted)
		}
	}
}

func TestPathFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		Path    string
		Secret  bool
		EnvFile bool `json:"env_file"`
	}](t, "secrets/paths.json")
	for _, c := range cases {
		if got := IsSecretPath(c.Path); got != c.Secret {
			t.Errorf("IsSecretPath(%q) = %v", c.Path, got)
		}
		if got := IsEnvFile(c.Path); got != c.EnvFile {
			t.Errorf("IsEnvFile(%q) = %v", c.Path, got)
		}
	}
}

func TestNameFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Name   string
		Secret bool
	}](t, "secrets/names.json") {
		if got := IsSecretName(c.Name); got != c.Secret {
			t.Errorf("IsSecretName(%q) = %v", c.Name, got)
		}
	}
}

func TestLinuxFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct{ Text, Redacted string }](t, "secrets/linux.json") {
		if got := RedactLinux(c.Text); got != c.Redacted {
			t.Errorf("%q\n  got  %q\n  want %q", c.Text, got, c.Redacted)
		}
	}
}

func TestEnvFileFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		Content string
		Entries []struct{ Name, Value, Line string }
		Masked  string
	}](t, "secrets/envfile.json")
	for _, c := range cases {
		got := EnvEntries(c.Content)
		if len(got) != len(c.Entries) {
			t.Errorf("%q: %d entries, want %d", c.Content, len(got), len(c.Entries))
			continue
		}
		for i, e := range c.Entries {
			if got[i] != (EnvEntry{Name: e.Name, Value: e.Value, Line: e.Line}) {
				t.Errorf("%q entry %d: got %+v, want %+v", c.Content, i, got[i], e)
			}
		}
		if m := MaskEnvFile(".env", c.Content); m != c.Masked {
			t.Errorf("%q masked\n  got  %q\n  want %q", c.Content, m, c.Masked)
		}
	}
}
