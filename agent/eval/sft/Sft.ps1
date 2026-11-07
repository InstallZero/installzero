# Shared by the SFT scripts: reading probe transcripts, deciding whether a
# run is worth imitating, and replaying a run into per-turn training
# examples with the context the probe would have sent a model of a given
# context size.

. (Join-Path $PSScriptRoot '..\..\AgentLoop.ps1')

$script:SftUtf8 = [Text.UTF8Encoding]::new($false)
$script:SftRepeatMark = '[This is call '
$script:SftGuidanceMark = "`n`nLessons from probes of other projects"

function Read-SftTranscript([string]$Path) {
    foreach ($line in [IO.File]::ReadAllLines($Path, $script:SftUtf8)) {
        if (-not $line.Trim()) { continue }
        try { $line.TrimStart([char]0xFEFF) | ConvertFrom-Json } catch { }
    }
}

function Get-SftProvider($Row) {
    if ($Row.provider) { "$($Row.provider)" } elseif ("$($Row.model)" -match '^(\w+):') { $Matches[1] } else { 'local' }
}

# Reasons a run should not be imitated (empty: keep). $Events are its
# transcript events, $Result the run record from the transcript's last line.
function Get-SftRunProblems($Events, $Result, [int]$MaxRepeats) {
    $p = [Collections.Generic.List[string]]::new()
    if (@($Events | Where-Object type -eq 'start').Count -ne 1 -or -not $Result) { $p.Add('bad-transcript'); return @($p) }
    if (-not $Result.finished -or $Result.stop_reason -ne 'finished') { $p.Add('not-finished') }
    if ([int]$Result.nudges -gt 0) { $p.Add('nudged') }
    if (@($Events | Where-Object { $_.type -eq 'tool' -and $_.name -eq 'finish' -and "$($_.output)" -ne 'Recorded.' }).Count) { $p.Add('finish-rejected') }
    if ([int]$Result.invalid_calls -gt 0) { $p.Add('invalid-calls') }
    if ([int]$Result.repeats -ge $MaxRepeats) { $p.Add('looping') }
    @($p)
}

# Characters of conversation the probe keeps for a model with $ContextTokens
# of context, as Invoke-AgentLoop computes it (reply cap 8192 tokens).
function Get-SftMaxChars([int]$ContextTokens) {
    $reply = [math]::Min(8192, [int]($ContextTokens / 3))
    [math]::Min(400000, [math]::Max(12000, ($ContextTokens - $reply - 1500) * 3))
}

# Host-specific text out of training data: the user's profile path and name.
function Protect-SftText([string]$Text) {
    if (-not $Text) { return $Text }
    $t = $Text
    foreach ($p in @($env:USERPROFILE, ($env:USERPROFILE -replace '\\', '/'), ($env:USERPROFILE -replace '\\', '\\'))) { if ($p) { $t = $t.Replace($p, 'C:\Users\user') } }
    if ($env:USERNAME -and $env:USERNAME.Length -ge 4) { $t = [regex]::Replace($t, "(?i)\b$([regex]::Escape($env:USERNAME))\b", 'user') }
    # Where the harness saved the document (eval folder, run stamp) is not the model's business.
    [regex]::Replace($t, '(?i)[a-z]:[\\/][^\s"''<>|]*?[\\/][\w.-]*?\.install\.md', 'install.md')
}

function ConvertTo-SftArgs($Raw) {
    if ($Raw -is [string]) { $Raw } elseif ($null -eq $Raw) { '{}' } else { $Raw | ConvertTo-Json -Compress -Depth 20 }
}

