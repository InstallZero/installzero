// Package track records what an install changed on a machine, so that
// installzero uninstall can undo exactly that. Before and after each step
// it takes a read-only snapshot (packages, files in the places installs
// write to, services, users, Docker objects; on Windows also programs,
// PATH, environment variables and scheduled tasks) and keeps the
// difference.
package track

import (
	"strings"

	"github.com/InstallZero/installzero/internal/target"
)

// Snapshot is a machine's state: per category, item key to extra text.
type Snapshot map[string]map[string]string

func parse(out string) Snapshot {
	s := Snapshot{}
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		cat, rest, ok := strings.Cut(l, "\t")
		if !ok || rest == "" {
			continue
		}
		key, extra, _ := strings.Cut(rest, "\t")
		if s[cat] == nil {
			s[cat] = map[string]string{}
		}
		s[cat][key] = extra
	}
	return s
}

// The bash snapshot: "category<TAB>key[<TAB>extra]" lines. Folders that
// hold many files of their own (.git, node_modules, virtual environments)
// are listed but not walked; they are removed whole.
const bashSnapshot = `set +e
DIR=%s
prune='( -name .git -o -name node_modules -o -name __pycache__ -o -name .venv -o -name venv -o -name site-packages -o -name dist-packages )'
if command -v dpkg-query >/dev/null 2>&1; then
  printf 'pm\tapt\n'
  dpkg-query -W -f='${db:Status-Abbrev}\t${Package}\t${Version}\n' 2>/dev/null | awk -F'\t' '$1 ~ /^ii/ {print "package\t" $2 "\t" $3}'
elif command -v rpm >/dev/null 2>&1; then
  printf 'pm\trpm\n'
  rpm -qa --qf 'package\t%%{NAME}\t%%{VERSION}-%%{RELEASE}\n' 2>/dev/null
fi
for r in /etc /opt /usr/local /srv /var/spool/cron "$DIR"; do
  [ -e "$r" ] && find "$r" -xdev $prune -prune -print -o -print 2>/dev/null | sed 's/^/path\t/'
done
find /etc -xdev -type f -size -4M -print0 2>/dev/null | xargs -0 md5sum 2>/dev/null | sed -E 's/^([0-9a-f]{32})  (.*)$/sum\t\2\t\1/'
for h in /root /home/*; do
  [ -d "$h" ] || continue
  for d in "$h" "$h/.config" "$h/.local/share" "$h/.local/bin" "$h/.cache"; do
    [ -d "$d" ] && find "$d" -mindepth 1 -maxdepth 1 2>/dev/null | sed 's/^/top\t/'
  done
done
for d in /var/lib /var/log /var/cache /usr/bin /usr/sbin /usr/lib/systemd/system; do
  [ -d "$d" ] && find "$d" -mindepth 1 -maxdepth 1 2>/dev/null | sed 's/^/top\t/'
done
if command -v systemctl >/dev/null 2>&1; then
  systemctl list-unit-files --state=enabled --no-legend --plain 2>/dev/null | awk '{print "enabled\t" $1}'
  systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null | awk '{print "running\t" $1}'
fi
cut -d: -f1 /etc/passwd 2>/dev/null | sed 's/^/user\t/'
cut -d: -f1 /etc/group 2>/dev/null | sed 's/^/group\t/'
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  docker ps -a --no-trunc --format 'container\t{{.ID}}\t{{.Names}} ({{.Image}})'
  docker images --no-trunc --format 'image\t{{.ID}}\t{{.Repository}}:{{.Tag}}'
  docker volume ls -q | sed 's/^/volume\t/'
  docker network ls --no-trunc --format 'network\t{{.ID}}\t{{.Name}}'
fi
true
`

const psSnapshot = `$ErrorActionPreference = 'SilentlyContinue'
$dir = %s
$o = [Collections.Generic.List[string]]::new()
foreach ($k in 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall', 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall') {
    Get-ChildItem $k | ForEach-Object {
        $p = Get-ItemProperty $_.PSPath
        if ($p.DisplayName -and -not $p.SystemComponent) {
            $u = if ($p.QuietUninstallString) { $p.QuietUninstallString } else { $p.UninstallString }
            $o.Add("program` + "`t" + `$($_.Name)` + "`t" + `$($p.DisplayName)|$u")
        }
    }
}
foreach ($s in 'Machine', 'User') {
    $scope = $s.ToLower()
    foreach ($e in @([Environment]::GetEnvironmentVariable('Path', $s) -split ';' | Where-Object { $_ })) { $o.Add("path-$scope` + "`t" + `$e") }
    foreach ($n in [Environment]::GetEnvironmentVariables($s).Keys) { if ($n -ne 'Path') { $o.Add("env-$scope` + "`t" + `$n") } }
}
Get-Service | ForEach-Object { $o.Add("service` + "`t" + `$($_.Name)` + "`t" + `$($_.DisplayName)") }
Get-ScheduledTask | ForEach-Object { $o.Add("task` + "`t" + `$($_.TaskPath)$($_.TaskName)") }
foreach ($t in @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:ProgramData, "$env:LOCALAPPDATA\Programs", $env:LOCALAPPDATA, $env:APPDATA, $env:USERPROFILE) | Where-Object { $_ -and (Test-Path -LiteralPath $_) }) {
    Get-ChildItem -LiteralPath $t -Force | ForEach-Object { $o.Add("top` + "`t" + `$($_.FullName)") }
}
$skip = '.git', 'node_modules', '__pycache__', '.venv', 'venv', 'site-packages'
function Walk([string]$p) {
    $o.Add("path` + "`t" + `$p")
    $i = Get-Item -LiteralPath $p -Force
    if ($i.PSIsContainer -and -not ($i.Attributes -band [IO.FileAttributes]::ReparsePoint) -and $i.Name -notin $skip) {
        Get-ChildItem -LiteralPath $p -Force | ForEach-Object { Walk $_.FullName }
    }
}
if ($dir -and (Test-Path -LiteralPath $dir)) { Walk $dir }
if (Get-Command docker -ErrorAction SilentlyContinue) {
    cmd /c "docker info >nul 2>&1"
    if ($LASTEXITCODE -eq 0) {
        cmd /c "docker ps -a --no-trunc --format ""container\t{{.ID}}\t{{.Names}} ({{.Image}})"" 2>nul" | ForEach-Object { $o.Add($_) }
        cmd /c "docker images --no-trunc --format ""image\t{{.ID}}\t{{.Repository}}:{{.Tag}}"" 2>nul" | ForEach-Object { $o.Add($_) }
        cmd /c "docker volume ls -q 2>nul" | ForEach-Object { $o.Add("volume` + "`t" + `$_") }
        cmd /c "docker network ls --no-trunc --format ""network\t{{.ID}}\t{{.Name}}"" 2>nul" | ForEach-Object { $o.Add($_) }
    }
}
$o -join "` + "`n" + `"
`

func snapshotScript(windows bool, dir string) (shell, code string) {
	if windows {
		return "powershell", strings.Replace(psSnapshot, "%s", "'"+strings.ReplaceAll(dir, "'", "''")+"'", 1)
	}
	return "bash", strings.Replace(strings.Replace(bashSnapshot, "%s", target.Quote(dir), 1), "%%", "%", -1)
}
