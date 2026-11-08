package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/InstallZero/installzero/internal/target"
)

// Exec runs one block's code in a shell (bash or powershell), in dir. A
// block that outlives timeout is killed and reported with code 124.
type Exec interface {
	Run(ctx context.Context, shell, code, dir string, timeout time.Duration) target.Result
}

// Non-interactive settings every block runs with: package managers and
// tools that would ask a question take their default or fail instead.
//
// PowerShell: a failing cmdlet, Write-Error, throw or .NET exception ends
// the block with exit 1. $ErrorActionPreference stays Continue because
// Windows PowerShell turns a program's redirected stderr (docker's "Unable
// to find image ... locally" under 2>&1 or 2>$null) into error records,
// which Stop would make fatal; cmdlets get Stop through their defaults.
const (
	bashPrelude = "set -e\nexport DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a PIP_NO_INPUT=1 PIP_DISABLE_PIP_VERSION_CHECK=1 CI=1 GIT_TERMINAL_PROMPT=0 NPM_CONFIG_YES=true HOMEBREW_NO_AUTO_UPDATE=1\n" +
		bashDownload
	psPrelude = "$ErrorActionPreference = 'Continue'; $PSDefaultParameterValues['*:ErrorAction'] = 'Stop'; $ProgressPreference = 'SilentlyContinue'; $ConfirmPreference = 'None'\n" +
		"trap { $l = \"$($_.InvocationInfo.Line)\".Trim(); [Console]::Error.WriteLine($_.Exception.Message + $(if ($l) { \"`n  at: $l\" })); exit 1 }\n" +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8\n$env:CI = '1'; $env:PIP_NO_INPUT = '1'; $env:GIT_TERMINAL_PROMPT = '0'\n" +
		psDownload
)

// Download helpers blocks may call (iz_download <url> <file>,
// Invoke-IzDownload -Uri <url> -OutFile <file>); installzero shows their
// progress (curl's redrawn meter, "##progress" lines) as a progress bar.
const (
	bashDownload = `iz_download() {
  echo "Downloading $1"
  if command -v curl >/dev/null 2>&1; then curl -fL --retry 3 --progress-bar -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
}
`
	psDownload = `function Invoke-IzDownload([Parameter(Mandatory)][string]$Uri, [Parameter(Mandatory)][string]$OutFile) {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  $name = Split-Path $OutFile -Leaf
  [Console]::Out.WriteLine("Downloading $Uri")
  $res = [Net.WebRequest]::Create($Uri).GetResponse()
  try {
    $total = $res.ContentLength; $in = $res.GetResponseStream(); $out = [IO.File]::Create($OutFile)
    try {
      $buf = New-Object byte[] 1048576; $done = 0; $last = -1
      while (($n = $in.Read($buf, 0, $buf.Length)) -gt 0) {
        $out.Write($buf, 0, $n); $done += $n
        if ($total -gt 0) { $pct = [int][Math]::Floor(100 * $done / $total); if ($pct -ne $last) { [Console]::Out.WriteLine("##progress $pct $name"); $last = $pct } }
      }
    } finally { $out.Close(); $in.Close() }
  } finally { $res.Close() }
}
`
)

func isPowerShell(shell string) bool { return shell == "powershell" || shell == "pwsh" }

// wrapBash puts code in a group reading from /dev/null, so a command that
// asks a question gets end of input, also when the script itself arrives
// on stdin (WSL).
func wrapBash(code string) string {
	return bashPrelude + "{\n" + strings.ReplaceAll(code, "\r\n", "\n") + "\n} </dev/null\n"
}

func wrapPowerShell(code string) string {
	return psPrelude + code + "\nif ($LASTEXITCODE) { exit $LASTEXITCODE }\n"
}

// Unix runs bash blocks on a Linux target (a WSL distro).
type Unix struct{ target.Runner }

func (u Unix) Run(ctx context.Context, shell, code, dir string, timeout time.Duration) target.Result {
	if isPowerShell(shell) {
		return target.Result{Code: 127, Out: "this target runs bash blocks only, not " + shell}
	}
	return u.Bash(ctx, wrapBash(code), timeout, dir)
}

// Local runs blocks on this machine: bash (Git for Windows' on Windows) and
// PowerShell. Live, when set, gets the output as it comes.
type Local struct {
	Live io.Writer
}

func (l Local) Run(ctx context.Context, shell, code, dir string, timeout time.Duration) target.Result {
	f, err := os.CreateTemp("", "iz-block-*"+map[bool]string{true: ".ps1", false: ".sh"}[isPowerShell(shell)])
	if err != nil {
		return target.Result{Code: 1, Out: err.Error()}
	}
	defer os.Remove(f.Name())
	var args []string
	if isPowerShell(shell) {
		// Windows PowerShell reads a BOM-less script in the console code page.
		f.WriteString("\ufeff" + wrapPowerShell(code))
		exe := "powershell.exe"
		if shell == "pwsh" || runtime.GOOS != "windows" {
			exe = "pwsh"
		}
		args = []string{exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", f.Name()}
	} else {
		f.WriteString(wrapBash(code))
		bash, why := findBash()
		if bash == "" {
			f.Close()
			return target.Result{Code: 127, Out: why}
		}
		args = []string{bash, "-l", f.Name()}
	}
	f.Close()
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(run, args[0], args[1:]...)
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			cmd.Dir = dir
		}
	}
	hideWindow(cmd)
	var buf bytes.Buffer
	w := io.Writer(&lockedWriter{w: &buf})
	if l.Live != nil {
		w = io.MultiWriter(w, l.Live)
	}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	out := strings.ToValidUTF8(buf.String(), "\uFFFD")
	if errors.Is(run.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return target.Result{Code: 124, Out: fmt.Sprintf("%s\n[timed out after %d s]", out, int(timeout/time.Second))}
	}
	code0 := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return target.Result{Code: 1, Out: err.Error()}
		}
		code0 = exit.ExitCode()
	}
	return target.Result{Code: code0, Out: strings.TrimRight(out, " \t\r\n")}
}

func findBash() (string, string) {
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p, ""
		}
		return "", "bash is not on PATH"
	}
	// Not System32\bash.exe, which is WSL's and runs in a distro.
	for _, p := range []string{os.Getenv("ProgramFiles") + `\Git\bin\bash.exe`, os.Getenv("ProgramFiles(x86)") + `\Git\bin\bash.exe`} {
		if _, err := os.Stat(p); err == nil {
			return p, ""
		}
	}
	if p, err := exec.LookPath("bash"); err == nil && !strings.EqualFold(filepath.Dir(p), filepath.Join(os.Getenv("SystemRoot"), "System32")) {
		return p, ""
	}
	return "", "bash is not available on this PC (install Git for Windows, or use a powershell block)"
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
