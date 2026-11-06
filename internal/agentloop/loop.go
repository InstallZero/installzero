// Package agentloop is the model loop the probe, the installer agent and the
// doctor share: tool calling with a visible trail (budget, condensed
// reasoning, purpose, finding), bounded by turns and time. A line-for-line
// port of agent/AgentLoop.ps1; behaviour changes go into both until the
// PowerShell engine is retired.
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/llm"
	"github.com/InstallZero/installzero/internal/psrx"
	"github.com/InstallZero/installzero/prompts"
)

// MaxToolChars is how much of a tool's output the model sees.
const MaxToolChars = 6000

// Model answers one turn; *llm.Client is one.
type Model interface {
	Turn(ctx context.Context, messages []llm.Message, tools []json.RawMessage, temperature float64, maxTokens int, extra json.RawMessage, stream bool) (*llm.Reply, error)
}

// Session is the model a run talks to.
type Session struct {
	Model Model
	Label string
	// Context is the model's context in tokens (32768 when 0).
	Context int
	// Request holds settings merged into every request (a GGUF's .iz.json).
	Request json.RawMessage
	// Hosted is a model reached with an API key (hosted, or hf:); its
	// assistant turns without text are sent with null content.
	Hosted bool
	// LlamaServer is a model on llama-server (local or hf:): replies are
	// streamed so loops can be stopped, and cut-off or oversized requests
	// are trimmed and shortened rather than resent unchanged.
	LlamaServer bool
	// ServerExited reports whether a local llama-server died, and its log.
	ServerExited func() (bool, string)
}

// Options configure a run; the zero values of the optional ones match
// Invoke-AgentLoop's defaults.
type Options struct {
	System, User string
	Tools        []json.RawMessage
	// Dispatch runs a tool and returns its output.
	Dispatch     func(name string, args *Args) (string, error)
	MaxTurns     int // 40 when 0
	MaxToolCalls int // MaxTurns*5 when 0
	Timeout      time.Duration
	Transcript   string
	// Log receives the transcript instead of the Transcript file when set.
	Log        io.Writer
	MaxTokens  int    // 4096 when 0
	FinishName string // "finish" when ""
	// FinishGuard returns "" to accept finish, or a message sending the
	// model back to work.
	FinishGuard func(finish *Args, lastLongText string) string
	// Checkpoint returns a message to add before the next turn, or "".
	// Called every turn after the first.
	Checkpoint func(run *Run) string
	// FinalTools are still offered once the budget is spent, and FinalNudge
	// is what the model is told then.
	FinalTools []string
	FinalNudge string
	// ResultSaved reports whether the result the final tools write exists.
	// While it does not, turns that force a result offer only the final
	// tools, not finish.
	ResultSaved func() bool
	// ParseFailureNudge is sent when replies keep coming back cut off or
	// unparseable (a long tool argument is the usual cause); only finish is
	// offered then.
	ParseFailureNudge string
	// Identical calls within one reply run once; beyond MaxCallsPerTurn
	// (10 when 0), the rest are not run.
	MaxCallsPerTurn int
	// UnchangedWriteHint is told to a model that re-sends its last result
	// unchanged: what to look for instead.
	UnchangedWriteHint string
	// Trail receives the progress lines; nil for none.
	Trail io.Writer
	// Sleep waits between retries of a hosted model (time.Sleep when nil).
	Sleep func(time.Duration)
	// Now is the clock the time limit is measured on (time.Now when nil).
	Now func() time.Time
}

