package agentloop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/InstallZero/installzero/internal/llm"
	"github.com/InstallZero/installzero/internal/psrx"
)

var (
	textCallBlock = psrx.MustCompileCase(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)
	xmlFunction   = psrx.MustCompile(`(?s)^<function=([\w.-]+)>(.*?)(</function>|$)`)
	xmlParameter  = psrx.MustCompileCase(`(?s)<parameter=([\w.-]+)>\r?\n?(.*?)\r?\n?</parameter>`)
	integerText   = psrx.MustCompile(`^-?\d+$`)
)

// TextToolCalls finds tool calls a model wrote as text instead of
// structured calls, which llama-server sometimes leaves in the reply or the
// reasoning: Qwen's XML form
// <tool_call><function=name><parameter=key>value</parameter></function></tool_call>
// and the JSON form <tool_call>{"name": ..., "arguments": {...}}</tool_call>.
func TextToolCalls(text string, turn int) []llm.ToolCall {
	var out []llm.ToolCall
	for _, m := range textCallBlock.AllGroups(text) {
		body := m[1]
		name, args := "", "{}"
		if g := xmlFunction.Groups(body); g != nil {
			name = g[1]
			var b strings.Builder
			b.WriteByte('{')
			var keys []string
			vals := map[string]string{}
			for _, p := range xmlParameter.AllGroups(g[2]) {
				k, v := p[1], p[2]
				var enc string
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && integerText.Match(v) {
					enc = strconv.FormatInt(n, 10)
				} else if strings.EqualFold(v, "true") || strings.EqualFold(v, "false") {
					enc = strconv.FormatBool(strings.EqualFold(v, "true"))
				} else {
					enc = psJSON(v)
				}
				if _, ok := vals[k]; !ok {
					keys = append(keys, k)
				}
				vals[k] = enc
			}
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(psJSON(k) + ":" + vals[k])
			}
			b.WriteByte('}')
			args = b.String()
		} else {
			var j struct {
				Name      any             `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal([]byte(lenientJSON(body)), &j) == nil {
				if s, ok := j.Name.(string); ok {
					name = s
				} else if j.Name != nil {
					name = fmt.Sprint(j.Name)
				}
				if truthy(j.Arguments) {
					args = psReencode(j.Arguments)
				}
			}
		}
		if name == "" {
			continue
		}
		out = append(out, llm.ToolCall{ID: fmt.Sprintf("text-%d-%d", turn, len(out)+1), Type: "function", Function: llm.FunctionCall{Name: name, Arguments: llm.Arguments(args)}})
	}
	return out
}

// truthy is PowerShell's truth test for a parsed JSON value.
func truthy(raw json.RawMessage) bool {
	switch t := strings.TrimSpace(string(raw)); t {
	case "", "null", "false", `""`, "0":
		return false
	}
	return true
}

// psReencode re-encodes JSON with keys in order and strings escaped as
// ConvertTo-Json does.
func psReencode(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var b strings.Builder
	if err := reencode(dec, &b); err != nil {
		return string(raw)
	}
	return b.String()
}

func reencode(dec *json.Decoder, b *strings.Builder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		end := "]"
		if t == '{' {
			end = "}"
		}
		b.WriteString(string(t))
		for i := 0; dec.More(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if t == '{' {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				b.WriteString(psJSON(k) + ":")
			}
			if err := reencode(dec, b); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		b.WriteString(end)
	case string:
		b.WriteString(psJSON(t))
	case json.Number:
		b.WriteString(t.String())
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	}
	return nil
}

// withProperty sets function.parameters.properties.<name> in a tool
// definition, keeping its keys in order (a new key goes last).
func withProperty(tool json.RawMessage, name string, value json.RawMessage) json.RawMessage {
	out, err := setPath(tool, []string{"function", "parameters", "properties", name}, value)
	if err != nil {
		return tool
	}
	return out
}

func setPath(raw json.RawMessage, path []string, value json.RawMessage) (json.RawMessage, error) {
	if len(path) == 0 {
		return value, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		keys = append(keys, k.(string))
		vals[k.(string)] = v
	}
	child, ok := vals[path[0]]
	if !ok {
		if len(path) > 1 {
			child = json.RawMessage("{}")
		}
		keys = append(keys, path[0])
	}
	nv, err := setPath(child, path[1:], value)
	if err != nil {
		return nil, err
	}
	vals[path[0]] = nv
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vals[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
