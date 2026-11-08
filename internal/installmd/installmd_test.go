package installmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/fixture"
)

// fields reads a JSON object into Fields, keeping its key order.
func fields(t *testing.T, raw json.RawMessage) Fields {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %s", raw)
	}
	var f Fields
	for dec.More() {
		k, _ := dec.Token()
		var v string
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		f = append(f, Field{k.(string), v})
	}
	return f
}

func readDoc(t *testing.T, file string) *Doc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.Dir(t), "installmd", "docs", file))
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(data))
}

func TestBlockOptions(t *testing.T) {
	d := readDoc(t, "options.md")
	b := d.Steps[0].Blocks[0]
	if b.Timeout != 90*time.Minute || b.Phase != "build" || len(b.When) != 0 {
		t.Fatalf("%+v", b)
	}
	bad := Parse("---\nname: x\n---\n## Step 1: a - A\n```install bash timeout=soon\nmake\n```\n```verify bash\ntest -f out\n```\n")
	if p := bad.Problems(); len(p) != 1 || !strings.Contains(p[0], "timeout=soon is not a duration") {
		t.Fatalf("%q", p)
	}
}

func TestDocFixtures(t *testing.T) {
	type block struct {
		Kind, Shell string
		When        json.RawMessage
		Code        string
	}
	cases := fixture.Load[struct {
		File  string
		Meta  json.RawMessage
		Steps []struct {
			Number     int
			ID         string `json:"id"`
			Title, Why string
			Line       int
			Requires   json.RawMessage
			Blocks     []block
		}
		Prose    string
		Problems []string
	}](t, "installmd/docs.json")
	for _, c := range cases {
		d := readDoc(t, c.File)
		if want := fields(t, c.Meta); !reflect.DeepEqual(d.Meta, want) {
			t.Errorf("%s meta: got %v, want %v", c.File, d.Meta, want)
		}
		if d.Prose != c.Prose {
			t.Errorf("%s prose:\n  got  %q\n  want %q", c.File, d.Prose, c.Prose)
		}
		if len(d.Steps) != len(c.Steps) {
			t.Errorf("%s: %d steps, want %d", c.File, len(d.Steps), len(c.Steps))
			continue
		}
		for i, ws := range c.Steps {
			s := d.Steps[i]
			if s.Number != ws.Number || s.ID != ws.ID || s.Title != ws.Title || s.Why != ws.Why || s.Line != ws.Line {
				t.Errorf("%s step %d: got %d %q %q %q line %d, want %d %q %q %q line %d", c.File, i, s.Number, s.ID, s.Title, s.Why, s.Line, ws.Number, ws.ID, ws.Title, ws.Why, ws.Line)
			}
			if want := fields(t, ws.Requires); !reflect.DeepEqual(s.Requires, want) {
				t.Errorf("%s step %s requires: got %v, want %v", c.File, s.ID, s.Requires, want)
			}
			if len(s.Blocks) != len(ws.Blocks) {
				t.Errorf("%s step %s: %d blocks, want %d", c.File, s.ID, len(s.Blocks), len(ws.Blocks))
				continue
			}
			for j, wb := range ws.Blocks {
				b := s.Blocks[j]
				if b.Kind != wb.Kind || b.Shell != wb.Shell || b.Code != wb.Code || !reflect.DeepEqual(b.When, fields(t, wb.When)) {
					t.Errorf("%s step %s block %d: got %q %q %v %q, want %q %q %s %q", c.File, s.ID, j, b.Kind, b.Shell, b.When, b.Code, wb.Kind, wb.Shell, wb.When, wb.Code)
				}
			}
		}
		got := d.Problems()
		if len(got) != 0 || len(c.Problems) != 0 {
			if !reflect.DeepEqual(got, c.Problems) {
				t.Errorf("%s problems:\n  got  %q\n  want %q", c.File, got, c.Problems)
			}
		}
	}
}

func TestSelectFixtures(t *testing.T) {
	cases := fixture.Load[struct {
		File    string
		Machine Machine
		Steps   []struct {
			ID                      string `json:"id"`
			Unmet                   []string
			Detect, Install, Verify *int
		}
	}](t, "installmd/select.json")
	for _, c := range cases {
		d := readDoc(t, c.File)
		for i, ws := range c.Steps {
			s := d.Steps[i]
			if got := s.Unmet(c.Machine); len(got)+len(ws.Unmet) > 0 && !reflect.DeepEqual(got, ws.Unmet) {
				t.Errorf("%s on %s, step %s unmet:\n  got  %q\n  want %q", c.File, c.Machine.OS, s.ID, got, ws.Unmet)
			}
			for kind, want := range map[string]*int{"detect": ws.Detect, "install": ws.Install, "verify": ws.Verify} {
				got := -1
				if b := s.Select(kind, c.Machine); b != nil {
					for j := range s.Blocks {
						if s.Blocks[j] == b {
							got = j
						}
					}
				}
				w := -1
				if want != nil {
					w = *want
				}
				if got != w {
					t.Errorf("%s on %s/%s gpu %s, step %s %s: block %d, want %d", c.File, c.Machine.OS, c.Machine.Arch, c.Machine.GPU, s.ID, kind, got, w)
				}
			}
		}
	}
}

func TestConditionFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Key, Want string
		Machine   Machine
		Met       bool
	}](t, "installmd/conditions.json") {
		if got := Met(c.Key, c.Want, c.Machine); got != c.Met {
			t.Errorf("Met(%q, %q) on %+v = %v", c.Key, c.Want, c.Machine, got)
		}
	}
}

func TestPlaceholderFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Code     string
		Values   map[string]string
		Expanded string
	}](t, "installmd/placeholders.json") {
		if got := Expand(c.Code, c.Values); got != c.Expanded {
			t.Errorf("Expand(%q)\n  got  %q\n  want %q", c.Code, got, c.Expanded)
		}
	}
}
