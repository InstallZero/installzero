<#
.SYNOPSIS
    Evaluates the InstallZero probe: reconstruct install.md files from live
    installs of known projects, score them, record the losses, and summarise
    what to improve in the next version.

.DESCRIPTION
    The loop, per project in repos.json:
      1. Install it on a test account and get it running: by hand, or
         -Install, which hands the project's own install instructions to
         Agent.ps1 (plain-instructions mode).
      2. -Probe: run Probe.ps1 on the live install (-NoDocs hides READMEs so
         the procedure must come from what runs), then score.
      3. Optionally replay the reconstructed install.md with Agent.ps1 on a
         clean account and score the replay (-ReplayReport).
      4. -Summary: misses by fact and category across runs, per model and
         probe prompt version: the list of what to fix next.

    Losses go to %LOCALAPPDATA%\installzero\eval\results.jsonl, one line per
    scored run.

.EXAMPLE
    .\Invoke-ProbeEval.ps1 -Repo comfyui -Install -Model gpt-oss-20b-MXFP4.gguf
.EXAMPLE
    .\Invoke-ProbeEval.ps1 -Repo comfyui -Probe -Target $env:USERPROFILE\iz-eval\comfyui\ComfyUI -NoDocs
.EXAMPLE
    .\Invoke-ProbeEval.ps1 -Repo rl-roboracer -Score .\rl-roboracer.install.md
.EXAMPLE
    .\Invoke-ProbeEval.ps1 -Summary
#>
[CmdletBinding(DefaultParameterSetName = 'list')]
param(
    [Parameter(ParameterSetName = 'install', Mandatory)][Parameter(ParameterSetName = 'probe', Mandatory)][Parameter(ParameterSetName = 'score', Mandatory)][string]$Repo,
    [Parameter(ParameterSetName = 'install', Mandatory)][switch]$Install,
    [Parameter(ParameterSetName = 'probe', Mandatory)][switch]$Probe,
    [Parameter(ParameterSetName = 'probe')][string]$Target = '',
    [Parameter(ParameterSetName = 'probe')][switch]$NoDocs,
    # WSL distro the project runs in (a restored install-bench snapshot).
    [Parameter(ParameterSetName = 'probe')][string]$Wsl = '',
    [Parameter(ParameterSetName = 'probe')][string[]]$HidePaths = @(),
    [Parameter(ParameterSetName = 'probe')][int]$Context = 0,
    [Parameter(ParameterSetName = 'probe')][int]$MaxTurns = 0,
    # Lesson set from the eval console, passed to Probe.ps1.
    [Parameter(ParameterSetName = 'probe')][string]$GuidanceFile = '',
    # ps runs Probe.ps1; go runs the Go probe (installzero probe, WSL only),
    # built from this checkout unless IZ_GO_EXE names a binary.
    [Parameter(ParameterSetName = 'probe')][ValidateSet('ps', 'go')][string]$Engine = 'ps',
    [Parameter(ParameterSetName = 'score', Mandatory)][string]$Score,
    [Parameter(ParameterSetName = 'score')][string]$ProbeResult = '',
    [Parameter(ParameterSetName = 'probe')][Parameter(ParameterSetName = 'score')][string]$ReplayReport = '',
    [Parameter(ParameterSetName = 'summary', Mandatory)][switch]$Summary,
    [string]$Model = '',
    [string]$Remote = '',
    [string]$EvalDir = (Join-Path $env:USERPROFILE 'iz-eval'),
    # Project definitions (facts, hidden paths); the eval console's worker
    # passes its own. Default: repos.json beside this script.
    [string]$ReposFile = '',
    # Also write this run's scored row here (JSON).
    [string]$ResultOut = ''
)

$ErrorActionPreference = 'Stop'
# Windows PowerShell leaves $PSScriptRoot empty in parameter defaults under -File.
if (-not $ReposFile) { $ReposFile = Join-Path $PSScriptRoot 'repos.json' }
$set = (Get-Content $ReposFile -Raw | ConvertFrom-Json).repos
$resultsDir = Join-Path $env:LOCALAPPDATA 'installzero\eval'
$resultsFile = Join-Path $resultsDir 'results.jsonl'
New-Item -ItemType Directory -Force $resultsDir | Out-Null
$agentDir = Split-Path $PSScriptRoot
$modelArgs = @(); if ($Remote) { $modelArgs += @('-Remote', $Remote) } elseif ($Model) { $modelArgs += @('-Model', $Model) }

