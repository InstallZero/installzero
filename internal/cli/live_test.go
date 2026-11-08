package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/InstallZero/installzero/internal/ui"
)

func TestLinePrefixerProgress(t *testing.T) {
	t.Setenv("FORCE_COLOR", "")
	var buf bytes.Buffer
	term := ui.New(&buf)
	l := &linePrefixer{w: term, term: term, prefix: "| "}
	l.Write([]byte("Downloading gym.zip\n##progress 10 gym.zip\n##progress 3/4\n"))
	l.Write([]byte("\r###      12.5%\r######   45.0%"))
	l.Write([]byte("\r##########  100.0%\r"))
	l.Write([]byte("\ndone\r\nlast"))
	l.Done()
	want := "| Downloading gym.zip\n| ##########  100.0%\n| done\n| last\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestStatusLine(t *testing.T) {
	if got := ui.Bar(0.5, 10); got != "[█████░░░░░]" {
		t.Fatalf("bar %q", got)
	}
	t.Setenv("FORCE_COLOR", "")
	var buf bytes.Buffer
	term := ui.New(&buf)
	term.SetStatus(ui.Status{Done: 1, Total: 4, Step: "gyms"})
	term.SetSub(40, "gym.zip")
	term.Write([]byte("line\n"))
	if got := buf.String(); got != "line\n" || strings.Contains(got, "\x1b") {
		t.Fatalf("a non-terminal got %q", got)
	}
}
