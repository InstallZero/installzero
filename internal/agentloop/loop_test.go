package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

func TestCompressFixtures(t *testing.T) {
	type changed struct {
		Index   int
		Content *string
		Args    []*string
	}
	for ci, c := range fixture.Load[struct {
		Messages []struct {
			Role    string
			Content *int
			Args    []int
		}
		MaxChars int `json:"max_chars"`
		Keep     int
		Changed  []changed
	}](t, "loops/compress.json") {
		var ms, before []llm.Message
		for i, m := range c.Messages {
			msg := llm.Message{Role: m.Role}
			if m.Content != nil {
				msg.Content = llm.Text(fixtureText(i+1, *m.Content))
			}
			for j, n := range m.Args {
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("c%d-%d", i+1, j+1), Type: "function", Function: llm.FunctionCall{Name: "x", Arguments: llm.Arguments(fixtureText((i+1)*10+j+1, n))}})
			}
			ms = append(ms, msg)
			cp := msg
			cp.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
			before = append(before, cp)
		}
		compress(ms, c.MaxChars, c.Keep)
		var got []changed
		for i := range ms {
			ch := changed{Index: i, Args: []*string{}}
			diff := false
			if ms[i].ContentText() != before[i].ContentText() {
				ch.Content = llm.Text(ms[i].ContentText())
				diff = true
			}
			for j := range ms[i].ToolCalls {
				if a := string(ms[i].ToolCalls[j].Function.Arguments); a != string(before[i].ToolCalls[j].Function.Arguments) {
					ch.Args = append(ch.Args, llm.Text(a))
					diff = true
				} else {
					ch.Args = append(ch.Args, nil)
				}
			}
			if diff {
				got = append(got, ch)
			}
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(c.Changed)
		if string(g) != string(w) && !(len(got) == 0 && len(c.Changed) == 0) {
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