# The Go probe for this checkout, built once per commit (and again when the
# Go sources have uncommitted changes).
function Get-GoProbe {
    if ($env:IZ_GO_EXE) {
        if (-not (Test-Path $env:IZ_GO_EXE)) { throw "IZ_GO_EXE is $env:IZ_GO_EXE, which does not exist" }
        return $env:IZ_GO_EXE
    }
    $repoRoot = Split-Path $agentDir
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'the go engine needs Go (go.dev/dl) to build installzero, or IZ_GO_EXE set to a built binary' }
    $rev = (& git -C $repoRoot rev-parse --short=12 HEAD 2>$null)
    $dirty = (& git -C $repoRoot status --porcelain -- cmd internal prompts catalog go.mod go.sum 2>$null)
    $binDir = Join-Path $env:LOCALAPPDATA 'installzero\bin'
    New-Item -ItemType Directory -Force $binDir | Out-Null
    $exe = Join-Path $binDir "installzero-$(if ($rev) { $rev } else { 'dev' })$(if ($dirty) { '-dirty' }).exe"
    if ($dirty -or -not $rev -or -not (Test-Path $exe)) {
        Push-Location $repoRoot
        try { & go build -o $exe ./cmd/installzero; if ($LASTEXITCODE) { throw "go build failed ($LASTEXITCODE)" } } finally { Pop-Location }
    }
    $exe
}

function Get-Repo([string]$Id) {
    $r = $set | Where-Object id -eq $Id
    if (-not $r) { throw "Unknown repo '$Id'. Known: $($set.id -join ', ')" }
    $r
}

function Measure-InstallMd($R, [string]$Path, $ProbeRes, $Replay) {
    $base = [ordered]@{
        time = (Get-Date).ToString('s'); repo = $R.id
        model = $(if ($ProbeRes) { $ProbeRes.model } else { '' }); prompt_version = $(if ($ProbeRes) { $ProbeRes.prompt_version } else { '' }); no_docs = $(if ($ProbeRes) { $ProbeRes.no_docs } else { $null })
        stop_reason = $(if ($ProbeRes) { $ProbeRes.stop_reason } else { '' })
        target_env = $(if ($ProbeRes -and $ProbeRes.target_env) { $ProbeRes.target_env } else { 'local' })
        probe = $(if ($ProbeRes) { [ordered]@{ finished = $ProbeRes.finished; confidence = $ProbeRes.confidence; gaps = $ProbeRes.gaps; stats = $ProbeRes.stats; blind = $ProbeRes.blind } } else { $null })
    }
    # A run that saved nothing failed to deliver; that is not a recall of 0.
    if (-not $Path -or -not (Test-Path -LiteralPath $Path)) {
        return $base + [ordered]@{ delivered = $false; recall = $null; misses = @(); format_problems = @(); steps = 0; live_checks = $null; replay = $null }
    }
    $text = Get-Content -LiteralPath $Path -Raw
    $hits = @(); $misses = @()
    foreach ($f in $R.facts) {
        if ($text -match "(?im)$($f.re)") { $hits += $f } else { $misses += $f }
    }
    $total = ($R.facts | Measure-Object weight -Sum).Sum
    $got = ($hits | Measure-Object weight -Sum).Sum
    . (Join-Path $agentDir 'InstallMd.ps1')
    $doc = Read-InstallMd $Path
    $problems = @(Test-InstallMd $doc)
    $replayScore = if ($Replay) {
        $st = @($Replay.steps.PSObject.Properties.Value)
        [ordered]@{ status = $Replay.status; steps_ok = @($st | Where-Object { $_ -in 'done', 'already-done' }).Count; steps = $st.Count; system_asks = @($Replay.commands | Where-Object tier -eq 'system').Count }
    } else { $null }
    $base + [ordered]@{
        delivered = $true; install_md = (Resolve-Path $Path).Path
        recall = [math]::Round($got / $total, 3); facts_hit = $hits.Count; facts = $R.facts.Count
        misses = @($misses | ForEach-Object { [ordered]@{ id = $_.id; category = $_.category; weight = $_.weight } })
        format_problems = $problems; steps = $doc.Steps.Count
        live_checks = $(if ($ProbeRes) { $ProbeRes.draft_checks } else { $null })
        replay = $replayScore
    }
}

# Rows written before 'delivered' existed: an empty or stepless file was not delivered.
function Test-Delivered($Row) { if ($null -ne $Row.delivered) { [bool]$Row.delivered } else { $Row.steps -gt 0 } }

