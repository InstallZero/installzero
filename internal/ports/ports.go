// Package ports finds and resolves port conflicts in installs: the ports an
// install.md declares (front matter "ports: web=8095, agent=50000",
// used in blocks as {{PORT_WEB}}) and the ones its blocks use, who listens
// on a port, and why a failed step could not bind one.
package ports

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/target"
)

// Exec runs a script on the target (runner.Exec).
type Exec interface {
	Run(ctx context.Context, shell, code, dir string, timeout time.Duration) target.Result
}

// Declared reads the front matter's ports value: name=port pairs separated
// by commas or spaces. Names are lower case.
func Declared(value string) (map[string]int, error) {
	out := map[string]int{}
	for _, f := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		name, p, ok := strings.Cut(f, "=")
		n, err := strconv.Atoi(p)
		if !ok || name == "" || err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("ports: %q is not name=port", f)
		}
		out[strings.ToLower(name)] = n
	}
	return out, nil
}

// Placeholder is the {{...}} name a declared port fills.
func Placeholder(name string) string {
	return "PORT_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

var used = regexp.MustCompile(`(?i)(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\]):(\d{2,5})\b|-p\s+(?:[\d.]+:)?(\d{2,5}):\d|--port[= ](\d{2,5})\b|\b\w*PORT=["']?(\d{2,5})\b|\blisten\s+(\d{2,5})\b`)

// Used lists the ports blocks listen on or check locally, ascending.
func Used(codes ...string) []int {
	seen := map[int]bool{}
	for _, c := range codes {
		for _, m := range used.FindAllStringSubmatch(c, -1) {
			for _, g := range m[1:] {
				if n, err := strconv.Atoi(g); err == nil && n > 0 && n <= 65535 {
					seen[n] = true
				}
			}
		}
	}
	var out []int
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Listener is who listens on a port.
type Listener struct {
	Process string // e.g. "java pid 8477"
	Unit    string // its systemd unit, when it has one
	Where   string // "" for the target, "Windows" for the Windows host of a WSL target
}

func (l Listener) String() string {
	s := l.Process
	if s == "" {
		s = "an unknown process"
	}
	if l.Unit != "" {
		s += " (" + l.Unit + ")"
	}
	if l.Where != "" {
		s += " on " + l.Where
	}
	return s
}

const bashListeners = `ss -ltnpH 2>/dev/null | while read -r _ _ _ local _ users; do
  port=${local##*:}
  pid=$(printf '%s' "$users" | sed -n 's/.*pid=\([0-9]*\).*/\1/p')
  name=$(printf '%s' "$users" | sed -n 's/.*(("\([^"]*\)".*/\1/p')
  unit=""
  [ -n "$pid" ] && unit=$(ps -o unit= -p "$pid" 2>/dev/null | tr -d ' ')
  case "$unit" in *.service) ;; *) unit="" ;; esac
  printf '%s\t%s pid %s\t%s\n' "$port" "$name" "$pid" "$unit"
done`

const psListeners = `$names = @{}; Get-Process -ErrorAction SilentlyContinue | ForEach-Object { $names[$_.Id] = $_.ProcessName }
$svcs = @{}; Get-CimInstance Win32_Service -Filter "ProcessId > 0" -ErrorAction SilentlyContinue | ForEach-Object { if (-not $svcs.ContainsKey([int]$_.ProcessId)) { $svcs[[int]$_.ProcessId] = $_.Name } }
Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | ForEach-Object {
    $id = [int]$_.OwningProcess
    "$($_.LocalPort)` + "`t" + `$($names[$id]) pid $id` + "`t" + `$($svcs[$id])"
}`

func parseListeners(out, where string, into map[int]Listener) {
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		f := strings.Split(l, "\t")
		n, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil || len(f) < 2 {
			continue
		}
		if _, ok := into[n]; ok {
			continue
		}
		li := Listener{Process: strings.TrimSpace(f[1]), Where: where}
		if len(f) > 2 {
			li.Unit = strings.TrimSpace(f[2])
		}
		into[n] = li
	}
}

// Ports reads the listeners of a target. Windows targets are asked with
// PowerShell; host, when set, is the Windows machine behind a WSL target
// in mirrored networking, where both share one set of ports.
func Listeners(ctx context.Context, e Exec, windows bool, host Exec) map[int]Listener {
	out := map[int]Listener{}
	if windows {
		parseListeners(e.Run(ctx, "powershell", psListeners, "", 2*time.Minute).Out, "", out)
		return out
	}
	parseListeners(e.Run(ctx, "bash", bashListeners, "", time.Minute).Out, "", out)
	if host != nil {
		parseListeners(host.Run(ctx, "powershell", psListeners, "", 2*time.Minute).Out, "Windows", out)
	}
	return out
}

// MirroredWSL reports whether WSL shares ports with Windows
// (networkingMode=mirrored in %USERPROFILE%\.wslconfig).
func MirroredWSL() bool {
	b, err := os.ReadFile(filepath.Join(os.Getenv("USERPROFILE"), ".wslconfig"))
	return err == nil && regexp.MustCompile(`(?im)^\s*networkingMode\s*=\s*mirrored\b`).Match(b)
}

// Free returns the first port from start that nobody listens on and that
// avoid does not hold; 0 when none within 200.
func Free(start int, listening map[int]Listener, avoid map[int]bool) int {
	for p := start; p < start+200 && p <= 65535; p++ {
		if _, busy := listening[p]; !busy && !avoid[p] {
			return p
		}
	}
	return 0
}

// Choice is how a declared port was settled.
type Choice struct {
	Name    string
	Want    int // the declared or previously used port
	Port    int // the port to use
	Owner   *Listener
	Reused  bool // the port this install used before and still holds
	Problem string
}

// Resolve settles declared ports (names: name to the declared port, or the
// port this install used before): each keeps its port when free; when the
// install finished before, a taken port is the install's own and kept; a
// port taken by something else moves to the next free one.
func Resolve(names map[string]int, listening map[int]Listener, finished bool) []Choice {
	var out []Choice
	taken := map[int]bool{}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		want := names[name]
		c := Choice{Name: name, Want: want, Port: want}
		if l, busy := listening[want]; busy {
			if finished {
				c.Reused = true
			} else {
				lc := l
				c.Owner = &lc
				c.Port = Free(want+1, listening, taken)
				if c.Port == 0 {
					c.Problem = fmt.Sprintf("no free port near %d", want)
					c.Port = want
				}
			}
		}
		taken[c.Port] = true
		out = append(out, c)
	}
	return out
}
