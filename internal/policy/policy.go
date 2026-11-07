// Package policy decides the safety tier of a command the installer agent
// runs (agent/INSTALL-MD.md, "Safety"). Ported from agent/Policy.ps1;
// fixtures/policy holds the shared cases.
//
//	read     read-only                                 runs
//	project  changes confined to the install folder    runs
//	system   machine-wide or unrecognised              asks
//	never    destructive                               refused
//
// Unknown commands are system, so a new tool is asked about, never assumed
// safe.
package policy

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/InstallZero/installzero/agent"
	"github.com/InstallZero/installzero/doctor"
	"github.com/InstallZero/installzero/internal/psrx"
)

// Tiers.
const (
	Read    = "read"
	Project = "project"
	System  = "system"
	Never   = "never"
)

// Result is a command's tier and why.
type Result struct{ Tier, Reason string }

var never = []struct {
	re  *psrx.Regexp
	why string
}{
	{psrx.MustCompile(`(?i)\b(Format-Volume|Clear-Disk|Remove-Partition|Initialize-Disk|diskpart|bcdedit|vssadmin\s+delete|cipher(\.exe)?\s+/w)\b`), "changes disks, partitions or boot settings"},
	{psrx.MustCompile(`(?i)\bformat(\.com)?\s+[a-z]:`), "formats a drive"},
	{psrx.MustCompile(`(?i)\bwsl(\.exe)?\s+--unregister\b`), "deletes a WSL distro and everything in it"},
	{psrx.MustCompile(`(?i)\bdocker\s+(volume\s+(rm|prune)|system\s+prune\b.*--volumes)`), "deletes Docker volumes (data)"},
	{psrx.MustCompile(`(?i)\breg(\.exe)?\s+delete\s+HK(LM|EY_LOCAL_MACHINE)`), "deletes machine registry keys"},
	{psrx.MustCompile(`(?i)\b(cmdkey|Remove-StoredCredential)\b|\bcredential\s+manager\b`), "touches stored credentials"},
	{psrx.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f?[a-z]*\s+(/|~|\$HOME)(\s|$)`), "deletes a home or root folder"},
	{psrx.MustCompile(`(?i)\b(Restart-Computer|Stop-Computer)\b|\bshutdown(\.exe)?\s+/[rsp]`), "restarts or shuts down the PC; ask the user to do it"},
}

var system = []*psrx.Regexp{
	psrx.MustCompile(`(?i)\b(winget|choco|scoop|msiexec|dism|apt(-get)?|dnf|yum|pacman|brew|sudo|snap)\b`),
	psrx.MustCompile(`(?i)\bwsl(\.exe)?\s+--(install|update|shutdown|set-default|set-version|import|export)`),
	psrx.MustCompile(`(?i)\b(Enable|Disable)-WindowsOptionalFeature\b|\bAdd-WindowsCapability\b`),
	psrx.MustCompile(`(?i)\b(Set|Start|Stop|Restart|New|Remove)-Service\b|\bsc(\.exe)\s+(config|create|delete|start|stop)`),
	psrx.MustCompile(`(?i)\bnetsh\b(?!.*\bshow\b)|\bNew-NetFirewallRule\b|\bpowercfg\b|\bsetx\b`),
	psrx.MustCompile(`(?i)\b(Set|New|Remove)-ItemProperty\b.*\bHK(LM|EY_LOCAL_MACHINE|CR)\b|\breg(\.exe)?\s+add\s+HK(LM|EY_LOCAL_MACHINE)`),
	psrx.MustCompile(`(?i)\b(Add|Remove)-LocalGroupMember\b|\bnet\s+(localgroup|user)\b`),
	psrx.MustCompile(`(?i)-Verb\s+RunAs\b`),
	psrx.MustCompile(`(?i)\bnpm\s+(i|install)\s+(-g|--global)\b|\bpip3?\s+install\b.*--user\b|\bInstall-Module\b`),
	psrx.MustCompile(`(?i)\[Environment\]::SetEnvironmentVariable`),
}

// projectCommands only act on the current folder or on paths named in the
// command; with the path check they stay inside the install folder.
var projectCommands = psrx.MustCompile(`(?i)^(git|docker|docker-compose|python|python3|py|pip|pip3|uv|conda|mamba|npm|npx|pnpm|yarn|node|cargo|go|dotnet|make|cmake|curl|wget|tar|unzip|7z|New-Item|mkdir|md|Set-Content|Add-Content|Out-File|Copy-Item|cp|copy|Move-Item|mv|move|Remove-Item|rm|del|rmdir|Rename-Item|Expand-Archive|Compress-Archive|Invoke-WebRequest|iwr|Invoke-RestMethod|irm|Set-Location|cd|Push-Location|Pop-Location|Write-Host|Write-Output|echo|Start-Sleep|ForEach-Object|Where-Object|%|\?|Select-Object|Test-Path|Join-Path|Get-\w+|Resolve-Path|Split-Path|\.\\[\w.-]+\.(ps1|bat|cmd|exe)|[\w.-]+\.(bat|cmd))$`)

var (
	drivePath = psrx.MustCompileCase(`(?i)(?<![\w/])([a-z]:[\\/][^\s'"` + "`" + `|;,)]*)`)
	envPath   = psrx.MustCompileCase(`(?i)(\$env:(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)|%(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)%|~[\\/])[^\s'"` + "`" + `|;,)]*`)
	bashWord  = psrx.MustCompileCase(`(?m)(^|[;&|]\s*)([\w./-]+)`)
	envPrefix = psrx.MustCompile(`^\$env:(\w+)`)
	pctPrefix = psrx.MustCompile(`^%(\w+)%`)
)

var bashBuiltins = []string{"cd", "export", "source", ".", "set", "echo", "test", "[", "true", "false"}

// commandPaths are the Windows paths a command names: drive paths and
// paths under the user's or the system's folders.
func commandPaths(command string) []string {
	var out []string
	for _, g := range drivePath.AllGroups(command) {
		out = append(out, g[1])
	}
	for _, g := range envPath.AllGroups(command) {
		out = append(out, g[0])
	}
	return out
}

func expandPath(p string) string {
	if g := envPrefix.Groups(p); g != nil {
		p = os.Getenv(g[1]) + p[len(g[0]):]
	} else if g := pctPrefix.Groups(p); g != nil {
		p = os.Getenv(g[1]) + p[len(g[0]):]
	} else if strings.HasPrefix(p, "~") {
		p = os.Getenv("USERPROFILE") + p[1:]
	}
	return p
}

// fullWindowsPath normalises a Windows path as [IO.Path]::GetFullPath does
// for a rooted one (separators, . and .. segments), on any OS; ok is false
// for a path that is not rooted at a drive.
func fullWindowsPath(p string) (string, bool) {
	p = strings.ReplaceAll(p, "/", `\`)
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' {
		return "", false
	}
	var parts []string
	for _, s := range strings.Split(p[3:], `\`) {
		switch s {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, strings.TrimRight(s, ". "))
		}
	}
	return p[:3] + strings.Join(parts, `\`), true
}

func insideFolder(path string, roots []string) bool {
	full, ok := fullWindowsPath(strings.TrimRight(expandPath(path), `\/`))
	if !ok {
		return false
	}
	for _, r := range roots {
		root, ok := fullWindowsPath(strings.TrimRight(r, `\/`))
		if !ok {
			continue
		}
		if strings.EqualFold(full, root) || (len(full) > len(root) && strings.EqualFold(full[:len(root)+1], root+`\`)) {
			return true
		}
	}
	return false
}

func isPowerShell(shell string) bool {
	return shell == "powershell" || shell == "pwsh" || shell == ""
}

// Bash returns the tier of a bash command. roots are the folders
// project-tier changes may touch.
func Bash(command string, roots []string) Result {
	if r, ok := neverOrSystem(command, roots); ok {
		return r
	}
	for _, g := range bashWord.AllGroups(command) {
		w := g[2]
		builtin := false
		for _, b := range bashBuiltins {
			if strings.EqualFold(w, b) {
				builtin = true
			}
		}
		if !projectCommands.Match(w) && !builtin {
			return Result{System, fmt.Sprintf("'%s' is not a known project-folder command", w)}
		}
	}
	return Result{Project, "changes only the install folder"}
}

// neverOrSystem applies the pattern and path rules shared by every shell
// (after the read-only check for PowerShell).
func neverOrSystem(command string, roots []string) (Result, bool) {
	if r, ok := neverTier(command); ok {
		return r, true
	}
	for _, re := range system {
		if g := re.Groups(command); g != nil {
			return Result{System, fmt.Sprintf("machine-wide change (%s)", strings.TrimSpace(g[0]))}, true
		}
	}
	var outside []string
	for _, p := range commandPaths(command) {
		if !insideFolder(p, roots) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		return Result{System, "touches paths outside the install folder: " + strings.Join(outside, ", ")}, true
	}
	return Result{}, false
}

func neverTier(command string) (Result, bool) {
	for _, n := range never {
		if n.re.Match(command) {
			return Result{Never, n.why}, true
		}
	}
	return Result{}, false
}

// Classify returns the tiers of commands for one shell. PowerShell commands
// go to agent/Policy.ps1 in one Windows PowerShell process (pwsh elsewhere),
// since the rules walk PowerShell's own syntax tree; bash is decided here.
func Classify(ctx context.Context, shell string, roots []string, commands ...string) ([]Result, error) {
	out := make([]Result, len(commands))
	if !isPowerShell(shell) {
		for i, c := range commands {
			out[i] = Bash(c, roots)
		}
		return out, nil
	}
	if len(commands) == 0 {
		return out, nil
	}
	b64 := func(s string) string { return "'" + base64.StdEncoding.EncodeToString([]byte(s)) + "'" }
	dec := func(v string) string { return "([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + v + ")))" }
	var script strings.Builder
	script.WriteString(doctor.ReadOnlyPS)
	script.WriteString("\n")
	script.WriteString(agent.PolicyPS)
	script.WriteString("\n$roots = @(")
	for i, r := range roots {
		if i > 0 {
			script.WriteString(",")
		}
		script.WriteString(dec(b64(r)))
	}
	script.WriteString(")\nforeach ($b in @(")
	for i, c := range commands {
		if i > 0 {
			script.WriteString(",")
		}
		script.WriteString(b64(c))
	}
	fmt.Fprintf(&script, `)) {
    $r = Get-CommandTier %s '%s' $roots
    [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($r.Tier + [char]9 + $r.Reason))
}
`, dec("$b"), shell)
	exe := "pwsh"
	if runtime.GOOS == "windows" {
		exe = "powershell.exe"
	}
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"$s = [Console]::In.ReadToEnd(); . ([scriptblock]::Create($s))")
	cmd.Stdin = strings.NewReader(script.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", exe, err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Fields(string(raw))
	if len(lines) != len(commands) {
		return nil, fmt.Errorf("%s returned %d answers for %d commands: %s", exe, len(lines), len(commands), strings.TrimSpace(stderr.String()))
	}
	for i, l := range lines {
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			return nil, fmt.Errorf("%s: answer %d: %v", exe, i, err)
		}
		tier, reason, _ := strings.Cut(string(b), "\t")
		out[i] = Result{tier, reason}
	}
	return out, nil
}
