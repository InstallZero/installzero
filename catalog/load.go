// Package catalog loads the bundled failure catalog.
// The JSON files beside this source are CC BY 4.0; this file is Apache 2.0.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.json
var files embed.FS

// Entry is one known failure. Entries are tried in ascending order and the
// first pattern match for the phase wins.
type Entry struct {
	ID        string
	Version   int
	Order     int
	HasOrder  bool
	Phases    []string
	Pattern   string
	Diagnosis string
	Advice    string
	Action    string
	Samples   []string
	re        *regexp.Regexp
}

// Catalog is the ordered set of entries.
type Catalog struct {
	entries []*Entry
	byID    map[string]*Entry
}

// Load reads the catalog bundled in the binary.
func Load() (*Catalog, error) {
	return LoadFS(files)
}

// LoadFS reads every *.json entry from fsys.
func LoadFS(fsys fs.FS) (*Catalog, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	cat := &Catalog{byID: map[string]*Entry{}}
	for _, name := range names {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		e, err := parseEntry(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if e.ID+".json" != name {
			return nil, fmt.Errorf("%s: id %q does not match the file name", name, e.ID)
		}
		if cat.byID[e.ID] != nil {
			return nil, fmt.Errorf("duplicate catalog id %q", e.ID)
		}
		cat.byID[e.ID] = e
		cat.entries = append(cat.entries, e)
	}
	sort.SliceStable(cat.entries, func(i, j int) bool {
		a, b := cat.entries[i], cat.entries[j]
		if a.HasOrder != b.HasOrder {
			return a.HasOrder
		}
		if a.HasOrder && a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.ID < b.ID
	})
	return cat, nil
}

func parseEntry(body []byte) (*Entry, error) {
	var raw struct {
		ID        string   `json:"id"`
		Version   int      `json:"version"`
		Order     *int     `json:"order"`
		Phases    []string `json:"phases"`
		Pattern   string   `json:"pattern"`
		Diagnosis string   `json:"diagnosis"`
		Advice    string   `json:"advice"`
		Fix       struct {
			Action string `json:"action"`
		} `json:"fix"`
		Samples []string `json:"samples"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.Version == 0 {
		raw.Version = 1
	}
	re, err := regexp.Compile("(?i)" + raw.Pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern: %w", err)
	}
	e := &Entry{
		ID: raw.ID, Version: raw.Version, Phases: raw.Phases, Pattern: raw.Pattern,
		Diagnosis: raw.Diagnosis, Advice: raw.Advice, Action: raw.Fix.Action, Samples: raw.Samples, re: re,
	}
	if raw.Order != nil {
		e.Order = *raw.Order
		e.HasOrder = true
	}
	return e, nil
}

// Entries returns the catalog in match order.
func (c *Catalog) Entries() []*Entry {
	out := make([]*Entry, len(c.entries))
	copy(out, c.entries)
	return out
}

// ByID returns the entry with that id, or nil.
func (c *Catalog) ByID(id string) *Entry {
	return c.byID[id]
}

// Match returns the first entry whose phase applies and whose pattern matches
// text. An empty phase considers every entry. Patterns are case-insensitive.
func (c *Catalog) Match(text, phase string) *Entry {
	for _, e := range c.entries {
		if phase != "" && !e.Applies(phase) {
			continue
		}
		if e.re.MatchString(text) {
			return e
		}
	}
	return nil
}

// Lookup returns an entry by exact id, otherwise the first pattern match.
func (c *Catalog) Lookup(text, phase string) *Entry {
	text = strings.TrimSpace(text)
	if e := c.byID[text]; e != nil && (phase == "" || e.Applies(phase)) {
		return e
	}
	return c.Match(text, phase)
}

// Applies reports whether the entry covers phase. "*" covers every phase.
func (e *Entry) Applies(phase string) bool {
	for _, p := range e.Phases {
		if p == "*" || p == phase {
			return true
		}
	}
	return false
}
