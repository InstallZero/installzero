package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeedInRequest(t *testing.T) {
	s, _, err := StartSession(context.Background(), SessionOptions{URL: "http://127.0.0.1:1", Seed: 641874537})
	if err != nil || string(s.Request) != `{"seed":641874537}` {
		t.Fatalf("url session request %s, %v", s.Request, err)
	}
	r, err := withSeed(json.RawMessage(`{"chat_template_kwargs":{"enable_thinking":false}}`), 7)
	if err != nil || string(r) != `{"chat_template_kwargs":{"enable_thinking":false},"seed":7}` {
		t.Errorf("merged %s, %v", r, err)
	}
	if r, _ := withSeed(json.RawMessage(`{"a":1}`), 0); string(r) != `{"a":1}` {
		t.Errorf("seed 0 changed the settings: %s", r)
	}
	s, _, _ = StartSession(context.Background(), SessionOptions{URL: "http://127.0.0.1:1"})
	if s.Request != nil {
		t.Errorf("no seed, request %s", s.Request)
	}
}

// IZ_LLAMA_TEST=1 starts the local model and runs a few loop turns on it
// with fake tools (IZ_LLAMA_MODEL chooses the model).
func TestLoopOnLocalModel(t *testing.T) {
	if os.Getenv("IZ_LLAMA_TEST") == "" {
		t.Skip("set IZ_LLAMA_TEST=1 to start llama-server")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	var trail strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	s, stop, err := StartSession(ctx, SessionOptions{Root: root, Model: os.Getenv("IZ_LLAMA_MODEL"), Context: 16384, Trail: &trail})
	defer stop()
	if err != nil {
		t.Fatal(err)
	}
	tools := []json.RawMessage{
		json.RawMessage(`{"type":"function","function":{"name":"list_dir","description":"List a folder on this machine.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}`),
		json.RawMessage(`{"type":"function","function":{"name":"finish","description":"Report what you found.","parameters":{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}}}`),
	}
	var calls []string
	transcript := filepath.Join(t.TempDir(), "run.jsonl")
	run := Loop(ctx, s, Options{
		System: "You inspect a machine with tools. Use list_dir, then call finish with a one-sentence summary.",
		User:   "Find out which program folders exist under C:\\Program Files.",
		Tools:  tools, MaxTurns: 6, Transcript: transcript, Trail: &trail,
		Dispatch: func(name string, a *Args) (string, error) {
			calls = append(calls, name+" "+a.String("path"))
			return "Git\nNVIDIA Corporation\nPowerShell\nWindows Defender\n[exit 0]", nil
		},
	})
	t.Log(trail.String())
	if run.Error != "" || run.Steps == 0 || len(calls) == 0 {
		t.Fatalf("run: %+v, calls %v", run, calls)
	}
	events, err := ReadTranscript(transcript)
	if err != nil || events[0].str("type") != "start" || events[len(events)-1].str("type") != "result" {
		t.Errorf("transcript: %d events, %v", len(events), err)
	}
	t.Logf("stop %s after %d turns, calls %v, finish %s", run.StopReason, run.Steps, calls, run.Finish.JSON())
}
