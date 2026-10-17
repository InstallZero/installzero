<#
.SYNOPSIS
    InstallZero agent: installs a project from an install.md, adapting to
    this machine and fixing failures with the doctor's tools.

.DESCRIPTION
    Reads an install.md (format in INSTALL-MD.md). For a structured file it
    works step by step: detect (skip when done), install, verify, and on a
    failure investigates with the doctor's read-only tools and tries a fix.
    A file without "## Step" headings is treated as plain install
    instructions (a project README works): the agent plans its own steps.

    Every command, from the file or the model, is classified first
    (Policy.ps1): read-only and install-folder changes run; machine-wide
    changes run only after you press Y; destructive ones are refused.
    Everything run is recorded in the report with its tier and result.

.PARAMETER InstallMd
    The install.md (or README) to install from.

.PARAMETER InstallDir
    Install folder. Default %USERPROFILE%\<name from the file>.

.PARAMETER DryRun
    Show the plan for this machine (which steps apply, which blocks would
    run, each command's tier) without a model and without running anything.

.EXAMPLE
    .\Agent.ps1 -InstallMd .\examples\comfyui.install.md -DryRun

.EXAMPLE
    .\Agent.ps1 -InstallMd .\examples\comfyui.install.md -Model gpt-oss-20b-MXFP4.gguf
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$InstallMd,
    [string]$InstallDir = '',
    [string]$Model = '',
    [ValidateSet('', 'cuda', 'vulkan', 'cpu')][string]$Backend = '',
    [string]$LlmUrl = '',
    [string]$Remote = '',
    [Alias('MaxSteps')][int]$MaxTurns = 120,
    [int]$TimeoutMinutes = 240,
    [int]$CommandTimeoutMinutes = 60,
    [string]$ResultFile = '',
    [switch]$DryRun,
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
$script:AgentQuiet = [bool]$Quiet
$doctorDir = Join-Path $PSScriptRoot '..\doctor'
. (Join-Path $doctorDir 'Llm.ps1')
. (Join-Path $doctorDir 'Tools.ps1')
. (Join-Path $PSScriptRoot 'InstallMd.ps1')
. (Join-Path $PSScriptRoot 'Policy.ps1')
. (Join-Path $PSScriptRoot 'AgentLoop.ps1')

$doc = Read-InstallMd $InstallMd
$structured = $doc.Steps.Count -gt 0
$name = if ($doc.Meta['name']) { $doc.Meta['name'] } else { [IO.Path]::GetFileNameWithoutExtension($InstallMd) -replace '\.install$', '' -replace '[^\w.-]', '-' }
if (-not $InstallDir) { $InstallDir = Join-Path $env:USERPROFILE $name }
$repoDir = Join-Path $InstallDir $name
$roots = @($InstallDir, (Join-Path $env:TEMP 'installzero'))
$placeholders = @{ INSTALL_DIR = $InstallDir; REPO_DIR = $repoDir }
$machine = Get-MachineProfile $InstallDir
$script:DoctorRepoDir = if (Test-Path $repoDir) { $repoDir } else { $InstallDir }

$problems = if ($structured) { @(Test-InstallMd $doc) } else { @() }
$machineText = "$($machine.os_version), $($machine.arch), GPU $($machine.gpu) ($($machine.gpus))$(if ($machine.driver) { ", NVIDIA driver $($machine.driver)" }), $($machine.ram_gb) GB RAM, $($machine.disk_gb) GB free on the install drive"

# Per step: unmet requirements and the blocks chosen for this machine.
function Get-StepPlan($Step) {
    $unmet = @(Get-UnmetRequirements $Step $machine)
    $blocks = [ordered]@{}
    foreach ($k in 'detect', 'install', 'verify') {
        $b = Select-StepBlock $Step $k $machine
        if ($b) {
            $code = Expand-Placeholders $b.Code $placeholders
            $shell = if ($b.Shell) { $b.Shell } else { 'powershell' }
            $blocks[$k] = [pscustomobject]@{ Shell = $shell; Code = $code; Policy = (Get-CommandTier $code $shell $roots) }
        }
    }
    [pscustomobject]@{ Step = $Step; Unmet = $unmet; Blocks = $blocks }
}

Write-AgentLine "InstallZero agent: $name from $($doc.Path)" 'Cyan'
Write-AgentLine "  This machine: $machineText"
Write-AgentLine "  Install folder: $InstallDir"
if ($problems) { Write-AgentLine "  Problems in the file: $($problems -join '; ')" 'Yellow' }

$plans = @(if ($structured) { $doc.Steps | ForEach-Object { Get-StepPlan $_ } })
if ($DryRun) {
    if (-not $structured) { Write-AgentLine '  No "## Step" headings: plain instructions; the agent plans its own steps from the text when run without -DryRun.' }
    foreach ($p in $plans) {
        $s = $p.Step
        Write-AgentLine ''
        Write-AgentLine "Step $($s.Number): $($s.Id) - $($s.Title)" 'White'
        if ($s.Why) { Write-AgentLine "  Why: $($s.Why)" }
        if ($p.Unmet) { Write-AgentLine "  Skipped on this machine: $($p.Unmet -join '; ')" 'Yellow'; continue }
        foreach ($k in 'detect', 'install', 'verify') {
            $b = $p.Blocks[$k]
            if (-not $b) { Write-AgentLine "  $($k): (none for this machine)" 'DarkGray'; continue }
            Write-AgentLine ("  {0} [{1}, {2}]: {3}" -f $k, $b.Shell, $b.Policy.Tier, $b.Policy.Reason) $(switch ($b.Policy.Tier) { 'never' { 'Red' } 'system' { 'Yellow' } default { '' } })
            ($b.Code -split "`n") | ForEach-Object { Write-AgentLine "      $_" 'DarkGray' }
        }
    }
    exit 0
}

# ---- tools ------------------------------------------------------------------

$changes = [System.Collections.Generic.List[object]]::new()
$stepStatus = [ordered]@{}
foreach ($p in $plans) { $stepStatus[$p.Step.Id] = $(if ($p.Unmet) { 'not-applicable' } else { 'pending' }) }

# Classifies, asks when needed, runs, and records one command.
function Invoke-TieredCommand([string]$Code, [string]$Shell, [string]$Origin, [string]$Why, [int]$TimeoutSec) {
    $policy = Get-CommandTier $Code $Shell $roots
    $entry = [ordered]@{ time = (Get-Date).ToString('s'); origin = $Origin; shell = $Shell; command = $Code; tier = $policy.Tier; reason = $policy.Reason; approved = $null; exit = $null }
    $changes.Add($entry)
    if ($policy.Tier -eq 'never') { $entry.approved = $false; return "refused: never allowed ($($policy.Reason)). Find another way or tell the user in finish." }
    if ($policy.Tier -eq 'system') {
        $ok = Read-AgentConsent "The agent wants to run a machine-wide command ($($policy.Reason)):" @(($Code -split "`n" | Select-Object -First 12) + @("Why: $Why"))
        $entry.approved = $ok
        if (-not $ok) { return 'refused: the user declined this machine-wide command. Do not ask again for the same command; continue without it or explain in finish.' }
    } else { $entry.approved = $true }
    New-Item -ItemType Directory -Force $InstallDir | Out-Null
    $cwd = if (Test-Path $repoDir) { $repoDir } else { $InstallDir }
    $r = Invoke-StreamedCommand $Code $Shell $cwd $TimeoutSec
    $entry.exit = $r.Code
    if (Test-Path $repoDir) { $script:DoctorRepoDir = $repoDir }
    "[exit $($r.Code)] ($($policy.Tier): $($policy.Reason))`n$(Protect-Secrets $r.Out)"
}

$tools = @($script:DoctorToolSchemas | Where-Object { $_.function.name -ne 'finish' })
$tools += @{ type = 'function'; function = @{ name = 'run_change'; description = 'Run a PowerShell (or bash) command that changes something: installs, clones, builds, starts services, writes files. It is classified first: install-folder changes run, machine-wide ones need the user''s Y, destructive ones are refused. Returns the exit code and output.'; parameters = @{ type = 'object'; properties = @{
    command = @{ type = 'string' }; shell = @{ type = 'string'; enum = @('powershell', 'bash'); description = 'default powershell' }
    timeout_minutes = @{ type = 'integer'; description = "default 20, max $CommandTimeoutMinutes" } }; required = @('command') } } }
$tools += @{ type = 'function'; function = @{ name = 'finish'; description = 'End the install: call when every step is done, or when you are blocked and need the user.'; parameters = @{ type = 'object'; properties = @{
    status = @{ type = 'string'; enum = @('installed', 'partial', 'failed') }
    summary = @{ type = 'string'; description = 'What was installed and how to use it (URLs, commands to start it).' }
    blocked_on = @{ type = 'string'; description = 'What the user must do, when not fully installed.' } }; required = @('status', 'summary') } } }
if ($structured) {
    $stepIds = @($plans | ForEach-Object { $_.Step.Id })
    $tools += @{ type = 'function'; function = @{ name = 'run_step'; description = 'Run the detect, install or verify block of a step from install.md, as chosen for this machine. Returns the exit code and output.'; parameters = @{ type = 'object'; properties = @{
        step = @{ type = 'string'; enum = $stepIds }; block = @{ type = 'string'; enum = @('detect', 'install', 'verify') } }; required = @('step', 'block') } } }
    $tools += @{ type = 'function'; function = @{ name = 'step_result'; description = 'Record the outcome of a step once its verify passed, it was already done, or it cannot be done.'; parameters = @{ type = 'object'; properties = @{
        step = @{ type = 'string'; enum = $stepIds }; status = @{ type = 'string'; enum = @('done', 'already-done', 'skipped', 'failed') }; evidence = @{ type = 'string' } }; required = @('step', 'status', 'evidence') } } }
}

$dispatch = {
    param($Name, $A)
    switch ($Name) {
        'run_change' {
            $mins = if ($A.timeout_minutes) { [math]::Min([int]$A.timeout_minutes, $CommandTimeoutMinutes) } else { 20 }
            $shell = if ($A.shell) { "$($A.shell)" } else { 'powershell' }
            return Invoke-TieredCommand "$($A.command)" $shell 'model' "$($A.purpose)" ($mins * 60)
        }
        'run_step' {
            $p = $plans | Where-Object { $_.Step.Id -eq "$($A.step)" } | Select-Object -First 1
            if (-not $p) { return "error: no step '$($A.step)'" }
            if ($p.Unmet) { return "not applicable on this machine: $($p.Unmet -join '; ')" }
            $b = $p.Blocks["$($A.block)"]
            if (-not $b) { return "step $($A.step) has no $($A.block) block for this machine" }
            $timeout = if ($A.block -eq 'install') { $CommandTimeoutMinutes * 60 } else { 300 }
            return Invoke-TieredCommand $b.Code $b.Shell "install.md $($A.step)/$($A.block)" "$($p.Step.Why)" $timeout
        }
        'step_result' {
            if (-not $stepStatus.Contains("$($A.step)")) { return "error: no step '$($A.step)'" }
            $stepStatus["$($A.step)"] = "$($A.status)"
            $left = @($stepStatus.Keys | Where-Object { $stepStatus[$_] -eq 'pending' })
            return "Recorded. Steps still pending: $(if ($left) { $left -join ', ' } else { 'none; call finish' })"
        }
        default { return Invoke-DoctorTool $Name $A }
    }
}

# ---- prompt -------------------------------------------------------------------

$planText = if ($structured) {
    ($plans | ForEach-Object {
        $p = $_; $s = $p.Step
        $head = "Step $($s.Number): $($s.Id) - $($s.Title)$(if ($s.Why) { "`n  Why: $($s.Why)" })"
        if ($p.Unmet) { "$head`n  NOT APPLICABLE here: $($p.Unmet -join '; ')" }
        else { "$head`n" + (($p.Blocks.Keys | ForEach-Object { "  $_ [$($p.Blocks[$_].Shell), $($p.Blocks[$_].Policy.Tier)]:`n    " + ($p.Blocks[$_].Code -replace "`n", "`n    ") }) -join "`n") }
    }) -join "`n`n"
} else { $doc.Text }

$system = @"
You are the InstallZero install agent. You install the project described below on this machine, adapting to it, and you are also an install doctor: when something fails you find the root cause with your read-only tools (system_facts, gpu_drivers, read_file, list_dir, grep, run_command, compose_ps, service_logs) and fix it.

This machine: $machineText.
Install folder: $InstallDir (the project's repo goes in $repoDir). Use absolute paths inside it.

How to work:
$(if ($structured) { @'
- Follow the steps in order. For each applicable step: run_step detect; exit 0 means it is already done (step_result already-done). Otherwise run_step install, then run_step verify. When verify passes, step_result done.
- When a block fails, read its output, investigate, and fix with run_change (or adapt the command to this machine), then verify again. After three failed attempts on one step, record it failed and decide whether later steps can still run.
'@ } else { @'
- The document is plain instructions. Work out the steps for this machine, then do them with run_change, checking each worked (a command that exits 0 and shows the expected version, file, port or page) before moving on.
'@ })
- Commands run in a fresh PowerShell each time (no state between calls): use absolute paths and full venv paths (e.g. $repoDir\.venv\Scripts\python.exe).
- Commands that change the machine outside the install folder (winget, WSL, services, global installs) need the user's Y. Ask only when the step needs it; if the user refuses, do not ask again for the same thing.
- Long-running servers must not block you: start them with run_change using Start-Process -FilePath <program inside the install folder> -WindowStyle Minimized, or docker compose up -d, then check the port or URL.
- Never put secret values in commands you show; if a token is needed, stop and say in finish which variable the user must set.
- Call finish when everything is installed and verified, or when you are blocked.
"@

$user = if ($structured) { "Install $name. The steps, with the blocks chosen for this machine and each block's safety tier:`n`n$planText" } else { "Install $name by following these instructions:`n`n$planText" }

# ---- run ----------------------------------------------------------------------

$runId = Get-Date -Format 'yyyyMMdd-HHmmss'
$logDir = Join-Path $env:LOCALAPPDATA 'installzero\agent'
New-Item -ItemType Directory -Force $logDir | Out-Null
$transcript = Join-Path $logDir "$runId-$name.jsonl"
$session = $null
try {
    $session = Start-AgentLlm $Model $Backend $Remote $LlmUrl
    $run = Invoke-AgentLoop -Session $session -System $system -User $user -Tools $tools -Dispatch $dispatch -MaxTurns $MaxTurns -FinalTools @('step_result') -TimeoutMinutes $TimeoutMinutes -Transcript $transcript
} finally { if ($session -and $session.Server) { Stop-LlmServer $session.Server } }

$report = [ordered]@{
    run_id = $runId; name = $name; install_md = $doc.Path; install_dir = $InstallDir; model = $session.Label; machine = $machine
    status = $(if ($run.Finish) { "$($run.Finish.status)" } else { 'unfinished' }); summary = $(if ($run.Finish) { "$($run.Finish.summary)" } else { '' })
    blocked_on = $(if ($run.Finish -and $run.Finish.PSObject.Properties.Name -contains 'blocked_on') { "$($run.Finish.blocked_on)" } else { '' })
    steps = $stepStatus; commands = $changes; file_problems = $problems
    stats = [ordered]@{ tool_calls = $run.tool_calls; model_turns = $run.steps; invalid_calls = $run.invalid_calls; nudges = $run.nudges; seconds = $run.seconds; prompt_tokens = $run.prompt_tokens; completion_tokens = $run.completion_tokens; stop_reason = $run.stop_reason; error = $run.error }
    transcript = $transcript
}
if (-not $ResultFile) { $ResultFile = Join-Path $logDir "$runId-$name.report.json" }
$report | ConvertTo-Json -Depth 8 | Set-Content -Path $ResultFile -Encoding UTF8

Write-AgentLine ''
Write-AgentLine "Install $($report.status): $name" $(if ($report.status -eq 'installed') { 'Green' } else { 'Yellow' })
if ($report.summary) { Write-AgentLine $report.summary }
if ($report.blocked_on) { Write-AgentLine "Needs you: $($report.blocked_on)" 'Yellow' }
if ($structured) { foreach ($k in $stepStatus.Keys) { Write-AgentLine "  $($k): $($stepStatus[$k])" } }
$sys = @($changes | Where-Object { $_.tier -eq 'system' })
Write-AgentLine "Commands run: $(@($changes | Where-Object approved).Count) ($($sys.Count) machine-wide asked, $(@($sys | Where-Object approved).Count) allowed; $(@($changes | Where-Object { $_.tier -eq 'never' }).Count) refused as destructive). Report: $ResultFile"