function Write-Score($S) {
    if (-not $S.delivered) {
        Write-Host "$($S.repo): no install.md delivered (stopped: $($S.stop_reason)$(if ($S.probe.stats.error) { " - $($S.probe.stats.error)" })). Not scored for recall." -ForegroundColor Yellow
        if ($S.probe) { Write-Host "  Probe: $($S.probe.stats.model_turns) turns, $($S.probe.stats.tool_calls) tool calls, $($S.probe.stats.seconds)s" }
        return
    }
    Write-Host ("{0}: recall {1:P0} ({2}/{3} facts), {4} steps, {5} format problems" -f $S.repo, $S.recall, $S.facts_hit, $S.facts, $S.steps, @($S.format_problems).Count) -ForegroundColor Cyan
    if ($S.misses) { Write-Host "  Missed: $(($S.misses | ForEach-Object { "$($_.id) ($($_.category), w$($_.weight))" }) -join ', ')" -ForegroundColor Yellow }
    if ($S.live_checks) { Write-Host "  Checks on the live install: $($S.live_checks.passed) passed, $($S.live_checks.failed) failed, $($S.live_checks.untested) untested" }
    if ($S.replay) { Write-Host "  Replay: $($S.replay.status), $($S.replay.steps_ok)/$($S.replay.steps) steps ok, $($S.replay.system_asks) machine-wide asks" }
    if ($S.probe) { Write-Host "  Probe: $($S.probe.stats.model_turns) turns, $($S.probe.stats.tool_calls) tool calls, $($S.probe.stats.invalid_calls) invalid, $($S.probe.stats.seconds)s, stopped: $($S.stop_reason)" }
}

