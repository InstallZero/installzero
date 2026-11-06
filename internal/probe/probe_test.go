package probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/target"
)

// fakeTarget answers every script with exit 0 and the script's last word,
// except scripts containing "fail".
type fakeTarget struct {
	target.Unix
	scripts []string
}

func (f *fakeTarget) Bash(_ context.Context, script string, _ time.Duration, dir string) target.Result {
	f.scripts = append(f.scripts, dir+"$ "+script)
	if strings.Contains(script, "fail") {
		return target.Result{Code: 1, Out: "it failed"}
	}
	return target.Result{Out: "ran in " + dir}
}

func newFake() *fakeTarget {
	f := &fakeTarget{}
	f.Unix = target.Unix{Runner: f, Label: "wsl:fake"}
	return f
}

const root = `C:\Users\me\installzero`

func TestHiddenPaths(t *testing.T) {
	b := newBlind(root, `C:\out\x.install.md`, []string{`C:\stage\hidden, 'C:\more'`}, true)
	for path, want := range map[string]bool{
		"/opt/app/README.md":                    true,
		"/opt/app/docs/setup.txt":               true,
		"/opt/app/install.sh":                   true,
		"/opt/app/compose.yml":                  false,
		"/root/.bash_history":                   true,
		"/etc/wsl.conf":                         true,
		"/var/log/apt/history.log":              true,
		"/var/log/nginx/access.log":             false,
		`C:\stage\hidden\notes.txt`:             true,
		`c:\STAGE\hidden`:                       true,
		`C:\stage\hiddenother`:                  false,
		`C:\more\x`:                             true,
		"/mnt/c/Users/me/installzero/agent/x":   true,
		`C:\Users\me\installzero\prompts\a.md`:  true,
		`C:\Users\me\installzero\.env`:          true,
		`C:\out\x.install.md`:                   false,
		"cat /srv/eval-results.jsonl | head -3": true,
	} {
		if got := b.isHidden(path); got != want {
			t.Errorf("isHidden(%q) = %v, want %v", path, got, want)
		}
	}
	if len(b.paths) != 2 {
		t.Errorf("hide paths: %v", b.paths)
	}
}

func TestOwnLines(t *testing.T) {
	b := newBlind(root, "", nil, true)
	got := b.removeOwnLines("a\r\n/mnt/c/Users/me/installzero/agent/Probe.ps1 x\nREADME.md stays\ndoctor-assets/x")
	if got != "a\nREADME.md stays" {
		t.Fatalf("got %q", got)
	}
}

func TestRemoveLeaked(t *testing.T) {
	b := newBlind(root, "", nil, true)
	long := []string{
		"docker compose -f stack/compose.yml up -d --build web",
		"export APP_DATA_DIR=/srv/izp-demo/data/storage",
		"systemctl enable --now izp-demo-worker.service",
		"ln -s /srv/izp-demo/releases/current /srv/izp-demo/app",
		"pip install -r requirements.txt --no-cache-dir",
	}
	for _, l := range long {
		b.leak[l] = true
	}
	if got := b.removeLeaked("[exit 0]\n  12| " + long[0] + "\nshort"); got != "[exit 0]\nshort\n[1 line(s) withheld: they come from install files, which the probe does not see]" {
		t.Fatalf("got %q", got)
	}
	if got := b.removeLeaked("[exit 0]\n" + strings.Join(long, "\n") + "\nshort"); got != "[output withheld: it is the content of install files, which the probe does not see]" {
		t.Fatalf("got %q", got)
	}
	if b.withheld != 6 {
		t.Fatalf("withheld %d", b.withheld)
	}
	if got := b.removeLeaked("nothing here"); got != "nothing here" {
		t.Fatalf("got %q", got)
	}
}

func testProbe(t *testing.T) (*probe, *fakeTarget) {
	t.Helper()
	f := newFake()
	p, err := newProbe(Options{Root: root, Target: f, Where: "/srv/app/", Out: filepath.Join(t.TempDir(), "x.install.md")})
	if err != nil {
		t.Fatal(err)
	}
	p.machine = installmd.Machine{OS: "linux", Arch: "x64", GPU: "none"}
	return p, f
}

const draft = "---\nname: demo\n---\n\n## Step 1: app - App\n\n```detect bash\ntest -d {{REPO_DIR}}\n```\n\n```install bash\ngit clone x {{REPO_DIR}}\n```\n\n```verify bash\nfail-check {{INSTALL_DIR}}\n```\n\n## Step 2: svc - Service\n\n```detect bash\nsystemctl restart app\n```\n\n```install bash\nsystemctl enable app\n```\n\n```verify bash\nsystemctl is-active app\n```\n"

