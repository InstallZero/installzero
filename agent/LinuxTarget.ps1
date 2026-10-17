# Probe target inside a WSL distro: bash commands run there as root, through
# wsl.exe with the script on stdin (Windows PowerShell mangles quotes in
# native arguments). Read-only allowlist for bash, Linux inventory, and the
# machine profile install.md variants are chosen against.

function Invoke-WslBash([string]$Distro, [string]$Script, [int]$TimeoutSec = 60, [string]$WorkingDirectory = '') {
    $psi = [Diagnostics.ProcessStartInfo]::new('wsl.exe', "-d $Distro -u root -- bash -ls")
    $psi.UseShellExecute = $false; $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true; $psi.RedirectStandardOutput = $true; $psi.RedirectStandardError = $true
    $psi.StandardOutputEncoding = [Text.UTF8Encoding]::new($false); $psi.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    $p = [Diagnostics.Process]::Start($psi)
    $out = $p.StandardOutput.ReadToEndAsync(); $err = $p.StandardError.ReadToEndAsync()
    $body = if ($WorkingDirectory) { "cd '$WorkingDirectory' 2>/dev/null`n$Script" } else { $Script }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($body -replace "`r`n", "`n") + "`n")
    $p.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length); $p.StandardInput.Close()
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
        try { $p.Kill() } catch { }
        return [pscustomobject]@{ Code = 124; Out = "$($out.Result)`n[timed out after $TimeoutSec s]" }
    }
    $p.WaitForExit()
    $text = ("$($out.Result)" + $(if ("$($err.Result)".Trim()) { "`n$($err.Result)" })).TrimEnd()
    [pscustomobject]@{ Code = $p.ExitCode; Out = $text }
}

# WSL distros share one network namespace, so a Docker daemon's bridges
# (docker0, br-*) and iptables rules outlive its distro and collide with the
# next daemon's subnets. Clears them all: run only while no other distro uses
# Docker (the eval scheduler runs one Docker install at a time). Stops
# Docker in the distro; boot it again to bring the install back.
function Clear-WslDockerNet([string]$Distro) {
    Invoke-WslBash $Distro @'
systemctl stop docker.socket docker containerd >/dev/null 2>&1 || true
for b in $(ip -o link show type bridge | awk -F': ' '{print $2}' | cut -d@ -f1 | grep -E '^(docker0|br-[0-9a-f]{12})$'); do ip link delete "$b" 2>/dev/null || true; done
if command -v iptables-save >/dev/null; then iptables-save | grep -vi docker | iptables-restore 2>/dev/null || true; fi
if command -v ip6tables-save >/dev/null; then ip6tables-save | grep -vi docker | ip6tables-restore 2>/dev/null || true; fi
true
'@ 120
}

function Format-WslResult($R, [int]$Max = 12000) {
    $t = "$($R.Out)"
    if ($t.Length -gt $Max) { $t = $t.Substring(0, $Max) + "`n[... cut at $Max characters]" }
    "[exit $($R.Code)]`n$t"
}

# Values of password, secret, token and key settings, in config files and
# environment listings.
function Protect-LinuxSecrets([string]$Text) {
    if (-not $Text) { return $Text }
    $Text -replace '(?im)^(\s*[\w.-]*(password|passwd|secret|token|api_?key|private_?key|access_?key)[\w.-]*\s*[:=]\s*)(?!\s*$)(?!<secret>)[^\r\n#]+', '$1<secret>'
}

