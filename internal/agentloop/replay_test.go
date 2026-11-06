package agentloop

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/InstallZero/installzero/internal/fixture"
)

// Recorded probe runs (fixtures/transcripts) replayed through the loop.
func TestReplayFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixture.Dir(t), "transcripts", "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no transcripts in fixtures/transcripts")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			events, err := ReadTranscript(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := Replay(events); err != nil {
				t.Error(err)
			}
		})
	}
}

// IZ_REPLAY_DIR=<folder of probe transcripts> replays every one the loop can,
// from runs since IZ_REPLAY_SINCE (a transcript name prefix; by default the
// first runs with the loop as it is now; older ones ran earlier versions).
func TestReplayLocal(t *testing.T) {
	dir := os.Getenv("IZ_REPLAY_DIR")
	if dir == "" {
		t.Skip("set IZ_REPLAY_DIR to replay a folder of transcripts")
	}
	since := os.Getenv("IZ_REPLAY_SINCE")
	if since == "" {
		since = "20261105-2058"
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	passed, failed, skipped := 0, 0, map[string]int{}
	for _, f := range files {
		if filepath.Base(f) < since {
			skipped["before "+since]++
			continue
		}
		events, err := ReadTranscript(f)
		if err != nil {
			skipped["unreadable"]++
			continue
		}
		if why := ReplaySkip(events); why != "" {
			skipped[why]++
			continue
		}
		if err := Replay(events); err != nil {
			failed++
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		passed++
	}
	t.Logf("%d replayed identically, %d differ, skipped %v", passed, failed, skipped)
}
