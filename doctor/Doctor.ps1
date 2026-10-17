<#
.SYNOPSIS
    Diagnoses an rl-roboracer install problem with a local open-weights model.

.DESCRIPTION
    Starts llama-server with a bundled model (or uses -LlmUrl), then lets the
    model investigate with read-only tools until it reports a root cause.
    Diagnosis only: nothing on the machine is changed. Every run is logged
    as JSON lines under %LOCALAPPDATA%\rl-roboracer\doctor\.

    See docs/install-doctor-plan.md and scripts/install/doctor/MODELS.md.

.EXAMPLE
    .\Doctor.ps1 -Problem "Training jobs fail with: DNS resolution failed for fly-brain:50061"

.EXAMPLE
    .\Doctor.ps1 -Problem "..." -Model gemma-4-E4B-it-Q4_K_M.gguf -Backend cpu

.EXAMPLE
    .\Doctor.ps1 -Mode optimize
    Reviews a short list of PC settings that affect training (WSL memory and
    processors, power plan, sleep, GPU driver against nvidia.com's latest,
    disk, Unity's GPU on hybrid laptops, GPU load while training) and
    recommends changes. GPU load is measured for 10 s before the doctor's
    own model loads. Power, sleep, driver version, disk and Unity's GPU are
    measured in code (Remedies.ps1), so a change that is already in place
    is never recommended. Nothing is changed; only the GPU model name is
    sent to nvidia.com.

.EXAMPLE
    .\Doctor.ps1 -Mode optimize -AllowApply
    As above, and the doctor may apply the power mode, sleep and Unity GPU
    changes it finds needed; each one is confirmed at the console (60 s,
    default no) and measured again afterwards.
#>
[CmdletBinding()]
param(
    [ValidateSet('diagnose', 'optimize')][string]$Mode = 'diagnose',
    [string]$Problem = '',
    # Read the problem from a file instead (the installer passes it this way).
    [string]$ProblemFile = '',
    [string]$RepoDir = '',
    [string]$Model = '',
    [ValidateSet('', 'cuda', 'vulkan', 'cpu')][string]$Backend = '',
    [string]$LlmUrl = '',
    # Hosted model for evaluation, "anthropic|openai|xai:<model-id>"; key from
    # ANTHROPIC_API_KEY / OPENAI_API_KEY / XAI_API_KEY. Sends logs and paths
    # from this PC to that provider.
    [string]$Remote = '',
    [int]$MaxSteps = 25,
    [int]$TimeoutMinutes = 15,
    [string]$ResultFile = '',
    # Optimize mode: let the model apply the changes Remedies.ps1 can make
    # (power mode, sleep, Unity's GPU), each confirmed by the user at the console.
    [switch]$AllowApply,
    [switch]$Quiet,
    [switch]$PassThru
)

$ErrorActionPreference = 'Stop'
if (-not $RepoDir) { $RepoDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path }
$script:DoctorRepoDir = $RepoDir
. (Join-Path $PSScriptRoot 'Llm.ps1')
. (Join-Path $PSScriptRoot 'Tools.ps1')
. (Join-Path $PSScriptRoot 'Remedies.ps1')
if ($ProblemFile) { $Problem = Get-Content -LiteralPath $ProblemFile -Raw -Encoding UTF8 }

# Kept short on purpose: each check must be answerable with the read-only tools
# in a few calls, and only settings with a clear effect on training are listed.
$script:OptimizeItems = @('memory', 'processors', 'power', 'sleep', 'gpu-driver', 'gpu-services', 'disk', 'unity-gpu', 'gpu-load')

function Get-OptimizeRequest([string]$GpuLoad, $Checks) {
    $measured = ($Checks.Values | ForEach-Object { "$([array]::IndexOf($script:OptimizeItems, $_.Item) + 1). $($_.Item): $($_.Status) - $($_.Current)" }) -join "`n"
    $applyList = @($Checks.Values | Where-Object { $_.Status -eq 'CHANGE' -and $_.Apply })
    $applyText = if ($AllowApply -and $applyList) {
        "`nYou may apply these with apply_change, which checks again that the change is needed, asks the user to confirm, applies it and measures again: $(($applyList | ForEach-Object { "$($_.Item) ($($_.Apply))" }) -join '; '). Call apply_change once for each of them before finish; the user decides at the console."
    } else { '' }
    @"
Review this PC's configuration for running rl-roboracer training for hours unattended. Check exactly these items, nothing else:

1. memory: Docker's memory (system_facts, docker info mem) against physical RAM, and the [wsl2] memory= and swap= lines in $env:USERPROFILE\.wslconfig. Read that file with read_file before judging items 1 and 2 (it may not exist; then WSL gets half of RAM). If Docker's memory does not match memory=, the file is not in effect: recommend saving work and running wsl --shutdown, then restarting Docker Desktop. Otherwise recommend a change only if Docker has less than 8 GB while the PC could give more, if memory= leaves Windows less than 6 GB (the Unity clients run on Windows), or if swap=0.
   Improves: the trainer (TensorFlow and its replay buffer) gets the memory intended for it, so long runs are not killed for running out of memory; or Windows and the Unity clients keep enough memory not to swap.
2. processors: Docker's CPUs against the logical processors (Get-CimInstance Win32_Processor) and any processors= line in .wslconfig. A mismatch with processors= means the file is not in effect, as in item 1. Recommend a change only if a processors= limit gives Docker fewer than half of them.
   Improves: the containers (trainer, ros-server bridges) get the CPUs intended for them, so steps are processed without waiting; or the Unity clients keep the cores they need to simulate at full speed.
3. power, 4. sleep, 5. gpu-driver, 7. disk, 8. unity-gpu: measured by the installer just now (Remedies.ps1). These verdicts are final: do not re-check them, do not contradict them, and leave them out of fix (the installer adds their steps):
$measured
$applyText
6. gpu-services: call gpu_drivers (ignore its driver version lines; item 5 covers them). With several NVIDIA GPUs, say which GPU each GPU service sees (gpu_drivers maps what each container sees to the host GPU by UUID) and recommend device_ids only if a larger GPU sits unused; name GPUs in device_ids by UUID ("GPU-..."), never by index, because indexes can differ inside containers. A FAILED in-container probe is a required fix: report its output. Services that are not running are not a finding. Also flag other programs holding most of the GPU memory (nvidia-smi).
   Improves: a working in-container GPU lets the trainer and fly-brain use the GPU at all; moving a service to an unused larger GPU, or closing programs that hold GPU memory, gives training more GPU memory and speed.
9. gpu-load: judge only from the measurement below, taken while the stack ran and before your own model was loaded. If the trainer logged no recent training step (within about 2 minutes), training was not running: UNKNOWN. If GPU memory peaked above 90% of the total, recommend fewer Unity clients (re-run the installer with a smaller -Clients) or closing the GPU programs listed. If average utilization stayed below 40% and memory below 60% while training ran, the GPU has room: recommend one more Unity client (re-run the installer with -Clients <current + 1>, at most 4). Otherwise OK.
   Improves: fewer clients or closed programs keep training from crashing when the GPU runs out of memory; another client collects more environment steps per hour on a GPU that is otherwise idle part of the time.

$GpuLoad

An item that already looks fine needs no change; do not suggest optional tweaks. run_command already runs PowerShell: call cmdlets directly, not through powershell -Command or wmic. If a check cannot be done with your tools, say so for that item instead of guessing.
Recommend a change only when the value you observed differs from what the item asks for; if the current value already meets it, the item is OK.
In finish, root_cause has exactly nine lines, one per item in this order, each "<n>. <item>: OK", "<n>. <item>: CHANGE - <reason>" or "<n>. <item>: UNKNOWN - <reason>" (items 3, 4, 5, 7 and 8 copied from the measured verdicts). fix lists only CHANGE items among 1, 2, 6 and 9, numbered by item, each with its own Improves line directly under its steps, based on the item's Improves note and the values you observed. Layout:
1. memory: <exact steps>
   Improves: ...
"@
}

$checks = $null
$applied = [System.Collections.Generic.List[string]]::new()
$offered = [System.Collections.Generic.List[string]]::new()
if ($Mode -eq 'optimize' -and -not $Problem.Trim()) {
    if (-not $Quiet) { Write-Host 'Measuring GPU load for 10 s before the model starts, and checking power, sleep, driver, disk and Unity GPU settings ...' }
    $load = Get-GpuLoadSnapshot 10
    $checks = [ordered]@{}
    foreach ($i in $script:SettingChecks.Keys) { $checks[$i] = Get-SettingCheck $i }
    $Problem = Get-OptimizeRequest $load $checks
}
if ($Mode -eq 'optimize' -and $AllowApply) {
    $script:DoctorToolSchemas += @{ type = 'function'; function = @{
        name = 'apply_change'
        description = 'Apply one settings change the review found needed. Checks again that it is needed, asks the user to confirm at the console, applies it and reports the value measured afterwards.'
        parameters = @{ type = 'object'; properties = @{ item = @{ type = 'string'; enum = @($script:SettingChecks.Keys | Where-Object { $script:SettingChecks[$_].Apply }); description = 'The item to change.' } }; required = @('item') }
    } }
}

# One keypress within $Seconds; Y applies, anything else or no key skips.
# Whole lines only: the installer pipes this script's output line by line.
function Read-ApplyConsent($Check, [int]$Seconds = 60) {
    if ($Quiet) { return $false }
    try { while ([Console]::KeyAvailable) { [void][Console]::ReadKey($true) } } catch { return $false }
    Write-Host ''
    Write-Host "The doctor wants to change $($Check.Item): $($Check.Apply)."
    Write-Host "  Now: $($Check.Current). Target: $($Check.Target)."
    Write-Host "  Press Y within $Seconds s to apply; any other key or no answer skips it."
    $until = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $until) {
        if ([Console]::KeyAvailable) {
            $yes = [Console]::ReadKey($true).KeyChar -in 'y', 'Y'
            Write-Host $(if ($yes) { '  Applying.' } else { '  Skipped.' })
            return $yes
        }
        Start-Sleep -Milliseconds 100
    }
    Write-Host '  No answer; skipped.'
    $false
}