# Read-only bash: every command in the script (including in pipelines,
# lists, subshells and command substitution) must be on the allowlist, with
# its read-only subcommands. No output redirection except to /dev/null.
$script:BashReadOnly = @{
    any     = '^(cd|cat|ls|ll|head|tail|grep|egrep|fgrep|zgrep|zcat|ps|ss|netstat|lsof|journalctl|which|whereis|type|uname|lsb_release|hostname|id|whoami|groups|getent|env|printenv|df|du|free|nproc|lscpu|lsblk|findmnt|nvidia-smi|nvcc|test|\[|\[\[|echo|printf|true|false|stat|file|readlink|realpath|wc|sort|uniq|cut|tr|jq|yq|tree|basename|dirname|date|uptime|column|namei|getfacl|ldd|md5sum|sha256sum|exit|sleep|seq|diff|cmp|strings|nl|less|more|pwd|ulimit|timedatectl|hostnamectl|lsmod|blkid|awk|sed|xxd|od|python3?|node|npm|pip3?|git|docker|systemctl|dpkg|dpkg-query|apt|apt-cache|apt-mark|snap|curl|wget|ip|sysctl|crontab|openssl|find|command|mount|java|go|rustc|cargo|conda|ruby|php|psql|mysql|redis-cli|helm|kubectl|kind|k3s)$'
    keyword = '^(if|then|else|elif|fi|for|while|until|do|done|in|case|esac|function|time|!|\{|\}|local|export|declare|readonly)$'
}
function Test-BashReadOnly([string]$Code) {
    $c = $Code -replace '\\\r?\n', ' '
    if ($c -match '`') { return 'uses backticks' }
    $noNull = $c -replace '\d?>>?\s*/dev/null|&>\s*/dev/null|\d?>&\d', ''
    if ($noNull -match '(^|[^<>=-])>{1,2}\s*[^\s>&]') { return 'writes a file (output redirection)' }
    if ($c -match '(?i)\b(sudo|su|tee|xargs|eval|exec|source|nohup|setsid|dd|truncate|shred)\b') { return "'$($Matches[1])' is not read-only" }
    # Command substitutions are checked on their own and stand for a word in
    # the command around them.
    $subs = [Collections.Generic.List[string]]::new()
    while ($c -match '(\$|<)\(([^()]*)\)') { $subs.Add($Matches[2]); $c = $c.Replace($Matches[0], 'SUBST') }
    foreach ($s in $subs) { if ($s.Trim()) { $w = Test-BashReadOnly $s; if ($w) { return $w } } }
    $segments = $c -replace '[()]', ';' -split '\|\||&&|[;|&\r\n]'
    foreach ($seg in $segments) {
        $words = @(($seg.Trim() -replace '^(\w+=("[^"]*"|''[^'']*''|\S*)\s+)+', '') -split '\s+' | Where-Object { $_ })
        if ($words.Count -and $words[0] -in 'for', 'select') { continue }
        while ($words.Count -and $words[0] -match $script:BashReadOnly.keyword) { $words = @($words | Select-Object -Skip 1) }
        if (-not $words.Count -or $words[0] -match '^\w+=' -or $words[0] -match '^[\w-]+\)$' -or $words[0] -eq ';;') { continue }
        $cmd = ($words[0] -replace '^.*/', ''); $rest = ($words | Select-Object -Skip 1) -join ' '
        if ($cmd -notmatch $script:BashReadOnly.any) { return "'$cmd' is not on the read-only list" }
        $sub = "$($words[1]) $($words[2]) $($words[3])".Trim()
        $why = switch -Regex ($cmd) {
            '^docker$' { if ($rest -notmatch '^(ps|images|inspect|logs|version|info|top|port|stats\b.*--no-stream|system df|image (ls|inspect|history)|container (ls|inspect|logs|top|port)|network (ls|inspect)|volume (ls|inspect)|compose\b.*\b(ps|ls|config|images|version|logs|top)\b|context (ls|show)|buildx (ls|version)|--version|-v)\b') { 'docker: only ps, images, inspect, logs, version, info and other listings' } }
            '^systemctl$' { if ($rest -notmatch '(^|\s)(status|list-units|list-unit-files|list-timers|list-sockets|show|is-active|is-enabled|is-failed|cat|get-default|--version)\b') { 'systemctl: only status, list-*, show, is-*, cat' } }
            '^(dpkg)$' { if ($rest -notmatch '^(-l|-s|-L|-S|-p|--list|--status|--listfiles|--search|--print-architecture|--get-selections|--version)') { 'dpkg: only queries' } }
            '^apt$' { if ($sub -notmatch '^(list|policy|show|search|--version)') { 'apt: only list, policy, show, search' } }
            '^apt-mark$' { if ($sub -notmatch '^(showmanual|showauto|showhold)') { 'apt-mark: only show*' } }
            '^snap$' { if ($sub -notmatch '^(list|info|version|services)') { 'snap: only list, info, services' } }
            '^pip3?$' { if ($sub -notmatch '^(list|show|freeze|--version|-V|config list)') { 'pip: only list, show, freeze' } }
            '^python3?$' { if ($rest -notmatch '^(--version|-V|-m pip (list|show|freeze)|-c\s+["'']?import \w+[\w., ]*;\s*print\([\w.]*(__version__|version)[\w.()]*\)["'']?\s*$)') { 'python: only --version, pip listings and printing a module version' } }
            '^node$' { if ($rest -notmatch '^(-v|--version)') { 'node: only --version' } }
            '^npm$' { if ($sub -notmatch '^(ls|list|-v|--version|config (get|list)|view|root)') { 'npm: only ls, view, config get' } }
            '^git$' { if ($rest -notmatch '^(-C\s+\S+\s+)?(status|log|remote|branch|rev-parse|describe|tag|ls-files|show-ref|config (--get|-l|--list)|submodule status|--version)\b' -or $rest -match '\s(-p|--patch)\b') { 'git: only status, log, remote, branch, rev-parse, describe, tag, ls-files' } }
            '^curl$' { if ($rest -match '(^|\s)(-o|-O|--output|--remote-name|-T|--upload-file|-d|--data\S*|-F|--form)\b' -or $rest -match '(-X|--request)\s+(?!GET|HEAD)') { 'curl: only GET and HEAD to stdout' } }
            '^wget$' { if ($rest -notmatch '(-O\s*-|-qO-|--spider)') { 'wget: only to stdout (-O -) or --spider' } }
            '^sed$' { if ($rest -match '(^|\s)-i|\bw\s|[;/]w\s*/|\be\b\s*$') { 'sed: no in-place edits or writes' } }
            '^awk$' { if ($rest -match 'system\s*\(|\|\s*"|print[^;}]*>') { 'awk: no system() or writes' } }
            '^find$' { if ($rest -match '-(delete|exec|execdir|ok|okdir|fprint\w*|fls)\b') { 'find: no -delete or -exec' } }
            '^sysctl$' { if ($rest -match '-w\b|=') { 'sysctl: read only' } }
            '^crontab$' { if ($rest -notmatch '^(-l|-u\s+\S+\s+-l)') { 'crontab: only -l' } }
            '^ip$' { if ($rest -notmatch '^(-\w+\s+)*(a|addr|address|r|route|l|link|n|neigh)(\s+(show|list|ls))?(\s|$)' -or $rest -match '\b(add|del|delete|set|flush|change|replace)\b') { 'ip: only show' } }
            '^mount$' { if ($rest) { 'mount: only without arguments' } }
            '^openssl$' { if ($sub -notmatch '^(version|x509|s_client)') { 'openssl: only version and x509 inspection' } }
            '^(psql|mysql|redis-cli)$' { 'database clients are not run' }
            '^(helm|kubectl)$' { if ($sub -notmatch '^(get|describe|version|list|ls|status|config (view|get-contexts|current-context)|api-resources|cluster-info|top|logs)') { "$($cmd): only get, describe, list, logs" } }
            '^(kind|k3s)$' { if ($sub -notmatch '^(get|version|--version)') { "$($cmd): only get and version" } }
            '^(conda)$' { if ($sub -notmatch '^(env list|list|info|--version)') { 'conda: only listings' } }
            '^(cargo|go|java|rustc|ruby|php)$' { if ($rest -notmatch '^(-?-?version|-v|-V|env|list)\b') { "$($cmd): only version queries" } }
        }
        if ($why) { return $why }
    }
    $null
}

