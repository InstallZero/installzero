// Package ui draws installzero's terminal output: colors, and a status line
// at the bottom of the screen with a progress bar that output scrolls
// above. Neither is drawn when the output is not a terminal (a log file, a
// pipe) or NO_COLOR is set; FORCE_COLOR turns colors on regardless.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Styles.
const (
	Bold   = "1"
	Dim    = "2"
	Red    = "31"
	Green  = "32"
	Yellow = "33"
	Cyan   = "36"
)

// Term writes to the terminal. Everything printed while a status line is
// shown goes through Write, which keeps the status line below it.
type Term struct {
	mu     sync.Mutex
	w      io.Writer
	color  bool
	live   bool
	width  func() int
	status *Status
	note   string // a line drawn in place of the status line (a download)
	shown  bool
	atBOL  bool // the last write ended a line (no prompt is waiting)
	stop   chan struct{}
	frame  int
}

// Status is what the status line shows.
type Status struct {
	Done, Total int    // steps finished, steps in the plan
	Step        string // the running step
	Block       string // the running block
	Started     time.Time
	// Sub is a percentage reported by the running block (a download), or
	// -1; SubLabel names it.
	Sub      float64
	SubLabel string
	subAt    time.Time
}

// New is a Term over w, with colors and the status line when w is a
// terminal.
func New(w io.Writer) *Term {
	t := &Term{w: w, atBOL: true, width: func() int { return 80 }}
	f, ok := w.(*os.File)
	tty := ok && isTerminal(f)
	if tty {
		t.width = func() int { return termWidth(f) }
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	force := os.Getenv("FORCE_COLOR") != "" && os.Getenv("FORCE_COLOR") != "0"
	t.color = force || (tty && !noColor && os.Getenv("TERM") != "dumb")
	t.live = tty && !noColor && os.Getenv("TERM") != "dumb"
	return t
}

// Color reports whether styles are drawn.
func (t *Term) Color() bool { return t.color }

// Live reports whether the status line is drawn.
func (t *Term) Live() bool { return t.live }

// Paint styles s (Bold, Red, ...; several may be given).
func (t *Term) Paint(s string, styles ...string) string {
	if !t.color || len(styles) == 0 || s == "" {
		return s
	}
	return "\x1b[" + strings.Join(styles, ";") + "m" + s + "\x1b[0m"
}

func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.erase()
	n, err := t.w.Write(p)
	if len(p) > 0 {
		t.atBOL = p[len(p)-1] == '\n'
	}
	t.draw()
	return n, err
}

// SetStatus shows s on the status line (from now on, when the terminal
// is live). A block's own progress carries over while the step and block
// stay the same.
func (t *Term) SetStatus(s Status) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s.Started.IsZero() {
		s.Started = time.Now()
	}
	s.Sub, s.SubLabel = -1, ""
	if p := t.status; p != nil && p.Step == s.Step && p.Block == s.Block {
		s.Sub, s.SubLabel, s.subAt = p.Sub, p.SubLabel, p.subAt
	}
	t.status = &s
	if t.live && t.stop == nil {
		t.stop = make(chan struct{})
		go t.tick(t.stop)
	}
	t.erase()
	t.draw()
}

// SetSub sets the running block's own progress (0-100), or clears it (-1).
func (t *Term) SetSub(percent float64, label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == nil {
		return
	}
	t.status.Sub, t.status.SubLabel, t.status.subAt = percent, label, time.Now()
	t.erase()
	t.draw()
}

// Clear removes the status line and stops drawing it.
func (t *Term) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.erase()
	t.status = nil
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
}

func (t *Term) tick(stop chan struct{}) {
	k := time.NewTicker(250 * time.Millisecond)
	defer k.Stop()
	for {
		select {
		case <-stop:
			return
		case <-k.C:
			t.mu.Lock()
			t.frame++
			if t.status != nil && t.status.Sub >= 0 && time.Since(t.status.subAt) > 20*time.Second {
				t.status.Sub, t.status.SubLabel = -1, ""
			}
			t.erase()
			t.draw()
			t.mu.Unlock()
		}
	}
}

