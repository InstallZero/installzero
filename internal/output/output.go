// Package output turns what an install command printed into what a person
// or a model should read: progress bars and colour codes removed, repeated
// lines collapsed, and long output cut to its start, its end and the lines
// that look like errors.
package output

import (
	"fmt"
	"strings"

	"github.com/InstallZero/installzero/internal/psrx"
)

var (
	ansi      = psrx.MustCompileCase(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	digits    = psrx.MustCompileCase(`\d+`)
	errorLike = psrx.MustCompile(`\b(error|err!|fatal|failed|failure|cannot|can't|could not|couldn't|unable to|denied|not found|no such|missing|traceback|exception|panic|conflict|unmet|broken|invalid|refused|timed out|killed)\b|^E: |^npm ERR|^\s*\^+\s*$`)
	prompts   = psrx.MustCompile(`(\[y/n\]|\(y/n\)|\[yes/no\]|\(yes/no\)|continue\?|proceed\?|password( for [^:]*)?:|passphrase|press (any key|enter|return)|enter [^:]{1,40}:|\?\s*$|:\s*$)`)
)

// Clean applies carriage returns the way a terminal does (a line keeps what
// was written after its last \r), removes colour and cursor codes, and
// trims trailing spaces.
func Clean(s string) string {
	s = ansi.Replace(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if k := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); k >= 0 {
			l = l[k+1:]
		}
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// Collapse replaces runs of lines that differ only in their numbers (a
// download's progress, a counter) with the run's first and last lines and a
// count.
func Collapse(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); {
		key := digits.Replace(lines[i], "#")
		j := i + 1
		for j < len(lines) && digits.Replace(lines[j], "#") == key {
			j++
		}
		switch n := j - i; {
		case n <= 3:
			out = append(out, lines[i:j]...)
		default:
			out = append(out, lines[i], fmt.Sprintf("[... %d similar lines]", n-2), lines[j-1])
		}
		i = j
	}
	return out
}

// Summarize is Clean and Collapse, then, when more than max characters are
// left, the first head lines, the error-like lines between, and the last
// lines that fit. max 0 is 6000.
func Summarize(s string, max int) string {
	if max <= 0 {
		max = 6000
	}
	var lines []string
	for _, l := range Collapse(strings.Split(Clean(s), "\n")) {
		if strings.TrimSpace(l) != "" || (len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "") {
			lines = append(lines, l)
		}
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(text) <= max {
		return text
	}
	const head = 15
	budget := max
	take := func(l string) bool {
		if len(l)+1 > budget {
			return false
		}
		budget -= len(l) + 1
		return true
	}
	var first []string
	for i := 0; i < len(lines) && i < head && take(lines[i]); i++ {
		first = append(first, lines[i])
	}
	// The end matters most (where the command stopped), then the errors.
	tailBudget := budget * 2 / 3
	var last []string
	for i := len(lines) - 1; i >= len(first); i-- {
		if len(lines[i])+1 > tailBudget {
			break
		}
		tailBudget -= len(lines[i]) + 1
		budget -= len(lines[i]) + 1
		last = append([]string{lines[i]}, last...)
	}
	var errs []string
	cut := 0
	for i := len(first); i < len(lines)-len(last); i++ {
		if errorLike.Match(lines[i]) && take(lines[i]) {
			errs = append(errs, lines[i])
		} else {
			cut++
		}
	}
	out := first
	if len(errs) > 0 {
		out = append(out, fmt.Sprintf("[... %d lines cut; error-like lines kept:]", cut))
		out = append(out, errs...)
		out = append(out, "[... end of error-like lines]")
	} else if cut > 0 {
		out = append(out, fmt.Sprintf("[... %d lines cut]", cut))
	}
	return strings.Join(append(out, last...), "\n")
}

// WaitingForInput returns the last line of a command's output when it reads
// like a question waiting for an answer (for a command that hung until its
// timeout); "" otherwise.
func WaitingForInput(s string) string {
	lines := strings.Split(strings.TrimRight(Clean(s), "\n "), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last != "" && len(last) < 200 && prompts.Match(last) {
		return last
	}
	return ""
}
