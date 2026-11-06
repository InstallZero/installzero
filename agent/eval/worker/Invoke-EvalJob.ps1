<#
.SYNOPSIS
    Runs one eval-console job in its own process; Start-EvalWorker.ps1
    starts it, tails its output and reports the outcome.

.DESCRIPTION
    The job file is the job as claimed from the console, with everything
    needed in .spec (recipe, facts, snippets), so this script never talks to
    the server. It writes <job dir>\outcome.json:
      probe  restore the snapshot into bench-<id>-<job suffix>, wait for the
             readiness check, probe and score with Invoke-ProbeEval.ps1,
             delete the distro. Outcome: the scored row with the install.md
             text, and the ports the install listens on.
      build  write the recipe files from the spec and run
             Build-WslSnapshot.ps1. Outcome: size and listening ports.
      draft  Invoke-RecipeDraft.ps1. Outcome: the drafted recipe and facts.
      diagnose, lessons  Invoke-LossAnalysis.ps1. Outcome: causes of one
             run's misses, or a drafted lesson set.
      measure  restore the snapshot, wait until ready, record the listening
             ports in <id>.meta.json (snapshots built before ports were
             recorded), delete the distro.
      sft-build  sft\Export-SftDataset.ps1 into
             %LOCALAPPDATA%\installzero\sft\sets\<set id>. Outcome: its manifest.
      train  sft\Invoke-SftTrain.ps1: LoRA fine-tune, GGUF into the doctor
             assets. Outcome: the model file, size and losses.
    Lines starting with "PHASE: " are progress for the console.
#>
param(
    [Parameter(Mandatory)][string]$JobFile,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'installzero\bench')
)

$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false) } catch { }
$job = Get-Content $JobFile -Raw -Encoding UTF8 | ConvertFrom-Json
$spec = $job.spec
$evalDir = Split-Path $PSScriptRoot
$agentDir = Split-Path $evalDir
. (Join-Path $agentDir 'LinuxTarget.ps1')
$work = Split-Path $JobFile
$outcomeFile = Join-Path $work 'outcome.json'
$bid = $spec.bench_id
$utf8 = [Text.UTF8Encoding]::new($false)

function Write-Phase([string]$Phase, [string]$Msg = '') { Write-Host "PHASE: $Phase$(if ($Msg) { " | $Msg" })" }
function Save-Outcome($O) { [IO.File]::WriteAllText($outcomeFile, ($O | ConvertTo-Json -Depth 15 -Compress), $utf8) }
function Get-WslNames { @(((wsl.exe -l -q) -replace "`0", '') -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ }) }
function Get-ListenPorts([string]$D) {
    $p = Invoke-WslBash $D "ss -ltnpH | grep -F 'users:((' | awk '{print `$4}' | sed -E 's/.*:([0-9]+)$/\1/' | sort -un"
    @($p.Out -split "`n" | Where-Object { $_ -match '^\d+$' } | ForEach-Object { [int]$_ })
}

# The recipe as files, in the layout Build-WslSnapshot.ps1 reads.
function Write-ProjectFiles {
    $dir = Join-Path $work 'projects'
    New-Item -ItemType Directory -Force $dir | Out-Null
    $r = $spec.recipe
    foreach ($s in @($spec.snippets)) { if ($s) { [IO.File]::WriteAllText((Join-Path $dir $s._id), ($s.script -replace "`r`n", "`n"), $utf8) } }
    [IO.File]::WriteAllText((Join-Path $dir "$bid.sh"), ($r.script -replace "`r`n", "`n"), $utf8)
    $def = [ordered]@{ id = $bid; recipe = "$bid.sh"; include = @($r.include | Where-Object { $_ }); target = $r.target; ready = $r.ready
        ready_timeout_sec = [int]$r.ready_timeout_sec; remove = @($r.remove | Where-Object { $_ }) }
    [IO.File]::WriteAllText((Join-Path $dir "$bid.json"), ($def | ConvertTo-Json -Depth 5), $utf8)
    $dir
}