func TestDraftCheck(t *testing.T) {
	p, f := testProbe(t)
	got := p.draftCheck(context.Background(), strings.ReplaceAll(draft, "\n", "\r\n"))
	want := "Saved " + p.out + ": 2 steps. Format OK.\n" +
		"Checks run here, where the project is installed (detect and verify should both pass; {{INSTALL_DIR}}=/srv, {{REPO_DIR}}=/srv/app):\n" +
		"  app detect: PASS - ran in /srv/app\n" +
		"  app verify: untested - not read-only ('fail-check' is not on the read-only list), so not run here (it may still be right)\n" +
		"  svc detect: untested - not read-only (systemctl: only status, list-*, show, is-*, cat), so not run here (it may still be right)\n" +
		"  svc verify: PASS - ran in /srv/app"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if s := p.stats; s.Steps != 2 || s.Passed != 2 || s.Untested != 2 || s.Failed != 0 {
		t.Fatalf("stats %+v", s)
	}
	data, _ := os.ReadFile(p.out)
	if strings.Contains(string(data), "\r") {
		t.Fatal("draft saved with CRLF")
	}
	if len(f.scripts) != 2 || f.scripts[0] != "/srv/app$ test -d /srv/app" {
		t.Fatalf("scripts %q", f.scripts)
	}
	// A worse draft later does not replace the best one.
	p.draftCheck(context.Background(), "---\nname: demo\n---\n")
	if p.best.stats.Steps != 2 {
		t.Fatalf("best draft replaced by a worse one: %+v", p.best.stats)
	}
}

func TestDispatch(t *testing.T) {
	p, f := testProbe(t)
	ctx := context.Background()
	call := func(name string, pairs ...any) string {
		out, err := p.dispatch(ctx, name, agentloop.NewArgs(pairs...))
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	}
	for _, c := range []struct{ got, want string }{
		{call("read_file", "path", "README.md"), refusedHidden},
		{call("read_file", "path", "/root/.ssh/id_ed25519"), "refused: that file holds secrets."},
		{call("run_command", "command", "git log -p"), "refused: git commands that print file contents, diffs or patches are not available, since they would show the hidden install files. git remote, branch, status, rev-parse and log without -p work."},
		{call("run_command", "command", "sudo ls"), "refused: 'sudo' is not needed or run here: you are already root, so run the command itself. Only read-only commands run here."},
		{call("run_command", "command", "ls"), "[exit 0]\nran in /srv/app"},
		{call("grep", "pattern", "x"), "ran in "},
		{call("read_file", "path", "a.py", "max_lines", "x"), `error: Cannot convert value "x" to type "System.Int32". Error: "Input string was not in a correct format."`},
		{call("inventory", "area", "bogus", "filter", "zzz"), ""},
		{call("frobnicate"), "error: unknown tool 'frobnicate'"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if p.blind.attempts != 2 {
		t.Errorf("hidden attempts %d", p.blind.attempts)
	}
	if !strings.Contains(strings.Join(f.scripts, "\n"), "-e 'x' '/srv/app'") {
		t.Errorf("grep did not default to the target folder: %q", f.scripts)
	}
}

func TestGuardAndCheckpoint(t *testing.T) {
	p, _ := testProbe(t)
	ctx := context.Background()
	if got := p.finishGuard(ctx, "all done"); got != message("probe_not_saved", nil) {
		t.Fatalf("finish without a draft: %q", got)
	}
	run := &agentloop.Run{Steps: 3, LastLongText: "Here it is:\n" + draft}
	if got := p.checkpoint(ctx, run); !strings.HasPrefix(got, strings.SplitN(message("probe_saved_from_message", map[string]any{"checks": ""}), "\n", 2)[0]) || !p.saved() {
		t.Fatalf("a document in a message was not saved: %q", got)
	}
	if got := p.checkpoint(ctx, run); got != "" {
		t.Fatalf("the same message was saved twice: %q", got)
	}
	if got := p.finishGuard(ctx, ""); got != "" {
		t.Fatalf("finish with a good draft: %q", got)
	}
}

func TestHalfwayReminder(t *testing.T) {
	p, _ := testProbe(t)
	ctx := context.Background()
	if got := p.checkpoint(ctx, &agentloop.Run{Steps: 19}); got != "" {
		t.Fatalf("reminded early: %q", got)
	}
	if got := p.checkpoint(ctx, &agentloop.Run{Steps: 20}); got != message("probe_halfway", map[string]any{"turns": 20, "max_turns": 40}) {
		t.Fatalf("got %q", got)
	}
	if got := p.checkpoint(ctx, &agentloop.Run{Steps: 21}); got != "" {
		t.Fatalf("reminded twice: %q", got)
	}
}
