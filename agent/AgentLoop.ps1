# The model loop shared by Agent.ps1 and Probe.ps1: tool calling with a
# visible trail (budget, condensed reasoning, purpose, finding), bounded by
# steps and time. Requires Llm.ps1 and Tools.ps1.

function Write-AgentLine([string]$Text, [string]$Color = '') {
    if ($script:AgentQuiet) { return }
    if ($Color) { Write-Host $Text -ForegroundColor $Color } else { Write-Host $Text }
}

function Format-AgentBrief([string]$Text, [int]$Max = 240) {
    $t = ($Text -replace '\s+', ' ').Trim()
    if ($t.Length -le $Max) { return $t }
    $tail = $t.Substring($t.Length - $Max)
    $cut = $tail.IndexOf('. ')
    if ($cut -ge 0 -and $cut -lt $Max / 2) { $tail = $tail.Substring($cut + 2) }
    "... $tail"
}

# Local llama-server, a given URL, or a hosted model ("provider:model-id").
function Start-AgentLlm([string]$Model, [string]$Backend, [string]$Remote, [string]$LlmUrl, [int]$Context = 32768) {
    if ($Remote) {
        $llm = Get-RemoteLlm $Remote
        # An hf: model is the local GGUF on a remote llama-server: keep the local context.
        $ctx = if ($llm.Provider -eq 'hf') { $Context } else { 200000 }
        $request = if ($llm.Provider -eq 'hf') { Get-LlmModelSettings $llm.Model } else { $null }
        return [pscustomobject]@{ Llm = $llm; Url = $llm.Url; Server = $null; Label = $Remote; Context = $ctx; Request = $request }
    }
    if ($LlmUrl) { return [pscustomobject]@{ Llm = $null; Url = $LlmUrl; Server = $null; Label = $LlmUrl; Context = $Context; Request = $null } }
    $assets = Find-DoctorAssets
    if (-not $Backend) { $Backend = Select-LlmBackend $assets }
    if (-not $Model) { $Model = Select-DoctorModel $assets $Backend }
    # A trainer sharing the GPU can leave too little memory for the model
    # plus its context; shrink the context first, fall back to CPU last.
    $modelPath = if (Test-Path $Model) { $Model } else { Join-Path $assets $Model }
    if ($Backend -ne 'cpu' -and (Test-Path $modelPath) -and (Get-Command nvidia-smi -ErrorAction SilentlyContinue)) {
        $freeMB = (@(nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits 2>$null | ForEach-Object { [int]"$_".Trim() }) | Measure-Object -Maximum).Maximum
        $modelMB = [int]((Get-Item $modelPath).Length / 1MB)
        $fit = foreach ($ctx in 32768, 16384, 8192) { if ($ctx -le $Context -and $freeMB -ge $modelMB + 512 + [int]($ctx / 24)) { $ctx; break } }
        if ($fit -and $fit -lt $Context) { Write-AgentLine "  Only $freeMB MB of GPU memory free (the model needs $modelMB MB): using a $fit-token context instead of $Context." 'Yellow'; $Context = $fit }
        elseif (-not $fit) { Write-AgentLine "  Only $freeMB MB of GPU memory free and the model needs $modelMB MB: running on the CPU (much slower). Pause GPU jobs, or use -Remote, for a faster run." 'Yellow'; $Backend = 'cpu' }
    }
    Write-AgentLine 'Starting the local model ...'
    $s = Start-LlmServer -Model $Model -Backend $Backend -Context $Context
    Write-AgentLine "  $($s.Model) on $($s.Backend), ready in $($s.LoadSeconds)s"
    [pscustomobject]@{ Llm = $null; Url = $s.Url; Server = $s; Label = "$($s.Model) ($($s.Backend))"; Context = $Context; Request = (Get-LlmModelSettings $modelPath) }
}

# One keypress within $Seconds: Y yes, anything else or nothing no.
function Read-AgentConsent([string]$Question, [string[]]$Details = @(), [int]$Seconds = 120) {
    try { while ([Console]::KeyAvailable) { [void][Console]::ReadKey($true) } } catch { return $false }
    Write-Host ''
    Write-Host $Question -ForegroundColor Yellow
    foreach ($d in $Details) { Write-Host "  $d" }
    Write-Host "  Press Y within $Seconds s to allow; any other key or no answer refuses."
    $until = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $until) {
        if ([Console]::KeyAvailable) {
            $yes = [Console]::ReadKey($true).KeyChar -in 'y', 'Y'
            Write-Host $(if ($yes) { '  Allowed.' } else { '  Refused.' })
            return $yes
        }
        Start-Sleep -Milliseconds 100
    }
    Write-Host '  No answer; refused.'
    $false
}

