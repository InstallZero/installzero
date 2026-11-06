package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/secrets"
)

// CallsResult is what CallTools writes: each call's output, and the
// counters the calls moved.
type CallsResult struct {
	Outputs        []string    `json:"outputs"`
	HiddenAttempts int         `json:"hidden_attempts"`
	WithheldLines  int         `json:"withheld_lines"`
	GuardedLines   int         `json:"guarded_lines"`
	DraftChecks    *DraftStats `json:"draft_checks"`
}

// CallTools runs the tool calls in file ([{name, arguments}]) as the model
// would make them, without a model (Probe.ps1 -ToolCalls).
func CallTools(ctx context.Context, o Options, file string) (*CallsResult, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var calls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\ufeff")), &calls); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	p, err := newProbe(o)
	if err != nil {
		return nil, err
	}
	p.known = secrets.Known(p.o.Root, os.Environ())
	p.blind.guard(ctx, func(ctx context.Context, script string, timeout time.Duration) string {
		return p.target.Bash(ctx, script, timeout, "").Out
	}, p.linuxDir)
	p.machine = p.target.Profile(ctx)
	res := &CallsResult{Outputs: []string{}}
	for _, c := range calls {
		a, err := objectArgs(c.Arguments)
		out := ""
		if err == nil {
			out, err = p.dispatch(ctx, c.Name, a)
		}
		if err != nil {
			out = "error: " + err.Error()
		}
		res.Outputs = append(res.Outputs, out)
	}
	res.HiddenAttempts, res.WithheldLines, res.GuardedLines, res.DraftChecks = p.blind.attempts, p.blind.withheld, len(p.blind.leak), p.stats
	return res, nil
}

// objectArgs reads a JSON object as tool-call arguments, keys in order.
func objectArgs(raw json.RawMessage) (*agentloop.Args, error) {
	a := &agentloop.Args{Values: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return a, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("arguments are not a JSON object")
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		key := k.(string)
		if !a.Has(key) {
			a.Keys = append(a.Keys, key)
		}
		a.Values[key] = v
	}
	return a, nil
}
