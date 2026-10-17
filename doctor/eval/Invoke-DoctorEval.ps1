<#
.SYNOPSIS
    Injects, checks, grades and restores the doctor's fault scenarios on an
    installed copy of rl-roboracer, and runs the doctor against them.

.DESCRIPTION
    Every scenario breaks something on purpose. Run it against a test install
    (e.g. the rltest account), or against a dev stack that is not training.

      -Action List      scenarios and their requirements
      -Action Inject    break the install (one scenario)
      -Action Check     is the failure gone? (Healthy test)
      -Action Grade     score a root-cause text against the scenario
      -Action Restore   undo the injection
      -Action Cycle     Inject, show the symptom, wait for Enter, Check, Restore
      -Action Run       for each model and scenario: inject, run the doctor,
                        grade, restore. Results append to -ResultsFile.

    Scenarios marked Requires (admin, second-account, test-install) are
    skipped by Run unless named with -Scenario.

.EXAMPLE
    .\Invoke-DoctorEval.ps1 -Action Run -Models gemma-4-E4B-it-Q4_K_M.gguf,gpt-oss-20b-MXFP4.gguf -Runs 3
#>
[CmdletBinding()]
param(
    [ValidateSet('List', 'Inject', 'Check', 'Grade', 'Restore', 'Cycle', 'Run', 'Regrade', 'RemoteModels')][string]$Action = 'List',
    [string[]]$Scenario = @(),
    [string]$RootCause = '',
    [string]$RepoDir = (Join-Path $env:USERPROFILE 'rl-roboracer\rl-roboracer'),
    [string[]]$Models = @('gemma-4-E4B-it-Q4_K_M.gguf', 'gpt-oss-20b-MXFP4.gguf'),
    [string]$Backend = '',
    [int]$Runs = 1,
    [int]$Clients = 1,
    [string]$ResultsFile = (Join-Path $env:LOCALAPPDATA 'rl-roboracer\doctor\eval-results.jsonl')
)

