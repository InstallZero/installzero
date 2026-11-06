// Package target is the machine the probe looks at and runs its checks on.
// A Linux target is anything that runs a bash script as root (a WSL distro
// today, the local machine later); Unix builds the probe's views on it.
package target

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/psrx"
)

// Result is a script's exit code and its output (stdout, then stderr).
type Result struct {
	Code int
	Out  string
}

// Format is Format-WslResult: the exit code, then the output cut at max
// characters (12000 when 0).
func (r Result) Format(max int) string {
	if max == 0 {
		max = 12000
	}
	t := r.Out
	if u := utf16Len(t); u > max {
		t = utf16Head(t, max) + fmt.Sprintf("\n[... cut at %d characters]", max)
	}
	return fmt.Sprintf("[exit %d]\n%s", r.Code, t)
}

// Runner runs a bash script on the target as root, in dir when it exists.
// A script that outlives timeout is killed and reported with code 124.
type Runner interface {
	Bash(ctx context.Context, script string, timeout time.Duration, dir string) Result
}

// Target is what the probe sees on a machine.
type Target interface {
	Runner
	// Name identifies the target in results, e.g. wsl:<distro>.
	Name() string
	Profile(ctx context.Context) installmd.Machine
	Inventory(ctx context.Context, area, path string) string
	ReadFile(ctx context.Context, path string, start, n int) string
	ListDir(ctx context.Context, path string) string
	Grep(ctx context.Context, pattern, include, path string) string
}

// Quote is a bash single-quoted word.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Unix is a Linux target reached through a Runner.
type Unix struct {
	Runner
	Label string
}

func (u *Unix) Name() string { return u.Label }

const profileScript = `. /etc/os-release; echo "os_version=$PRETTY_NAME"
echo "arch=$(uname -m)"
echo "ram_gb=$(free -g | awk '/^Mem:/{print $2}')"
echo "disk_gb=$(df -BG --output=avail / | tail -1 | tr -dc 0-9)"
if command -v nvidia-smi >/dev/null 2>&1 || [ -x /usr/lib/wsl/lib/nvidia-smi ]; then
  s=$(command -v nvidia-smi || echo /usr/lib/wsl/lib/nvidia-smi)
  echo "gpus=$($s --query-gpu=name --format=csv,noheader 2>/dev/null | paste -sd, -)"
  echo "driver=$($s --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1)"
fi`

var (
	profileLine = psrx.MustCompile(`^(\w+)=(.*)$`)
	nvidia      = psrx.MustCompile(`NVIDIA`)
)

// Profile is Get-WslMachineProfile: what install.md variants are chosen
// against.
func (u *Unix) Profile(ctx context.Context) installmd.Machine {
	r := u.Bash(ctx, profileScript, 30*time.Second, "")
	v := map[string]string{}
	for _, l := range strings.Split(r.Out, "\n") {
		if g := profileLine.Groups(l); g != nil {
			v[strings.ToLower(g[1])] = strings.TrimSpace(g[2])
		}
	}
	m := installmd.Machine{OS: "linux", Arch: v["arch"], GPUs: v["gpus"], Driver: v["driver"], OSVersion: v["os_version"]}
	switch v["arch"] {
	case "x86_64":
		m.Arch = "x64"
	case "aarch64":
		m.Arch = "arm64"
	}
	switch {
	case nvidia.Match(v["gpus"]):
		m.GPU = "nvidia"
	case v["gpus"] != "":
		m.GPU = "other"
	default:
		m.GPU = "none"
	}
	m.RAMGB = float64(leadingInt(v["ram_gb"]))
	disk := float64(leadingInt(v["disk_gb"]))
	m.DiskGB = &disk
	return m
}

// leadingInt is [int]("0" + s) for the digit strings the profile prints.
func leadingInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ReadFile shows n lines from start, numbered, with the file's line count.
func (u *Unix) ReadFile(ctx context.Context, path string, start, n int) string {
	script := fmt.Sprintf(`f=%s; [ -f "$f" ] || { echo "not a file: $f"; exit 1; }; grep -Iq . "$f" || [ ! -s "$f" ] || { echo 'binary file'; exit 1; }; echo "$f ($(wc -l < "$f") lines)"; awk -v s=%d -v e=%d 'NR>=s && NR<=e {printf "%%d|%%s\n", NR, $0}' "$f"`,
		Quote(path), start, start+n-1)
	return u.Bash(ctx, script, 60*time.Second, "").Out
}

// ListDir is ls -la of a folder.
func (u *Unix) ListDir(ctx context.Context, path string) string {
	return u.Bash(ctx, "ls -la --time-style=long-iso "+Quote(path), 60*time.Second, "").Out
}

// Grep searches files under path for an extended regex, first 300 hits.
func (u *Unix) Grep(ctx context.Context, pattern, include, path string) string {
	inc := ""
	if include != "" {
		inc = "--include=" + Quote(include)
	}
	return u.Bash(ctx, fmt.Sprintf("grep -rnIE %s --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=proc --exclude-dir=sys -e %s %s 2>/dev/null | cut -c1-400 | head -300",
		inc, Quote(pattern), Quote(path)), 90*time.Second, "").Out
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func utf16Head(s string, n int) string {
	k := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if k+w > n {
			return s[:i]
		}
		k += w
	}
	return s
}