# Runs a changing command with its output streamed (long installs show
# progress), killing it after $TimeoutSec. Returns @{ Code; Out }.
function Invoke-StreamedCommand([string]$Command, [string]$Shell, [string]$WorkingDirectory, [int]$TimeoutSec = 1800) {
    $id = [guid]::NewGuid().ToString('N').Substring(0, 8)
    $outFile = Join-Path $env:TEMP "iz-$id.out"; $errFile = Join-Path $env:TEMP "iz-$id.err"
    if ($Shell -in 'bash', 'sh') {
        $bash = @('C:\Program Files\Git\bin\bash.exe', 'C:\Program Files (x86)\Git\bin\bash.exe') | Where-Object { Test-Path $_ } | Select-Object -First 1
        if (-not $bash) { return @{ Code = 127; Out = 'bash is not available on this PC (install Git for Windows, or use a powershell block)' } }
        $scriptFile = Join-Path $env:TEMP "iz-$id.sh"
        [IO.File]::WriteAllText($scriptFile, "set -e`n" + $Command.Replace("`r`n", "`n"))
        $p = Start-Process -FilePath $bash -ArgumentList @('-l', "`"$scriptFile`"") -WorkingDirectory $WorkingDirectory -RedirectStandardOutput $outFile -RedirectStandardError $errFile -PassThru -WindowStyle Hidden
    } else {
        $wrapped = "`$ErrorActionPreference = 'Stop'; `$ProgressPreference = 'SilentlyContinue'; [Console]::OutputEncoding = [Text.Encoding]::UTF8`n$Command`nif (`$LASTEXITCODE) { exit `$LASTEXITCODE }"
        $enc = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($wrapped))
        $p = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', $enc) -WorkingDirectory $WorkingDirectory -RedirectStandardOutput $outFile -RedirectStandardError $errFile -PassThru -WindowStyle Hidden
    }
    $handle = $p.Handle  # keeps ExitCode readable after exit
    $shown = 0; $deadline = (Get-Date).AddSeconds($TimeoutSec); $timedOut = $false
    while (-not $p.HasExited) {
        if ((Get-Date) -gt $deadline) { & taskkill.exe /PID $p.Id /T /F 2>&1 | Out-Null; $timedOut = $true; break }
        Start-Sleep -Seconds 3
        $lines = @(Get-Content $outFile -ErrorAction SilentlyContinue)
        if ($lines.Count -gt $shown) {
            $lines[$shown..($lines.Count - 1)] | Where-Object { $_.Trim() } | Select-Object -Last 3 | ForEach-Object { Write-AgentLine "      | $(Format-AgentBrief $_ 150)" 'DarkGray' }
            $shown = $lines.Count
        }
    }
    $p.WaitForExit()
    $out = ((Get-Content $outFile -Raw -ErrorAction SilentlyContinue) + $(if ((Get-Item $errFile -ErrorAction SilentlyContinue).Length) { "`n[stderr]`n" + (Get-Content $errFile -Raw) }))
    Remove-Item $outFile, $errFile -ErrorAction SilentlyContinue
    if ($Shell -in 'bash', 'sh') { Remove-Item (Join-Path $env:TEMP "iz-$id.sh") -ErrorAction SilentlyContinue }
    $code = if ($timedOut) { 124 } else { $p.ExitCode }
    @{ Code = $code; Out = $(if ($timedOut) { "[timed out after $TimeoutSec s]`n" } else { '' }) + "$out" }
}

# Tool calls a model wrote as text instead of structured calls, which
# llama-server sometimes leaves in the reply or the reasoning: Qwen's XML form
# <tool_call><function=name><parameter=key>value</parameter></function></tool_call>
# and the JSON form <tool_call>{"name": ..., "arguments": {...}}</tool_call>.
function ConvertFrom-TextToolCalls([string]$Text, [int]$Turn = 0) {
    $n = 0
    foreach ($m in [regex]::Matches($Text, '(?s)<tool_call>\s*(.*?)\s*</tool_call>')) {
        $body = $m.Groups[1].Value; $name = $null; $callArgs = [ordered]@{}
        if ($body -match '(?s)^<function=([\w.-]+)>(.*?)(</function>|$)') {
            $name = $Matches[1]
            foreach ($p in [regex]::Matches($Matches[2], '(?s)<parameter=([\w.-]+)>\r?\n?(.*?)\r?\n?</parameter>')) {
                $v = $p.Groups[2].Value
                $callArgs[$p.Groups[1].Value] = $(if ($v -match '^-?\d+$') { [int64]$v } elseif ($v -in 'true', 'false') { $v -eq 'true' } else { $v })
            }
        } else {
            try { $j = $body | ConvertFrom-Json; $name = $j.name; if ($j.arguments) { $callArgs = $j.arguments } } catch { }
        }
        if (-not $name) { continue }
        $n++
        [pscustomobject]@{ id = "text-$Turn-$n"; type = 'function'; function = [pscustomobject]@{ name = $name; arguments = ($callArgs | ConvertTo-Json -Compress -Depth 10) } }
    }
}

function Measure-AgentMessage($m) { "$($m.content)".Length + (@($m.tool_calls) | ForEach-Object { "$($_.function.arguments)".Length } | Measure-Object -Sum).Sum }
function Measure-AgentMessages($Messages) { ($Messages | ForEach-Object { Measure-AgentMessage $_ } | Measure-Object -Sum).Sum }

# llama-server parses the arguments of every earlier tool call when it builds
# the prompt, so one invalid argument string fails every later request.
function Test-AgentJson([string]$Text) {
    if (-not $Text.Trim()) { return $false }
    try { $null = $Text | ConvertFrom-Json; $true } catch { $false }
}

# Trims the oldest tool outputs (and long tool-call arguments, such as earlier
# drafts) until the conversation fits $MaxChars; the latest message is kept.
function Compress-AgentMessages($Messages, [int]$MaxChars = 80000, [int]$Keep = 300) {
    $size = { param($m) Measure-AgentMessage $m }
    $total = Measure-AgentMessages $Messages
    for ($i = 2; $i -lt $Messages.Count - 1 -and $total -gt $MaxChars; $i++) {
        $m = $Messages[$i]; $before = & $size $m
        if ($m.role -eq 'tool' -and "$($m.content)".Length -gt $Keep + 100) { $m.content = "$($m.content)".Substring(0, $Keep) + "`n[older output trimmed]" }
        foreach ($c in @($m.tool_calls)) {
            if ($c -and "$($c.function.arguments)".Length -gt 2000) { $c.function.arguments = (@{ trimmed = "earlier argument of $("$($c.function.arguments)".Length) characters removed to save space" } | ConvertTo-Json -Compress) }
        }
        $total -= ($before - (& $size $m))
    }
    if ($total -gt $MaxChars) { Remove-AgentTurns $Messages $MaxChars }
}

# Many small calls leave nothing to trim: drop the oldest whole turns (an
# assistant message and everything up to the next one, so no call loses its
# result) and list their calls after the task, the newest 40 of them.
# $Messages must be an ArrayList; the latest turn is kept.
function Remove-AgentTurns($Messages, [int]$MaxChars) {
    $header = Get-PromptMessage 'loop_dropped_turns'
    $more = (Get-PromptMessage 'loop_dropped_more' @{ count = '{{count}}' }) -split '\{\{count\}\}', 2
    $task = "$($Messages[1].content)"
    $lines = [Collections.Generic.List[string]]::new(); $notListed = 0
    $at = $task.IndexOf("`n`n$header", [StringComparison]::Ordinal)
    if ($at -ge 0) {
        foreach ($l in $task.Substring($at + 2 + $header.Length).Split("`n")) {
            if (-not $l) { continue }
            if ($l.Length -gt $more[0].Length + $more[1].Length -and $l.StartsWith($more[0], [StringComparison]::Ordinal) -and $l.EndsWith($more[1], [StringComparison]::Ordinal)) {
                $n = $l.Substring($more[0].Length, $l.Length - $more[0].Length - $more[1].Length)
                if ($n -cmatch '^[0-9]+$') { $notListed = [int]$n; continue }
            }
            $lines.Add($l)
        }
        $task = $task.Substring(0, $at)
    }
    while ((Measure-AgentMessages $Messages) -gt $MaxChars) {
        $end = 3
        while ($end -lt $Messages.Count -and $Messages[$end].role -ne 'assistant') { $end++ }
        if ($end -ge $Messages.Count) { break }
        $turn = $Messages.GetRange(2, $end - 2)
        foreach ($m in $turn) {
            foreach ($c in @($m.tool_calls | Where-Object { $_ })) {
                $out = @($turn | Where-Object { $_.role -eq 'tool' -and $_.tool_call_id -and $_.tool_call_id -ceq $c.id })
                $lines.Add((Format-AgentDroppedCall $c $(if ($out.Count) { $out[0].content } else { $null })))
            }
        }
        $Messages.RemoveRange(2, $end - 2)
        if ($lines.Count -gt 40) { $notListed += $lines.Count - 40; $lines.RemoveRange(0, $lines.Count - 40) }
        $block = @($header) + @(if ($notListed) { $more[0] + $notListed + $more[1] }) + @($lines)
        $Messages[1].content = $task + "`n`n" + ($block -join "`n")
    }
}

# One line per dropped call: name, arguments without the purpose, and the
# first line of its result.
function Format-AgentDroppedCall($Call, $Output) {
    $a = "$($Call.function.arguments)" -creplace ',[ \t\r\n]*"purpose"[ \t\r\n]*:[ \t\r\n]*"(?:[^"\\]|\\.)*"', '' -creplace '"purpose"[ \t\r\n]*:[ \t\r\n]*"(?:[^"\\]|\\.)*"[ \t\r\n]*,?', '' -creplace '[ \t\r\n]+', ' '
    $first = ''
    if ($null -ne $Output) { foreach ($l in "$Output".Split("`n")) { $t = $l.Trim([char[]]" `t`r"); if ($t) { $first = $t; break } } }
    $cut = { param([string]$s, [int]$max) if ($s.Length -le $max) { $s } else { $s.Substring(0, $max) + '...' } }
    "- $($Call.function.name) $(& $cut $a 150) -> $(if ($first) { & $cut $first 100 } else { '(no output)' })"
}

# $Dispatch: scriptblock ($name, $args) -> output text. Returns the run's
# record; .Finish holds the finish call's arguments when the model concluded.
# The budget is model turns (a turn may hold several parallel tool calls) and
# time; $MaxToolCalls only stops runaway parallel calling.
function Invoke-AgentLoop {
    param($Session, [string]$System, [string]$User, $Tools, [scriptblock]$Dispatch,
          [int]$MaxTurns = 40, [int]$MaxToolCalls = 0, [int]$TimeoutMinutes = 30, [string]$Transcript, [int]$MaxTokens = 4096, [string]$FinishName = 'finish',
          # ($finishArgs, $assistantText) -> $null to accept finish, or a message sending the model back to work.
          [scriptblock]$FinishGuard = $null,
          # ($run) -> a message to add before the next turn, or $null. Called every turn.
          [scriptblock]$Checkpoint = $null,
          # Tools still offered once the budget is spent, and what the model is told then.
          [string[]]$FinalTools = @(), [string]$FinalNudge = '',
          # () -> whether the result the final tools write exists. While it does
          # not, turns that force a result offer only the final tools, not finish.
          [scriptblock]$ResultSaved = $null,
          # Sent when replies keep coming back cut off or unparseable (a long
          # tool argument is the usual cause); only finish is offered then.
          [string]$ParseFailureNudge = (Get-PromptMessage 'loop_parse_failure'),
          # Identical calls within one reply run once; beyond this many, the rest are not run.
          [int]$MaxCallsPerTurn = 10,
          # Told to a model that re-sends its last result unchanged: what to look for instead.
          [string]$UnchangedWriteHint = (Get-PromptMessage 'loop_unchanged_hint'))
    if ($MaxToolCalls -le 0) { $MaxToolCalls = $MaxTurns * 5 }
    # Prompt plus reply must fit the context: cap the reply at a third of it
    # and trim old tool output so the conversation stays within the rest.
    # Characters per token start at 3 and are measured from each reply's
    # prompt_tokens (paths, JSON and command output run denser than prose).
    $context = if ($Session.Context) { [int]$Session.Context } else { 32768 }
    $MaxTokens = [math]::Min($MaxTokens, [int]($context / 3))
    $charsPerToken = 3.0
    $budgetChars = { [math]::Min(400000, [math]::Max(12000, [int](($context - $MaxTokens - 1500) * $charsPerToken))) }
    $maxChars = & $budgetChars
    # A model on llama-server (local or on Hugging Face) gets its cut-off or
    # oversized requests trimmed and shortened rather than resent unchanged.
    $llamaServer = -not $Session.Llm -or $Session.Llm.Provider -eq 'hf'
    if (-not $FinalNudge) { $FinalNudge = Get-PromptMessage 'loop_final' @{ finish = $FinishName } }
    $finalSet = @($Tools | Where-Object { $_.function.name -eq $FinishName -or $FinalTools -contains $_.function.name })
    $writeSet = @($Tools | Where-Object { $_.function.name -ne $FinishName -and $FinalTools -contains $_.function.name })
    $resultSet = { if ($writeSet.Count -and $ResultSaved -and -not (& $ResultSaved)) { $writeSet } else { $finalSet } }
    $loopTurns = 0
    $sameRuns = @{}; $guardRejections = 0; $lastLongText = ''; $lastErr = ''; $idleTurns = 0; $finalNudges = 0
    $lastWriteKey = ''; $unchangedWrites = 0; $script:LlmLoopPartial = $null
    foreach ($t in $Tools) {
        if ($t.function.name -eq $FinishName) { continue }
        $t.function.parameters.properties.purpose = Get-PromptParameter 'purpose'
    }
    # UTF-8 without a BOM, as the Go engine writes (Add-Content -Encoding UTF8 starts the file with one).
    if ($Transcript) { $Transcript = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Transcript) }
    $utf8 = [Text.UTF8Encoding]::new($false)
    $log = { param($e) if ($Transcript) { [IO.File]::AppendAllText($Transcript, ($e | ConvertTo-Json -Depth 20 -Compress) + "`n", $utf8) } }
    $run = [ordered]@{ steps = 0; tool_calls = 0; invalid_calls = 0; repeats = 0; blocked = 0; loops_stopped = 0; nudges = 0; server_errors = 0; prompt_tokens = 0; completion_tokens = 0; finished = $false; stop_reason = ''; error = ''; seconds = 0; last_long_text = ''; Finish = $null }
    $blockedNoted = 0; $forceResult = $false
    # A run about to end with no result saved keeps the document from a reply
    # stopped for looping, if there was one.
    $salvage = {
        $p = $script:LlmLoopPartial
        if (-not ($p -and $ResultSaved -and -not (& $ResultSaved) -and $writeSet.function.name -contains $p.name)) { return }
        $doc = Get-LoopSalvage $p
        if (-not $doc) { return }
        $out = try { & $Dispatch $p.name ([pscustomobject]@{ content = $doc }) } catch { "error: $_" }
        & $log @{ type = 'salvaged'; name = $p.name; chars = $doc.Length; output = (Limit-Text (ConvertTo-PlainText "$out")) }
        Write-AgentLine "  Saved the document from a reply stopped for looping ($($doc.Length) characters, repeated tail removed)." 'Yellow'
    }
    $messages = [System.Collections.ArrayList]@(@{ role = 'system'; content = $System }, @{ role = 'user'; content = $User })
    & $log @{ type = 'start'; model = $Session.Label; system = $System; user = $User }
    $sw = [Diagnostics.Stopwatch]::StartNew()
    try {
        while (-not $run.Finish) {
            if ($sw.Elapsed.TotalMinutes -ge $TimeoutMinutes) { $run.error = "timed out after $TimeoutMinutes minutes"; $run.stop_reason = 'timeout'; break }
            $toolsNow = $Tools; $budgetOut = $false
            if ($Checkpoint -and $run.steps -gt 0) {
                $note = & $Checkpoint $run
                if ($note) { Write-AgentLine "  Note to the model: $(Format-AgentBrief (@($note -split "`n")[0]) 200)" 'Yellow'; [void]$messages.Add(@{ role = 'user'; content = $note }) }
            }
            if ($run.steps -ge $MaxTurns -or $run.tool_calls -ge $MaxToolCalls) {
                if ($finalNudges -ge 3) { & $salvage; $run.stop_reason = 'budget'; break }
                $finalNudges++; $run.nudges++
                $toolsNow = @(& $resultSet); $budgetOut = $true
                Write-AgentLine "  Budget spent: asking for the result now, offering only $(($toolsNow.function.name) -join ' and ') (attempt $finalNudges of 3)." 'Yellow'
                [void]$messages.Add(@{ role = 'user'; content = $FinalNudge })
            } elseif ($forceResult) {
                $toolsNow = @(& $resultSet)
                Write-AgentLine "  Asking for the result: offering only $(($toolsNow.function.name) -join ' and ') for one turn." 'Yellow'
            }
            $forceResult = $false
            $maxChars = & $budgetChars
            Compress-AgentMessages $messages $maxChars
            $r = $null; $attempt = 0; $parseFails = 0; $loopNoted = $false; $loopFails = 0
            foreach ($temperature in 0.2, 0.6, 0.9, 0.9) {
                $attempt++
                try { $r = Invoke-ChatTurn $Session.Llm $Session.Url $messages $toolsNow $temperature $MaxTokens $Session.Request -Stream:$llamaServer; break }
                catch {
                    $err = "$_"; $lastErr = $err
                    if ($err -match 'the reply started repeating itself') {
                        $run.loops_stopped++; $loopFails++
                        & $log @{ type = 'loop_stopped'; temperature = $temperature; error = $err }
                        Write-AgentLine "  Stopped a looping reply (attempt $attempt of 4): $(Format-AgentBrief $err 160)" 'Yellow'
                        if (-not $loopNoted) {
                            $loopNoted = $true
                            [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'loop_repeating') })
                        }
                        continue
                    }
                    if ($err -notmatch '429|500|502|503|529|Internal Server Error|does not match|overloaded|rate limit|out of memory|failed to|context|could not be resolved|Unable to connect|connection was closed|underlying connection') { throw }
                    $run.server_errors++
                    & $log @{ type = 'server_error'; temperature = $temperature; error = $err }
                    Write-AgentLine "  Model error (attempt $attempt of 4): $(Format-AgentBrief $err 200)" 'Yellow'
                    if ($Session.Server -and $Session.Server.Process.HasExited) { throw "llama-server exited (see $($Session.Server.Log)); last error: $err" }
                    # A local reply that llama-server cannot parse was cut off or
                    # malformed: resending the same request repeats it. Make room
                    # first, then ask for a short reply without long tool arguments.
                    if ($llamaServer -and $err -match '"code"\s*:\s*500|server_error|parse|exceed') {
                        $parseFails++
                        if ($err -match 'request \((\d+) tokens\) exceeds') {
                            $charsPerToken = [math]::Max(1.0, [math]::Min($charsPerToken, (Measure-AgentMessages $messages) / [int]$Matches[1]))
                            $maxChars = & $budgetChars
                        }
                        if ($parseFails -eq 1) {
                            Write-AgentLine '  The reply was cut off or unparseable: trimming the conversation to make room and retrying.' 'Yellow'
                            Compress-AgentMessages $messages ([int]($maxChars / 2)) 150
                        } elseif ($parseFails -eq 2) {
                            Write-AgentLine '  Still unparseable: asking for a short reply, with only finish offered.' 'Yellow'
                            [void]$messages.Add(@{ role = 'user'; content = $ParseFailureNudge })
                            $toolsNow = @($Tools | Where-Object { $_.function.name -eq $FinishName })
                        }
                    } elseif ($Session.Llm) { Start-Sleep -Seconds (10 * $attempt) }
                }
            }
            # A turn whose every attempt looped costs a turn, not the run: the
            # next asks for a compact result (up to 3 such turns in a row).
            if (-not $r -and $loopFails -eq $attempt -and $loopTurns -lt 2) {
                $loopTurns++; $run.steps++; $forceResult = $true
                Write-AgentLine "  Every reply this turn looped; asking for a compact result next ($loopTurns of 3)." 'Yellow'
                [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'loop_keep_repeating' @{ tools = (((& $resultSet).function.name) -join ' or ') }) })
                continue
            }
            if ($r) { $loopTurns = 0 }
            if (-not $r) { & $salvage; $run.stop_reason = 'model-errors'; throw "the model failed to produce a usable reply four times in a row; last error: $(Format-AgentBrief $lastErr 400)" }
            $run.steps++
            if ($r.usage) { $run.prompt_tokens += [int]$r.usage.prompt_tokens; $run.completion_tokens += [int]$r.usage.completion_tokens }
            if ($r.usage -and [int]$r.usage.prompt_tokens -gt 4000) {
                $charsPerToken = [math]::Max(1.0, [math]::Min(4.0, (Measure-AgentMessages $messages) / [int]$r.usage.prompt_tokens))
            }
            $msg = $r.choices[0].message
            $calls = if ($msg.PSObject.Properties.Name -contains 'tool_calls' -and $msg.tool_calls) { @($msg.tool_calls) } else { @() }
            $text = ConvertTo-PlainText "$($msg.content)"
            $reasoning = if ($msg.PSObject.Properties.Name -contains 'reasoning_content') { ConvertTo-PlainText "$($msg.reasoning_content)" } else { '' }
            if (-not $calls) {
                $calls = @(ConvertFrom-TextToolCalls "$($msg.content)`n$(if ($msg.PSObject.Properties.Name -contains 'reasoning_content') { $msg.reasoning_content })" $run.steps)
                if ($calls) { $text = ($text -replace '(?s)<tool_call>.*?(</tool_call>|$)', '').Trim() }
            }
            & $log @{ type = 'assistant'; content = $text; reasoning = $reasoning; tool_calls = @($calls | Where-Object { $_ }); usage = $r.usage }
            if ($text.Length -gt 500) { $lastLongText = $text; $run.last_long_text = $text }
            Write-AgentLine ''
            Write-AgentLine ("Turn {0} of {1}: {2} tool calls so far, {3:h\:mm\:ss} elapsed (stops at {4} min)" -f $run.steps, $MaxTurns, $run.tool_calls, $sw.Elapsed, $TimeoutMinutes) 'DarkCyan'
            if ($reasoning.Trim()) { Write-AgentLine "  Thinking: $(Format-AgentBrief $reasoning)" 'DarkGray' }
            if ($text.Trim()) { Write-AgentLine "  Says: $(Format-AgentBrief $text)" 'DarkGray' }
            if (-not $calls) {
                if ($text -match '(?s)\{.*"(summary|status)".*\}') { try { $run.Finish = $Matches[0] | ConvertFrom-Json; break } catch { } }
                if ($idleTurns -ge 3) { $run.stop_reason = $(if ($budgetOut) { 'budget' } else { 'no-tool-calls' }); break }
                $idleTurns++; $run.nudges++
                [void]$messages.Add(@{ role = 'assistant'; content = $text })
                [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'loop_continue' @{ finish = $FinishName }) })
                continue
            }
            $idleTurns = 0
            $argJson = { param($izRaw) if ($izRaw -is [string]) { $izRaw } elseif ($null -eq $izRaw) { '{}' } else { $izRaw | ConvertTo-Json -Compress -Depth 10 } }
            $calls = @($calls)
            $unique = [ordered]@{}
            foreach ($c in $calls) { $k = "$($c.function.name)|$(& $argJson $c.function.arguments)"; if (-not $unique.Contains($k)) { $unique[$k] = $c } }
            $duplicates = $calls.Count - $unique.Count
            $calls = @($unique.Values)
            $overCap = [math]::Max(0, $calls.Count - $MaxCallsPerTurn)
            if ($overCap) { $calls = @($calls | Select-Object -First $MaxCallsPerTurn) }
            if ($duplicates -or $overCap) { Write-AgentLine "  Dropped $duplicates duplicate and $overCap excess calls from this reply." 'Yellow' }
            $echo = @($calls | ForEach-Object {
                $a = & $argJson $_.function.arguments
                $e = @{ id = $_.id; type = 'function'; function = @{ name = $_.function.name; arguments = $(if (Test-AgentJson $a) { $a } else { '{}' }) } }
                if ($_.PSObject.Properties.Name -contains 'extra_content') { $e.extra_content = $_.extra_content }
                $e
            })
            $assistant = @{ role = 'assistant'; content = $(if ($Session.Llm -and -not $text.Trim()) { $null } else { $text }); tool_calls = $echo }
            if ($msg.PSObject.Properties.Name -contains '_responses_items') { $assistant._responses_items = $msg._responses_items }
            [void]$messages.Add($assistant)
            foreach ($izCall in $calls) {
                $run.tool_calls++
                $izName = $izCall.function.name
                $izRaw = & $argJson $izCall.function.arguments
                $izArgs = $null
                try { $izArgs = if ($izRaw.Trim()) { $izRaw | ConvertFrom-Json } else { [pscustomobject]@{} } } catch {
                    $run.invalid_calls++
                    $izOut = Get-PromptMessage 'loop_invalid_json' @{ chars = $izRaw.Length; name = $izName; start = (Format-AgentBrief $izRaw.Substring(0, [math]::Min(300, $izRaw.Length)) 300) }
                }
                if ($null -ne $izArgs) {
                    $guard = if ($izName -eq $FinishName -and $FinishGuard -and $guardRejections -lt 2) { & $FinishGuard $izArgs $lastLongText } else { $null }
                    if ($guard) {
                        $guardRejections++; $izOut = $guard
                        if ($writeSet.Count -and $ResultSaved -and -not (& $ResultSaved)) { $forceResult = $true }
                        Write-AgentLine "  Not done yet: $(Format-AgentBrief (@($guard -split "`n")[0]) 200)" 'Yellow'
                    } elseif ($izName -eq $FinishName) {
                        $run.Finish = $izArgs; $izOut = Get-PromptMessage 'recorded'
                        Write-AgentLine '  Concluding.' 'Cyan'
                    } else {
                        $purpose = if ($izArgs.PSObject.Properties.Name -contains 'purpose') { "$($izArgs.purpose)" } else { '' }
                        $shown = [ordered]@{}
                        foreach ($izProp in $izArgs.PSObject.Properties) { if ($izProp.Name -notin 'purpose', 'content') { $shown[$izProp.Name] = $izProp.Value } }
                        $argText = if ($shown.Count) { $shown | ConvertTo-Json -Compress -Depth 5 } else { '' }
                        if ($purpose.Trim()) { Write-AgentLine "  Doing: $(Format-AgentBrief $purpose 200)" }
                        Write-AgentLine ("    {0} {1}" -f $izName, $(if ($argText.Length -gt 140) { $argText.Substring(0, 140) + '...' } else { $argText })) 'DarkGray'
                        # A call that returned the same result three times in a row
                        # is not run again (a changed result, as after a fix, resets it).
                        $keyArgs = [ordered]@{}
                        foreach ($izProp in $izArgs.PSObject.Properties) { if ($izProp.Name -ne 'purpose') { $keyArgs[$izProp.Name] = $izProp.Value } }
                        $argKey = "$izName|$($keyArgs | ConvertTo-Json -Compress -Depth 10)"
                        $same = $sameRuns[$argKey]
                        # Re-saving the document unchanged reruns its live checks, whose output
                        # varies, so the guard below never sees it as a repeat; small models
                        # can spend the rest of the run doing it.
                        $isWrite = $writeSet.function.name -contains $izName
                        if ($budgetOut -and $finalSet.function.name -notcontains $izName) { $izOut = Get-PromptMessage 'loop_budget_spent' @{ tools = (($finalSet.function.name) -join ' and ') } }
                        elseif ($isWrite -and $argKey -eq $lastWriteKey) {
                            $run.repeats++; $run.blocked++; $unchangedWrites++
                            $left = [math]::Max(0, $MaxTurns - $run.steps)
                            $izOut = Get-PromptMessage 'loop_unchanged_write' @{ name = $izName; left = $left; max_turns = $MaxTurns; hint = $UnchangedWriteHint; finish = $FinishName; count = $unchangedWrites + 1 }
                            if (($unchangedWrites -ge 4 -or ($unchangedWrites -ge 2 -and $left -le 3)) -and $ResultSaved -and (& $ResultSaved)) { $run.stop_reason = 'unchanged-writes' }
                        }
                        elseif ($same -and $same.n -ge 3) {
                            $run.repeats++; $run.blocked++
                            $izOut = Get-PromptMessage 'loop_blocked_call' @{ count = $same.n }
                        } else {
                            try { $izOut = & $Dispatch $izName $izArgs } catch { $izOut = "error: $_" }
                            if ($isWrite) { $lastWriteKey = $argKey; $unchangedWrites = 0 }
                            if ("$izOut" -match '^error: unknown tool') { $run.invalid_calls++ }
                            $hash = "$izOut".GetHashCode()
                            if ($same -and $same.hash -eq $hash) { $same.n++ } else { $same = @{ hash = $hash; n = 1 }; $sameRuns[$argKey] = $same }
                            if ($same.n -ge 2) { $run.repeats++; $izOut = "$izOut" + (Get-PromptMessage 'loop_same_result' @{ count = $same.n; last = $same.n -ge 3 }) }
                        }
                        $first = @("$izOut" -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -and $_ -notmatch '^\[exit 0\]$|^-+$' }) | Select-Object -First 1
                        Write-AgentLine "    Result: $(Format-AgentBrief "$first" 160)" 'DarkGray'
                    }
                }
                $izOut = Limit-Text (ConvertTo-PlainText "$izOut")
                & $log @{ type = 'tool'; name = $izName; arguments = $izRaw; output = $izOut }
                [void]$messages.Add(@{ role = 'tool'; tool_call_id = $izCall.id; content = $izOut })
            }
            if ($run.stop_reason -eq 'unchanged-writes') { Write-AgentLine '  The model keeps re-saving the same document: ending the run with it.' 'Yellow'; break }
            if ($duplicates -or $overCap) {
                $notRun = @()
                if ($duplicates) { $notRun += Get-PromptMessage 'loop_not_run_duplicates' @{ count = $duplicates } }
                if ($overCap) { $notRun += Get-PromptMessage 'loop_not_run_over_cap' @{ count = $overCap; max = $MaxCallsPerTurn } }
                $messages[$messages.Count - 1].content += Get-PromptMessage 'loop_not_run' @{ calls = ($notRun -join ' and ') }
            }
            # A model that keeps re-sending blocked calls is told to move on at 5,
            # and at every 10 gets one turn with only the result tools.
            if ($run.blocked -ge 5 -and $blockedNoted -lt 5) {
                $blockedNoted = 5
                [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'loop_blocked_move_on' @{ count = $run.blocked; tools = (($finalSet.function.name) -join ' or ') }) })
            }
            if ($run.blocked -ge $blockedNoted + 10 -or ($run.blocked -ge 10 -and $blockedNoted -lt 10)) {
                $blockedNoted = 10 * [math]::Floor($run.blocked / 10)
                $forceResult = $true
                [void]$messages.Add(@{ role = 'user'; content = (Get-PromptMessage 'loop_blocked_result_only' @{ count = $run.blocked; tools = (($finalSet.function.name) -join ' and ') }) })
            }
        }
        $run.finished = [bool]$run.Finish
        if ($run.finished) { $run.stop_reason = 'finished' }
    } catch { $run.error = "$_" }
    $run.seconds = [math]::Round($sw.Elapsed.TotalSeconds, 1)
    & $log @{ type = 'result'; run = $run }
    if (-not $run.stop_reason) { $run.stop_reason = 'error' }
    $run.last_long_text = $lastLongText
    $how = if ($run.finished) { "Concluded after $($run.steps) turns, $($run.tool_calls) tool calls" } elseif ($run.error) { "Stopped without a conclusion ($($run.error))" } elseif ($run.stop_reason -eq 'no-tool-calls') { "Stopped without a conclusion: three replies in a row had no usable tool call" } elseif ($run.stop_reason -eq 'unchanged-writes') { "Stopped without a conclusion: the model kept re-saving the same result" } else { "Stopped without a conclusion: the budget ($MaxTurns turns) ran out" }
    Write-AgentLine ''
    Write-AgentLine "($how, $($run.seconds)s$(if ($Transcript) { ", transcript $Transcript" }))"
    [pscustomobject]$run
}