// Run is a run's record, written to the transcript's result event.
type Run struct {
	Steps            int     `json:"steps"`
	ToolCalls        int     `json:"tool_calls"`
	InvalidCalls     int     `json:"invalid_calls"`
	Repeats          int     `json:"repeats"`
	Blocked          int     `json:"blocked"`
	LoopsStopped     int     `json:"loops_stopped"`
	Nudges           int     `json:"nudges"`
	ServerErrors     int     `json:"server_errors"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Finished         bool    `json:"finished"`
	StopReason       string  `json:"stop_reason"`
	Error            string  `json:"error"`
	Seconds          float64 `json:"seconds"`
	LastLongText     string  `json:"last_long_text"`
	// Finish holds the finish call's arguments when the model concluded.
	Finish *Args `json:"Finish"`
}

var (
	loopStopped   = regexp.MustCompile(`(?i)the reply started repeating itself`)
	retryable     = regexp.MustCompile(`(?i)429|500|502|503|529|Internal Server Error|does not match|overloaded|rate limit|out of memory|failed to|context|could not be resolved|Unable to connect|connection was closed|underlying connection`)
	unparseable   = regexp.MustCompile(`(?i)"code"\s*:\s*500|server_error|parse|exceed`)
	tooManyTokens = regexp.MustCompile(`(?i)request \((\d+) tokens\) exceeds`)
	unknownTool   = regexp.MustCompile(`(?i)^error: unknown tool`)
	textCallTags  = psrx.MustCompile(`(?s)<tool_call>.*?(</tool_call>|$)`)
	finishInText  = psrx.MustCompile(`(?s)\{.*"(summary|status)".*\}`)
	resultLines   = regexp.MustCompile(`\r?\n`)
	trivialLine   = regexp.MustCompile(`(?i)^\[exit 0\]$|^-+$`)
	spaces        = regexp.MustCompile(`\s+`)
)

type sameRun struct {
	out string
	n   int
}

// promptError carries a missing prompt out of the loop.
type promptError struct{ err error }

func say(name string, v prompts.Values) string {
	t, err := prompts.Message(name, v)
	if err != nil {
		panic(promptError{err})
	}
	return t
}

// Loop runs the model until it calls finish, or the budget, the time or
// its errors end the run.
func Loop(ctx context.Context, s *Session, o Options) *Run {
	if o.MaxTurns <= 0 {
		o.MaxTurns = 40
	}
	if o.MaxToolCalls <= 0 {
		o.MaxToolCalls = o.MaxTurns * 5
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 4096
	}
	if o.FinishName == "" {
		o.FinishName = "finish"
	}
	if o.MaxCallsPerTurn <= 0 {
		o.MaxCallsPerTurn = 10
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	l := &loop{s: s, o: o, run: &Run{}}
	return l.start(ctx)
}

type loop struct {
	s   *Session
	o   Options
	run *Run

	tools              []json.RawMessage
	names              []string
	finalSet, writeSet []int
	context, maxTokens int
	charsPerToken      float64
	maxChars           int
	messages           []llm.Message
	partial            *llm.Partial
	transcript         io.Writer
	started            time.Time
	lastLongText       string
}

func (l *loop) line(format string, args ...any) {
	if l.o.Trail != nil {
		fmt.Fprintf(l.o.Trail, format+"\n", args...)
	}
}

func (l *loop) log(e any) {
	if l.transcript == nil {
		return
	}
	b, err := json.Marshal(e)
	if err == nil {
		l.transcript.Write(append(b, '\n'))
	}
}

func (l *loop) elapsed() time.Duration { return l.o.Now().Sub(l.started) }

func (l *loop) budgetChars() int {
	return min(400000, max(12000, roundInt(float64(l.context-l.maxTokens-1500)*l.charsPerToken)))
}

func (l *loop) setNames(set []int, sep string) string {
	var n []string
	for _, i := range set {
		n = append(n, l.names[i])
	}
	return strings.Join(n, sep)
}

func (l *loop) inSet(set []int, name string) bool {
	for _, i := range set {
		if l.names[i] == name {
			return true
		}
	}
	return false
}

func (l *loop) pick(set []int) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(set))
	for _, i := range set {
		out = append(out, l.tools[i])
	}
	return out
}

// resultSet is what a turn that forces a result offers: the final tools
// without finish while no result is saved, else with it.
func (l *loop) resultSet() []int {
	if len(l.writeSet) > 0 && l.o.ResultSaved != nil && !l.o.ResultSaved() {
		return l.writeSet
	}
	return l.finalSet
}

func (l *loop) add(m llm.Message) { l.messages = append(l.messages, m) }

func (l *loop) user(text string) { l.add(llm.Message{Role: "user", Content: llm.Text(text)}) }

func (l *loop) start(ctx context.Context) (run *Run) {
	o, s := l.o, l.s
	run = l.run
	if o.Log != nil {
		l.transcript = o.Log
	} else if o.Transcript != "" {
		if f, err := os.OpenFile(o.Transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			l.transcript = f
			defer f.Close()
		}
	}
	// Prompt plus reply must fit the context: cap the reply at a third of
	// it and trim old tool output so the conversation stays within the
	// rest. Characters per token start at 3 and are measured from each
	// reply's prompt_tokens (paths, JSON and command output run denser than
	// prose).
	l.context = s.Context
	if l.context == 0 {
		l.context = 32768
	}
	l.maxTokens = min(o.MaxTokens, roundInt(float64(l.context)/3))
	l.charsPerToken = 3.0
	l.maxChars = l.budgetChars()
	l.started = o.Now()

	defer func() {
		if p := recover(); p != nil {
			pe, ok := p.(promptError)
			if !ok {
				panic(p)
			}
			run.Error = pe.err.Error()
		}
		run.Seconds = math.Round(l.elapsed().Seconds()*10) / 10
		l.log(map[string]any{"type": "result", "run": run})
		if run.StopReason == "" {
			run.StopReason = "error"
		}
		run.LastLongText = l.lastLongText
		how := ""
		switch {
		case run.Finished:
			how = fmt.Sprintf("Concluded after %d turns, %d tool calls", run.Steps, run.ToolCalls)
		case run.Error != "":
			how = fmt.Sprintf("Stopped without a conclusion (%s)", run.Error)
		case run.StopReason == "no-tool-calls":
			how = "Stopped without a conclusion: three replies in a row had no usable tool call"
		case run.StopReason == "unchanged-writes":
			how = "Stopped without a conclusion: the model kept re-saving the same result"
		default:
			how = fmt.Sprintf("Stopped without a conclusion: the budget (%d turns) ran out", o.MaxTurns)
		}
		l.line("")
		where := ""
		if o.Transcript != "" {
			where = ", transcript " + o.Transcript
		}
		l.line("(%s, %ss%s)", how, formatSeconds(run.Seconds), where)
	}()

	if o.FinalNudge == "" {
		o.FinalNudge = say("loop_final", prompts.Values{"finish": o.FinishName})
	}
	if o.ParseFailureNudge == "" {
		o.ParseFailureNudge = say("loop_parse_failure", nil)
	}
	if o.UnchangedWriteHint == "" {
		o.UnchangedWriteHint = say("loop_unchanged_hint", nil)
	}
	l.o = o
	purpose, err := prompts.Parameter("purpose", nil)
	if err != nil {
		panic(promptError{err})
	}
	for _, t := range o.Tools {
		name := toolName(t)
		if name != o.FinishName {
			t = withProperty(t, "purpose", purpose)
		}
		l.tools = append(l.tools, t)
		l.names = append(l.names, name)
	}
	for i, n := range l.names {
		if n == o.FinishName || contains(o.FinalTools, n) {
			l.finalSet = append(l.finalSet, i)
		}
		if n != o.FinishName && contains(o.FinalTools, n) {
			l.writeSet = append(l.writeSet, i)
		}
	}
	l.messages = []llm.Message{{Role: "system", Content: llm.Text(o.System)}, {Role: "user", Content: llm.Text(o.User)}}
	l.log(map[string]any{"type": "start", "model": s.Label, "system": o.System, "user": o.User})

	if err := l.turns(ctx); err != nil {
		run.Error = err.Error()
	} else {
		run.Finished = run.Finish != nil
		if run.Finished {
			run.StopReason = "finished"
		}
	}
	return run
}

// salvage keeps the document from a reply stopped for looping when a run
// is about to end with no result saved.
func (l *loop) salvage() {
	p := l.partial
	if p == nil || l.o.ResultSaved == nil || l.o.ResultSaved() || !l.inSet(l.writeSet, p.Name) {
		return
	}
	doc := llm.LoopSalvage(p)
	if doc == "" {
		return
	}
	out, err := l.o.Dispatch(p.Name, NewArgs("content", doc))
	if err != nil {
		out = "error: " + err.Error()
	}
	l.log(map[string]any{"type": "salvaged", "name": p.Name, "chars": len16(doc), "output": LimitText(llm.PlainText(out), MaxToolChars)})
	l.line("  Saved the document from a reply stopped for looping (%d characters, repeated tail removed).", len16(doc))
}

func (l *loop) turns(ctx context.Context) error {
	o, s, run := l.o, l.s, l.run
	var guardRejections, idleTurns, finalNudges, loopTurns, unchangedWrites, blockedNoted int
	var lastErr, lastWriteKey, izOut string
	sameRuns := map[string]*sameRun{}
	forceResult := false
	all := make([]int, len(l.tools))
	for i := range all {
		all[i] = i
	}
	for run.Finish == nil {
		if l.elapsed() >= o.Timeout {
			run.Error = fmt.Sprintf("timed out after %s minutes", formatMinutes(o.Timeout))
			run.StopReason = "timeout"
			break
		}
		toolsNow, budgetOut := all, false
		if o.Checkpoint != nil && run.Steps > 0 {
			if note := o.Checkpoint(run); note != "" {
				l.line("  Note to the model: %s", brief(strings.Split(note, "\n")[0], 200))
				l.user(note)
			}
		}
		if run.Steps >= o.MaxTurns || run.ToolCalls >= o.MaxToolCalls {
			if finalNudges >= 3 {
				l.salvage()
				run.StopReason = "budget"
				break
			}
			finalNudges++
			run.Nudges++
			toolsNow, budgetOut = l.resultSet(), true
			l.line("  Budget spent: asking for the result now, offering only %s (attempt %d of 3).", l.setNames(toolsNow, " and "), finalNudges)
			l.user(o.FinalNudge)
		} else if forceResult {
			toolsNow = l.resultSet()
			l.line("  Asking for the result: offering only %s for one turn.", l.setNames(toolsNow, " and "))
		}
		forceResult = false
		l.maxChars = l.budgetChars()
		compress(l.messages, l.maxChars, 300)

		var r *llm.Reply
		attempt, parseFails, loopFails := 0, 0, 0
		loopNoted := false
		for _, temperature := range []float64{0.2, 0.6, 0.9, 0.9} {
			attempt++
			var err error
			r, err = s.Model.Turn(ctx, l.messages, l.pick(toolsNow), temperature, l.maxTokens, s.Request, s.LlamaServer)
			if err == nil {
				break
			}
			r = nil
			e := err.Error()
			lastErr = e
			if loopStopped.MatchString(e) {
				run.LoopsStopped++
				loopFails++
				if le, ok := err.(*llm.LoopError); ok && le.Partial != nil && (l.partial == nil || len16(l.partial.Args) < len16(le.Partial.Args)) {
					l.partial = le.Partial
				}
				l.log(map[string]any{"type": "loop_stopped", "temperature": temperature, "error": e})
				l.line("  Stopped a looping reply (attempt %d of 4): %s", attempt, brief(e, 160))
				if !loopNoted {
					loopNoted = true
					l.user(say("loop_repeating", nil))
				}
				continue
			}
			if !retryable.MatchString(e) {
				return err
			}
			run.ServerErrors++
			l.log(map[string]any{"type": "server_error", "temperature": temperature, "error": e})
			l.line("  Model error (attempt %d of 4): %s", attempt, brief(e, 200))
			if s.ServerExited != nil {
				if exited, logPath := s.ServerExited(); exited {
					return fmt.Errorf("llama-server exited (see %s); last error: %s", logPath, e)
				}
			}
			// A local reply that llama-server cannot parse was cut off or
			// malformed: resending the same request repeats it. Make room
			// first, then ask for a short reply without long tool arguments.
			if s.LlamaServer && unparseable.MatchString(e) {
				parseFails++
				if m := tooManyTokens.FindStringSubmatch(e); m != nil {
					var n float64
					fmt.Sscan(m[1], &n)
					l.charsPerToken = math.Max(1.0, math.Min(l.charsPerToken, float64(measureAll(l.messages))/n))
					l.maxChars = l.budgetChars()
				}
				if parseFails == 1 {
					l.line("  The reply was cut off or unparseable: trimming the conversation to make room and retrying.")
					compress(l.messages, roundInt(float64(l.maxChars)/2), 150)
				} else if parseFails == 2 {
					l.line("  Still unparseable: asking for a short reply, with only finish offered.")
					l.user(o.ParseFailureNudge)
					toolsNow = nil
					for i, n := range l.names {
						if n == o.FinishName {
							toolsNow = append(toolsNow, i)
						}
					}
				}
			} else if s.Hosted {
				o.Sleep(time.Duration(10*attempt) * time.Second)
			}
		}
		// A turn whose every attempt looped costs a turn, not the run: the
		// next asks for a compact result (up to 3 such turns in a row).
		if r == nil && loopFails == attempt && loopTurns < 2 {
			loopTurns++
			run.Steps++
			forceResult = true
			l.line("  Every reply this turn looped; asking for a compact result next (%d of 3).", loopTurns)
			l.user(say("loop_keep_repeating", prompts.Values{"tools": l.setNames(l.resultSet(), " or ")}))
			continue
		}
		if r != nil {
			loopTurns = 0
		}
		if r == nil {
			l.salvage()
			run.StopReason = "model-errors"
			return fmt.Errorf("the model failed to produce a usable reply four times in a row; last error: %s", brief(lastErr, 400))
		}
		run.Steps++
		if r.Usage != nil {
			run.PromptTokens += r.Usage.PromptTokens
			run.CompletionTokens += r.Usage.CompletionTokens
			if r.Usage.PromptTokens > 4000 {
				l.charsPerToken = math.Max(1.0, math.Min(4.0, float64(measureAll(l.messages))/float64(r.Usage.PromptTokens)))
			}
		}
		msg := r.Message
		calls := msg.ToolCalls
		text := llm.PlainText(msg.ContentText())
		reasoning := llm.PlainText(msg.ReasoningContent)
		if len(calls) == 0 {
			calls = TextToolCalls(msg.ContentText()+"\n"+msg.ReasoningContent, run.Steps)
			if len(calls) > 0 {
				text = strings.TrimSpace(textCallTags.Replace(text, ""))
			}
		}
		logged := calls
		if logged == nil {
			logged = []llm.ToolCall{}
		}
		l.log(map[string]any{"type": "assistant", "content": text, "reasoning": reasoning, "tool_calls": logged, "usage": r.Usage})
		if len16(text) > 500 {
			l.lastLongText = text
			run.LastLongText = text
		}
		l.line("")
		l.line("Turn %d of %d: %d tool calls so far, %s elapsed (stops at %s min)", run.Steps, o.MaxTurns, run.ToolCalls, formatElapsed(l.elapsed()), formatMinutes(o.Timeout))
		if strings.TrimSpace(reasoning) != "" {
			l.line("  Thinking: %s", brief(reasoning, 240))
		}
		if strings.TrimSpace(text) != "" {
			l.line("  Says: %s", brief(text, 240))
		}
		if len(calls) == 0 {
			if g := finishInText.Groups(text); g != nil {
				if a, err := parseArgs(g[0]); err == nil && a != nil {
					run.Finish = a
					break
				}
			}
			if idleTurns >= 3 {
				run.StopReason = "no-tool-calls"
				if budgetOut {
					run.StopReason = "budget"
				}
				break
			}
			idleTurns++
			run.Nudges++
			l.add(llm.Message{Role: "assistant", Content: llm.Text(text)})
			l.user(say("loop_continue", prompts.Values{"finish": o.FinishName}))
			continue
		}
		idleTurns = 0
		var unique []llm.ToolCall
		seen := map[string]bool{}
		for _, c := range calls {
			k := strings.ToLower(c.Function.Name + "|" + argText(c.Function.Arguments))
			if !seen[k] {
				seen[k] = true
				unique = append(unique, c)
			}
		}
		duplicates := len(calls) - len(unique)
		calls = unique
		overCap := max(0, len(calls)-o.MaxCallsPerTurn)
		if overCap > 0 {
			calls = calls[:o.MaxCallsPerTurn]
		}
		if duplicates > 0 || overCap > 0 {
			l.line("  Dropped %d duplicate and %d excess calls from this reply.", duplicates, overCap)
		}
		echo := make([]llm.ToolCall, 0, len(calls))
		for _, c := range calls {
			a := argText(c.Function.Arguments)
			if !validJSON(a) {
				a = "{}"
			}
			echo = append(echo, llm.ToolCall{ID: c.ID, Type: "function", Function: llm.FunctionCall{Name: c.Function.Name, Arguments: llm.Arguments(a)}, ExtraContent: c.ExtraContent})
		}
		assistant := llm.Message{Role: "assistant", Content: llm.Text(text), ToolCalls: echo, ResponsesItems: msg.ResponsesItems}
		if s.Hosted && strings.TrimSpace(text) == "" {
			assistant.Content = nil
		}
		l.add(assistant)
		for _, call := range calls {
			run.ToolCalls++
			name := call.Function.Name
			raw := argText(call.Function.Arguments)
			args, err := parseArgs(raw)
			if err != nil {
				run.InvalidCalls++
				izOut = say("loop_invalid_json", prompts.Values{"chars": len16(raw), "name": name, "start": brief(head16(raw, 300), 300)})
			}
			if args != nil {
				guard := ""
				if name == o.FinishName && o.FinishGuard != nil && guardRejections < 2 {
					guard = o.FinishGuard(args, l.lastLongText)
				}
				switch {
				case guard != "":
					guardRejections++
					izOut = guard
					if len(l.writeSet) > 0 && o.ResultSaved != nil && !o.ResultSaved() {
						forceResult = true
					}
					l.line("  Not done yet: %s", brief(strings.Split(guard, "\n")[0], 200))
				case name == o.FinishName:
					run.Finish = args
					izOut = say("recorded", nil)
					l.line("  Concluding.")
				default:
					purpose := args.String("purpose")
					shown := args.JSON("purpose", "content")
					if shown == "{}" {
						shown = ""
					}
					if strings.TrimSpace(purpose) != "" {
						l.line("  Doing: %s", brief(purpose, 200))
					}
					if r := []rune(shown); len(r) > 140 {
						shown = string(r[:140]) + "..."
					}
					l.line("    %s %s", name, shown)
					// A call that returned the same result three times in a
					// row is not run again (a changed result, as after a
					// fix, resets it).
					argKey := strings.ToLower(name + "|" + args.JSON("purpose"))
					same := sameRuns[argKey]
					// Re-saving the document unchanged reruns its live checks,
					// whose output varies, so the guard below never sees it as a
					// repeat; small models can spend the rest of the run doing it.
					isWrite := l.inSet(l.writeSet, name)
					switch {
					case budgetOut && !l.inSet(l.finalSet, name):
						izOut = say("loop_budget_spent", prompts.Values{"tools": l.setNames(l.finalSet, " and ")})
					case isWrite && argKey == lastWriteKey:
						run.Repeats++
						run.Blocked++
						unchangedWrites++
						left := max(0, o.MaxTurns-run.Steps)
						izOut = say("loop_unchanged_write", prompts.Values{"name": name, "left": left, "max_turns": o.MaxTurns, "hint": o.UnchangedWriteHint, "finish": o.FinishName, "count": unchangedWrites + 1})
						if (unchangedWrites >= 4 || (unchangedWrites >= 2 && left <= 3)) && o.ResultSaved != nil && o.ResultSaved() {
							run.StopReason = "unchanged-writes"
						}
					case same != nil && same.n >= 3:
						run.Repeats++
						run.Blocked++
						izOut = say("loop_blocked_call", prompts.Values{"count": same.n})
					default:
						out, err := o.Dispatch(name, args)
						if err != nil {
							out = "error: " + err.Error()
						}
						izOut = out
						if isWrite {
							lastWriteKey = argKey
							unchangedWrites = 0
						}
						if unknownTool.MatchString(izOut) {
							run.InvalidCalls++
						}
						if same != nil && same.out == izOut {
							same.n++
						} else {
							same = &sameRun{out: izOut, n: 1}
							sameRuns[argKey] = same
						}
						if same.n >= 2 {
							run.Repeats++
							izOut += say("loop_same_result", prompts.Values{"count": same.n, "last": same.n >= 3})
						}
					}
					first := ""
					for _, x := range resultLines.Split(izOut, -1) {
						if x = strings.TrimSpace(x); x != "" && !trivialLine.MatchString(x) {
							first = x
							break
						}
					}
					l.line("    Result: %s", brief(first, 160))
				}
			}
			izOut = LimitText(llm.PlainText(izOut), MaxToolChars)
			l.log(map[string]any{"type": "tool", "name": name, "arguments": raw, "output": izOut})
			l.add(llm.Message{Role: "tool", ToolCallID: call.ID, Content: llm.Text(izOut)})
		}
		if run.StopReason == "unchanged-writes" {
			l.line("  The model keeps re-saving the same document: ending the run with it.")
			break
		}
		if duplicates > 0 || overCap > 0 {
			var notRun []string
			if duplicates > 0 {
				notRun = append(notRun, say("loop_not_run_duplicates", prompts.Values{"count": duplicates}))
			}
			if overCap > 0 {
				notRun = append(notRun, say("loop_not_run_over_cap", prompts.Values{"count": overCap, "max": o.MaxCallsPerTurn}))
			}
			last := &l.messages[len(l.messages)-1]
			last.Content = llm.Text(last.ContentText() + say("loop_not_run", prompts.Values{"calls": strings.Join(notRun, " and ")}))
		}
		// A model that keeps re-sending blocked calls is told to move on at
		// 5, and at every 10 gets one turn with only the result tools.
		if run.Blocked >= 5 && blockedNoted < 5 {
			blockedNoted = 5
			l.user(say("loop_blocked_move_on", prompts.Values{"count": run.Blocked, "tools": l.setNames(l.finalSet, " or ")}))
		}
		if run.Blocked >= blockedNoted+10 || (run.Blocked >= 10 && blockedNoted < 10) {
			blockedNoted = 10 * (run.Blocked / 10)
			forceResult = true
			l.user(say("loop_blocked_result_only", prompts.Values{"count": run.Blocked, "tools": l.setNames(l.finalSet, " and ")}))
		}
	}
	return nil
}

// argText is a call's argument text; null arrives as "{}".
func argText(a llm.Arguments) string { return string(a) }

func measure(m llm.Message) int {
	n := len16(m.ContentText())
	for _, c := range m.ToolCalls {
		n += len16(string(c.Function.Arguments))
	}
	return n
}

func measureAll(ms []llm.Message) int {
	n := 0
	for _, m := range ms {
		n += measure(m)
	}
	return n
}

// compress trims the oldest tool outputs (and long tool-call arguments,
// such as earlier drafts) until the conversation fits maxChars; the latest
// message is kept.
func compress(ms []llm.Message, maxChars, keep int) {
	total := measureAll(ms)
	for i := 2; i < len(ms)-1 && total > maxChars; i++ {
		m := &ms[i]
		before := measure(*m)
		if m.Role == "tool" && len16(m.ContentText()) > keep+100 {
			m.Content = llm.Text(head16(m.ContentText(), keep) + "\n[older output trimmed]")
		}
		for j := range m.ToolCalls {
			c := &m.ToolCalls[j].Function
			if n := len16(string(c.Arguments)); n > 2000 {
				c.Arguments = llm.Arguments(fmt.Sprintf(`{"trimmed":"earlier argument of %d characters removed to save space"}`, n))
			}
		}
		total -= before - measure(*m)
	}
}

// Brief is Format-AgentBrief: whitespace collapsed, and a long text shown
// as its end, from a sentence start when one is near.
func Brief(text string, max int) string { return brief(text, max) }

func brief(text string, max int) string {
	t := strings.TrimSpace(spaces.ReplaceAllString(text, " "))
	r := []rune(t)
	if len(r) <= max {
		return t
	}
	tail := string(r[len(r)-max:])
	if cut := strings.Index(tail, ". "); cut >= 0 && len([]rune(tail[:cut])) < max/2 {
		tail = tail[cut+2:]
	}
	return "... " + tail
}

func toolName(raw json.RawMessage) string {
	var t struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	json.Unmarshal(raw, &t)
	return t.Function.Name
}

// roundInt is PowerShell's [int] cast: to the nearest, halves to even.
func roundInt(f float64) int { return int(math.RoundToEven(f)) }

func formatSeconds(s float64) string { return fmt.Sprint(s) }

func formatMinutes(d time.Duration) string { return fmt.Sprint(d.Minutes()) }

func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}