# Measured items override whatever the model wrote about them; its fix keeps
# only the blocks for the items it judged (1, 2, 6, 9).
function Merge-OptimizeResult([string]$RootCause, [string]$Fix) {
    $final = [ordered]@{}
    foreach ($i in $script:SettingChecks.Keys) { $final[$i] = Get-SettingCheck $i }
    $modelLines = @{}
    foreach ($line in ($RootCause -split "`r?`n")) {
        if ($line -notmatch '^\s*(?:(\d+)\s*[.:)]\s*)?(?:([a-z][\w-]*)\s*:\s*)?(OK|CHANGE|UNKNOWN)\b\s*-?\s*(.*)$') { continue }
        $name = if ($Matches[2] -and $script:OptimizeItems -contains $Matches[2].ToLowerInvariant()) { $Matches[2].ToLowerInvariant() } elseif ($Matches[1]) { $script:OptimizeItems[[int]$Matches[1] - 1] } else { $null }
        if ($name) { $modelLines[$name] = @{ Status = $Matches[3]; Text = $Matches[4].Trim() } }
    }
    $rc = for ($n = 1; $n -le $script:OptimizeItems.Count; $n++) {
        $item = $script:OptimizeItems[$n - 1]
        if ($final.Contains($item)) {
            $c = $final[$item]
            $done = @($applied | Where-Object { $_.StartsWith("$($item):") })
            if ($done -and $c.Status -eq 'OK') { "$n. $($item): OK - changed by the doctor; now $($c.Current)" } else { "$n. $($item): $($c.Status) - $($c.Current)" }
        } elseif ($modelLines[$item]) {
            "$n. $($item): $($modelLines[$item].Status)$(if ($modelLines[$item].Text) { " - $($modelLines[$item].Text)" })"
        } else { "$n. $($item): UNKNOWN - not reported" }
    }
    $modelChanges = @($script:OptimizeItems | Where-Object { -not $final.Contains($_) -and $modelLines[$_] -and $modelLines[$_].Status -eq 'CHANGE' })
    $blocks = @()
    if ($modelChanges) {
        $cur = $null
        foreach ($line in ($Fix -split "`r?`n")) {
            if ($line -match '^\d+\.\s') { if ($cur) { $blocks += , $cur }; $cur = [System.Collections.Generic.List[string]]::new() }
            if ($null -ne $cur) { $cur.Add($line) }
        }
        if ($cur) { $blocks += , $cur }
        $blocks = @($blocks | Where-Object {
            $head = $_[0]
            $named = if ($head -match '^(\d+)\.\s*([a-z][\w-]*)\s*:') { if ($script:OptimizeItems -contains $Matches[2]) { $Matches[2] } else { $script:OptimizeItems[[int]$Matches[1] - 1] } } else { $null }
            if ($named) { $modelChanges -contains $named } else { $head -notmatch '(?i)power|sleep|hibernat|driver|disk|storage|graphics|gpu preference|unity-gpu' }
        } | ForEach-Object { ($_ -join "`n").TrimEnd() })
    }
    foreach ($c in $final.Values | Where-Object { $_.Status -eq 'CHANGE' }) {
        $n = [array]::IndexOf($script:OptimizeItems, $c.Item) + 1
        $text = "$n. $($c.Item): $($c.Steps)"
        if ($c.Improves) { $text += "`n   Improves: $($c.Improves)" }
        if ($c.Apply -and $AllowApply) { $text += "`n   Not applied: skipped at the prompt." }
        elseif ($c.Apply) { $text += "`n   The doctor can make this change for you (Doctor.ps1 -Mode optimize -AllowApply): $($c.Apply)." }
        $blocks += $text
    }
    @{ RootCause = ($rc -join "`n"); Fix = ($blocks -join "`n`n") }
}
if (-not $Problem.Trim()) { throw 'Describe the problem with -Problem or -ProblemFile.' }
if ($Mode -eq 'optimize') {
    $finishTool = ($script:DoctorToolSchemas | Where-Object { $_.function.name -eq 'finish' }).function
    $finishTool.description = 'Report the review. Call once every item is checked or known to be uncheckable.'
    $finishTool.parameters.properties.root_cause.description = 'One line per item: "<item>: OK", "<item>: CHANGE - <what and why>" or "<item>: UNKNOWN - <why>".'
    $finishTool.parameters.properties.evidence.description = 'The values you observed for each item.'
    $finishTool.parameters.properties.fix.description = 'Numbered recommended changes. Each: exact steps (settings to open, lines to put in .wslconfig, commands to run), then a line "Improves: <what gets better for training>". Empty if nothing needs to change.'
}
$Problem = Protect-Secrets $Problem

