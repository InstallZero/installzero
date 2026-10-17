<#
.SYNOPSIS
    Build an install-bench snapshot: a WSL distro with one project installed
    and running, its install files removed, exported as a tar.

.DESCRIPTION
    projects\<id>.json names the recipe (bash, run as root), the folder the
    project lives in, its readiness check, and the install files to remove.

    1. Imports a fresh Ubuntu 24.04 (Ubuntu's WSL root filesystem, downloaded
       once) as bench-<id>-build. It is sealed from Windows: no drive
       mounts, no Windows programs or PATH, systemd on, root by default, and
       /etc/hosts left alone.
    2. Runs the recipe and waits for the readiness check.
    3. Copies the install files to <Root>\snapshots\<id>.hidden (the probe's
       content guard fingerprints them), then deletes them in the distro,
       with shell history, apt and dpkg logs, and package caches.
    4. Exports <Root>\snapshots\<id>.tar and unregisters the build distro
       (-Keep leaves it). <id>.meta.json records the TCP ports the install
       listens on beyond a bare Ubuntu: WSL distros share one network, so
       the eval scheduler never runs two snapshots with a port in common.

.EXAMPLE
    .\Build-WslSnapshot.ps1 -Project harbor
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Project,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'installzero\bench'),
    [string]$RootfsUrl = 'https://cloud-images.ubuntu.com/wsl/releases/24.04/current/ubuntu-noble-wsl-amd64-24.04lts.rootfs.tar.gz',
    # Where <id>.json and its scripts are; the eval console's worker writes them from MongoDB.
    [string]$ProjectsDir = (Join-Path $PSScriptRoot 'projects'),
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\..\LinuxTarget.ps1')
$def = Get-Content (Join-Path $ProjectsDir "$Project.json") -Raw | ConvertFrom-Json
$recipe = (@($def.include | Where-Object { $_ }) + $def.recipe | ForEach-Object { Get-Content (Join-Path $ProjectsDir $_) -Raw }) -join "`n"
$distro = "bench-$($def.id)-build"
foreach ($d in 'images', 'distros', 'snapshots') { New-Item -ItemType Directory -Force (Join-Path $Root $d) | Out-Null }
$rootfs = Join-Path $Root 'images\ubuntu-24.04.rootfs.tar.gz'
$snapshot = Join-Path $Root "snapshots\$($def.id).tar"
$hiddenDir = Join-Path $Root "snapshots\$($def.id).hidden"
$log = Join-Path $Root "snapshots\$($def.id).build.log"
function Write-Step([string]$m) { Write-Host "[$(Get-Date -Format HH:mm:ss)] $m" -ForegroundColor Cyan }
function Get-WslNames { @(((wsl.exe -l -q) -replace "`0", '') -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ }) }
function Get-ListenPorts([string]$D) {
    $p = Invoke-WslBash $D "ss -ltnpH | grep -F 'users:((' | awk '{print `$4}' | sed -E 's/.*:([0-9]+)$/\1/' | sort -un"
    @($p.Out -split "`n" | Where-Object { $_ -match '^\d+$' } | ForEach-Object { [int]$_ })
}

if (-not (Test-Path $rootfs)) {
    Write-Step "Downloading Ubuntu 24.04 WSL root filesystem"
    $old = $ProgressPreference; $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest $RootfsUrl -OutFile $rootfs -UseBasicParsing
    $ProgressPreference = $old
}
if ((Get-WslNames) -contains $distro) { Write-Step "Removing the previous $distro"; wsl.exe --unregister $distro | Out-Null }

Write-Step "Importing $distro"
$dir = Join-Path $Root "distros\$distro"
New-Item -ItemType Directory -Force $dir | Out-Null
wsl.exe --import $distro $dir $rootfs --version 2 | Out-Null
if ($LASTEXITCODE -ne 0) { throw "wsl --import failed ($LASTEXITCODE)" }
$seal = Invoke-WslBash $distro @'
cat > /etc/wsl.conf <<'EOF'
[boot]
systemd=true
[automount]
enabled=false
mountFsTab=false
[interop]
enabled=false
appendWindowsPath=false
[network]
generateHosts=false
[user]
default=root
EOF
'@
if ($seal.Code) { throw "sealing the distro failed: $($seal.Out)" }
wsl.exe --terminate $distro | Out-Null
$up = Invoke-WslBash $distro 'for i in $(seq 1 30); do s=$(systemctl is-system-running 2>/dev/null); case "$s" in running|degraded) echo $s; exit 0;; esac; sleep 2; done; echo "systemd: $s"; exit 1' 90
Write-Step "systemd: $($up.Out)"
$usesDocker = $recipe -match '\bdocker\b'
if ($usesDocker) { Clear-WslDockerNet $distro | Out-Null }
$basePorts = Get-ListenPorts $distro

