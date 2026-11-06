package llm

import (
	"testing"

	"github.com/InstallZero/installzero/internal/fixture"
)

func want(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestRepeatLoopFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Text string
		Unit *string
	}](t, "loops/repeat.json") {
		if got := FindRepeatLoop(c.Text); got != want(c.Unit) {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Text), got, want(c.Unit))
		}
	}
}

func TestTemplateLoopFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Text string
		Line *string
	}](t, "loops/template.json") {
		if got := FindTemplateLoop(c.Text); got != want(c.Line) {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Text), got, want(c.Line))
		}
	}
}

func TestLoopSalvageFixtures(t *testing.T) {
	for _, c := range fixture.Load[struct {
		Partial Partial
		Content *string
	}](t, "loops/salvage.json") {
		if got := LoopSalvage(&c.Partial); got != want(c.Content) {
			t.Errorf("%q\n  got  %q\n  want %q", fixture.Short(c.Partial.Args), fixture.Short(got), fixture.Short(want(c.Content)))
		}
	}
}

func TestDecodeJSONString(t *testing.T) {
	for in, out := range map[string]string{
		`a\nb`: "a\nb", "a\nb": "a\nb", `a\'b`: "a'b", `\u00e9\/`: "é/", `\ud83d\ude00`: "😀", `\ud83d`: "\ufffd", ``: "",
	} {
		if got, ok := decodeJSONString(in); !ok || got != out {
			t.Errorf("decodeJSONString(%q) = %q, %v; want %q", in, got, ok, out)
		}
	}
	for _, in := range []string{`a\xb`, `a\u12`, `a\`, `a"b`} {
		if got, ok := decodeJSONString(in); ok {
			t.Errorf("decodeJSONString(%q) = %q; want an error", in, got)
		}
	}
}
