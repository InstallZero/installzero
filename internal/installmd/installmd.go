// Package installmd reads install.md files (format in INSTALL-MD.md), checks
// their format, and picks the blocks that apply to a machine. Ported from
// agent/InstallMd.ps1; fixtures/installmd holds the shared cases.
package installmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/InstallZero/installzero/internal/psrx"
)

// Field is one entry of an ordered key/value list (front matter, a step's
// requirements, a block's conditions). Keys are lower case.
type Field struct{ Key, Value string }

// Fields keeps keys in the order they first appear; setting a key again
// replaces its value in place.
type Fields []Field

// Get returns a key's value.
func (f Fields) Get(key string) (string, bool) {
	for _, x := range f {
		if x.Key == key {
			return x.Value, true
		}
	}
	return "", false
}

func (f *Fields) set(key, value string) {
	for i := range *f {
		if (*f)[i].Key == key {
			(*f)[i].Value = value
			return
		}
	}
	*f = append(*f, Field{key, value})
}

// Block is a fenced block in a step: ```<kind> [shell] [key=value ...].
type Block struct {
	Kind  string
	Shell string
	When  Fields
	Lines []string
	Code  string
}

// Step is a "## Step <n>: <id> - <title>" section.
type Step struct {
	Number   int
	ID       string
	Title    string
	Why      string
	Requires Fields
	Blocks   []*Block
	Line     int
}

// Doc is a parsed install.md.
type Doc struct {
	Meta  Fields
	Steps []*Step
	Prose string
	Text  string
}

var (
	metaLine      = psrx.MustCompile(`^\s*([\w-]+)\s*:\s*(.*)$`)
	blockEnd      = psrx.MustCompile("^```\\s*$")
	headingWithID = psrx.MustCompile(`^##\s+Step\s+(\d+)\s*:\s*([a-z0-9][a-z0-9.-]*)\s*(?:[-:]+\s*(.*))?$`)
	heading       = psrx.MustCompile(`^##\s+Step\s+(\d+)\s*[:.)-]\s*(.+)$`)
	notIDChars    = psrx.MustCompile(`[^a-z0-9]+`)
	blockStart    = psrx.MustCompile("^```(\\w+)\\s*(.*)$")
	whitespace    = psrx.MustCompile(`\s+`)
	whyLine       = psrx.MustCompile(`^\s*Why:\s*(.+)$`)
	requireLine   = psrx.MustCompile(`^\s*([\w-]+)\s*:\s*(.+?)\s*(#.*)?$`)
)

// Parse reads an install.md's text.
func Parse(text string) *Doc {
	lines := splitLines(text)
	doc := &Doc{Text: strings.Join(lines, "\n")}
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i = 1; i < len(lines) && strings.TrimSpace(lines[i]) != "---"; i++ {
			if g := metaLine.Groups(lines[i]); g != nil {
				doc.Meta.set(strings.ToLower(g[1]), strings.TrimSpace(g[2]))
			}
		}
		i++
	}
	var step *Step
	var block *Block
	var prose []string
	for ; i < len(lines); i++ {
		line := lines[i]
		if block != nil {
			if blockEnd.Match(line) {
				step.Blocks = append(step.Blocks, block)
				block = nil
			} else {
				block.Lines = append(block.Lines, line)
			}
			continue
		}
		var h *Step
		if g := headingWithID.Groups(line); g != nil {
			h = &Step{Number: atoi(g[1]), ID: g[2], Title: strings.TrimSpace(g[3])}
		} else if g := heading.Groups(line); g != nil {
			// Without an id ("## Step 3: Clone the repository") the id comes from the title.
			id := strings.Trim(notIDChars.Replace(strings.ToLower(g[2]), "-"), "-")
			h = &Step{Number: atoi(g[1]), ID: id, Title: strings.TrimSpace(g[2])}
		}
		if h != nil {
			h.Line = i + 1
			step = h
			doc.Steps = append(doc.Steps, step)
			continue
		}
		if step != nil {
			if g := blockStart.Groups(line); g != nil {
				block = &Block{Kind: strings.ToLower(g[1])}
				for _, w := range whitespace.Split(strings.TrimSpace(g[2])) {
					if w == "" {
						continue
					}
					if k, v, ok := strings.Cut(w, "="); ok {
						block.When.set(strings.ToLower(k), strings.ToLower(v))
					} else if block.Shell == "" {
						block.Shell = strings.ToLower(w)
					}
				}
				continue
			}
			if step.Why == "" {
				if g := whyLine.Groups(line); g != nil {
					step.Why = strings.TrimSpace(g[1])
					continue
				}
			}
		} else {
			prose = append(prose, line)
		}
	}
	for _, s := range doc.Steps {
		for _, b := range s.Blocks {
			b.Code = strings.TrimSpace(strings.Join(b.Lines, "\n"))
		}
		for _, b := range s.Blocks {
			if b.Kind != "requires" {
				continue
			}
			for _, l := range strings.Split(b.Code, "\n") {
				if g := requireLine.Groups(l); g != nil {
					s.Requires.set(strings.ToLower(g[1]), strings.ToLower(strings.TrimSpace(g[2])))
				}
			}
		}
	}
	doc.Prose = strings.TrimSpace(strings.Join(prose, "\n"))
	return doc
}

