package agentinstall

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/readonly"
	"github.com/InstallZero/installzero/internal/secrets"
	"github.com/InstallZero/installzero/internal/target"
)

const (
	maxGrepMatches = 100
	maxGrepFile    = 2 << 20
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".installzero": true}

// readTool runs one of the model's read-only tools.
func (a *agent) readTool(ctx context.Context, name string, args *agentloop.Args) (string, error) {
	var out string
	switch name {
	case "run_command":
		out = a.runCommand(ctx, args.String("command"), intArg(args, "timeout_seconds", 60))
	case "read_file":
		out = a.readFile(ctx, a.path(args.String("path")), max(1, intArg(args, "start_line", 1)), min(400, intArg(args, "max_lines", 200)))
	case "list_dir":
		out = a.listDir(ctx, a.path(args.String("path")))
	case "grep":
		out = a.grep(ctx, args.String("pattern"), a.path(args.String("path")), args.String("include"))
	case "compose_ps":
		out = a.quiet(ctx, "docker compose ps -a\ndocker compose config --services", time.Minute)
	case "service_logs":
		out = a.serviceLogs(ctx, args.String("service"), intArg(args, "lines", 150), args.String("filter"))
	case "system_facts":
		out = a.quiet(ctx, map[bool]string{true: windowsFacts, false: linuxFacts}[a.windows], 2*time.Minute)
	case "gpu_drivers":
		out = a.quiet(ctx, map[bool]string{true: windowsGPU, false: linuxGPU}[a.windows], time.Minute)
	default:
		return "error: unknown tool '" + name + "'", nil
	}
	out = secrets.Redact(out, a.known)
	if a.o.Remote || !a.windows {
		out = secrets.RedactLinux(out)
	}
	return out, nil
}

// path makes a tool's path absolute: relative ones are in the install folder.
func (a *agent) path(p string) string {
	dir := a.o.Runner.Dir
	switch {
	case p == "":
		return dir
	case a.o.Remote:
		if strings.HasPrefix(p, "/") {
			return p
		}
		return strings.TrimSuffix(dir, "/") + "/" + p
	case filepath.IsAbs(p):
		return p
	}
	return filepath.Join(dir, p)
}

func (a *agent) shell() string { return map[bool]string{true: "powershell", false: "bash"}[a.windows] }

// quiet runs a read-only command of the harness's own on the target.
func (a *agent) quiet(ctx context.Context, code string, timeout time.Duration) string {
	return a.o.Runner.Read.Run(ctx, a.shell(), code, a.o.Runner.Dir, timeout).Format(0)
}

func (a *agent) runCommand(ctx context.Context, code string, seconds int) string {
	if strings.TrimSpace(code) == "" {
		return "error: command is empty"
	}
	why := ""
	if a.windows {
		r, err := readonly.PowerShell(ctx, code)
		if err != nil {
			return "error: " + err.Error()
		}
		why = r[0]
	} else {
		why = readonly.Bash(code)
	}
	if why != "" {
		return "refused: " + why + ". Only read-only commands run here; use run_change for changes."
	}
	seconds = min(180, max(1, seconds))
	return a.o.Runner.Read.Run(ctx, a.shell(), code, a.o.Runner.Dir, time.Duration(seconds)*time.Second).Format(0)
}

func (a *agent) readFile(ctx context.Context, path string, start, n int) string {
	if secrets.IsSecretPath(path) {
		return "refused: that file holds secrets."
	}
	if a.o.Remote {
		code := fmt.Sprintf(`test -f %s || { echo "no such file"; exit 1; }; awk -v s=%d -v e=%d 'NR>=s && NR<=e {printf "%%5d| %%s\n", NR, $0}' %s`, target.Quote(path), start, start+n-1, target.Quote(path))
		return a.o.Runner.Read.Run(ctx, "bash", code, "", time.Minute).Format(0)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "error: " + err.Error()
	}
	if secrets.IsEnvFile(path) {
		return secrets.MaskEnvFile(path, string(data))
	}
	lines := secrets.Lines(string(data))
	var b strings.Builder
	for i := start - 1; i < len(lines) && i < start-1+n; i++ {
		fmt.Fprintf(&b, "%5d| %s\n", i+1, lines[i])
	}
	if end := start - 1 + n; end < len(lines) {
		fmt.Fprintf(&b, "[%d more lines; read on with start_line %d]\n", len(lines)-end, end+1)
	}
	if b.Len() == 0 {
		return fmt.Sprintf("%s has %d lines", path, len(lines))
	}
	return b.String()
}

func (a *agent) listDir(ctx context.Context, path string) string {
	if a.o.Remote {
		return a.o.Runner.Read.Run(ctx, "bash", "ls -la "+target.Quote(path), "", time.Minute).Format(0)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "error: " + err.Error()
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := "<dir>"
		if !e.IsDir() {
			size = strconv.FormatInt(info.Size(), 10)
		}
		fmt.Fprintf(&b, "%-12s %s  %s\n", size, info.ModTime().Format("2006-01-02 15:04"), e.Name())
	}
	if b.Len() == 0 {
		return "(empty)"
	}
	return b.String()
}

