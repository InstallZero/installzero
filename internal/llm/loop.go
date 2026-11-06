// Package llm talks to the model. This file: spotting a reply that has
// started repeating itself, and saving what came before. Ported from
// doctor/Llm.ps1; fixtures/loops holds the shared cases. Lengths and
// offsets count UTF-16 code units, as the PowerShell engine's do.
package llm

import (
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/InstallZero/installzero/internal/psrx"
)

// FindRepeatLoop returns the repeating unit when text ends in one chunk (up
// to 400 characters) repeated back to back over at least 400 characters and
// 6 repetitions; "" otherwise.
func FindRepeatLoop(text string) string {
	u := utf16.Encode([]rune(text))
	n := len(u)
	for p := 1; p <= 400; p++ {
		span := max(400, 6*p)
		if span > n {
			break
		}
		if equalUnits(u[n-span:n-p], u[n-span+p:n]) {
			return string(utf16.Decode(u[n-p:]))
		}
	}
	return ""
}

func equalUnits(a, b []uint16) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	templateMinLines = 40
	templateMaxBlock = 24
)

var (
	lineBreak = regexp.MustCompile(`\r?\n|\\n`)
	nonWord   = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func words(line string) []string {
	var out []string
	for _, w := range nonWord.Split(line, -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// FindTemplateLoop returns the last line when text ends in lines that
// follow one template: all but 10% of the last max(40, 4k) lines (of 3+
// words; JSON-escaped \n counts as a line break) share at least 60% of
// their words with the line k before, for some k up to 24. This catches
// loops that vary a word each time (a counter, a path segment), which
// FindRepeatLoop misses; a real list of near-identical lines (a verify block
// checking 25 packages) stays under 40. "" otherwise.
func FindTemplateLoop(text string) string {
	var lines [][]string
	for _, l := range lineBreak.Split(text, -1) {
		if w := words(l); len(w) >= 3 {
			lines = append(lines, w)
		}
	}
	n := len(lines)
	if n < templateMinLines {
		return ""
	}
	for k := 1; k <= templateMaxBlock; k++ {
		m := max(templateMinLines, 4*k)
		if m+k > n {
			break
		}
		miss, allowed := 0, int(math.Floor(float64(m)*0.1))
		for i := n - 1; i >= n-m; i-- {
			if similarity(lines[i], lines[i-k]) < 0.6 {
				if miss++; miss > allowed {
					break
				}
			}
		}
		if miss <= allowed {
			return strings.Join(lines[n-1], " ")
		}
	}
	return ""
}

// similarity is the share of words two lines have in common (each word of
// b matched at most as often as it occurs in a), ignoring case.
func similarity(a, b []string) float64 {
	counts := map[string]int{}
	for _, w := range a {
		counts[strings.ToLower(w)]++
	}
	common := 0
	for _, w := range b {
		if k := strings.ToLower(w); counts[k] > 0 {
			counts[k]--
			common++
		}
	}
	return float64(common) / float64(max(len(a), len(b)))
}

// Partial is a tool call cut off while it looped: its name, its arguments
// as streamed so far, and the repeating unit ("" for a templated loop).
type Partial struct {
	Name string `json:"name"`
	Args string `json:"args"`
	Unit string `json:"unit"`
}

var (
	contentArg       = psrx.MustCompile(`"content"\s*:\s*"`)
	jsonStringBody   = regexp.MustCompile(`^(?:[^"\\]|\\.)*`)
	cutUnicodeEscape = psrx.MustCompile(`\\u[0-9a-fA-F]{0,3}$`)
)

// LoopSalvage returns the content argument of a tool call cut off while it
// looped, without the repeated tail; "" when there is none (or under 200
// characters).
func LoopSalvage(p *Partial) string {
	if p == nil {
		return ""
	}
	_, end := contentArg.Index(p.Args)
	if end < 0 {
		return ""
	}
	raw := utf16.Encode([]rune(p.Args)[end:])
	if p.Unit != "" {
		u := utf16.Encode([]rune(p.Unit))
		cut := 0
		for len(raw)-cut >= 2*len(u) && equalUnits(raw[len(raw)-cut-len(u):len(raw)-cut], u) {
			cut += len(u)
		}
		raw = raw[:len(raw)-cut]
	}
	body := cutUnicodeEscape.Replace(jsonStringBody.FindString(string(utf16.Decode(raw))), "")
	text, ok := decodeJSONString(body)
	if !ok {
		return ""
	}
	lines := strings.Split(text, "\n")
	if p.Unit == "" {
		// A templated loop: cut where the trailing run of lines that echo one
		// of the 24 before them begins (short lines inside it go too, not
		// before it).
		start := len(lines)
		for i := len(lines) - 1; i >= 1 && len(lines)-i <= 400; i-- {
			w := words(lines[i])
			if len(w) < 3 {
				continue
			}
			echo := false
			for k := 1; k <= 24 && i-k >= 0 && !echo; k++ {
				prev := words(lines[i-k])
				if len(prev) < 3 {
					continue
				}
				common := 0
				for _, x := range w {
					for _, y := range prev {
						if strings.EqualFold(x, y) {
							common++
							break
						}
					}
				}
				if float64(common)/float64(max(len(w), len(prev))) >= 0.6 {
					echo = true
				}
			}
			if !echo {
				break
			}
			start = i
		}
		if start < len(lines) {
			lines = append(lines[:start], "")
		}
	}
	if len(lines) > 1 {
		lines = lines[:len(lines)-1]
	}
	doc := strings.TrimRightFunc(strings.Join(lines, "\n"), unicode.IsSpace)
	if len(utf16.Encode([]rune(doc))) < 200 {
		return ""
	}
	return doc
}

// decodeJSONString decodes the inside of a JSON string literal as Windows
// PowerShell's ConvertFrom-Json does: raw control characters and \' are
// accepted, unknown escapes and short \u escapes are not, and a lone
// surrogate becomes U+FFFD.
func decodeJSONString(s string) (string, bool) {
	var u []uint16
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '"' {
			return "", false
		}
		if c != '\\' {
			u = append(u, utf16.Encode([]rune{c})...)
			continue
		}
		if i++; i >= len(r) {
			return "", false
		}
		switch r[i] {
		case '"', '\\', '/', '\'':
			u = append(u, uint16(r[i]))
		case 'b':
			u = append(u, '\b')
		case 'f':
			u = append(u, '\f')
		case 'n':
			u = append(u, '\n')
		case 'r':
			u = append(u, '\r')
		case 't':
			u = append(u, '\t')
		case 'u':
			if i+4 >= len(r) {
				return "", false
			}
			var v uint16
			for _, h := range r[i+1 : i+5] {
				d := strings.IndexRune("0123456789abcdef", unicode.ToLower(h))
				if d < 0 {
					return "", false
				}
				v = v*16 + uint16(d)
			}
			u = append(u, v)
			i += 4
		default:
			return "", false
		}
	}
	return string(utf16.Decode(u)), true
}
