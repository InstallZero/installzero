package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// Message is one chat message as sent to and received from the model.
type Message struct {
	Role string `json:"role"`
	// Content is nil for a hosted model's assistant turn with only tool calls
	// (sent as null).
	Content          *string    `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	// ResponsesItems are an OpenAI Responses API reply's raw output items,
	// sent back verbatim on later turns to keep the model's reasoning.
	ResponsesItems []json.RawMessage `json:"-"`
}

// Text returns a pointer to s, for Message.Content.
func Text(s string) *string { return &s }

// ContentText is the content, "" when null.
func (m *Message) ContentText() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// ToolCall is one function call in an assistant message.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	// ExtraContent rides along unchanged (Gemini's thought signatures).
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

// FunctionCall names the function and holds its arguments as JSON text.
type FunctionCall struct {
	Name      string    `json:"name"`
	Arguments Arguments `json:"arguments"`
}

// Arguments is a call's argument JSON as text; a server that sends an
// object instead of a string gets it compacted, and null becomes "{}".
type Arguments string

func (a *Arguments) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = Arguments(s)
		return nil
	}
	if string(bytes.TrimSpace(b)) == "null" {
		*a = "{}"
		return nil
	}
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return err
	}
	*a = Arguments(out.String())
	return nil
}

// Usage is a reply's token counts; Raw is the provider's object as sent.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Raw              json.RawMessage
}

func (u *Usage) UnmarshalJSON(b []byte) error {
	var v struct {
		PromptTokens     float64 `json:"prompt_tokens"`
		CompletionTokens float64 `json:"completion_tokens"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	u.PromptTokens, u.CompletionTokens = int(v.PromptTokens), int(v.CompletionTokens)
	u.Raw = append(json.RawMessage(nil), b...)
	return nil
}

func (u Usage) MarshalJSON() ([]byte, error) {
	if u.Raw != nil {
		return u.Raw, nil
	}
	return json.Marshal(map[string]int{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens})
}

// Reply is one model turn's answer.
type Reply struct {
	Message      Message
	FinishReason string
	Usage        *Usage
}

// LoopError is a streamed reply stopped because it started repeating
// itself. Partial is the tool call it was writing, when it was writing one.
type LoopError struct {
	Message string
	Partial *Partial
}

func (e *LoopError) Error() string { return e.Message }

// Client sends chat requests to one model.
type Client struct {
	// Llm is the hosted or Hugging Face model; nil for a local llama-server
	// or a given URL.
	Llm     *Remote
	URL     string
	Timeout time.Duration
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 600 * time.Second
}

func (c *Client) provider() string {
	if c.Llm != nil && c.Llm.Provider != "" {
		return c.Llm.Provider
	}
	return "local"
}

// Body builds a chat request. Hosted reasoning models reject sampling
// parameters (OpenAI also rejects max_tokens), so they get only a token
// limit. extra (a JSON object) adds or overrides request fields.
// llama-server's DRY sampling is left off: an install.md repeats
// {{REPO_DIR}} paths, block fences and commands by design, and penalizing
// repeats bent them into variants ({{REPO_DIR}}xxx) that the model then
// looped on; streaming stops real loops instead.
func (c *Client) Body(messages []Message, tools []json.RawMessage, temperature float64, maxTokens int, extra json.RawMessage) (map[string]any, error) {
	if tools == nil {
		tools = []json.RawMessage{}
	}
	body := map[string]any{"messages": messages, "tools": tools}
	switch p := c.provider(); p {
	case "openai":
		body["model"] = c.Llm.Model
		body["max_completion_tokens"] = maxTokens * 4
	case "xai", "anthropic", "google":
		body["model"] = c.Llm.Model
		body["max_tokens"] = maxTokens * 4
	default:
		body["temperature"] = temperature
		body["max_tokens"] = maxTokens
	}
	if len(extra) > 0 && string(extra) != "null" {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(extra, &m); err != nil {
			return nil, fmt.Errorf("model request settings: %v", err)
		}
		for k, v := range m {
			body[k] = v
		}
	}
	return body, nil
}

var versionedBase = regexp.MustCompile(`(?i)/(v1|openai)$`)

func (c *Client) endpoint() string {
	if versionedBase.MatchString(c.URL) {
		return c.URL + "/chat/completions"
	}
	return c.URL + "/v1/chat/completions"
}