# Every investigative call says why it is made, so the user can follow along.
foreach ($t in $script:DoctorToolSchemas) {
    if ($t.function.name -eq 'finish') { continue }
    $t.function.parameters.properties.purpose = @{ type = 'string'; description = 'Always set: one short sentence for the user saying what this call checks and why.' }
}

function Write-Step([string]$Text, [string]$Color = '') {
    if ($Quiet) { return }
    if ($Color) { Write-Host $Text -ForegroundColor $Color } else { Write-Host $Text }
}

# One line of at most $Max characters; long text keeps its end, where a
# model's reasoning reaches its decision.
function Format-Brief([string]$Text, [int]$Max = 240) {
    $t = ($Text -replace '\s+', ' ').Trim()
    if ($t.Length -le $Max) { return $t }
    $tail = $t.Substring($t.Length - $Max)
    $cut = $tail.IndexOf('. ')
    if ($cut -ge 0 -and $cut -lt $Max / 2) { $tail = $tail.Substring($cut + 2) }
    "... $tail"
}

function Get-FirstFinding([string]$Out) {
    $line = @($Out -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -and $_ -notmatch '^\[exit 0\]$|^-+$' }) | Select-Object -First 1
    Format-Brief "$line" 160
}

# Keeps the conversation inside a 32K-token context (~3.5 characters per
# token, with room for the reply): the oldest tool outputs are cut to a stub.
function Compress-OldToolOutput($Messages, [int]$MaxChars = 80000) {
    $total = ($Messages | ForEach-Object { "$($_.content)".Length } | Measure-Object -Sum).Sum
    foreach ($m in $Messages) {
        if ($total -le $MaxChars) { break }
        $old = "$($m.content)"
        if ($m.role -ne 'tool' -or $old.Length -le 400 -or $old.EndsWith('[older output trimmed]')) { continue }
        $m.content = $old.Substring(0, 300) + "`n[older output trimmed]"
        $total -= ($old.Length - $m.content.Length)
    }
}

