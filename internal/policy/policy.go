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
	"github.com/InstallZero/installzero/internal/readonly"
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
	psrx.MustCompile(`(?i)\bNew-Object\b[^;|\n]*-Com(Object)?\b`),
}

// projectCommands only act on the current folder or on paths named in the
// command; with the path check they stay inside the install folder.
var projectCommands = psrx.MustCompile(`(?i)^(git|docker|docker-compose|python|python3|py|pip|pip3|uv|conda|mamba|npm|npx|pnpm|yarn|node|cargo|go|dotnet|make|cmake|curl|wget|tar|unzip|7z|New-Item|New-Object|mkdir|md|Set-Content|Add-Content|Out-File|Copy-Item|cp|copy|Move-Item|mv|move|Remove-Item|rm|del|rmdir|Rename-Item|Expand-Archive|Compress-Archive|Invoke-WebRequest|iwr|Invoke-RestMethod|irm|Invoke-IzDownload|iz_download|Set-Location|cd|Push-Location|Pop-Location|Write-Host|Write-Output|Write-(Error|Warning|Verbose|Debug|Information|Progress)|Out-Null|Out-String|Format-\w+|Sort-Object|Measure-Object|Group-Object|Compare-Object|Tee-Object|Select-String|ConvertTo-\w+|ConvertFrom-\w+|Unblock-File|Wait-Process|(Set|Clear|Remove)-Variable|echo|Start-Sleep|ForEach-Object|Where-Object|%|\?|Select-Object|Test-Path|Join-Path|Get-\w+|Resolve-Path|Split-Path|\.\\[\w.-]+\.(ps1|bat|cmd|exe)|[\w.-]+\.(bat|cmd))$`)

var (
	drivePath = psrx.MustCompileCase(`(?i)(?<![\w/])([a-z]:[\\/][^\s'"` + "`" + `|;,)]*)`)
	envPath   = psrx.MustCompileCase(`(?i)(\$env:(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)|%(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)%|~[\\/])[^\s'"` + "`" + `|;,)]*`)
	envPrefix = psrx.MustCompile(`^\$env:(\w+)`)
	pctPrefix = psrx.MustCompile(`^%(\w+)%`)
)

var bashBuiltins = []string{"cd", "export", "source", ".", "set", "echo", "test", "[", "true", "false"}

// Bash only: destructive and machine-wide commands of Linux and macOS.
var (
	neverUnix = []struct {
		re  *psrx.Regexp
		why string
	}{
		{psrx.MustCompile(`(?i)\b(mkfs(\.\w+)?|wipefs|fdisk|sfdisk|parted|sgdisk)\b|\bdd\b[^;&|\n]*\bof=/dev/`), "changes disks or partitions"},
		{psrx.MustCompile(`(?i)\brm\s+(-\w+\s+)*-\w*r\w*\s+(-\w+\s+)*(/(etc|usr|var|boot|lib|lib64|bin|sbin|opt|home|root|srv)|~|\$HOME|\$\{HOME\})/?(\s|$)`), "deletes a system or home folder"},
		{psrx.MustCompile(`(?i)\bchmod\s+-R\s+\S+\s+/(\s|$)|\bchown\s+-R\s+\S+\s+/(\s|$)`), "changes permissions of the whole file system"},
		{psrx.MustCompile(`(?i)(^|[;&|]\s*)(sudo\s+)?(reboot|poweroff|halt)\b|\bshutdown\s+(-[rhP]|now)|\bsystemctl\s+(reboot|poweroff|halt)\b`), "restarts or shuts down the machine; ask the user to do it"},
		{psrx.MustCompile(`:\(\)\s*\{`), "a fork bomb"},
	}
	systemUnix = []*psrx.Regexp{
		psrx.MustCompile(`(?i)\b(curl|wget)\b[^|\n]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`),
		psrx.MustCompile(`(?i)\bsystemctl\s+(--\S+\s+)*(enable|disable|start|stop|restart|reload|mask|unmask|daemon-reload|edit|set-default|isolate|link)\b`),
		psrx.MustCompile(`(?i)\bservice\s+\S+\s+(start|stop|restart|reload)\b`),
		psrx.MustCompile(`(?i)\b(useradd|usermod|userdel|groupadd|groupdel|adduser|addgroup|deluser|passwd|chpasswd|visudo|chsh)\b`),
		psrx.MustCompile(`(?i)\b(update-alternatives|ldconfig|update-ca-certificates|locale-gen|modprobe|ufw|iptables|ip6tables|nft|firewall-cmd|flatpak)\b|\bsysctl\s+-w\b|\b(timedatectl|hostnamectl)\s+set`),
		psrx.MustCompile(`(?i)\b(gem|cargo|go|pipx)\s+install\b|\bnpm\s+(i|install)\b[^;&|\n]*\s(-g|--global)\b`),
		psrx.MustCompile(`(?i)\bmake\b[^;&|\n]*\binstall\b`),
		psrx.MustCompile(`(?i)\bcrontab\s+(?!-l\b)`),
	}
	unixPath    = psrx.MustCompileCase(`(?<=^|[\s=('"<>])(~(/[^\s'"` + "`" + `|;&)<>]*)?|\$\{?HOME\}?(/[^\s'"` + "`" + `|;&)<>]*)?|/[^\s'"` + "`" + `|;&)<>]*)`)
	safeUnix    = psrx.MustCompileCase(`^(/dev/(null|stdout|stderr|stdin|zero|u?random|tty|fd/\d+)|/tmp(/.*)?|/var/tmp(/.*)?)$`)
	binDir      = psrx.MustCompileCase(`^/(usr/(local/)?)?s?bin/[^/]+$`)
	continued   = psrx.MustCompile(`\\\r?\n`)
	shellWord   = psrx.MustCompile(`^(ba|z|da)?sh$`)
	keywordWord = psrx.MustCompile(`^(if|then|else|elif|fi|for|while|until|do|done|in|case|esac|function|time|!|\{|\}|local|declare|readonly|nohup|exec|command|env|xargs)$`)
	assignWord  = psrx.MustCompile(`^\w+=`)
	venvPath    = psrx.MustCompile(`(venv|\.venv|env)[\\/]`)
)