switch ($PSCmdlet.ParameterSetName) {
    'list' {
        foreach ($r in $set) { Write-Host ("{0,-14} {1,-55} serves {2}" -f $r.id, $r.source, $r.serves); Write-Host "               $($r.why)" -ForegroundColor DarkGray }
        Write-Host "`nResults: $resultsFile"
    }
    'install' {
        $r = Get-Repo $Repo
        if ($r.instructions -notmatch '^https?://') { throw "$Repo has no downloadable instructions ($($r.instructions)); install it by hand, then use -Probe." }
        $dir = Join-Path $EvalDir $r.id
        New-Item -ItemType Directory -Force $dir | Out-Null
        $md = Join-Path $dir 'upstream-instructions.md'
        Invoke-WebRequest $r.instructions -UseBasicParsing -OutFile $md
        Write-Host "Installing $($r.id) with Agent.ps1 from its own instructions into $dir" -ForegroundColor Cyan
        & (Join-Path $agentDir 'Agent.ps1') -InstallMd $md -InstallDir $dir @modelArgs -ResultFile (Join-Path $dir 'install-report.json')
        Write-Host "When it serves on $($r.serves), probe it: .\Invoke-ProbeEval.ps1 -Repo $($r.id) -Probe -Target <its folder> -NoDocs"
    }
    'probe' {
        $r = Get-Repo $Repo
        $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
        $out = Join-Path $resultsDir "$($r.id)-$stamp.install.md"
        $res = Join-Path $resultsDir "$($r.id)-$stamp.probe.json"
        $probeArgs = @{ Out = $out; ResultFile = $res }
        if ($Target) { $probeArgs.Target = $Target }
        if ($NoDocs) { $probeArgs.NoDocs = $true }
        if ($r.hide -or $HidePaths) { $probeArgs.HidePaths = @(@($r.hide) + $HidePaths | Where-Object { $_ }) }
        if ($Wsl) { $probeArgs.Wsl = $Wsl }
        if ($Context) { $probeArgs.Context = $Context }
        if ($MaxTurns) { $probeArgs.MaxTurns = $MaxTurns }
        if ($GuidanceFile) { $probeArgs.GuidanceFile = $GuidanceFile }
        if ($Remote) { $probeArgs.Remote = $Remote } elseif ($Model) { $probeArgs.Model = $Model }
        if ($Engine -eq 'go') {
            if (-not $Wsl) { throw 'the go engine probes WSL installs only (-Wsl)' }
            $exe = Get-GoProbe
            $goArgs = @('probe', '--wsl', $Wsl, '--out', $out, '--result', $res)
            if ($Target) { $goArgs += @('--target', $Target) }
            if ($NoDocs) { $goArgs += '--no-docs' }
            foreach ($h in @($probeArgs.HidePaths)) { if ($h) { $goArgs += @('--hide', $h) } }
            if ($Context) { $goArgs += @('--context', $Context) }
            if ($MaxTurns) { $goArgs += @('--max-turns', $MaxTurns) }
            if ($GuidanceFile) { $goArgs += @('--guidance', $GuidanceFile) }
            if ($Remote) { $goArgs += @('--remote', $Remote) } elseif ($Model) { $goArgs += @('--model', $Model) }
            Write-Host "Go probe: $exe $($goArgs -join ' ')"
            & $exe @goArgs | Out-Host
            if ($LASTEXITCODE) { throw "the Go probe failed (exit $LASTEXITCODE)" }
        } else {
            & (Join-Path $agentDir 'Probe.ps1') @probeArgs
        }
        $probeRes = Get-Content $res -Raw | ConvertFrom-Json
        $transcript = Join-Path $resultsDir "$($r.id)-$stamp.transcript.jsonl"
        if ($probeRes.transcript -and (Test-Path $probeRes.transcript)) { Copy-Item $probeRes.transcript $transcript } else { $transcript = '' }
        $s = Measure-InstallMd $r $(if (Test-Path $out) { $out } else { '' }) $probeRes $(if ($ReplayReport) { Get-Content $ReplayReport -Raw | ConvertFrom-Json })
        $s['transcript'] = $transcript
        $s['engine'] = $Engine
        Add-Content -Path $resultsFile -Value ($s | ConvertTo-Json -Depth 8 -Compress) -Encoding UTF8
        if ($ResultOut) { [IO.File]::WriteAllText($ResultOut, ($s | ConvertTo-Json -Depth 8 -Compress), [Text.UTF8Encoding]::new($false)) }
        Write-Score $s
    }
    'score' {
        $r = Get-Repo $Repo
        $s = Measure-InstallMd $r $Score $(if ($ProbeResult) { Get-Content $ProbeResult -Raw | ConvertFrom-Json }) $(if ($ReplayReport) { Get-Content $ReplayReport -Raw | ConvertFrom-Json })
        Add-Content -Path $resultsFile -Value ($s | ConvertTo-Json -Depth 8 -Compress) -Encoding UTF8
        Write-Score $s
    }
    'summary' {
        if (-not (Test-Path $resultsFile)) { Write-Host 'No results yet.'; return }
        $rows = @(Get-Content $resultsFile | ForEach-Object { $_ | ConvertFrom-Json })
        Write-Host "Probe evaluation: $($rows.Count) scored runs ($resultsFile)" -ForegroundColor Cyan
        foreach ($g in ($rows | Group-Object { "$($_.model) | $($_.prompt_version) | no_docs=$($_.no_docs)$(if ($_.engine -and $_.engine -ne 'ps') { " | engine=$($_.engine)" })" })) {
            $ok = @($g.Group | Where-Object { Test-Delivered $_ })
            $failed = @($g.Group | Where-Object { -not (Test-Delivered $_) })
            $line = "  {0}: delivered {1} of {2}" -f $g.Name, $ok.Count, $g.Count
            if ($ok) { $line += ", mean recall {0:P0}, mean format problems {1:N1}" -f ($ok | Measure-Object recall -Average).Average, (($ok | ForEach-Object { @($_.format_problems).Count }) | Measure-Object -Average).Average }
            if ($failed) { $line += "; not delivered: $(($failed | Group-Object { if ($_.stop_reason) { $_.stop_reason } else { 'unknown' } } | ForEach-Object { "$($_.Name) x$($_.Count)" }) -join ', ')" }
            Write-Host $line
        }
        $miss = @($rows | Where-Object { Test-Delivered $_ } | ForEach-Object { $r = $_; $_.misses | ForEach-Object { [pscustomobject]@{ repo = $r.repo; id = $_.id; category = $_.category; weight = $_.weight } } })
        Write-Host "`nWeighted misses by category (where the probe loses most):" -ForegroundColor Cyan
        $miss | Group-Object category | ForEach-Object { [pscustomobject]@{ Category = $_.Name; Weight = ($_.Group | Measure-Object weight -Sum).Sum; Misses = $_.Count } } | Sort-Object Weight -Descending | Format-Table -AutoSize | Out-String | Write-Host
        Write-Host 'Most-missed facts (candidates for the next probe prompt, tool or inventory area):' -ForegroundColor Cyan
        $miss | Group-Object { "$($_.repo)/$($_.id)" } | Sort-Object { ($_.Group | Measure-Object weight -Sum).Sum } -Descending | Select-Object -First 15 | ForEach-Object { Write-Host "  $($_.Name): missed $($_.Count)x ($($_.Group[0].category), w$($_.Group[0].weight))" }
        $struct = @($rows | Where-Object { $_.live_checks })
        if ($struct) {
            Write-Host ("`nLive checks across runs: {0} passed, {1} failed, {2} untested" -f ($struct.live_checks | Measure-Object passed -Sum).Sum, ($struct.live_checks | Measure-Object failed -Sum).Sum, ($struct.live_checks | Measure-Object untested -Sum).Sum)
        }
        $rep = @($rows | Where-Object { $_.replay })
        if ($rep) { Write-Host "Replays: $(@($rep | Where-Object { $_.replay.status -eq 'installed' }).Count) of $($rep.Count) installed" }
    }
}