func (t *Term) erase() {
	if t.shown {
		io.WriteString(t.w, "\r\x1b[2K")
		t.shown = false
	}
}

// draw shows the status line (or, without one, the note), unless a prompt
// is waiting for an answer.
func (t *Term) draw() {
	if !t.live || !t.atBOL {
		return
	}
	switch {
	case t.status != nil:
		io.WriteString(t.w, t.line(t.width()-1))
	case t.note != "":
		io.WriteString(t.w, t.note)
	default:
		return
	}
	t.shown = true
}

// Meter shows a download's progress: redrawn in place on a terminal, a
// line at each quarter otherwise.
type Meter struct {
	t       *Term
	w       io.Writer
	label   string
	total   int64
	done    int64
	quarter int64
	drawn   time.Time
}

// NewMeter is a Meter for total bytes (0 when unknown) on w.
func NewMeter(w io.Writer, label string, total int64) *Meter {
	m := &Meter{w: w, label: label, total: total}
	if t, ok := w.(*Term); ok {
		m.t = t
	} else if f, ok := w.(*os.File); ok {
		m.t = New(f)
	}
	return m
}

// Write counts p.
func (m *Meter) Write(p []byte) (int, error) {
	m.done += int64(len(p))
	switch {
	case m.t != nil && m.t.live:
		if time.Since(m.drawn) > 100*time.Millisecond {
			m.drawn = time.Now()
			m.t.setNote(m.text())
		}
	case m.total > 0:
		if q := 4 * m.done / m.total; q > m.quarter && q < 4 {
			m.quarter = q
			fmt.Fprintf(m.w, "  %s: %d%% (%s of %s)\n", m.label, 25*q, Bytes(m.done), Bytes(m.total))
		}
	}
	return len(p), nil
}

// Done ends the meter.
func (m *Meter) Done() {
	if m.t != nil {
		m.t.setNote("")
	}
}

func (m *Meter) text() string {
	if m.total <= 0 {
		return m.t.Paint(fmt.Sprintf("  %s %s", m.label, Bytes(m.done)), Cyan)
	}
	frac := float64(m.done) / float64(m.total)
	return m.t.Paint(fmt.Sprintf("  %s %s %3.0f%%", m.label, Bar(frac, 30), 100*frac), Cyan) +
		m.t.Paint(fmt.Sprintf(" %s of %s", Bytes(m.done), Bytes(m.total)), Dim)
}

func (t *Term) setNote(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.erase()
	t.note = s
	t.draw()
}

func (t *Term) line(width int) string {
	s := t.status
	spin := `|/-\`[t.frame%4 : t.frame%4+1]
	frac := 0.0
	if s.Total > 0 {
		frac = float64(s.Done) / float64(s.Total)
	}
	head := fmt.Sprintf(" %s %s %d/%d ", spin, Bar(frac, 20), s.Done, s.Total)
	text := s.Step
	if s.Block != "" {
		text += " · " + s.Block
	}
	text += " · " + Elapsed(time.Since(s.Started))
	if s.Sub >= 0 {
		label := s.SubLabel
		if label != "" {
			label += " "
		}
		text += fmt.Sprintf(" · %s%s %3.0f%%", label, Bar(s.Sub/100, 10), s.Sub)
	}
	plain := head + text
	if n := utf8.RuneCountInString(plain); n > width && width > 10 {
		r := []rune(text)
		cut := len(r) - (n - width) - 1
		if cut < 0 {
			cut = 0
		}
		text = string(r[:cut]) + "…"
	}
	return t.Paint(head, Cyan) + t.Paint(text, Dim)
}

// Bar is a progress bar of width cells for frac (0-1).
func Bar(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac*float64(width) + 0.5)
	return "[" + strings.Repeat("█", n) + strings.Repeat("░", width-n) + "]"
}

// Elapsed is d as 42s, 3m05s or 1h02m.
func Elapsed(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// Bytes is n as 512 B, 3.4 MB or 1.20 GB.
func Bytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}
