package agentloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Args is a tool call's arguments: a JSON object with its keys in order.
type Args struct {
	Keys   []string
	Values map[string]json.RawMessage
}

// Has reports whether the call set key.
func (a *Args) Has(key string) bool {
	_, ok := a.Values[key]
	return ok
}

// String returns a value as PowerShell would interpolate it: a string as
// is, null as "", anything else as its JSON text.
func (a *Args) String(key string) string {
	raw, ok := a.Values[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	switch strings.TrimSpace(string(raw)) {
	case "null":
		return ""
	case "true":
		return "True"
	case "false":
		return "False"
	}
	return string(raw)
}

// JSON encodes the arguments, leaving out the given keys.
func (a *Args) JSON(without ...string) string {
	var b strings.Builder
	b.WriteByte('{')
	n := 0
	for _, k := range a.Keys {
		if contains(without, k) {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(a.Values[k])
	}
	b.WriteByte('}')
	return b.String()
}

func (a *Args) MarshalJSON() ([]byte, error) { return []byte(a.JSON()), nil }

// NewArgs builds arguments from key/value pairs (values are encoded as JSON).
func NewArgs(pairs ...any) *Args {
	a := &Args{Values: map[string]json.RawMessage{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		k := fmt.Sprint(pairs[i])
		v, _ := json.Marshal(pairs[i+1])
		if !a.Has(k) {
			a.Keys = append(a.Keys, k)
		}
		a.Values[k] = v
	}
	return a
}

// parseArgs reads a call's argument text as Windows PowerShell's
// ConvertFrom-Json would: raw control characters in strings and \' are
// accepted. Empty text is an empty object; JSON null is nil with no error;
// valid JSON that is not an object gives empty arguments.
func parseArgs(text string) (*Args, error) {
	if strings.TrimSpace(text) == "" {
		return &Args{Values: map[string]json.RawMessage{}}, nil
	}
	fixed := lenientJSON(text)
	if !json.Valid([]byte(fixed)) {
		return nil, fmt.Errorf("not valid JSON")
	}
	trimmed := strings.TrimSpace(fixed)
	if trimmed == "null" {
		return nil, nil
	}
	a := &Args{Values: map[string]json.RawMessage{}}
	if !strings.HasPrefix(trimmed, "{") {
		return a, nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		var c bytes.Buffer
		if err := json.Compact(&c, v); err != nil {
			return nil, err
		}
		if !a.Has(k) {
			a.Keys = append(a.Keys, k)
		}
		a.Values[k] = json.RawMessage(c.Bytes())
	}
	return a, nil
}

// validJSON is Test-AgentJson: non-blank text ConvertFrom-Json accepts.
func validJSON(text string) bool {
	return strings.TrimSpace(text) != "" && json.Valid([]byte(lenientJSON(text)))
}

// lenientJSON escapes raw control characters inside strings and turns \'
// into ', which ConvertFrom-Json accepts and encoding/json does not.
func lenientJSON(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in && c == '\\' && i+1 < len(s):
			if s[i+1] == '\'' {
				b.WriteByte('\'')
			} else {
				b.WriteByte(c)
				b.WriteByte(s[i+1])
			}
			i++
		case c == '"':
			in = !in
			b.WriteByte(c)
		case in && c < 0x20:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// psJSON encodes a value as Windows PowerShell's ConvertTo-Json -Compress
// does for the strings, numbers and booleans the text tool calls carry: <,
// >, & and ' are escaped.
func psJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if err := enc.Encode(v); err != nil {
		return "null"
	}
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "'", `\u0027`)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// len16 counts UTF-16 code units, as .NET string lengths do.
func len16(s string) int { return len(utf16.Encode([]rune(s))) }

// head16 is s.Substring(0, n) in UTF-16 code units.
func head16(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if n >= len(u) {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// LimitText keeps a third of max from the start of a long text and the rest
// from its end.
func LimitText(text string, max int) string {
	u := utf16.Encode([]rune(text))
	if len(u) <= max {
		return text
	}
	head := roundInt(float64(max) / 3)
	tail := max - head
	return string(utf16.Decode(u[:head])) + fmt.Sprintf("\n... [%d characters omitted] ...\n", len(u)-max) + string(utf16.Decode(u[len(u)-tail:]))
}