// Turn runs one model turn, whatever the backend. stream (llama-server
// only) stops a reply that starts repeating itself, with a *LoopError.
func (c *Client) Turn(ctx context.Context, messages []Message, tools []json.RawMessage, temperature float64, maxTokens int, extra json.RawMessage, stream bool) (*Reply, error) {
	if c.provider() == "openai" {
		return c.responses(ctx, messages, tools, maxTokens)
	}
	body, err := c.Body(messages, tools, temperature, maxTokens, extra)
	if err != nil {
		return nil, err
	}
	if stream {
		return c.chatStream(ctx, body)
	}
	return c.chat(ctx, body)
}

func (c *Client) post(ctx context.Context, url string, body any) (*http.Response, error) {
	data, err := marshalNoEscape(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range Headers(c.Llm) {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, statusError(resp.StatusCode, detail)
	}
	return resp, nil
}

// Errors read like .NET's, which the loop's retry rules (and transcripts)
// were written against.
func statusError(code int, detail []byte) error {
	msg := fmt.Sprintf("The remote server returned an error: (%d) %s.", code, http.StatusText(code))
	if d := strings.TrimSpace(string(detail)); d != "" {
		msg += " " + d
	}
	return errors.New(msg)
}

func transportError(err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return fmt.Errorf("The operation has timed out: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return fmt.Errorf("Unable to connect to the remote server: %v", err)
	}
	return fmt.Errorf("The underlying connection was closed: %v", err)
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

func (c *Client) chat(ctx context.Context, body map[string]any) (*Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	resp, err := c.post(ctx, c.endpoint(), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, transportError(err)
	}
	var r chatResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the model's reply is not JSON: %v: %s", err, truncate(string(data), 300))
	}
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("the model's reply has no choices: %s", truncate(string(data), 300))
	}
	return &Reply{Message: r.Choices[0].Message, FinishReason: r.Choices[0].FinishReason, Usage: r.Usage}, nil
}

type streamCall struct {
	id, name string
	args     []uint16
}

