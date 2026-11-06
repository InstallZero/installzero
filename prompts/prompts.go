// Package prompts holds the text the harness sends the model: system
// prompts, messages and tool definitions, shared with the PowerShell engine
// (doctor/Prompts.ps1). Format and rules: README.md.
package prompts

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

//go:embed *.md *.json
var files embed.FS

// Values fills placeholders. A value is a string, bool, number, nil, or (for
// a tools.json string that is only a placeholder) a list.
type Values map[string]any

var (
	sectionTag = regexp.MustCompile(`\{\{([#^/])([a-z][a-z0-9_]*)\}\}`)
	valueTag   = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)\}\}`)
	wholeTag   = regexp.MustCompile(`^\{\{([a-z][a-z0-9_]*)\}\}$`)
)

func lookup(v Values, name string) (any, error) {
	x, ok := v[name]
	if !ok {
		return nil, fmt.Errorf("prompt value '%s' is not set", name)
	}
	return x, nil
}

func isSet(x any) bool {
	switch t := x.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	}
	return Text(x) != ""
}

// Text formats a value the way PowerShell interpolates it into a string.
func Text(x any) string {
	switch t := x.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []string:
		return strings.Join(t, " ")
	}
	return fmt.Sprint(x)
}

// Render fills a template: sections first, innermost first, then values in
// one pass.
func Render(template string, v Values) (string, error) {
	for {
		tags := sectionTag.FindAllStringSubmatchIndex(template, -1)
		done := true
		for i := 0; i+1 < len(tags); i++ {
			open, close := tags[i], tags[i+1]
			kind, name := template[open[2]:open[3]], template[open[4]:open[5]]
			if kind == "/" || template[close[2]:close[3]] != "/" || template[close[4]:close[5]] != name {
				continue
			}
			x, err := lookup(v, name)
			if err != nil {
				return "", err
			}
			keep := isSet(x)
			if kind == "^" {
				keep = !keep
			}
			body := ""
			if keep {
				body = template[open[1]:close[0]]
			}
			template = template[:open[0]] + body + template[close[1]:]
			done = false
			break
		}
		if done {
			if len(tags) > 0 {
				return "", fmt.Errorf("prompt section %s is not closed or not opened", template[tags[0][0]:tags[0][1]])
			}
			break
		}
	}
	var b strings.Builder
	at := 0
	for _, m := range valueTag.FindAllStringSubmatchIndex(template, -1) {
		x, err := lookup(v, template[m[2]:m[3]])
		if err != nil {
			return "", err
		}
		b.WriteString(template[at:m[0]])
		b.WriteString(Text(x))
		at = m[1]
	}
	b.WriteString(template[at:])
	return b.String(), nil
}

// Raw returns a file's text; a .md file without its final newline.
func Raw(name string) (string, error) {
	data, err := files.ReadFile(name)
	if err != nil {
		return "", err
	}
	s := string(data)
	if strings.HasSuffix(name, ".md") {
		s = strings.TrimSuffix(s, "\n")
	}
	return s, nil
}

// Prompt renders <name>.md.
func Prompt(name string, v Values) (string, error) {
	t, err := Raw(name + ".md")
	if err != nil {
		return "", err
	}
	return Render(t, v)
}

var (
	loadOnce sync.Once
	messages map[string]string
	tools    struct {
		Tools      map[string]json.RawMessage `json:"tools"`
		Parameters map[string]json.RawMessage `json:"parameters"`
	}
	loadErr error
)

func load() {
	loadOnce.Do(func() {
		data, err := files.ReadFile("messages.json")
		if err == nil {
			err = json.Unmarshal(data, &messages)
		}
		if err == nil {
			data, err = files.ReadFile("tools.json")
		}
		if err == nil {
			err = json.Unmarshal(data, &tools)
		}
		loadErr = err
	})
}

// Message renders one entry of messages.json.
func Message(name string, v Values) (string, error) {
	load()
	if loadErr != nil {
		return "", loadErr
	}
	t, ok := messages[name]
	if !ok {
		return "", fmt.Errorf("messages.json has no message '%s'", name)
	}
	return Render(t, v)
}

// Tool returns one tool definition from tools.json as JSON, keys in file
// order, with placeholders filled.
func Tool(key string, v Values) (json.RawMessage, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	raw, ok := tools.Tools[key]
	if !ok {
		return nil, fmt.Errorf("tools.json has no tool '%s'", key)
	}
	return fill(raw, v)
}

// Parameter returns one parameter schema from tools.json, like Tool.
func Parameter(key string, v Values) (json.RawMessage, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	raw, ok := tools.Parameters[key]
	if !ok {
		return nil, fmt.Errorf("tools.json has no parameter '%s'", key)
	}
	return fill(raw, v)
}

// fill re-encodes JSON compactly in its own key order, rendering strings.
func fill(raw json.RawMessage, v Values) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	if err := fillValue(dec, &out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func fillValue(dec *json.Decoder, out *bytes.Buffer, v Values) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		end := byte(']')
		if t == '{' {
			end = '}'
		}
		out.WriteByte(byte(t))
		for i := 0; dec.More(); i++ {
			if i > 0 {
				out.WriteByte(',')
			}
			if t == '{' {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				kb, _ := json.Marshal(k)
				out.Write(kb)
				out.WriteByte(':')
			}
			if err := fillValue(dec, out, v); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		out.WriteByte(end)
		return nil
	case string:
		var x any = t
		if m := wholeTag.FindStringSubmatch(t); m != nil {
			if x, err = lookup(v, m[1]); err != nil {
				return err
			}
		} else if strings.Contains(t, "{{") {
			if x, err = Render(t, v); err != nil {
				return err
			}
		}
		b, err := marshal(x)
		if err != nil {
			return err
		}
		out.Write(b)
		return nil
	}
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	out.Write(b)
	return nil
}

func marshal(x any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(x); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
