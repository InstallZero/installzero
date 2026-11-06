package agentloop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/InstallZero/installzero/internal/llm"
	"github.com/InstallZero/installzero/internal/psrx"
	"github.com/InstallZero/installzero/prompts"
)

// Event is one transcript line.
type Event map[string]any

func (e Event) str(k string) string {
	s, _ := e[k].(string)
	return s
}

// ReadTranscript reads a transcript's events.
func ReadTranscript(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var out []Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// ReplaySkip is why a transcript cannot be replayed; "" when it can.
func ReplaySkip(events []Event) string {
	if len(events) == 0 || events[0].str("type") != "start" {
		return "no start event"
	}
	if !strings.HasPrefix(events[0].str("system"), "You are the InstallZero probe") {
		return "not a probe run"
	}
	last := events[len(events)-1]
	run, _ := last["run"].(map[string]any)
	if last.str("type") != "result" || run == nil {
		return "unfinished"
	}
	if _, ok := run["loops_stopped"]; !ok {
		return "written by an older loop"
	}
	for _, e := range events {
		if e.str("type") == "salvaged" {
			return "salvages a looped reply (the stream's partial call is not recorded)"
		}
	}
	return ""
}

var turnsInPrompt = regexp.MustCompile(`\(you have (\d+) turns\)`)

// Replay runs a recorded probe transcript through the loop: the model's
// replies and errors and the tools' outputs come from the transcript, and
// every event the loop writes must match the recorded one. It returns the
// first difference, or nil.
func Replay(events []Event) error {
	if why := ReplaySkip(events); why != "" {
		return fmt.Errorf("cannot replay: %s", why)
	}
	start := events[0]
	maxTurns := 40
	if m := turnsInPrompt.FindStringSubmatch(start.str("system")); m != nil {
		maxTurns, _ = strconv.Atoi(m[1])
	}
	result, _ := events[len(events)-1]["run"].(map[string]any)
	timedOut := result["stop_reason"] == "timeout"
	label := start.str("model")
	hosted := llm.IsRemoteSpec(label)
	r := &replayer{events: events, last: map[string]string{}}
	session := &Session{
		Model:       r,
		Label:       label,
		Hosted:      hosted,
		LlamaServer: !hosted || strings.HasPrefix(label, "hf:"),
	}
	msg := func(name string) string {
		t, err := prompts.Message(name, nil)
		if err != nil {
			panic(err)
		}
		return t
	}
	savedPrefix := strings.SplitN(msg2("probe_saved_at_finish", prompts.Values{"checks": "\x00"}), "\x00", 2)[0]
	recorded := msg("recorded")
	saved, savedFromText := false, ""
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := Options{
		System: start.str("system"), User: start.str("user"),
		Tools:    replayTools("list_dir", "write_install_md", "finish"),
		MaxTurns: maxTurns, MaxTokens: 8192, Timeout: 30 * time.Minute,
		Log:                &r.log,
		FinalTools:         []string{"write_install_md"},
		FinalNudge:         msg("probe_final"),
		ParseFailureNudge:  msg("probe_parse_failure"),
		UnchangedWriteHint: msg("probe_unchanged_hint"),
		ResultSaved:        func() bool { return saved },
		Sleep:              func(time.Duration) {},
		Now: func() time.Time {
			if timedOut && r.exhausted() {
				return base.Add(31 * time.Minute)
			}
			return base
		},
		Dispatch: func(name string, args *Args) (string, error) {
			out, err := r.toolOutput(name)
			if err != nil {
				return "", err
			}
			if name == "write_install_md" {
				saved = true
			}
			// A result marked as a repeat is the one the same call returned
			// last time (rebuilding a cut output twice need not agree).
			key := strings.ToLower(name + "|" + args.JSON("purpose"))
			raw, repeat := unwrapOutput(out)
			if prev, ok := r.last[key]; ok && repeat {
				raw = prev
			}
			r.last[key] = raw
			return raw, nil
		},
		FinishGuard: func(_ *Args, _ string) string {
			out, err := r.toolOutput("finish")
			if err != nil || out == recorded {
				return ""
			}
			if savedPrefix != "" && strings.HasPrefix(out, savedPrefix) {
				saved = true
			}
			return out
		},
		Checkpoint: func(run *Run) string {
			if t := run.LastLongText; t != "" && t != savedFromText && draftInText(t) {
				savedFromText = t
				saved = true
			}
			return ""
		},
	}
	run := Loop(context.Background(), session, o)
	r.flush()
	if r.err != nil {
		return r.err
	}
	if r.at < len(events) {
		return fmt.Errorf("event %d (%s): the loop ended early (stop reason %q, error %q)", r.at+1, events[r.at].str("type"), run.StopReason, run.Error)
	}
	return nil
}

func msg2(name string, v prompts.Values) string {
	t, _ := prompts.Message(name, v)
	return t
}

var draftStart = psrx.MustCompile(`(?ms)^name\s*:.*^##\s+Step\s+\d+`)

// draftInText is the probe's test for an install.md written as a message.
func draftInText(t string) bool {
	i := strings.Index(t, "---\n")
	if i < 0 {
		i = strings.Index(t, "---\r\n")
	}
	return i >= 0 && draftStart.Match(t[i:])
}

func replayTools(names ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, n := range names {
		out = append(out, json.RawMessage(fmt.Sprintf(`{"type":"function","function":{"name":%q,"description":"","parameters":{"type":"object","properties":{}}}}`, n)))
	}
	return out
}

var omitted = regexp.MustCompile(`\n\.\.\. \[(\d+) characters omitted\] \.\.\.\n`)

// unwrapOutput undoes what the loop adds to a tool's output, so the loop
// adds it again: the note on a repeated result, and the cut of a long one
// (the omitted middle comes back as filler of the same length). repeat
// reports a note on a repeated result.
func unwrapOutput(out string) (raw string, repeat bool) {
	for n := 2; n <= 200; n++ {
		if s := msg2("loop_same_result", prompts.Values{"count": n, "last": n >= 3}); strings.HasSuffix(out, s) {
			out = strings.TrimSuffix(out, s)
			repeat = true
			break
		}
	}
	u := utf16.Encode([]rune(out))
	head := roundInt(float64(MaxToolChars) / 3)
	if len(u) > head {
		rest := string(utf16.Decode(u[head:]))
		if m := omitted.FindStringSubmatchIndex(rest); m != nil && m[0] == 0 {
			n, _ := strconv.Atoi(rest[m[2]:m[3]])
			return string(utf16.Decode(u[:head])) + strings.Repeat("x", n) + rest[m[1]:], repeat
		}
	}
	return out, repeat
}

// replayer is the model (the recorded replies and errors in order) and
// the transcript checker.
type replayer struct {
	events []Event
	at     int // next recorded event to match
	turn   int // index of the next recorded model outcome
	log    lineWriter
	err    error
	last   map[string]string // the output each call returned last
}

func (r *replayer) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

func isOutcome(e Event) bool {
	switch e.str("type") {
	case "assistant", "server_error", "loop_stopped":
		return true
	}
	return false
}

func (r *replayer) exhausted() bool {
	for i := r.turn; i < len(r.events); i++ {
		if isOutcome(r.events[i]) {
			return false
		}
	}
	return true
}

func (r *replayer) Turn(_ context.Context, _ []llm.Message, _ []json.RawMessage, temperature float64, _ int, _ json.RawMessage, _ bool) (*llm.Reply, error) {
	for r.turn < len(r.events) && !isOutcome(r.events[r.turn]) {
		r.turn++
	}
	if r.turn >= len(r.events) {
		// An error the loop does not retry is recorded only in the result.
		run, _ := r.events[len(r.events)-1]["run"].(map[string]any)
		if e, _ := run["error"].(string); e != "" && run["stop_reason"] == "" {
			return nil, fmt.Errorf("%s", e)
		}
		r.fail("the loop asked for a model turn after the recorded ones ran out")
		return nil, fmt.Errorf("replay: no more recorded turns")
	}
	e := r.events[r.turn]
	r.turn++
	switch e.str("type") {
	case "server_error":
		if t, _ := e["temperature"].(float64); t != temperature {
			r.fail("server error at temperature %v recorded, the loop asked at %v", t, temperature)
		}
		return nil, fmt.Errorf("%s", e.str("error"))
	case "loop_stopped":
		if t, _ := e["temperature"].(float64); t != temperature {
			r.fail("stopped loop at temperature %v recorded, the loop asked at %v", t, temperature)
		}
		return nil, &llm.LoopError{Message: e.str("error")}
	}
	m := llm.Message{Role: "assistant", Content: llm.Text(e.str("content")), ReasoningContent: e.str("reasoning")}
	m.ToolCalls = recordedCalls(e["tool_calls"])
	reply := &llm.Reply{Message: m}
	if e["usage"] != nil {
		raw, _ := json.Marshal(e["usage"])
		var u llm.Usage
		if json.Unmarshal(raw, &u) == nil {
			reply.Usage = &u
		}
	}
	return reply, nil
}

// toolOutput is the recorded output of the tool event the loop is about to
// write.
func (r *replayer) toolOutput(name string) (string, error) {
	r.flush()
	if r.at >= len(r.events) || r.events[r.at].str("type") != "tool" {
		r.fail("event %d: the loop ran %s where the recording has no tool event", r.at+1, name)
		return "", fmt.Errorf("replay: no recorded tool event")
	}
	e := r.events[r.at]
	if e.str("name") != name {
		r.fail("event %d: the loop ran %s, the recording %s", r.at+1, name, e.str("name"))
	}
	return e.str("output"), nil
}

// flush matches the events the loop has written so far.
func (r *replayer) flush() {
	for _, line := range r.log.take() {
		var got Event
		if err := json.Unmarshal(line, &got); err != nil {
			r.fail("the loop wrote a line that is not JSON: %v", err)
			continue
		}
		if r.at >= len(r.events) {
			r.fail("the loop wrote a %s event after the recording ended", got.str("type"))
			continue
		}
		want := r.events[r.at]
		if d := diffEvent(want, got); d != "" {
			r.fail("event %d (%s): %s", r.at+1, want.str("type"), d)
		}
		r.at++
	}
}

func diffEvent(want, got Event) string {
	if want.str("type") != got.str("type") {
		return fmt.Sprintf("the loop wrote a %s event", got.str("type"))
	}
	var keys []string
	switch want.str("type") {
	case "start":
		keys = []string{"model", "system", "user"}
	case "assistant":
		keys = []string{"content", "reasoning"}
		if d := diffCalls(want["tool_calls"], got["tool_calls"]); d != "" {
			return d
		}
	case "tool":
		keys = []string{"name", "arguments", "output"}
	case "server_error", "loop_stopped":
		keys = []string{"temperature", "error"}
	case "result":
		w, _ := want["run"].(map[string]any)
		g, _ := got["run"].(map[string]any)
		for k, v := range w {
			if k == "seconds" || (k == "error" && strings.HasPrefix(fmt.Sprint(v), "timed out")) {
				continue
			}
			if !reflect.DeepEqual(v, g[k]) {
				return fmt.Sprintf("run.%s is %s, recorded %s", k, brief(jsonText(g[k]), 300), brief(jsonText(v), 300))
			}
		}
		return ""
	}
	for _, k := range keys {
		if !reflect.DeepEqual(want[k], got[k]) {
			return fmt.Sprintf("%s differs:\n  recorded: %s\n  loop:     %s", k, brief(jsonText(want[k]), 600), brief(jsonText(got[k]), 600))
		}
	}
	return ""
}

func diffCalls(want, got any) string {
	norm := func(v any) []string {
		var out []string
		for _, c := range recordedCalls(v) {
			out = append(out, c.ID+" "+c.Function.Name+" "+string(c.Function.Arguments))
		}
		return out
	}
	w, g := norm(want), norm(got)
	if !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("tool calls differ:\n  recorded: %s\n  loop:     %s", brief(strings.Join(w, " | "), 600), brief(strings.Join(g, " | "), 600))
	}
	return ""
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// lineWriter collects whole lines.
type lineWriter struct{ buf bytes.Buffer }

func (w *lineWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *lineWriter) take() [][]byte {
	var out [][]byte
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			return out
		}
		line := make([]byte, i)
		copy(line, w.buf.Next(i+1)[:i])
		out = append(out, line)
	}
}

// recordedCalls reads an assistant event's tool calls. PowerShell logs a
// single call as an object rather than a one-element array, and none as null.
func recordedCalls(v any) []llm.ToolCall {
	if _, one := v.(map[string]any); one {
		v = []any{v}
	}
	raw, _ := json.Marshal(v)
	var calls []llm.ToolCall
	json.Unmarshal(raw, &calls)
	return calls
}