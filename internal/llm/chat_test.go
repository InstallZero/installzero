package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// server answers chat requests with handle and keeps the last request.
func server(t *testing.T, handle func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *map[string]any, *string) {
	t.Helper()
	var last map[string]any
	var path string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		last = nil
		json.Unmarshal(b, &last)
		handle(w, last)
	}))
	t.Cleanup(s.Close)
	return s, &last, &path
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

var hello = []Message{{Role: "system", Content: Text("s")}, {Role: "user", Content: Text("u")}}

func TestEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"http://127.0.0.1:1":        "http://127.0.0.1:1/v1/chat/completions",
		"https://api.x.ai/v1":       "https://api.x.ai/v1/chat/completions",
		"https://g.example/openai":  "https://g.example/openai/chat/completions",
		"https://hf.example/V1":     "https://hf.example/V1/chat/completions",
		"https://hf.example/v1beta": "https://hf.example/v1beta/v1/chat/completions",
	} {
		if got := (&Client{URL: url}).endpoint(); got != want {
			t.Errorf("%s: got %s, want %s", url, got, want)
		}
	}
}

func TestBody(t *testing.T) {
	local, _ := (&Client{}).Body(hello, nil, 0.6, 8192, json.RawMessage(`{"chat_template_kwargs":{"enable_thinking":false},"temperature":0.1}`))
	if fmt.Sprintf("%s", local["temperature"]) != "0.1" {
		t.Errorf("extra settings do not override: %v", local["temperature"])
	}
	if local["max_tokens"] != 8192 || local["model"] != nil {
		t.Errorf("local body: %v", local)
	}
	hosted, _ := (&Client{Llm: &Remote{Provider: "anthropic", Model: "m"}}).Body(hello, nil, 0.6, 1000, nil)
	if hosted["temperature"] != nil || hosted["max_tokens"] != 4000 || hosted["model"] != "m" {
		t.Errorf("hosted body: %v", hosted)
	}
	oa, _ := (&Client{Llm: &Remote{Provider: "openai", Model: "m"}}).Body(hello, nil, 0.6, 1000, nil)
	if oa["max_tokens"] != nil || oa["max_completion_tokens"] != 4000 {
		t.Errorf("openai body: %v", oa)
	}
}

func TestChat(t *testing.T) {
	s, last, path := server(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_content":"hm",
			"tool_calls":[{"id":"a","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\"/opt\"}"}},
			              {"id":"b","type":"function","function":{"name":"finish","arguments":{"summary":"x"}},"extra_content":{"google":{"thought_signature":"sig"}}}]}}],
			"usage":{"prompt_tokens":5000,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`)
	})
	c := &Client{URL: s.URL}
	r, err := c.Turn(context.Background(), hello, []json.RawMessage{json.RawMessage(`{"type":"function"}`)}, 0.2, 100, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if *path != "/v1/chat/completions" || (*last)["stream"] != nil {
		t.Errorf("request to %s, stream %v", *path, (*last)["stream"])
	}
	m := r.Message
	if m.Content != nil || m.ReasoningContent != "hm" || len(m.ToolCalls) != 2 || r.FinishReason != "tool_calls" {
		t.Fatalf("message: %+v", m)
	}
	if m.ToolCalls[0].Function.Arguments != `{"path":"/opt"}` || m.ToolCalls[1].Function.Arguments != `{"summary":"x"}` {
		t.Errorf("arguments: %q, %q", m.ToolCalls[0].Function.Arguments, m.ToolCalls[1].Function.Arguments)
	}
	if string(m.ToolCalls[1].ExtraContent) != `{"google":{"thought_signature":"sig"}}` {
		t.Errorf("extra_content: %s", m.ToolCalls[1].ExtraContent)
	}
	if r.Usage.PromptTokens != 5000 || !strings.Contains(string(r.Usage.Raw), "cached_tokens") {
		t.Errorf("usage: %+v", r.Usage)
	}
	// The conversation goes back with null content for a call-only turn.
	b, _ := marshalNoEscape(Message{Role: "assistant", ToolCalls: m.ToolCalls})
	if !strings.Contains(string(b), `"content":null`) || !strings.Contains(string(b), `"extra_content"`) {
		t.Errorf("assistant message sent as %s", b)
	}
}

func TestChatErrors(t *testing.T) {
	s, _, _ := server(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"code":500,"message":"Failed to parse tool call arguments as JSON","type":"server_error"}}`+"\n")
	})
	_, err := (&Client{URL: s.URL}).Turn(context.Background(), hello, nil, 0.2, 100, nil, true)
	want := `The remote server returned an error: (500) Internal Server Error. {"error":{"code":500,"message":"Failed to parse tool call arguments as JSON","type":"server_error"}}`
	if err == nil || err.Error() != want {
		t.Errorf("got %v\nwant %s", err, want)
	}
	_, err = (&Client{URL: "http://127.0.0.1:1"}).Turn(context.Background(), hello, nil, 0.2, 100, nil, false)
	if err == nil || !strings.HasPrefix(err.Error(), "Unable to connect to the remote server") {
		t.Errorf("no server: %v", err)
	}
	s2, _, _ := server(t, func(w http.ResponseWriter, _ map[string]any) {
		sse(w, `{"choices":[{"delta":{"content":"hi"}}]}`, `{"error":{"message":"oom"}}`)
	})
	_, err = (&Client{URL: s2.URL}).Turn(context.Background(), hello, nil, 0.2, 100, nil, true)
	if err == nil || err.Error() != `The remote server returned an error in the stream: {"error":{"message":"oom"}}` {
		t.Errorf("stream error: %v", err)
	}
}