function Get-WslMachineProfile([string]$Distro) {
    $r = Invoke-WslBash $Distro @'
. /etc/os-release; echo "os_version=$PRETTY_NAME"
echo "arch=$(uname -m)"
echo "ram_gb=$(free -g | awk '/^Mem:/{print $2}')"
echo "disk_gb=$(df -BG --output=avail / | tail -1 | tr -dc 0-9)"
if command -v nvidia-smi >/dev/null 2>&1 || [ -x /usr/lib/wsl/lib/nvidia-smi ]; then
  s=$(command -v nvidia-smi || echo /usr/lib/wsl/lib/nvidia-smi)
  echo "gpus=$($s --query-gpu=name --format=csv,noheader 2>/dev/null | paste -sd, -)"
  echo "driver=$($s --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1)"
fi
'@ 30
    $v = @{}; foreach ($l in ($r.Out -split "`n")) { if ($l -match '^(\w+)=(.*)$') { $v[$Matches[1]] = $Matches[2].Trim() } }
    [pscustomobject]@{
        os = 'linux'; arch = $(switch ($v.arch) { 'x86_64' { 'x64' } 'aarch64' { 'arm64' } default { "$($v.arch)" } })
        gpu = $(if ($v.gpus -match 'NVIDIA') { 'nvidia' } elseif ($v.gpus) { 'other' } else { 'none' }); gpus = "$($v.gpus)"
        ram_gb = [int]("0$($v.ram_gb)"); disk_gb = [int]("0$($v.disk_gb)"); driver = $(if ($v.driver) { $v.driver } else { $null })
        os_version = "$($v.os_version)"
    }
}

