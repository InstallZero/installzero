// Package psrx runs regular expressions the way PowerShell's -match,
// -replace and -split do: .NET syntax and semantics (lookarounds, Unicode
// \w and \b, $ before a final newline) and case-insensitive. Code ported
// from PowerShell uses it so the patterns carry over unchanged.
package psrx

import (
	"time"

	"github.com/dlclark/regexp2"
)

// Regexp is a compiled .NET pattern.
type Regexp struct{ re *regexp2.Regexp }

// MustCompile compiles a pattern as -match would use it (case-insensitive).
func MustCompile(pattern string) *Regexp { return compile(pattern, regexp2.IgnoreCase) }

// MustCompileCase compiles a pattern for -cmatch (case-sensitive).
func MustCompileCase(pattern string) *Regexp { return compile(pattern, regexp2.None) }

func compile(pattern string, opt regexp2.RegexOptions) *Regexp {
	re := regexp2.MustCompile(pattern, opt)
	re.MatchTimeout = 5 * time.Second
	return &Regexp{re}
}

// Match reports whether s matches (-match).
func (r *Regexp) Match(s string) bool {
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}

// Groups returns the groups of the first match ($Matches: 0 is the whole
// match, an unmatched group is ""), or nil when there is none.
func (r *Regexp) Groups(s string) []string {
	m, err := r.re.FindStringMatch(s)
	if err != nil || m == nil {
		return nil
	}
	gs := m.Groups()
	out := make([]string, len(gs))
	for i := range gs {
		if len(gs[i].Captures) > 0 {
			out[i] = gs[i].String()
		}
	}
	return out
}

// AllGroups returns the groups of every match ([regex]::Matches).
func (r *Regexp) AllGroups(s string) [][]string {
	var out [][]string
	m, err := r.re.FindStringMatch(s)
	for err == nil && m != nil {
		gs := m.Groups()
		g := make([]string, len(gs))
		for i := range gs {
			if len(gs[i].Captures) > 0 {
				g[i] = gs[i].String()
			}
		}
		out = append(out, g)
		m, err = r.re.FindNextMatch(m)
	}
	return out
}

// Index returns the rune offsets of the first match, or -1, -1.
func (r *Regexp) Index(s string) (start, end int) {
	m, err := r.re.FindStringMatch(s)
	if err != nil || m == nil {
		return -1, -1
	}
	return m.Index, m.Index + m.Length
}

// Replace replaces every match (-replace); repl uses .NET substitutions
// ($1, ${name}, $$).
func (r *Regexp) Replace(s, repl string) string {
	out, err := r.re.Replace(s, repl, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// Split splits s at every match (-split), keeping empty pieces. Patterns
// must not match the empty string.
func (r *Regexp) Split(s string) []string {
	runes := []rune(s)
	var out []string
	at := 0
	m, err := r.re.FindRunesMatch(runes)
	for err == nil && m != nil {
		out = append(out, string(runes[at:m.Index]))
		at = m.Index + m.Length
		m, err = r.re.FindNextMatch(m)
	}
	return append(out, string(runes[at:]))
}