# Keep the distro running while the recipe and the export prepare.
$keepAlive = Start-Process wsl.exe -ArgumentList "-d $distro -u root -- sleep infinity" -WindowStyle Hidden -PassThru
try {
    Write-Step "Running the $Project recipe (log: $log)"
    # Also written as it runs, to tail from Windows:
    # Get-Content \\wsl.localhost\<distro>\root\bench-recipe.log -Wait
    $r = Invoke-WslBash $distro ("exec > >(tee /root/bench-recipe.log) 2>&1`n" + $recipe) 3600
    Set-Content $log $r.Out -Encoding utf8
    if ($r.Code) { throw "the recipe failed (exit $($r.Code)); see $log`n$(($r.Out -split "`n" | Select-Object -Last 15) -join "`n")" }

    Write-Step "Waiting for readiness: $($def.ready)"
    $ready = Invoke-WslBash $distro "for i in `$(seq 1 $([math]::Ceiling($def.ready_timeout_sec / 5))); do ($($def.ready)) && exit 0; sleep 5; done; exit 1" ($def.ready_timeout_sec + 30)
    if ($ready.Code) { throw "not ready after $($def.ready_timeout_sec)s: $($ready.Out)" }
    $ports = @(Get-ListenPorts $distro | Where-Object { $_ -notin $basePorts })
    Write-Step "Listening ports: $($ports -join ' ')"

    Write-Step "Saving and removing install files"
    if (Test-Path $hiddenDir) { Remove-Item $hiddenDir -Recurse -Force }
    New-Item -ItemType Directory -Force $hiddenDir | Out-Null
    foreach ($f in $def.remove) {
        if ($f -match '\.(tgz|tar|gz|zip)$') { continue }
        $c = Invoke-WslBash $distro "[ -f '$f' ] && cat '$f'"
        if (-not $c.Code) { Set-Content (Join-Path $hiddenDir (($f.TrimStart('/') -replace '/', '__') + '.txt')) $c.Out -Encoding utf8 }
    }
    # Not with the hidden files: the recipe writes config files the probe should read.
    Set-Content (Join-Path $Root "snapshots\$($def.id).recipe.sh") $recipe -Encoding utf8
    $rm = ($def.remove | ForEach-Object { "'$_'" }) -join ' '
    $clean = Invoke-WslBash $distro @"
rm -rf $rm
rm -f /root/bench-recipe.log /root/.bash_history /home/*/.bash_history /root/.wget-hsts /root/.lesshst
rm -rf /var/log/apt/* /var/log/dpkg.log /var/log/installer /tmp/* /var/tmp/*
apt-get clean
journalctl --rotate >/dev/null 2>&1; journalctl --vacuum-time=1s >/dev/null 2>&1
true
"@
    if ($clean.Code) { throw "cleanup failed: $($clean.Out)" }
    if ($usesDocker) { Clear-WslDockerNet $distro | Out-Null }
} finally {
    if ($keepAlive -and -not $keepAlive.HasExited) { Stop-Process -Id $keepAlive.Id -Force -ErrorAction SilentlyContinue }
}

Write-Step "Exporting $snapshot"
wsl.exe --terminate $distro | Out-Null
wsl.exe --export $distro $snapshot | Out-Null
if ($LASTEXITCODE -ne 0) { throw "wsl --export failed ($LASTEXITCODE)" }
if (-not $Keep) { wsl.exe --unregister $distro | Out-Null; Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue }
$meta = [ordered]@{ id = $def.id; ports = $ports; base_ports = $basePorts; built_at = (Get-Date).ToUniversalTime().ToString('o'); size_gb = [math]::Round((Get-Item $snapshot).Length / 1GB, 2) }
[IO.File]::WriteAllText((Join-Path $Root "snapshots\$($def.id).meta.json"), ($meta | ConvertTo-Json -Compress), [Text.UTF8Encoding]::new($false))
Write-Step ("Done: {0} ({1:N1} GB); install files kept for the content guard in {2}" -f $snapshot, ((Get-Item $snapshot).Length / 1GB), $hiddenDir)
