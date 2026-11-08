package runner

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/target"
)

// files serves `cat -- '<path>'` from a map; every other block passes.
type files map[string]string

func (f files) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	if p, ok := strings.CutPrefix(code, "cat -- '"); ok {
		body, found := f[strings.TrimSuffix(p, "'")]
		if !found {
			return target.Result{Code: 1, Out: "No such file or directory"}
		}
		return target.Result{Out: body}
	}
	return target.Result{}
}

func TestScriptsIn(t *testing.T) {
	code := "bash scripts/setup.sh --fast\ncd app && ./run.sh\nsource env.sh\necho see install.sh\n" +
		"powershell -NoProfile -File .\\tools\\Start.ps1\n& '.\\Start-Clients.ps1' -Clients 2\ncmd /c build.bat\nSet-Content notes.ps1 'x'"
	want := []string{"scripts/setup.sh", "./run.sh", "env.sh", `.\Start-Clients.ps1`, `.\tools\Start.ps1`, "build.bat"}
	got := scriptsIn(code)
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Fatalf("got %q, want %q", got, want)
	}
	help := "<#\n.DESCRIPTION\n    Install.ps1 puts a shortcut here.\n.EXAMPLE\n    .\\scripts\\Start.ps1 -N 2\n#>\n# ./old.sh is gone\nWrite-Output hi"
	if got := scriptsIn(help); len(got) != 0 {
		t.Errorf("comments: %q", got)
	}
	if got := scriptsIn("& (Join-Path $repo 'scripts\\Start-Clients.ps1') -N 2"); !reflect.DeepEqual(got, []string{computed}) {
		t.Errorf("computed call: %q", got)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func scriptPlan(t *testing.T, fs files, install string) *Step {
	t.Helper()
	d := installmd.Parse("---\nname: demo\n---\n\n## Step 1: app - App\n\n```install bash\n" + install + "\n```\n\n```verify bash\ntest -f /home/u/demo/out\n```\n")
	o := linux(fs)
	p, err := NewPlan(context.Background(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	return p.Steps[0]
}

func TestScriptTiers(t *testing.T) {
	fs := files{
		"/home/u/demo/scripts/setup.sh": "set -e\nsudo apt-get install -y libfoo\n",
		"/home/u/demo/build.sh":         "mkdir -p out\ncp src/* out/\n",
		"/home/u/demo/run.sh":           "mkdir -p out\n./tools/inner.sh\n",
		"/home/u/demo/tools/inner.sh":   "mkfs.ext4 /dev/sdb1\n",
	}
	for _, c := range []struct{ install, tier, reason string }{
		{"bash scripts/setup.sh", "system", "runs /home/u/demo/scripts/setup.sh: machine-wide change"},
		{"./build.sh", "project", "changes only the install folder"},
		{"./run.sh", "never", "runs /home/u/demo/tools/inner.sh: "},
		{"bash missing.sh", "system", "cannot be read now"},
		{"bash $HOME/x.sh", "system", ""},
		{"cmd /c build.bat", "system", ""},
		{"bash ${APP}/x.sh", "system", ""},
		{"sh tools/inner.sh", "never", "runs /home/u/demo/tools/inner.sh: "},
		{"mkdir -p out", "project", "changes only the install folder"},
	} {
		b := scriptPlan(t, fs, c.install).Install
		if b.Tier.Tier != c.tier || !strings.Contains(b.Tier.Reason, c.reason) {
			t.Errorf("%s: %+v, want %s (%s)", c.install, b.Tier, c.tier, c.reason)
		}
	}
}