function Get-SystemPrompt {
    $services = if (Test-Path (Join-Path $RepoDir 'docker-compose.yml')) {
        ((Invoke-Docker ((Get-RepoComposeArgs) + @('config', '--services'))).Out.Trim() -split "`r?`n") -join ', '
    } else { "unknown: $RepoDir has no docker-compose.yml (the repo may not be cloned yet)" }
    $task = if ($Mode -eq 'optimize') {
        'The install works. Review the PC settings the user lists and recommend only changes that would make training faster or more reliable. Do not look for faults outside that list.'
    } else { 'Find the root cause of the problem the user reports.' }
    $conclude = if ($Mode -eq 'optimize') {
        "- Report each listed item as OK, CHANGE or UNKNOWN. Recommend a change only when you observed a value that calls for it.`n- Call finish once every item is covered. You have at most $MaxSteps tool calls."
    } else {
        "- The root cause is the underlying condition, not the symptom. When something is missing, check whether it is not configured at all or configured but not running, and say which.`n- Call finish as soon as the evidence supports a root cause. You have at most $MaxSteps tool calls."
    }
    @"
You are the InstallZero install doctor for rl-roboracer, a reinforcement-learning racing stack on a Windows PC. $task

How the stack works:
- Docker Desktop (WSL 2 backend) runs a docker compose project from $RepoDir with services: $services.
- sim-controller runs the trainer (robotaxi.py) and TensorBoard. ros-server (and ros-server-1..3 for extra clients) bridge to Unity over ROS-TCP on host ports 10000-10003. mongo holds jobs and settings. dashboard is the web UI on http://127.0.0.1 (port 80). fly-brain is a gRPC server that fly courses reach at fly-brain:50061. madscientist proposes experiments.
- Unity simulator clients run natively on Windows, each supervised by scripts\RunClientWrapper.ps1 in its own PowerShell window. They are started by scripts\Start-Stack.ps1 or scripts\Start-Clients.ps1, or at sign-in by scripts\install\Start-ClientAtLogon.ps1. Client N connects to 127.0.0.1:(10000+N); its ros-server dials back to host.docker.internal:(5005+N). A client must connect after its ros-server started.
- Unity builds live under unity\Builds (latest, and per-client copies in instances\N) and ..\UnityBinary.

How to work:
- Gather evidence before concluding: check what is actually running, read logs and configuration. Prefer one specific check over guessing.
$(if ($Mode -eq 'optimize' -and $AllowApply) { '- Every command is read-only. Settings change only through apply_change, for the items the user lists, and only after the user confirms. Put everything else in finish.' } else { '- You cannot change anything; every command is read-only. Put the fix in finish.' })
- Secret values (API keys, passwords) are never shown: tool output has them replaced by <secret:NAME> or <secret>, and .env shows only their length. That a value is masked tells you it is set; ask the user to check its content when that matters.
$conclude

This PC: $((Get-CimInstance Win32_OperatingSystem).Caption), user $env:USERNAME, $(Get-Date -Format 'yyyy-MM-dd HH:mm').
"@
}