// insideUnix reports whether a Unix path is inside one of the roots (Unix
// roots only); ~ and $HOME count as outside, since the home folder belongs
// to the user, not the project.
func insideUnix(p string, roots []string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	full := cleanUnix(p)
	for _, r := range roots {
		if !strings.HasPrefix(r, "/") {
			continue
		}
		root := cleanUnix(r)
		if full == root || strings.HasPrefix(full, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

func cleanUnix(p string) string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, s)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// words splits a simple command into words, keeping quoted parts together
// and dropping a # comment.
func words(cmd string) []string {
	var out []string
	var cur strings.Builder
	var q rune
	for _, ch := range cmd {
		switch {
		case q == 0 && ch == '#' && cur.Len() == 0:
			return out
		case q != 0:
			if ch == q {
				q = 0
			} else {
				cur.WriteRune(ch)
			}
		case ch == '\'' || ch == '"':
			q = ch
		case ch == ' ' || ch == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(ch)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

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
	// A wildcard pattern is as wide as its literal part.
	if i := strings.IndexAny(path, "*?"); i >= 0 {
		path = path[:i]
	}
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
// project-tier changes may touch (Unix paths, or Windows ones for bash on
// Windows).
//
// Unlike agent/Policy.ps1, which only met bash in Windows installs, this
// knows Linux and macOS: read-only commands are read, Unix paths outside
// the roots and Linux system commands are system, and programs inside the
// project (./configure, venv/bin/pip) are project.
func Bash(command string, roots []string) Result {
	c := continued.Replace(command, " ")
	for _, n := range neverUnix {
		if n.re.Match(c) {
			return Result{Never, n.why}
		}
	}
	if r, ok := neverTier(c); ok {
		return r
	}
	if readonly.Bash(c) == "" {
		return Result{Read, "read-only"}
	}
	for _, re := range append(append([]*psrx.Regexp{}, system...), systemUnix...) {
		if g := re.Groups(c); g != nil {
			return Result{System, fmt.Sprintf("machine-wide change (%s)", strings.TrimSpace(g[0]))}
		}
	}
	var outside []string
	for _, p := range commandPaths(c) {
		if !insideFolder(p, roots) {
			outside = append(outside, p)
		}
	}
	venv := venvPath.Match(c)
	for _, seg := range readonly.SplitBash(c) {
		ws := words(seg)
		i := 0
		for i < len(ws) && (keywordWord.Match(ws[i]) || assignWord.Match(ws[i])) {
			// A loop header names a variable and lists words; command
			// substitutions in the list are segments of their own.
			if ws[i] == "for" || ws[i] == "select" {
				i = len(ws)
				break
			}
			i++
		}
		if i >= len(ws) {
			continue
		}
		name, args := ws[i], ws[i+1:]
		for _, a := range args {
			for _, g := range unixPath.AllGroups(a) {
				if p := g[1]; !safeUnix.Match(p) && !insideUnix(p, roots) {
					outside = append(outside, p)
				}
			}
		}
		switch {
		case strings.HasPrefix(name, "/") && insideUnix(name, roots):
			continue
		case strings.HasPrefix(name, "/") && binDir.Match(name):
			name = name[strings.LastIndex(name, "/")+1:]
		case strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") || strings.HasPrefix(name, "$"):
			return Result{System, fmt.Sprintf("runs a program outside the install folder (%s)", name)}
		case strings.Contains(name, "/"):
			continue
		}
		if shellWord.Match(name) {
			if len(args) == 0 || strings.HasPrefix(args[0], "-") {
				return Result{System, fmt.Sprintf("'%s' runs a script from its input", name)}
			}
			continue
		}
		if readonly.Bash(seg) == "" {
			continue
		}
		builtin := false
		for _, b := range bashBuiltins {
			if strings.EqualFold(name, b) {
				builtin = true
			}
		}
		if !builtin && !projectCommands.Match(name) {
			return Result{System, fmt.Sprintf("'%s' is not a known project-folder command", name)}
		}
		if (name == "pip" || name == "pip3") && !venv {
			return Result{System, "pip outside a virtual environment installs for the whole machine; use the venv's pip"}
		}
	}
	if len(outside) > 0 {
		return Result{System, "touches paths outside the install folder: " + strings.Join(outside, ", ")}
	}
	return Result{Project, "changes only the install folder"}
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
	dec := func(v string) string {
		return "([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + v + ")))"
	}
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