func TestChatStream(t *testing.T) {
	s, last, _ := server(t, func(w http.ResponseWriter, _ map[string]any) {
		sse(w,
			`{"choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"look."}}]}`,
			`{"choices":[{"delta":{"content":"Looking \u2014 now."}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"list_","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"dir","arguments":"th\":\"/opt\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"finish","arguments":"{}"}}]}}]}`,
			`{"choices":[{"finish_reason":"tool_calls","delta":{}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	})
	r, err := (&Client{URL: s.URL}).Turn(context.Background(), hello, nil, 0.9, 100, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if (*last)["stream"] != true || (*last)["stream_options"] == nil {
		t.Errorf("not streamed: %v", *last)
	}
	m := r.Message
	if m.ContentText() != "Looking \u2014 now." || m.ReasoningContent != "Let me look." || r.FinishReason != "tool_calls" || r.Usage.CompletionTokens != 5 {
		t.Errorf("reply: %+v %q %+v", m, r.FinishReason, r.Usage)
	}
	if len(m.ToolCalls) != 2 || m.ToolCalls[0].ID != "c1" || m.ToolCalls[0].Function.Name != "list_dir" || m.ToolCalls[0].Function.Arguments != `{"path":"/opt"}` || m.ToolCalls[1].Function.Name != "finish" {
		t.Errorf("tool calls: %+v", m.ToolCalls)
	}
}

func TestChatStreamStopsLoops(t *testing.T) {
	// Text that repeats one line is stopped once it has repeated enough.
	s, _, _ := server(t, func(w http.ResponseWriter, _ map[string]any) {
		var chunks []string
		for i := 0; i < 200; i++ {
			chunks = append(chunks, `{"choices":[{"delta":{"content":"- install the same package again\n"}}]}`)
		}
		sse(w, chunks...)
	})
	_, err := (&Client{URL: s.URL}).Turn(context.Background(), hello, nil, 0.2, 100, nil, true)
	le, ok := err.(*LoopError)
	if !ok || le.Partial != nil {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(le.Message, `the reply started repeating itself ("`) || !strings.HasSuffix(le.Message, `" over and over) and was stopped`) {
		t.Errorf("message: %s", le.Message)
	}
	// A tool call's arguments that loop are kept for salvage, with the unit.
	s2, _, _ := server(t, func(w http.ResponseWriter, _ map[string]any) {
		chunks := []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"w","function":{"name":"write_install_md","arguments":"{\"content\":\"---\\nname: x\\n"}}]}}]}`}
		for i := 0; i < 200; i++ {
			chunks = append(chunks, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pip install foo\\n"}}]}}]}`)
		}
		sse(w, chunks...)
	})
	_, err = (&Client{URL: s2.URL}).Turn(context.Background(), hello, nil, 0.2, 100, nil, true)
	le, ok = err.(*LoopError)
	if !ok || le.Partial == nil || le.Partial.Name != "write_install_md" || le.Partial.Unit == "" || !strings.HasPrefix(le.Partial.Args, `{"content":"---\nname: x\n`) {
		t.Fatalf("got %#v", err)
	}
}

func TestResponses(t *testing.T) {
	s, last, path := server(t, func(w http.ResponseWriter, _ map[string]any) {
		fmt.Fprint(w, `{"output":[{"type":"reasoning","id":"r1","summary":[]},
			{"type":"message","content":[{"type":"output_text","text":"Done."}]},
			{"type":"function_call","call_id":"f1","name":"finish","arguments":"{\"summary\":\"ok\"}"}],
			"usage":{"input_tokens":100,"output_tokens":20}}`)
	})
	c := &Client{Llm: &Remote{Provider: "openai", Model: "gpt", URL: s.URL, APIKey: "k"}}
	prior := Message{Role: "assistant", ResponsesItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"r0"}`)}}
	msgs := append(append([]Message{}, hello...), prior, Message{Role: "tool", ToolCallID: "f0", Content: Text("out")})
	tools := []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"finish","description":"d","parameters":{"type":"object"}}}`)}
	r, err := c.Turn(context.Background(), msgs, tools, 0.2, 1000, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if *path != "/responses" || (*last)["instructions"] != "s" || (*last)["max_output_tokens"] != float64(4000) {
		t.Errorf("request: %s %v", *path, *last)
	}
	in, _ := json.Marshal((*last)["input"])
	if string(in) != `[{"content":"u","role":"user"},{"id":"r0","type":"reasoning"},{"call_id":"f0","output":"out","type":"function_call_output"}]` {
		t.Errorf("input: %s", in)
	}
	if r.Message.ContentText() != "Done." || len(r.Message.ToolCalls) != 1 || r.Message.ToolCalls[0].ID != "f1" || len(r.Message.ResponsesItems) != 3 || r.Usage.PromptTokens != 100 {
		t.Errorf("reply: %+v", r.Message)
	}
}

func TestPlainText(t *testing.T) {
	if got := PlainText("\u201ca\u201d \u2018b\u2019 c\u2013d\u2212e\u00a0f"); got != `"a" 'b' c-d-e f` {
		t.Errorf("got %q", got)
	}
}

func TestHeaders(t *testing.T) {
	h := Headers(&Remote{Provider: "anthropic", APIKey: "k"})
	if h["Authorization"] != "Bearer k" || h["x-api-key"] != "k" || h["anthropic-version"] == "" {
		t.Errorf("anthropic: %v", h)
	}
	if Headers(nil) != nil {
		t.Error("a local model gets headers")
	}
}