$runId = Get-Date -Format 'yyyyMMdd-HHmmss'
$logDir = Join-Path $env:LOCALAPPDATA 'rl-roboracer\doctor'
New-Item -ItemType Directory -Force $logDir | Out-Null
$transcript = Join-Path $logDir "$runId.jsonl"
function Write-Transcript($Entry) { Add-Content -Path $transcript -Value ($Entry | ConvertTo-Json -Depth 20 -Compress) -Encoding UTF8 }

$server = $null
$result = [ordered]@{ run_id = $runId; mode = $Mode; problem = $Problem; model = $Model; backend = $Backend; steps = 0; tool_calls = 0; invalid_calls = 0; refused_commands = 0; nudges = 0; server_errors = 0; prompt_tokens = 0; completion_tokens = 0
    finished = $false; root_cause = ''; evidence = ''; fix = ''; confidence = ''; applied = $applied; seconds = 0; transcript = $transcript; error = '' }
$sw = [Diagnostics.Stopwatch]::StartNew()
try {
    $llm = $null
    if ($Remote) {
        $llm = Get-RemoteLlm $Remote
        $LlmUrl = $llm.Url
        $result.model = $Remote; $result.backend = 'remote'
    } elseif (-not $LlmUrl) {
        Write-Step 'Starting the local model...'
        $server = Start-LlmServer -Model $Model -Backend $Backend
        $LlmUrl = $server.Url
        $result.model = $server.Model; $result.backend = $server.Backend
        Write-Step "  $($server.Model) on $($server.Backend), ready in $($server.LoadSeconds)s"
    }

    $messages = [System.Collections.ArrayList]@(
        @{ role = 'system'; content = (Get-SystemPrompt) },
        @{ role = 'user'; content = $Problem }
    )
    Write-Transcript @{ type = 'start'; problem = $Problem; model = $result.model; url = $LlmUrl; system = $messages[0].content }

    $finish = $null; $lastText = ''
    while (-not $finish) {
        if ($sw.Elapsed.TotalMinutes -ge $TimeoutMinutes) { $result.error = "timed out after $TimeoutMinutes minutes"; break }
        if ($result.tool_calls -ge $MaxSteps) {
            if ($result.nudges -ge 3) { break }
            $result.nudges++
            Write-Step "  Out of tool calls: asking the doctor to conclude now (attempt $($result.nudges) of 3)." 'Yellow'
            [void]$messages.Add(@{ role = 'user'; content = 'You have used all your tool calls. Call finish now with your best diagnosis.' })
        }

        Compress-OldToolOutput $messages
        # llama-server answers 500 when it cannot parse the model's tool-call
        # syntax; a resample at a higher temperature usually parses. Hosted
        # APIs also answer 429/5xx when busy; those get a backoff.
        $r = $null
        $attempt = 0
        foreach ($temperature in 0.2, 0.6, 0.9, 0.9) {
            $attempt++
            try { $r = Invoke-ChatTurn $llm $LlmUrl $messages $script:DoctorToolSchemas $temperature 4096; break }
            catch {
                $err = "$_"
                $detail = try { $sr = New-Object IO.StreamReader($_.Exception.Response.GetResponseStream()); $sr.ReadToEnd() } catch { '' }
                if ("$err $detail" -notmatch '429|500|502|503|529|Internal Server Error|does not match|overloaded|rate limit') { throw "$err $detail" }
                $result.server_errors++
                Write-Transcript @{ type = 'server_error'; temperature = $temperature; error = "$err $detail" }
                if ($llm) { Start-Sleep -Seconds (10 * $attempt) }
            }
        }
        if (-not $r) { throw 'the model failed to produce a usable reply four times in a row' }
        $result.steps++
        if ($r.usage) { $result.prompt_tokens += [int]$r.usage.prompt_tokens; $result.completion_tokens += [int]$r.usage.completion_tokens }
        $msg = $r.choices[0].message
        $calls = if ($msg.PSObject.Properties.Name -contains 'tool_calls' -and $msg.tool_calls) { @($msg.tool_calls) } else { @() }
        $text = ConvertTo-PlainText "$($msg.content)"
        $reasoning = if ($msg.PSObject.Properties.Name -contains 'reasoning_content') { ConvertTo-PlainText "$($msg.reasoning_content)" } else { '' }
        Write-Transcript @{ type = 'assistant'; content = $text; reasoning = $reasoning; tool_calls = $calls; usage = $r.usage }
        $elapsed = $sw.Elapsed
        Write-Step ''
        Write-Step ("Step {0}: {1} of {2} checks used, {3:m\:ss} elapsed (stops at {4} min)" -f $result.steps, $result.tool_calls, $MaxSteps, $elapsed, $TimeoutMinutes) 'DarkCyan'
        if ($reasoning.Trim()) { Write-Step "  Thinking: $(Format-Brief $reasoning)" 'DarkGray' }
        if ($text.Trim() -and $calls) { Write-Step "  Says: $(Format-Brief $text)" 'DarkGray' }

        if (-not $calls) {
            # Some models write the finish call as JSON text instead of calling it.
            if ($text -match '(?s)\{.*"root_cause".*\}') {
                try { $finish = $Matches[0] | ConvertFrom-Json; Write-Transcript @{ type = 'finish_from_text' }; break } catch { }
            }
            if ($text.Trim()) { $lastText = $text }
            if ($result.nudges -ge 2) { break }
            $result.nudges++
            [void]$messages.Add(@{ role = 'assistant'; content = $text })
            [void]$messages.Add(@{ role = 'user'; content = 'Continue: use a tool to check something, or call finish with root_cause, evidence, fix and confidence.' })
            continue
        }

        $argJson = { param($raw) if ($raw -is [string]) { $raw } elseif ($null -eq $raw) { '{}' } else { $raw | ConvertTo-Json -Compress -Depth 10 } }
        # extra_content carries Gemini's thought signatures, which must come back unchanged.
        $echo = @($calls | ForEach-Object {
            $e = @{ id = $_.id; type = 'function'; function = @{ name = $_.function.name; arguments = (& $argJson $_.function.arguments) } }
            if ($_.PSObject.Properties.Name -contains 'extra_content') { $e.extra_content = $_.extra_content }
            $e
        })
        # Anthropic rejects empty text content alongside tool calls.
        $assistant = @{ role = 'assistant'; content = $(if ($llm -and -not $text.Trim()) { $null } else { $text }); tool_calls = $echo }
        if ($msg.PSObject.Properties.Name -contains '_responses_items') { $assistant._responses_items = $msg._responses_items }
        [void]$messages.Add($assistant)
        foreach ($c in $calls) {
            $result.tool_calls++
            $name = $c.function.name
            $raw = & $argJson $c.function.arguments
            $a = $null
            try { $a = if ($raw.Trim()) { $raw | ConvertFrom-Json } else { [pscustomobject]@{} } }
            catch { $result.invalid_calls++; $out = "error: arguments are not valid JSON: $raw" }
            if ($null -ne $a) {
                if ($name -eq 'finish') {
                    $finish = $a; $out = 'Recorded.'
                    Write-Step '  Concluding: writing up the findings.' 'Cyan'
                } else {
                    $purpose = if ($a.PSObject.Properties.Name -contains 'purpose') { "$($a.purpose)" } else { '' }
                    $shown = [ordered]@{}
                    foreach ($p in $a.PSObject.Properties) { if ($p.Name -ne 'purpose') { $shown[$p.Name] = $p.Value } }
                    $argText = if ($shown.Count) { $shown | ConvertTo-Json -Compress -Depth 5 } else { '' }
                    if ($purpose.Trim()) { Write-Step "  Checking: $(Format-Brief $purpose 200)" }
                    Write-Step ("    {0} {1}" -f $name, $(if ($argText.Length -gt 120) { $argText.Substring(0, 120) + '...' } else { $argText })) 'DarkGray'
                    if ($name -eq 'apply_change' -and $AllowApply) {
                        $offered.Add("$($a.item)")
                        $out = Invoke-SettingChange "$($a.item)" { param($c) Read-ApplyConsent $c }
                        if ($out.StartsWith('applied')) { $applied.Add("$($a.item): $out") }
                        Write-Step "    $out"
                    } else {
                        try { $out = Invoke-DoctorTool $name $a } catch { $out = "error: $_" }
                    }
                    if ($name -eq 'run_command' -and "$out".StartsWith('refused:')) { $result.refused_commands++ }
                    if ($out -match '^error: unknown tool') { $result.invalid_calls++ }
                }
            }
            $out = Limit-Text (ConvertTo-PlainText "$out")
            if ($name -ne 'finish' -and $name -ne 'apply_change') { Write-Step "    Found: $(Get-FirstFinding $out)" 'DarkGray' }
            Write-Transcript @{ type = 'tool'; name = $name; arguments = "$($c.function.arguments)"; output = $out }
            [void]$messages.Add(@{ role = 'tool'; tool_call_id = $c.id; content = $out })
        }
    }

    if ($finish) {
        $result.finished = $true
        $result.root_cause = ConvertTo-PlainText "$($finish.root_cause)"
        $result.evidence = ConvertTo-PlainText "$($finish.evidence)"
        $result.fix = ConvertTo-PlainText "$($finish.fix)"
        $result.confidence = "$($finish.confidence)"
    } elseif ($lastText) {
        $result.root_cause = $lastText
        $result.confidence = 'low'
    }
    # Changes the model did not offer are offered here, on the same terms.
    if ($checks -and $AllowApply) {
        foreach ($i in @($script:SettingChecks.Keys | Where-Object { $script:SettingChecks[$_].Apply -and $offered -notcontains $_ })) {
            if ((Get-SettingCheck $i).Status -ne 'CHANGE') { continue }
            $out = Invoke-SettingChange $i { param($c) Read-ApplyConsent $c }
            Write-Transcript @{ type = 'apply_fallback'; item = $i; output = $out }
            if ($out.StartsWith('applied')) { $applied.Add("$($i): $out") }
            Write-Step "    $out"
        }
    }
} catch {
    $result.error = "$_"
} finally {
    Stop-LlmServer $server
    # Even when the model failed, the measured items are reported.
    if ($checks) {
        $merged = Merge-OptimizeResult $result.root_cause $result.fix
        $result.root_cause = $merged.RootCause
        $result.fix = $merged.Fix
    }
    $result.seconds = [math]::Round($sw.Elapsed.TotalSeconds, 1)
    Write-Transcript @{ type = 'result'; result = $result }
}