func (a *agent) grep(ctx context.Context, pattern, path, include string) string {
	if a.o.Remote {
		inc := ""
		if include != "" {
			inc = "--include=" + target.Quote(include) + " "
		}
		code := fmt.Sprintf("grep -rnIE %s-e %s %s 2>/dev/null | head -%d", inc, target.Quote(pattern), target.Quote(path), maxGrepMatches)
		res := a.o.Runner.Read.Run(ctx, "bash", code, "", 2*time.Minute)
		if strings.TrimSpace(res.Out) == "" {
			return "no matches"
		}
		return res.Out
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return "error: invalid pattern: " + err.Error()
	}
	var hits []string
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(hits) >= maxGrepMatches {
			return nil
		}
		if d.IsDir() {
			if p != path && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if include != "" {
			if ok, _ := filepath.Match(include, d.Name()); !ok {
				return nil
			}
		}
		if secrets.IsSecretPath(p) || secrets.IsEnvFile(p) {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxGrepFile {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), maxGrepFile)
		for n := 1; sc.Scan(); n++ {
			if re.MatchString(sc.Text()) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", p, n, agentloop.Brief(sc.Text(), 300)))
				if len(hits) >= maxGrepMatches {
					break
				}
			}
		}
		return nil
	})
	if len(hits) == 0 {
		return "no matches"
	}
	return strings.Join(hits, "\n")
}

func (a *agent) serviceLogs(ctx context.Context, service string, lines int, filter string) string {
	if !regexp.MustCompile(`^[\w.-]+$`).MatchString(service) {
		return "error: not a service name"
	}
	out := a.quiet(ctx, fmt.Sprintf("docker compose logs --no-color --tail %d %s", min(max(lines, 1), 2000), service), time.Minute)
	if filter == "" {
		return out
	}
	re, err := regexp.Compile("(?i)" + filter)
	if err != nil {
		return "error: invalid filter: " + err.Error()
	}
	var kept []string
	for _, l := range strings.Split(out, "\n") {
		if re.MatchString(l) {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return "no matching lines"
	}
	return strings.Join(kept, "\n")
}

// intArg reads an integer argument as PowerShell's [int] would, or def.
func intArg(args *agentloop.Args, key string, def int) int {
	s := strings.TrimSpace(args.String(key))
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return int(math.RoundToEven(f))
}

const (
	windowsFacts = `$os = Get-CimInstance Win32_OperatingSystem
"Windows: $($os.Caption) $($os.Version), RAM $([math]::Round($os.TotalVisibleMemorySize / 1MB, 1)) GB, $([math]::Round($os.FreePhysicalMemory / 1MB, 1)) GB free"
Get-PSDrive -PSProvider FileSystem | Where-Object { $_.Used -ne $null } | ForEach-Object { "Drive $($_.Name): $([math]::Round($_.Free / 1GB, 1)) GB free" }
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { nvidia-smi --query-gpu=index,name,driver_version,memory.used,memory.total --format=csv,noheader 2>&1 } else { 'nvidia-smi: not found' }
if (Get-Command docker -ErrorAction SilentlyContinue) {
  docker version --format 'Docker client {{.Client.Version}}, server {{.Server.Version}}' 2>&1
  docker info --format 'Docker: {{.OperatingSystem}}, {{.NCPU}} CPUs, {{.MemTotal}} bytes, {{.ContainersRunning}} containers running' 2>&1
} else { 'docker: not installed' }
if (Get-Command wsl -ErrorAction SilentlyContinue) { 'WSL distros:'; (wsl -l -v 2>&1 | Out-String) -replace "` + "`" + `0", '' }
'Excluded TCP port ranges:'; netsh interface ipv4 show excludedportrange protocol=tcp 2>&1 | Select-Object -Skip 3
`
	windowsGPU = `Get-CimInstance Win32_VideoController | Format-Table Name, DriverVersion, DriverDate -AutoSize | Out-String -Width 200
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { nvidia-smi 2>&1 } else { 'nvidia-smi: not found' }
`
	linuxFacts = `uname -a; grep -E '^(PRETTY_NAME|VERSION_ID)=' /etc/os-release
free -g | head -2; df -h / /root 2>/dev/null
command -v nvidia-smi >/dev/null && nvidia-smi --query-gpu=index,name,driver_version,memory.used,memory.total --format=csv,noheader || echo 'nvidia-smi: not found'
command -v docker >/dev/null && { docker version --format 'Docker client {{.Client.Version}}, server {{.Server.Version}}' 2>&1; docker info --format 'Docker: {{.OperatingSystem}}, {{.NCPU}} CPUs, {{.ContainersRunning}} containers running' 2>&1; } || echo 'docker: not installed'
echo 'Listening TCP ports:'; ss -ltnp 2>/dev/null | head -40
`
	linuxGPU = `lspci 2>/dev/null | grep -iE 'vga|3d|display' || echo 'lspci: no display adapters listed'
command -v nvidia-smi >/dev/null && nvidia-smi || echo 'nvidia-smi: not found'
`
)
