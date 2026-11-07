package output

import (
	"fmt"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	in := "\x1b[32mok\x1b[0m  \r\nDownloading  10%\rDownloading  55%\rDownloading 100%\ndone"
	want := "ok\nDownloading 100%\ndone"
	if got := Clean(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCollapse(t *testing.T) {
	var in []string
	in = append(in, "start")
	for i := 1; i <= 50; i++ {
		in = append(in, fmt.Sprintf("Get:%d http://archive.ubuntu.com/ubuntu noble/main amd64 lib%d [%d kB]", i, i, i*3))
	}
	in = append(in, "a 1", "a 2", "end")
	got := strings.Join(Collapse(in), "\n")
	want := "start\nGet:1 http://archive.ubuntu.com/ubuntu noble/main amd64 lib1 [3 kB]\n[... 48 similar lines]\nGet:50 http://archive.ubuntu.com/ubuntu noble/main amd64 lib50 [150 kB]\na 1\na 2\nend"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestSummarizeKeepsStartEndAndErrors(t *testing.T) {
	var b strings.Builder
	b.WriteString("Reading package lists...\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "Setting up package-%c%c ...\n", 'a'+i%26, 'a'+i/26)
		if i == 200 {
			b.WriteString("E: Unable to locate package libfoo-dev\n")
		}
	}
	b.WriteString("Processing triggers for man-db\nexit status 100\n")
	got := Summarize(b.String(), 2000)
	if len(got) > 2200 {
		t.Errorf("summary is %d characters", len(got))
	}
	for _, want := range []string{"Reading package lists...", "E: Unable to locate package libfoo-dev", "exit status 100", "lines cut"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestSummarizeShortIsUnchanged(t *testing.T) {
	if got := Summarize("one\ntwo\n\n\n\nthree\n", 0); got != "one\ntwo\n\nthree" {
		t.Errorf("got %q", got)
	}
}

func TestWaitingForInput(t *testing.T) {
	for in, want := range map[string]string{
		"Reading state...\nDo you want to continue? [Y/n] ": "Do you want to continue? [Y/n]",
		"[sudo] password for ben: ":                         "[sudo] password for ben:",
		"Built wheel\nSuccessfully installed foo-1.0\n":     "",
	} {
		if got := WaitingForInput(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