if ($ResultFile) { $result | ConvertTo-Json -Depth 5 | Set-Content -Path $ResultFile -Encoding UTF8 }
if (-not $Quiet) {
    Write-Host ''
    if ($result.error) { Write-Host "Error: $($result.error)" -ForegroundColor Red }
    if ($Mode -eq 'optimize') {
        Write-Host "Findings ($($result.confidence)):" -ForegroundColor Cyan
        Write-Host $result.root_cause
        if ($applied.Count) { Write-Host "Changed by the doctor:`n$(($applied | ForEach-Object { "  $_" }) -join "`n")" -ForegroundColor Green }
        if ($result.fix) { Write-Host "Recommended changes (not applied):`n$($result.fix)" -ForegroundColor Green }
    } else {
        Write-Host "Root cause ($($result.confidence)): $($result.root_cause)" -ForegroundColor Cyan
        if ($result.evidence) { Write-Host "Evidence: $($result.evidence)" }
        if ($result.fix) { Write-Host "Fix: $($result.fix)" -ForegroundColor Green }
    }
    $how = if ($result.finished) { "Concluded after $($result.tool_calls) checks" }
           elseif ($result.error) { "Stopped without a conclusion ($($result.error)) after $($result.tool_calls) checks" }
           else { "Stopped without a conclusion: the check budget ($MaxSteps) ran out after $($result.nudges) reminders" }
    Write-Host "($how, $($result.seconds)s, transcript $transcript)"
}
if ($PassThru) { [pscustomobject]$result }