try {
    switch -Regex ($job.type) {
        '^(probe|measure)$' {
            $snapshot = Join-Path $Root "snapshots\$bid.tar"
            if (-not (Test-Path $snapshot)) { throw "no snapshot at $snapshot; build it first" }
            $hiddenDir = Join-Path $Root "snapshots\$bid.hidden"
            $metaFile = Join-Path $Root "snapshots\$bid.meta.json"
            $meta = if (Test-Path $metaFile) { Get-Content $metaFile -Raw | ConvertFrom-Json } else { $null }
            $basePorts = if ($meta -and $meta.base_ports) { @($meta.base_ports) } else { @(53) }
            $suffix = ($job._id -replace '[^a-z0-9]', '').Substring([math]::Max(0, ($job._id -replace '[^a-z0-9]', '').Length - 8))
            $distro = "bench-$bid-$suffix-$([int]$job.attempts)"
            $dir = Join-Path $Root "distros\$distro"
            Set-Content (Join-Path $work 'distro.txt') $distro
            Write-Phase 'restoring' $distro
            if ((Get-WslNames) -contains $distro) { wsl.exe --unregister $distro | Out-Null }
            New-Item -ItemType Directory -Force $dir | Out-Null
            wsl.exe --import $distro $dir $snapshot --version 2 | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "wsl --import failed ($LASTEXITCODE)" }
            $keepAlive = Start-Process wsl.exe -ArgumentList "-d $distro -u root -- sleep infinity" -WindowStyle Hidden -PassThru
            $docker = -not (Invoke-WslBash $distro 'command -v dockerd >/dev/null').Code
            try {
                # Docker started on boot next to bridges left by an earlier distro;
                # clear them, then boot again so every service starts in order.
                if ($docker) {
                    Write-Phase 'starting' 'clearing stale Docker networks and rebooting'
                    Clear-WslDockerNet $distro | Out-Null
                    if (-not $keepAlive.HasExited) { Stop-Process -Id $keepAlive.Id -Force -ErrorAction SilentlyContinue }
                    wsl.exe --terminate $distro | Out-Null
                    $keepAlive = Start-Process wsl.exe -ArgumentList "-d $distro -u root -- sleep infinity" -WindowStyle Hidden -PassThru
                }
                Reset-WslFirewall $distro | Out-Null
                Write-Phase 'starting' 'waiting for the readiness check'
                $t0 = Get-Date
                $r = $spec.recipe
                $ready = Invoke-WslBash $distro "$($WslCurlTimeout)for i in `$(seq 1 $([math]::Ceiling($r.ready_timeout_sec / 5))); do ($($r.ready)) && exit 0; sleep 5; done; exit 1" ($r.ready_timeout_sec + 30)
                if ($ready.Code) { throw "not ready after $($r.ready_timeout_sec)s: $($ready.Out)" }
                $ports = @(Get-ListenPorts $distro | Where-Object { $_ -notin $basePorts })
                if (-not $meta -or -not @($meta.ports).Count) {
                    $m = [ordered]@{ id = $bid; ports = @($ports); base_ports = @($basePorts); measured_at = (Get-Date).ToUniversalTime().ToString('o'); size_gb = [math]::Round((Get-Item $snapshot).Length / 1GB, 2) }
                    [IO.File]::WriteAllText($metaFile, ($m | ConvertTo-Json -Compress), $utf8)
                }
                if ($job.type -eq 'measure') {
                    Write-Phase 'measured' "ready after $([int]((Get-Date) - $t0).TotalSeconds)s; ports $($ports -join ' ')"
                    Save-Outcome @{ status = 'done'; ports = $ports }
                    return
                }
                Write-Phase 'probing' "ready after $([int]((Get-Date) - $t0).TotalSeconds)s; ports $($ports -join ' ')"
                $reposFile = Join-Path $work 'repos.json'
                $def = [ordered]@{ id = $bid; source = $spec.url; serves = $spec.serves; why = $spec.why; facts = @($spec.facts); hide = @() }
                [IO.File]::WriteAllText($reposFile, (@{ repos = @($def) } | ConvertTo-Json -Depth 8), $utf8)
                $rowFile = Join-Path $work 'row.json'
                $evalArgs = @{ Repo = $bid; Probe = $true; Wsl = $distro; Target = $r.target; ReposFile = $reposFile; ResultOut = $rowFile }
                if (Test-Path $hiddenDir) { $evalArgs.HidePaths = @($hiddenDir) }
                if ($job.max_turns) { $evalArgs.MaxTurns = [int]$job.max_turns }
                if ($job.engine) { $evalArgs.Engine = [string]$job.engine }
                if ($spec.guidance -and "$($spec.guidance.text)".Trim()) {
                    $evalArgs.GuidanceFile = Join-Path $work 'guidance.json'
                    [IO.File]::WriteAllText($evalArgs.GuidanceFile, (@{ id = $spec.guidance.id; text = $spec.guidance.text } | ConvertTo-Json), $utf8)
                }
                $model = [string]$job.model
                if ($model -match '^(anthropic|openai|xai|google|hf):') { $evalArgs.Remote = $model } else { $evalArgs.Model = ($model -replace '^local:', '') }
                & (Join-Path $evalDir 'Invoke-ProbeEval.ps1') @evalArgs
                if (-not (Test-Path $rowFile)) { throw 'the probe finished without a scored row' }
                $row = Get-Content $rowFile -Raw -Encoding UTF8 | ConvertFrom-Json
                if ($row.install_md -and (Test-Path $row.install_md)) { $row | Add-Member install_md_text ([IO.File]::ReadAllText($row.install_md, $utf8)) -Force }
                Write-Phase 'scored' $(if ($row.delivered) { "recall $([math]::Round($row.recall * 100))% ($($row.facts_hit)/$($row.facts))" } else { "not delivered ($($row.stop_reason))" })
                $transcript = if ($row.transcript -and (Test-Path $row.transcript)) { [IO.File]::ReadAllText($row.transcript) } else { '' }
                Save-Outcome @{ status = 'done'; result = $row; ports = $ports; transcript = $transcript }
            } finally {
                Reset-WslFirewall $distro | Out-Null
                if ($keepAlive -and -not $keepAlive.HasExited) { Stop-Process -Id $keepAlive.Id -Force -ErrorAction SilentlyContinue }
                Write-Phase 'cleanup' "removing $distro"
                if ($docker) { Clear-WslDockerNet $distro | Out-Null }
                wsl.exe --unregister $distro | Out-Null
                Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
        '^build$' {
            $projects = Write-ProjectFiles
            # Named so the worker deletes it if the job is cancelled or killed.
            Set-Content (Join-Path $work 'distro.txt') "bench-$bid-build"
            Write-Phase 'building' "recipe for $bid"
            try { & (Join-Path $evalDir 'wsl\Build-WslSnapshot.ps1') -Project $bid -ProjectsDir $projects -Root $Root }
            catch {
                $log = Join-Path $Root "snapshots\$bid.build.log"
                $tail = if (Test-Path $log) { (Get-Content $log -Tail 80 -Encoding UTF8) -join "`n" } else { '' }
                Save-Outcome @{ status = 'failed'; error = $_.Exception.Message; snapshot = @{ log_tail = $tail } }
                Write-Host "FAILED: $($_.Exception.Message)"
                exit 1
            }
            $meta = Get-Content (Join-Path $Root "snapshots\$bid.meta.json") -Raw | ConvertFrom-Json
            Write-Phase 'built' "$($meta.size_gb) GB, ports $(@($meta.ports) -join ' ')"
            Save-Outcome @{ status = 'done'; snapshot = @{ size_gb = $meta.size_gb; ports = @($meta.ports) } }
        }
        '^draft$' {
            Write-Phase 'drafting' "with $($job.model)"
            $out = Join-Path $work 'draft.json'
            & (Join-Path $PSScriptRoot 'Invoke-RecipeDraft.ps1') -JobFile $JobFile -Out $out
            $draft = Get-Content $out -Raw -Encoding UTF8 | ConvertFrom-Json
            Write-Phase 'drafted' "$(@($draft.facts).Count) facts"
            Save-Outcome @{ status = 'done'; draft = $draft }
        }
        '^(diagnose|lessons)$' {
            Write-Phase $(if ($job.type -eq 'diagnose') { 'diagnosing' } else { 'drafting lessons' }) "with $($job.model)"
            $out = Join-Path $work "$($job.type).json"
            & (Join-Path $PSScriptRoot 'Invoke-LossAnalysis.ps1') -JobFile $JobFile -Out $out
            $o = [IO.File]::ReadAllText($out) | ConvertFrom-Json
            if ($job.type -eq 'diagnose') { Write-Phase 'diagnosed' "$(@($o.misses).Count) misses"; Save-Outcome @{ status = 'done'; diagnosis = $o } }
            else { Write-Phase 'drafted' "$(@($o.items).Count) lessons"; Save-Outcome @{ status = 'done'; lessons = $o } }
        }
        '^sft-build$' {
            Write-Phase 'building' "SFT set $($spec.set_id)"
            $out = Join-Path $work 'manifest.json'
            & (Join-Path $evalDir 'sft\Export-SftDataset.ps1') -JobFile $JobFile -OutDir (Join-Path $env:LOCALAPPDATA "installzero\sft\sets\$($spec.set_id)") -Out $out
            Save-Outcome @{ status = 'done'; sft = ([IO.File]::ReadAllText($out) | ConvertFrom-Json) }
        }
        '^train$' {
            Write-Phase 'setup' "training run $($spec.run_id) on $($spec.base_model)"
            $out = Join-Path $work 'trained.json'
            & (Join-Path $evalDir 'sft\Invoke-SftTrain.ps1') -JobFile $JobFile -Out $out
            Save-Outcome @{ status = 'done'; train = ([IO.File]::ReadAllText($out) | ConvertFrom-Json) }
        }
        default { throw "unknown job type '$($job.type)'" }
    }
} catch {
    if (-not (Test-Path $outcomeFile)) { Save-Outcome @{ status = 'failed'; error = $_.Exception.Message } }
    Write-Host "FAILED: $($_.Exception.Message)"
    Write-Host $_.ScriptStackTrace
    exit 1
}
