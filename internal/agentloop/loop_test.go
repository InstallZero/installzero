package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/InstallZero/installzero/internal/fixture"
	"github.com/InstallZero/installzero/internal/llm"
)

// fixtureText is New-FixtureText in fixtures/Update-Fixtures.ps1.
func fixtureText(n, length int) string {
	unit := fmt.Sprintf("text %d ", n)
	return strings.Repeat(unit, int(math.Ceil(float64(length)/6))+1)[:length]
}

// fixtureBody is a fixture field that is either a character count (filled by
// fixtureText) or the literal text.
func fixtureBody(n int, raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return llm.Text(s)
	}
	var length int
	if json.Unmarshal(raw, &length) == nil {
		return llm.Text(fixtureText(n, length))
	}
	return nil
}

func TestCompressFixtures(t *testing.T) {
	type kept struct {
		Index   int       `json:"index"`
		Content *string   `json:"content"`
		Args    []*string `json:"args"`
	}
	for ci, c := range fixture.Load[struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
			Args    []json.RawMessage
			Name    string
		}
		MaxChars int `json:"max_chars"`
		Keep     int
		Kept     []kept
	}](t, "loops/compress.json") {
		var ms, before []llm.Message
		lastAssistant, toolK := -1, 0
		for i, m := range c.Messages {
			msg := llm.Message{Role: m.Role, Content: fixtureBody(i+1, m.Content), ReasoningContent: strconv.Itoa(i)}
			name := m.Name
			if name == "" {
				name = "x"
			}
			for j, n := range m.Args {
				arg := ""
				if b := fixtureBody((i+1)*10+j+1, n); b != nil {
					arg = *b
				}
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("c%d-%d", i+1, j+1), Type: "function", Function: llm.FunctionCall{Name: name, Arguments: llm.Arguments(arg)}})
			}
			if m.Role == "assistant" {
				lastAssistant, toolK = len(ms), 0
			} else if m.Role == "tool" && lastAssistant >= 0 {
				toolK++
				if calls := ms[lastAssistant].ToolCalls; toolK <= len(calls) {
					msg.ToolCallID = calls[toolK-1].ID
				}
			}
			ms = append(ms, msg)
			cp := msg
			cp.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
			before = append(before, cp)
		}
		ms = compress(ms, c.MaxChars, c.Keep)
		var got []kept
		for _, m := range ms {
			idx, _ := strconv.Atoi(m.ReasoningContent)
			b := before[idx]
			k := kept{Index: idx, Args: []*string{}}
			if m.ContentText() != b.ContentText() {
				k.Content = llm.Text(m.ContentText())
			}
			for j := range m.ToolCalls {
				if a := string(m.ToolCalls[j].Function.Arguments); a != string(b.ToolCalls[j].Function.Arguments) {
					k.Args = append(k.Args, llm.Text(a))
				} else {
					k.Args = append(k.Args, nil)
				}
			}
			got = append(got, k)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(c.Kept)
		if string(g) != string(w) {
			t.Errorf("case %d:\n  got  %s\n  want %s", ci+1, g, w)
		}
	}
}

func TestLimitText(t *testing.T) {
	if got := LimitText("short", 6000); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("a", 3000) + strings.Repeat("b", 5000)
	got := LimitText(long, 6000)
	want := strings.Repeat("a", 2000) + "\n... [2000 characters omitted] ...\n" + strings.Repeat("b", 4000)
	if got != want {
		t.Errorf("long text: got %d characters, want %d", len(got), len(want))
	}
}

// scripted is a model that plays replies and errors in order.
type scripted struct {
	steps []any
	at    int
}

func (s *scripted) Turn(context.Context, []llm.Message, []json.RawMessage, float64, int, json.RawMessage, bool) (*llm.Reply, error) {
	if s.at >= len(s.steps) {
		return nil, fmt.Errorf("scripted: no more replies")
	}
	x := s.steps[s.at]
	s.at++
	if err, ok := x.(error); ok {
		return nil, err
	}
	return x.(*llm.Reply), nil
}

func tool(name string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"function","function":{"name":%q,"parameters":{"type":"object","properties":{}}}}`, name))
}

// A run that ends on model errors with no result saved keeps the document
// a looping reply was writing.
func TestSalvageOnModelErrors(t *testing.T) {
	// The salvage drops the last line, which a cut-off reply leaves unfinished.
	doc := "---\nname: app\n---\n\n## Step 1: Install\n\n" + strings.Repeat("Install the app with care. ", 20) + "\n"
	unit := "\\nrepeat this line"
	partial := &llm.Partial{Name: "write_install_md", Args: `{"content":"` + strings.ReplaceAll(doc, "\n", `\n`) + strings.Repeat(unit, 30), Unit: unit}
	loopErr := &llm.LoopError{Message: `the reply started repeating itself ("repeat this line" over and over) and was stopped`, Partial: partial}
	down := fmt.Errorf("Unable to connect to the remote server")
	model := &scripted{steps: []any{loopErr, down, down, down}}
	var saved string
	var log strings.Builder
	run := Loop(context.Background(), &Session{Model: model, LlamaServer: true}, Options{
		System: "s", User: "u", Tools: []json.RawMessage{tool("write_install_md"), tool("finish")},
		FinalTools: []string{"write_install_md"}, ResultSaved: func() bool { return saved != "" },
		Dispatch: func(name string, a *Args) (string, error) {
			saved = a.String("content")
			return "Saved.", nil
		},
		Log: &log,
	})
	if run.StopReason != "model-errors" || !strings.HasPrefix(run.Error, "the model failed to produce a usable reply four times") {
		t.Fatalf("stop %q, error %q", run.StopReason, run.Error)
	}
	if want := strings.TrimRight(doc, " \n"); saved != want {
		t.Errorf("salvaged:\n%q\nwant\n%q", saved, want)
	}
	if !strings.Contains(log.String(), `"type":"salvaged"`) {
		t.Error("no salvaged event in the transcript")
	}
}
