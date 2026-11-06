<#
.SYNOPSIS
    Diagnoses an rl-roboracer install problem with a local open-weights model.

.DESCRIPTION
    Starts llama-server with a bundled model (or uses -LlmUrl), then lets the
    model investigate with read-only tools until it reports a root cause.
    Diagnosis only: nothing on the machine is changed. Every run is logged
    as JSON lines under %LOCALAPPDATA%\rl-roboracer\doctor\.

    See docs/install-doctor-plan.md and doctor/MODELS.md.

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
    # The project's checkout; defaults to the current folder.
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
    # Write the system prompt, first user message and tools as JSON and stop.
    [string]$PromptOut = '',
    [switch]$Quiet,
    [switch]$PassThru
)

$ErrorActionPreference = 'Stop'
if (-not $RepoDir) { $RepoDir = (Get-Location).Path }
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
    $applyText = if ($AllowApply -and $applyList) { Get-PromptMessage 'doctor_apply_offer' @{ items = (($applyList | ForEach-Object { "$($_.Item) ($($_.Apply))" }) -join '; ') } } else { '' }
    Get-Prompt 'doctor-optimize' @{ user_profile = $env:USERPROFILE; measured = $measured; apply_offer = $applyText; gpu_load = $GpuLoad }
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
    $script:DoctorToolSchemas += Get-PromptTool 'doctor/apply_change' @{ items = @($script:SettingChecks.Keys | Where-Object { $script:SettingChecks[$_].Apply }) }
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
    # Assigned into the existing finish tool, in this order: assignment moves
    # keys in the serialized request, and this is the order models saw.
    $finishTool = ($script:DoctorToolSchemas | Where-Object { $_.function.name -eq 'finish' }).function
    $review = (Get-PromptTool 'doctor/finish_optimize').function
    $finishTool.description = $review.description
    foreach ($p in 'root_cause', 'evidence', 'fix') { $finishTool.parameters.properties.$p.description = $review.parameters.properties.$p.description }
}
$Problem = Protect-Secrets $Problem

# Every investigative call says why it is made, so the user can follow along.
foreach ($t in $script:DoctorToolSchemas) {
    if ($t.function.name -eq 'finish') { continue }
    $t.function.parameters.properties.purpose = Get-PromptParameter 'purpose_doctor'
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
    Get-Prompt 'doctor' @{ optimize = $Mode -eq 'optimize'; apply = $Mode -eq 'optimize' -and $AllowApply; repo_dir = $RepoDir; services = $services; max_steps = $MaxSteps
        os = "$((Get-CimInstance Win32_OperatingSystem).Caption)"; user = $env:USERNAME; now = (Get-Date -Format 'yyyy-MM-dd HH:mm') }
}

if ($PromptOut) {
    [IO.File]::WriteAllText([IO.Path]::GetFullPath($PromptOut), (ConvertTo-Json ([ordered]@{ system = (Get-SystemPrompt); user = $Problem; tools = @($script:DoctorToolSchemas) }) -Depth 20), [Text.UTF8Encoding]::new($false))
    return
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
            [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'doctor_out_of_calls') })
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
            [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'doctor_continue') })
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
            catch { $result.invalid_calls++; $out = Get-PromptMessage 'doctor_invalid_json' @{ arguments = $raw } }
            if ($null -ne $a) {
                if ($name -eq 'finish') {
                    $finish = $a; $out = Get-PromptMessage 'recorded'
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