// chatStream reads a llama-server reply as it is generated and stops it as
// soon as its text, reasoning or a tool call's arguments start repeating
// one chunk over and over (such a reply would run to the token limit and be
// cut off). Lengths count UTF-16 code units, as the PowerShell engine's do.
func (c *Client) chatStream(ctx context.Context, body map[string]any) (*Reply, error) {
	body["stream"] = true
	body["stream_options"] = map[string]bool{"include_usage": true}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	resp, err := c.post(ctx, c.endpoint(), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var content, reasoning []uint16
	var calls []*streamCall
	byIndex := map[int]*streamCall{}
	var usage *Usage
	finish := ""
	sinceCheck, checks := 0, 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Usage   *Usage          `json:"usage"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        *struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("the model's stream is not JSON: %v: %s", err, truncate(data, 300))
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return nil, fmt.Errorf("The remote server returned an error in the stream: %s", data)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			d := ch.Delta
			if d == nil {
				continue
			}
			if d.Content != "" {
				u := utf16.Encode([]rune(d.Content))
				content = append(content, u...)
				sinceCheck += len(u)
			}
			if d.ReasoningContent != "" {
				u := utf16.Encode([]rune(d.ReasoningContent))
				reasoning = append(reasoning, u...)
				sinceCheck += len(u)
			}
			for _, tc := range d.ToolCalls {
				call := byIndex[tc.Index]
				if call == nil {
					call = &streamCall{}
					byIndex[tc.Index] = call
					calls = append(calls, call)
				}
				if tc.ID != "" {
					call.id = tc.ID
				}
				call.name += tc.Function.Name
				if tc.Function.Arguments != "" {
					u := utf16.Encode([]rune(tc.Function.Arguments))
					call.args = append(call.args, u...)
					sinceCheck += len(u)
				}
			}
		}
		if sinceCheck >= 300 {
			sinceCheck = 0
			checks++
			type source struct {
				buf  []uint16
				call string
			}
			sources := []source{{content, ""}, {reasoning, ""}}
			for _, call := range calls {
				sources = append(sources, source{call.args, call.name})
			}
			for _, src := range sources {
				if len(src.buf) < 400 {
					continue
				}
				unit := FindRepeatLoop(tailText(src.buf, 2400))
				exact := unit != ""
				if !exact && checks%3 == 0 && len(src.buf) >= 3000 {
					unit = FindTemplateLoop(tailText(src.buf, 8000))
				}
				if unit == "" {
					continue
				}
				le := &LoopError{}
				if src.call != "" {
					// Kept for the caller: a document that loops near its end
					// is still worth saving if nothing better arrives.
					le.Partial = &Partial{Name: src.call, Args: string(utf16.Decode(src.buf))}
					if exact {
						le.Partial.Unit = unit
					}
				}
				shown := []rune(strings.TrimSpace(whitespaceRun.ReplaceAllString(unit, " ")))
				brief := string(shown)
				if len(shown) > 80 {
					brief = string(shown[:80]) + "..."
				}
				le.Message = `the reply started repeating itself ("` + brief + `" over and over) and was stopped`
				return nil, le
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, transportError(err)
	}
	msg := Message{Role: "assistant", Content: Text(string(utf16.Decode(content))), ReasoningContent: string(utf16.Decode(reasoning))}
	for _, call := range calls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: call.id, Type: "function", Function: FunctionCall{Name: call.name, Arguments: Arguments(utf16.Decode(call.args))}})
	}
	return &Reply{Message: msg, FinishReason: finish, Usage: usage}, nil
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func tailText(u []uint16, n int) string {
	if len(u) > n {
		u = u[len(u)-n:]
	}
	return string(utf16.Decode(u))
}

// responses runs a turn on OpenAI's Responses API, which reasoning models
// need to take tools with reasoning on. The chat-format conversation is
// converted to Responses input and the reply back to a chat message.
func (c *Client) responses(ctx context.Context, messages []Message, tools []json.RawMessage, maxTokens int) (*Reply, error) {
	instructions := ""
	items := []any{}
	for _, m := range messages {
		switch m.Role {
		case "system":
			instructions = m.ContentText()
		case "user":
			items = append(items, map[string]any{"role": "user", "content": m.ContentText()})
		case "tool":
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.ContentText()})
		case "assistant":
			if m.ResponsesItems != nil {
				for _, it := range m.ResponsesItems {
					items = append(items, it)
				}
				continue
			}
			if m.ContentText() != "" {
				items = append(items, map[string]any{"role": "assistant", "content": m.ContentText()})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Function.Name, "arguments": string(tc.Function.Arguments)})
			}
		}
	}
	rtools := []any{}
	for _, raw := range tools {
		var t struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		rtools = append(rtools, map[string]any{"type": "function", "name": t.Function.Name, "description": t.Function.Description, "parameters": t.Function.Parameters})
	}
	body := map[string]any{"model": c.Llm.Model, "instructions": instructions, "input": items, "max_output_tokens": maxTokens * 4, "tools": rtools}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	resp, err := c.post(ctx, c.Llm.URL+"/responses", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, transportError(err)
	}
	var r struct {
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the model's reply is not JSON: %v: %s", err, truncate(string(data), 300))
	}
	var texts []string
	msg := Message{Role: "assistant", ResponsesItems: r.Output}
	for _, raw := range r.Output {
		var it struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Args    string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch it.Type {
		case "message":
			var parts []string
			for _, c := range it.Content {
				if c.Type == "output_text" {
					parts = append(parts, c.Text)
				}
			}
			texts = append(texts, strings.Join(parts, "\n"))
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: it.CallID, Type: "function", Function: FunctionCall{Name: it.Name, Arguments: Arguments(it.Args)}})
		}
	}
	msg.Content = Text(strings.Join(texts, ""))
	usage := &Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens}
	return &Reply{Message: msg, Usage: usage}, nil
}

// PlainText replaces the Unicode dashes, quotes and spaces models emit with
// ASCII ones.
func PlainText(s string) string { return plainReplacer.Replace(s) }

var plainReplacer = func() *strings.Replacer {
	var pairs []string
	for r := '\u2010'; r <= '\u2015'; r++ {
		pairs = append(pairs, string(r), "-")
	}
	pairs = append(pairs, "\u2212", "-", "\u2018", "'", "\u2019", "'", "\u201C", `"`, "\u201D", `"`,
		"\u00A0", " ", "\u2007", " ", "\u2009", " ", "\u202F", " ")
	return strings.NewReplacer(pairs...)
}()

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