$ErrorActionPreference = 'Stop'
# powershell -File passes "a,b" as one string
$Models = @($Models | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
$Scenario = @($Scenario | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
$RepoDir = (Resolve-Path $RepoDir).Path
$script:DoctorRepoDir = $RepoDir
. (Join-Path $PSScriptRoot 'Scenarios.ps1')

function Get-Scenario([string]$Id) {
    $s = $DoctorScenarios | Where-Object { $_.Id -eq $Id }
    if (-not $s) { throw "No scenario '$Id'. Known: $(($DoctorScenarios | ForEach-Object { $_.Id }) -join ', ')" }
    $s
}

function Test-RootCause($S, [string]$Text) {
    $Text = $Text -replace '[\u2010-\u2015\u2212]', '-' -replace '[\u2018\u2019]', "'" -replace '[\u201C\u201D]', '"'
    $missing = @($S.RootCause | Where-Object { $group = $_; -not ($group | Where-Object { $Text -match $_ }) })
    $rejected = @(if ($S.ContainsKey('Reject')) { $S.Reject | Where-Object { $Text -match $_ } })
    [pscustomobject]@{ Pass = ($missing.Count -eq 0 -and $rejected.Count -eq 0); Missing = @($missing | ForEach-Object { $_ -join '|' }); Rejected = $rejected }
}

function Wait-Healthy($S, [bool]$Want, [int]$TimeoutSec) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    do {
        if ([bool](& $S.Healthy) -eq $Want) { return $true }
        Start-Sleep -Seconds 5
    } while ($sw.Elapsed.TotalSeconds -lt $TimeoutSec)
    $false
}

if ($Action -eq 'List') {
    $DoctorScenarios | ForEach-Object {
        [pscustomobject]@{ Id = $_.Id; Requires = $(if ($_.ContainsKey('Requires')) { $_.Requires } else { '' }); Source = $_.Source }
    } | Format-Table -AutoSize -Wrap
    return
}

if ($Action -eq 'Run') {
    . (Join-Path $PSScriptRoot '..\Llm.ps1')
    $doctor = Join-Path $PSScriptRoot '..\Doctor.ps1'
    $selected = if ($Scenario) { @($Scenario | ForEach-Object { Get-Scenario $_ }) } else { @($DoctorScenarios | Where-Object { -not $_.ContainsKey('Requires') }) }
    New-Item -ItemType Directory -Force (Split-Path $ResultsFile) | Out-Null
    $summary = @()
    foreach ($m in $Models) {
        Write-Host "=== $m" -ForegroundColor Cyan
        $remote = Test-RemoteSpec $m
        $server = if ($remote) { $llm = Get-RemoteLlm $m; [pscustomobject]@{ Model = $m; Backend = 'remote'; Url = $llm.Url } } else { Start-LlmServer -Model $m -Backend $Backend }
        $llmArgs = if ($remote) { @{ Remote = $m } } else { @{ LlmUrl = $server.Url } }
        try {
            foreach ($s in $selected) {
                for ($run = 1; $run -le $Runs; $run++) {
                    $row = [ordered]@{ at = (Get-Date).ToString('s'); model = $server.Model; backend = $server.Backend; scenario = $s.Id; run = $run }
                    if (-not (Wait-Healthy $s $true 60)) {
                        $row.outcome = 'skipped: not healthy before injection'
                        Write-Host "  $($s.Id) #$run skipped: not healthy before injection" -ForegroundColor Yellow
                    } else {
                        try {
                            & $s.Inject
                            if (-not (Wait-Healthy $s $false 90)) {
                                $row.outcome = 'skipped: injection had no effect'
                                Write-Host "  $($s.Id) #$run skipped: injection had no effect" -ForegroundColor Yellow
                            } else {
                                $r = & $doctor -Problem $s.Symptom -RepoDir $RepoDir @llmArgs -Quiet -PassThru
                                $g = Test-RootCause $s $r.root_cause
                                $row.outcome = if ($r.error) { "error: $($r.error)" } elseif ($g.Pass) { 'correct' } else { 'wrong' }
                                foreach ($k in 'tool_calls', 'invalid_calls', 'refused_commands', 'nudges', 'server_errors', 'prompt_tokens', 'completion_tokens', 'finished', 'confidence', 'seconds', 'root_cause', 'fix', 'transcript') { $row[$k] = $r.$k }
                                Write-Host ("  {0} #{1}: {2} ({3} calls, {4}s) {5}" -f $s.Id, $run, $row.outcome, $r.tool_calls, $r.seconds, $(if ($r.root_cause.Length -gt 140) { $r.root_cause.Substring(0, 140) + '...' } else { $r.root_cause }))
                            }
                        } catch {
                            $row.outcome = "harness error: $_"
                            Write-Host "  $($s.Id) #$run harness error: $_" -ForegroundColor Red
                        }
                        & $s.Restore
                        if (-not (Wait-Healthy $s $true 240)) {
                            $row.restore = 'FAILED'
                            Add-Content -Path $ResultsFile -Value ([pscustomobject]$row | ConvertTo-Json -Compress -Depth 5) -Encoding UTF8
                            throw "Scenario '$($s.Id)' did not recover after Restore. Stopping so later scenarios do not run on a broken stack."
                        }
                    }
                    Add-Content -Path $ResultsFile -Value ([pscustomobject]$row | ConvertTo-Json -Compress -Depth 5) -Encoding UTF8
                    $summary += [pscustomobject]$row
                }
            }
        } finally { Stop-LlmServer $server }
    }
    Write-Host ''
    $summary | Group-Object model | ForEach-Object {
        $done = @($_.Group | Where-Object { $_.outcome -in 'correct', 'wrong' })
        [pscustomobject]@{ Model = $_.Name; Correct = @($done | Where-Object { $_.outcome -eq 'correct' }).Count; Graded = $done.Count
            AvgCalls = [math]::Round((@($done.tool_calls) | Measure-Object -Average).Average, 1); AvgSeconds = [math]::Round((@($done.seconds) | Measure-Object -Average).Average) }
    } | Format-Table -AutoSize
    Write-Host "Results: $ResultsFile"
    return
}

if ($Action -eq 'RemoteModels') {
    . (Join-Path $PSScriptRoot '..\Llm.ps1')
    foreach ($p in 'anthropic', 'openai', 'xai', 'google') {
        try { Get-RemoteModelIds $p | ForEach-Object { $_ -replace '^models/', '' } | Where-Object { $_ -match 'opus|grok-4|gpt-5|gemini-[3-9]' } | ForEach-Object { "${p}:$_" } }
        catch { "${p}: $($_.Exception.Message)" }
    }
    return
}

if ($Action -eq 'Regrade') {
    $rows = @(Get-Content $ResultsFile -Encoding UTF8 | ConvertFrom-Json | Where-Object { $_.root_cause -and (-not $Scenario -or $Scenario -contains $_.scenario) })
    $rows | ForEach-Object {
        $g = Test-RootCause (Get-Scenario $_.scenario) $_.root_cause
        [pscustomobject]@{ At = $_.at; Model = $_.model; Scenario = $_.scenario; Run = $_.run; Was = $_.outcome; Now = $(if ($g.Pass) { 'correct' } else { 'wrong' }) }
    } | Format-Table -AutoSize
    return
}

if ($Scenario.Count -ne 1) { throw '-Scenario (exactly one) is required for this action.' }
$s = Get-Scenario $Scenario[0]

switch ($Action) {
    'Inject'  { & $s.Inject; Write-Host "Injected '$($s.Id)'. Symptom to give the doctor:"; Write-Host "  $($s.Symptom)" }
    'Check'   { $ok = [bool](& $s.Healthy); Write-Host "$($s.Id): $(if ($ok) { 'healthy' } else { 'still failing' })"; $ok }
    'Restore' { & $s.Restore; Write-Host "Restored '$($s.Id)'." }
    'Grade'   {
        $g = Test-RootCause $s $RootCause
        Write-Host "$($s.Id): root cause $(if ($g.Pass) { 'PASS' } elseif ($g.Rejected) { "FAIL (known wrong answer: $($g.Rejected -join '; '))" } else { "FAIL (nothing matched: $($g.Missing -join '; '))" })"
        $g
    }
    'Cycle'   {
        & $s.Inject
        Write-Host "Injected '$($s.Id)'. Symptom:"; Write-Host "  $($s.Symptom)"
        Start-Sleep -Seconds 15
        Write-Host "Healthy right after injection (should be False): $([bool](& $s.Healthy))"
        Read-Host 'Investigate or run the doctor now, then press Enter to check and restore'
        Write-Host "Healthy before restore: $([bool](& $s.Healthy))"
        & $s.Restore
        Start-Sleep -Seconds 15
        Write-Host "Healthy after restore (should be True): $([bool](& $s.Healthy))"
    }
}
