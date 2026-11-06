// Package fixture reads the shared test fixtures in fixtures/, the cases
// fixtures/Update-Fixtures.ps1 fills in from the PowerShell engine.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Dir is the repository's fixtures folder.
func Dir(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "fixtures")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's folder")
		}
		dir = parent
	}
}

// Load reads the cases of fixtures/<rel>.
func Load[T any](t testing.TB, rel string) []T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Cases []T }
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("%s has no cases", rel)
	}
	return f.Cases
}

// Text is a fixture string, given either whole or as pieces to join (so no
// file holds a whole fake key).
type Text string

func (x *Text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*x = Text(s)
		return nil
	}
	var parts []string
	if err := json.Unmarshal(b, &parts); err != nil {
		return err
	}
	s = ""
	for _, p := range parts {
		s += p
	}
	*x = Text(s)
	return nil
}

// Short shortens a case's input for a failure message.
func Short(s string) string {
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "..."
	}
	return s
}