$script:LinuxInventoryAreas = 'software', 'containers', 'processes', 'ports', 'services', 'system', 'files', 'python', 'node', 'git', 'gpu', 'env'
$script:LinuxInventoryHelp = 'Read-only inventory of one area of this Linux machine. software: packages installed by hand (apt-mark showmanual with versions), apt sources and keyrings, snaps, /opt and /usr/local. containers: containers with compose project, working dir and files, images, volumes, networks. processes: all processes with command lines. ports: listening TCP and UDP ports with the owning program. services: running and enabled systemd services, timers, cron. system: OS, kernel, hostname, /etc/hosts, addresses, users and groups, sysctl settings, mounts, disk and memory. files: folders created or changed since the OS was installed, under /etc, /opt, /srv, /data, /usr/local, /var/lib, /home and /root. python: interpreters, venvs under path with their packages. node: node, npm and global packages. git: repos under path with remote, branch, commit. gpu: nvidia-smi, nvcc, CUDA. env: environment of a login shell, /etc/environment and /etc/profile.d (secrets masked).'

function Get-LinuxInventory([string]$Distro, [string]$Area, [string]$Path) {
    $p = if ($Path) { $Path } else { '/' }
    $sh = switch ($Area) {
        'software' { @'
echo "Packages installed by hand (apt-mark showmanual):"; dpkg-query -W -f='${Package} ${Version}\n' $(apt-mark showmanual) 2>/dev/null
echo; echo "apt sources:"; ls -la /etc/apt/sources.list.d/ 2>/dev/null; for f in /etc/apt/sources.list.d/*; do [ -f "$f" ] && { echo "--- $f"; grep -v '^#' "$f" | grep -v '^\s*$'; }; done
echo; echo "keyrings:"; ls -la /etc/apt/keyrings /usr/share/keyrings 2>/dev/null
echo; echo "snaps:"; snap list 2>/dev/null || echo "(no snapd)"
echo; echo "/opt:"; ls -la /opt; echo; echo "/usr/local/bin:"; ls -la /usr/local/bin
'@ }
        'containers' { @'
command -v docker >/dev/null || { echo "docker is not installed"; exit 0; }
echo "docker: $(docker --version)"; docker info --format 'server {{.ServerVersion}}, storage {{.Driver}}, root {{.DockerRootDir}}, cgroup {{.CgroupDriver}}' 2>&1
echo; echo "Containers:"; docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}\tproject={{.Label "com.docker.compose.project"}} service={{.Label "com.docker.compose.service"}}'
echo; echo "Compose projects:"; docker compose ls -a 2>&1
echo; echo "Compose working dirs and files:"; for c in $(docker ps -aq); do docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}: {{index .Config.Labels "com.docker.compose.project.working_dir"}} [{{index .Config.Labels "com.docker.compose.project.config_files"}}]' $c; done | sort -u
echo; echo "Images:"; docker images --format '{{.Repository}}:{{.Tag}}  {{.Size}}  {{.CreatedSince}}' | head -80
echo; echo "Volumes:"; docker volume ls --format '{{.Name}} {{.Mountpoint}}'
echo; echo "Networks:"; docker network ls --format '{{.Name}} {{.Driver}}'
'@ }
        'processes' { "ps -eo pid,user,etime,args --sort=pid | grep -v ' \[[^]]*\]`$' | grep -v 'bash -ls`$' | head -150" }
        'ports' { 'ss -ltnup 2>/dev/null | head -80' }
        'services' { @'
echo "Running services:"; systemctl list-units --type=service --state=running --no-pager --no-legend 2>&1
echo; echo "Enabled unit files:"; systemctl list-unit-files --state=enabled --no-pager --no-legend 2>&1
echo; echo "Units in /etc/systemd/system:"; find /etc/systemd/system -maxdepth 2 \( -name '*.service' -o -name '*.timer' -o -name '*.conf' \) 2>/dev/null
echo; echo "Timers:"; systemctl list-timers --no-pager --no-legend 2>&1 | head -30
echo; echo "Cron:"; for u in $(cut -d: -f1 /etc/passwd); do crontab -l -u $u 2>/dev/null | sed "s/^/$u: /"; done; ls -la /etc/cron.d 2>/dev/null
'@ }
        'system' { @'
. /etc/os-release; echo "OS: $PRETTY_NAME; kernel $(uname -r); arch $(uname -m)"; echo "hostname: $(hostname)"
echo; echo "/etc/hosts:"; grep -v '^#' /etc/hosts | grep -v '^\s*$'
echo; echo "addresses:"; ip -br addr 2>/dev/null
echo; echo "users with a shell:"; getent passwd | awk -F: '$7 !~ /(nologin|false|sync)$/ {print $1" uid="$3" home="$6" shell="$7}'
echo; echo "groups with members:"; getent group | awk -F: '$4 != "" {print $1": "$4}'
echo; echo "sysctl settings in /etc:"; grep -hv '^\s*[#;]' /etc/sysctl.conf /etc/sysctl.d/*.conf 2>/dev/null | grep -v '^\s*$'
echo; echo "mounts:"; findmnt -rn -o TARGET,SOURCE,FSTYPE -t ext4,xfs,btrfs,nfs,nfs4,cifs,overlay 2>/dev/null | grep -v '/var/lib/docker' | head -30
echo; df -h / ; free -h
'@ }
        'files' { "for d in /etc /opt /srv /data /usr/local /var/lib /home /root; do [ -d `$d ] && find `$d -xdev -maxdepth 3 -newer /usr/lib/os-release -type d 2>/dev/null; done | grep -Ev '^/var/lib/(docker|containerd|apt|dpkg|systemd|ucf|polkit|private)/|^/etc/(alternatives|ssl/certs)' | sort | head -250" }
        'python' { "echo `"python3: `$(python3 --version 2>&1) at `$(command -v python3)`"; command -v conda >/dev/null && conda env list; find '$p' -maxdepth 5 -name pyvenv.cfg 2>/dev/null | head -8 | while read c; do d=`$(dirname `$c); echo `"venv `$d: `$(tr '\n' ';' < `$c)`"; `$d/bin/pip list --format freeze 2>/dev/null | head -80 | paste -sd, - | sed 's/^/  packages: /'; done" }
        'node' { 'echo "node $(node -v 2>&1); npm $(npm -v 2>&1)"; npm ls -g --depth 0 2>&1' }
        'git' { "find '$p' -maxdepth 4 -name .git -type d 2>/dev/null | head -15 | while read g; do d=`$(dirname `$g); echo `"`$d`"; echo `"  remote: `$(git -C `$d remote get-url origin 2>&1)`"; echo `"  branch: `$(git -C `$d rev-parse --abbrev-ref HEAD 2>&1) at `$(git -C `$d log -1 --format='%h %cs %s' 2>&1)`"; echo `"  local changes: `$(git -C `$d status --porcelain 2>/dev/null | wc -l) files`"; done" }
        'gpu' { 'export PATH=$PATH:/usr/lib/wsl/lib; nvidia-smi 2>&1 | head -25; echo "nvcc: $(nvcc --version 2>&1 | tail -2 | tr "\n" " ")"; ls -d /usr/local/cuda* 2>/dev/null' }
        'env' { 'echo "Login shell environment:"; env | sort | grep -v "^_="; echo; echo "/etc/environment:"; cat /etc/environment; echo; echo "/etc/profile.d:"; ls /etc/profile.d' }
        default { return "error: unknown area '$Area'; use $($script:LinuxInventoryAreas -join ', ')" }
    }
    (Invoke-WslBash $Distro $sh 90).Out
}
