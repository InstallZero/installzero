<#
.SYNOPSIS
    Run the probe against install-bench projects in fresh WSL distros: restore
    the snapshot, wait until it serves, probe, score, delete the distro.

.DESCRIPTION
    Per project (an id in repos.json with a 'wsl' entry and a snapshot built
    by Build-WslSnapshot.ps1), per model, per run:
      1. wsl --import the snapshot as bench-<id>-<stamp> (a throwaway copy).
      2. Keep it running and wait for the readiness check in
         projects\<id>.json (services come back up through systemd).
      3. Invoke-ProbeEval.ps1 -Probe -Wsl <distro>: probe, score against the
         facts, append to results.jsonl. The removed install files are
         hidden, so their lines are withheld wherever they show up.
      4. wsl --unregister the copy (-KeepDistro leaves it for inspection).
    Then loads results into MongoDB (installzero.bench_results), unless -NoSync.

    Models: 'local:<file.gguf>' for a local model, anything else is passed
    as -Remote (e.g. anthropic:claude-opus-5-5).

.EXAMPLE
    .\Invoke-WslBench.ps1 -Project harbor -Models anthropic:claude-opus-5-5
.EXAMPLE
    .\Invoke-WslBench.ps1 -Project harbor -Models local:gpt-oss-20b-MXFP4.gguf,openai:gpt-5.5 -Runs 2
#>
[CmdletBinding()]
param(
    [string[]]$Project = @('harbor'),
    [Parameter(Mandatory)][string[]]$Models,
    [int]$Runs = 1,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'installzero\bench'),
    [switch]$KeepDistro,
    [switch]$NoSync
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\..\LinuxTarget.ps1')
$evalDir = Split-Path $PSScriptRoot
$Models = @($Models | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
function Write-Step([string]$m) { Write-Host "[$(Get-Date -Format HH:mm:ss)] $m" -ForegroundColor Cyan }

foreach ($p in $Project) {
    $def = Get-Content (Join-Path $PSScriptRoot "projects\$p.json") -Raw | ConvertFrom-Json
    $snapshot = Join-Path $Root "snapshots\$($def.id).tar"
    if (-not (Test-Path $snapshot)) { throw "No snapshot for $p; build it: .\Build-WslSnapshot.ps1 -Project $p" }
    $hiddenDir = Join-Path $Root "snapshots\$($def.id).hidden"
    foreach ($model in $Models) {
        for ($i = 1; $i -le $Runs; $i++) {
            $distro = "bench-$($def.id)-$(Get-Date -Format 'MMddHHmmss')"
            $dir = Join-Path $Root "distros\$distro"
            Write-Step "$p, $model, run $i of $($Runs): restoring $distro"
            New-Item -ItemType Directory -Force $dir | Out-Null
            wsl.exe --import $distro $dir $snapshot --version 2 | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "wsl --import failed ($LASTEXITCODE)" }
            $keepAlive = Start-Process wsl.exe -ArgumentList "-d $distro -u root -- sleep infinity" -WindowStyle Hidden -PassThru
            try {
                $t0 = Get-Date
                $ready = Invoke-WslBash $distro "for i in `$(seq 1 $([math]::Ceiling($def.ready_timeout_sec / 5))); do ($($def.ready)) && exit 0; sleep 5; done; exit 1" ($def.ready_timeout_sec + 30)
                if ($ready.Code) { Write-Warning "$p did not become ready in $($def.ready_timeout_sec)s; skipping this run. $($ready.Out)"; continue }
                Write-Step "ready after $([int]((Get-Date) - $t0).TotalSeconds)s; probing"
                $evalArgs = @{ Repo = $def.id; Probe = $true; Wsl = $distro; Target = $def.target; HidePaths = @($hiddenDir) }
                if ($model -match '^local:(.+)$') { $evalArgs.Model = $Matches[1] } else { $evalArgs.Remote = $model }
                & (Join-Path $evalDir 'Invoke-ProbeEval.ps1') @evalArgs
            } finally {
                if (-not $keepAlive.HasExited) { Stop-Process -Id $keepAlive.Id -Force -ErrorAction SilentlyContinue }
                if ($KeepDistro) { Write-Step "kept $distro (remove: wsl --unregister $distro)" }
                else { wsl.exe --unregister $distro | Out-Null; Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue }
            }
        }
    }
}

if (-not $NoSync) {
    Write-Step 'Loading results into MongoDB'
    & (Join-Path $evalDir 'bench\Sync-BenchDb.ps1') -SkipRepos
}