# One run -> training examples. Each is the conversation as the probe would
# have sent it at that turn (oldest tool output trimmed, and whole turns
# dropped, to fit $MaxChars, exactly as Compress-AgentMessages does during a
# run) followed by the
# model's reply there. Replies whose calls repeated an earlier call stay in
# later context but are not targets.
# Returns @{ Examples = list of @{ Turn; Chars; Line (JSON) }; Skipped = @{ reason = n }; Turns }.
function ConvertTo-SftExamples($Events, [int]$MaxChars, [hashtable]$Meta, [switch]$IncludeReasoning) {
    $start = @($Events | Where-Object type -eq 'start')[0]
    $system = "$($start.system)"
    $cut = $system.IndexOf($script:SftGuidanceMark)
    if ($cut -ge 0) { $system = $system.Substring(0, $cut) }
    $messages = [Collections.ArrayList]@(
        @{ role = 'system'; content = (Protect-SftText $system) },
        @{ role = 'user'; content = (Protect-SftText "$($start.user)") })
    $examples = [Collections.Generic.List[object]]::new()
    $skipped = @{}
    $metaJson = ($Meta | ConvertTo-Json -Compress)
    $metaJson = $metaJson.Substring(1, $metaJson.Length - 2)

    $steps = [Collections.Generic.List[object]]::new()
    $cur = $null
    foreach ($e in $Events) {
        if ($e.type -eq 'assistant') { $cur = @{ A = $e; Tools = [Collections.Generic.List[object]]::new() }; $steps.Add($cur) }
        elseif ($e.type -eq 'tool' -and $cur) { $cur.Tools.Add($e) }
    }

    $turn = 0
    foreach ($s in $steps) {
        $turn++
        $a = $s.A
        $calls = @($a.tool_calls | Where-Object { $_ })
        $msg = @{ role = 'assistant'; content = $(if ("$($a.content)".Trim()) { Protect-SftText "$($a.content)" } else { $null }) }
        if ($IncludeReasoning -and "$($a.reasoning)".Trim()) { $msg.reasoning_content = Protect-SftText "$($a.reasoning)" }
        $i = 0
        if ($calls.Count) {
            $msg.tool_calls = @($calls | ForEach-Object {
                $i++
                $name = if ($_.function) { $_.function.name } else { $_.name }
                $argv = if ($_.function) { $_.function.arguments } else { $_.arguments }
                @{ id = "call_$($turn)_$i"; type = 'function'; function = @{ name = "$name"; arguments = (Protect-SftText (ConvertTo-SftArgs $argv)) } }
            })
        }

        Compress-AgentMessages $messages $MaxChars
        $why = if (-not $calls.Count) { 'no-tool-call' }
               elseif (@($s.Tools | Where-Object { "$($_.output)".Contains($script:SftRepeatMark) }).Count) { 'repeated-call' }
               elseif (@($s.Tools | Where-Object { "$($_.output)" -match '^error: (arguments are not valid JSON|unknown tool)' }).Count) { 'invalid-call' }
               else { $null }
        if ($why) { $skipped[$why] = 1 + [int]$skipped[$why] }
        else {
            $ctx = @($messages | ForEach-Object { $_ | ConvertTo-Json -Compress -Depth 20 }) -join ','
            $tgt = $msg | ConvertTo-Json -Compress -Depth 20
            $chars = (($messages | ForEach-Object { "$($_.content)".Length + (@($_.tool_calls) | ForEach-Object { "$($_.function.arguments)".Length } | Measure-Object -Sum).Sum }) | Measure-Object -Sum).Sum
            $examples.Add(@{ Turn = $turn; Chars = [int]$chars; Line = "{$metaJson,`"turn`":$turn,`"context_chars`":$chars,`"messages`":[$ctx,$tgt]}" })
        }

        [void]$messages.Add($msg)
        $k = 0
        foreach ($t in $s.Tools) {
            $k++
            $id = if ($k -le $msg.tool_calls.Count) { $msg.tool_calls[$k - 1].id } else { "call_$($turn)_$k" }
            [void]$messages.Add(@{ role = 'tool'; tool_call_id = $id; name = "$($t.name)"; content = (Protect-SftText "$($t.output)") })
        }
    }
    @{ Examples = $examples; Skipped = $skipped; Turns = $turn }
}