var (
	endsExit0  = psrx.MustCompile(`(?m)^\s*exit\s+0\s*\z`)
	canFail    = psrx.MustCompile(`\b(if|throw|exit\s+[1-9])\b`)
	knownKinds = []string{"requires", "detect", "install", "verify"}
	knownShell = []string{"powershell", "pwsh", "bash", "sh"}
)

// Problems lists what makes a document unusable as structured steps; empty
// when it is fine.
func (d *Doc) Problems() []string {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }
	if name, _ := d.Meta.Get("name"); name == "" {
		add("front matter has no name:")
	}
	if len(d.Steps) == 0 {
		add(`no "## Step <n>: <id> - <title>" headings`)
	}
	seen := map[string]bool{}
	for _, s := range d.Steps {
		key := strings.ToLower(s.ID)
		if seen[key] {
			add("step id '%s' is used twice", s.ID)
		}
		seen[key] = true
		has := func(kind string) bool {
			for _, b := range s.Blocks {
				if b.Kind == kind {
					return true
				}
			}
			return false
		}
		if !has("verify") {
			add("step %d (%s) has no verify block", s.Number, s.ID)
		}
		if !has("install") && !has("detect") {
			add("step %d (%s) has neither install nor detect", s.Number, s.ID)
		}
		for _, b := range s.Blocks {
			if !in(b.Kind, knownKinds) {
				add("step %d (%s): unknown block kind '%s'", s.Number, s.ID, b.Kind)
			}
			if b.Kind != "requires" && b.Shell != "" && !in(b.Shell, knownShell) {
				add("step %d (%s): unknown shell '%s'", s.Number, s.ID, b.Shell)
			}
			if b.Kind != "requires" && b.Code == "" {
				add("step %d (%s): empty %s block", s.Number, s.ID, b.Kind)
			}
			if (b.Kind == "detect" || b.Kind == "verify") && endsExit0.Match(b.Code) && !canFail.Match(b.Code) {
				add("step %d (%s): %s ends with exit 0 and has no failing path, so it cannot fail; exit non-zero when the check does not hold", s.Number, s.ID, b.Kind)
			}
		}
	}
	return p
}

// Expand fills {{NAME}} placeholders.
func Expand(code string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		code = strings.ReplaceAll(code, "{{"+k+"}}", values[k])
	}
	return code
}

// splitLines splits text into lines as Get-Content does: at \r\n, \n or \r,
// without an empty last line after a final line break, without a BOM.
func splitLines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' || text[i] == '\r' {
			out = append(out, text[start:i])
			if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func in(s string, list []string) bool {
	for _, x := range list {
		if strings.EqualFold(s, x) {
			return true
		}
	}
	return false
}
