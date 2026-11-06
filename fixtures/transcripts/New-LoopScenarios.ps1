<#
.SYNOPSIS
    Records scripted runs of the PowerShell agent loop as transcripts, which
    the Go loop must replay identically (internal/agentloop).

.DESCRIPTION
    Each scenario feeds Invoke-AgentLoop canned model replies and errors in
    place of a model, and canned tool outputs, to reach the paths real probe
    runs rarely take: server errors, unparseable replies, tool calls written
    as text, replies without calls, duplicate and excess calls, the budget
    running out, replies that keep looping. The options match the probe's,
    which the replay assumes. Writes scenario-<name>.jsonl here.
.PARAMETER Check
    Compare instead of write: exit code 1 when a transcript differs from what
    the loop records now.
#>
[CmdletBinding()]
param([switch]$Check)

$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot)
. (Join-Path $root 'doctor\Prompts.ps1')
. (Join-Path $root 'doctor\Llm.ps1')
. (Join-Path $root 'doctor\Tools.ps1')
. (Join-Path $root 'agent\AgentLoop.ps1')
$script:AgentQuiet = $true

# The model: the scenario's replies in order; a string is thrown as an error.
function Invoke-ChatTurn($Llm, [string]$Url, $Messages, $Tools, [double]$Temperature, [int]$MaxTokens, $Extra = $null, [switch]$Stream) {
    if ($script:At -ge $script:Replies.Count) { throw 'scenario: no more replies' }
    $next = $script:Replies[$script:At]; $script:At++
    if ($next -is [string]) { throw $next }
    $next
}
function Start-Sleep { }

function Reply([string]$Content = '', $Calls = @(), [string]$Reasoning = '', [int]$PromptTokens = 3000) {
    $msg = [pscustomobject]@{ role = 'assistant'; content = $Content }
    if ($Reasoning) { $msg | Add-Member reasoning_content $Reasoning }
    if (@($Calls).Count) { $msg | Add-Member tool_calls @($Calls) }
    [pscustomobject]@{ choices = @([pscustomobject]@{ message = $msg; finish_reason = 'stop' }); usage = [pscustomobject]@{ prompt_tokens = $PromptTokens; completion_tokens = 40 } }
}
$script:CallId = 0
function Call([string]$Name, $Arguments = @{}) {
    $script:CallId++
    $a = if ($Arguments -is [string]) { $Arguments } else { $Arguments | ConvertTo-Json -Compress -Depth 10 }
    [pscustomobject]@{ id = "call-$script:CallId"; type = 'function'; function = [pscustomobject]@{ name = $Name; arguments = $a } }
}
function Look([string]$Path) { Call 'list_dir' ([ordered]@{ path = $Path; purpose = "look at $Path" }) }
function Write-Doc([string]$Content) { Call 'write_install_md' ([ordered]@{ content = $Content; purpose = 'save the draft' }) }

$parseError = 'The remote server returned an error: (500) Internal Server Error. {"error":{"code":500,"message":"Failed to parse tool call arguments as JSON","type":"server_error"}}'
$exceedError = 'The remote server returned an error: (400) Bad Request. {"error":{"code":400,"message":"the request (40000 tokens) exceeds the available context size (32768 tokens), try increasing it","type":"exceed_context_size_error"}}'
$loopError = 'the reply started repeating itself ("- name: x - name: x" over and over) and was stopped'
$doc = "---`nname: app`n---`n`n## Step 1: Install`n`n" + ('Install the app. ' * 40)

$scenarios = [ordered]@{
    'text-calls' = @{ Turns = 8; Replies = @(
        (Reply "Let me look.`n<tool_call>`n<function=list_dir>`n<parameter=path>`n/opt/app`n</parameter>`n<parameter=depth>`n2`n</parameter>`n<parameter=all>`nTrue`n</parameter>`n</function>`n</tool_call>"),
        (Reply '' -Reasoning ('I will read it. <tool_call>{"name": "run_command", "arguments": {"command": "cat <a> && echo ''x''", "purpose": "read"}}</tool_call>')),
        (Reply ('Two at once: <tool_call>{"name": "list_dir", "arguments": "{\"path\": \"/srv\"}"}</tool_call> and <tool_call>{"name": "list_dir", "arguments": {"path": "/etc", "n": 1.5, "deep": {"a": [1, true, null]}}}</tool_call> done')),
        (Reply ('<tool_call><function=write_install_md><parameter=content>' + $doc + '</parameter></function></tool_call>')),
        (Reply '<tool_call>{"name": "finish", "arguments": {"summary": "done"}}</tool_call>')
    ) }
    'server-errors' = @{ Turns = 8; Replies = @(
        $parseError, $parseError, (Reply '' @(Look '/opt')),
        $exceedError, (Reply '' @(Write-Doc $doc) -PromptTokens 9000),
        'Unable to connect to the remote server', 'The underlying connection was closed: An unexpected error occurred on a receive.',
        (Reply '' @(Call 'finish' @{ summary = 'saved' }))
    ) }
    'model-errors' = @{ Turns = 8; Replies = @(
        (Reply '' @(Look '/opt')),
        'Unable to connect to the remote server', 'The remote server returned an error: (503) Server Unavailable.', 'Unable to connect to the remote server', 'Unable to connect to the remote server'
    ) }
    'fatal-error' = @{ Turns = 8; Replies = @(
        (Reply '' @(Look '/opt')),
        'The remote server returned an error: (401) Unauthorized.'
    ) }
    'idle' = @{ Turns = 10; Replies = @(
        (Reply 'Thinking about it.'), (Reply 'Still thinking.'), (Reply ('A long thought. ' * 40)), (Reply 'Done thinking.')
    ) }
    'finish-in-text' = @{ Turns = 10; Replies = @(
        (Reply 'Let me think.'),
        (Reply ('Here is my answer: {"summary": "installed", "status": "ok"} and that is all.'))
    ) }
    'repeats' = @{ Turns = 12; Guard = 1; Replies = @(
        (Reply '' @((Look '/a'), (Look '/a'), (Look '/b'), (Look '/c'), (Look '/d'), (Look '/e'), (Look '/f'), (Look '/g'), (Look '/h'), (Look '/i'), (Look '/j'), (Look '/k'), (Look '/l'))),
        (Reply '' @((Look '/a'), (Look '/b'), (Look '/c'), (Look '/d'), (Look '/e'))),
        (Reply '' @((Look '/a'), (Look '/b'), (Look '/c'), (Look '/d'), (Look '/e'))),
        (Reply '' @((Look '/a'), (Look '/b'), (Look '/c'), (Look '/d'), (Look '/e'))),
        (Reply '' @((Look '/A'), (Look '/b'), (Look '/c'), (Look '/d'), (Look '/e'))),
        (Reply '' @((Write-Doc $doc), (Call 'run_command' '{"command": "ls'), (Call 'run_command' ''), (Call 'bogus' @{ x = 1 }), (Call 'fail' @{ }))),
        (Reply '' @(Write-Doc $doc)),
        (Reply '' @(Call 'finish' @{ summary = 'early' })),
        (Reply '' @(Call 'finish' @{ summary = 'done' }))
    ) }
    'budget' = @{ Turns = 3; Replies = @(
        (Reply '' @(Look '/a')), (Reply '' @(Look '/b')), (Reply '' @(Look '/c')),
        (Reply '' @(Look '/d')), (Reply 'No calls this time.'), (Reply '' @(Look '/e'))
    ) }
    'loops-recover' = @{ Turns = 6; Replies = @(
        $loopError, $loopError, $loopError, $loopError,
        $loopError, (Reply '' @(Write-Doc $doc)),
        $loopError, $loopError, $loopError, $loopError,
        (Reply '' @(Call 'finish' @{ summary = 'done' }))
    ) }
    'loops-fail' = @{ Turns = 6; Replies = @(
        $loopError, $loopError, $loopError, $loopError,
        $loopError, $loopError, $loopError, $loopError,
        $loopError, $loopError, $loopError, $loopError
    ) }
    'hosted' = @{ Turns = 6; Hosted = $true; Replies = @(
        'The remote server returned an error: (529) Overloaded. {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}',
        (Reply '' @(Look '/opt')), (Reply 'Saving it.' @(Write-Doc $doc)), (Reply '' @(Call 'finish' @{ summary = 'done' }))
    ) }
}

$failed = 0
foreach ($name in $scenarios.Keys) {
    $sc = $scenarios[$name]
    $script:Replies = @($sc.Replies); $script:At = 0; $script:CallId = 0
    $script:Saved = $false; $script:GuardRejects = [int]$sc.Guard
    $file = Join-Path $PSScriptRoot "scenario-$name.jsonl"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) "iz-scenario-$PID-$name.jsonl"
    Remove-Item $tmp -ErrorAction SilentlyContinue
    $tools = @('list_dir', 'run_command', 'write_install_md', 'finish' | ForEach-Object {
        @{ type = 'function'; function = @{ name = $_; description = "The $_ tool."; parameters = @{ type = 'object'; properties = @{} } } } })
    $session = if ($sc.Hosted) { [pscustomobject]@{ Llm = [pscustomobject]@{ Provider = 'anthropic'; Model = 'scripted' }; Url = ''; Server = $null; Label = 'anthropic:scripted'; Context = 200000; Request = $null } }
               else { [pscustomobject]@{ Llm = $null; Url = ''; Server = $null; Label = 'scripted'; Context = 32768; Request = $null } }
    $dispatch = {
        param($n, $a)
        switch ($n) {
            'write_install_md' { $script:Saved = $true; "Saved: $("$($a.content)".Length) characters. Format OK." }
            'list_dir' { "listing of $($a.path)`n[exit 0]" }
            'run_command' { "ran $($a.command)" }
            'fail' { throw 'the tool failed' }
            default { "error: unknown tool '$n'" }
        }
    }
    $guard = { param($A, $Text) if ($script:GuardRejects -gt 0) { $script:GuardRejects--; return 'Not done yet: check the document once more.' }; $null }
    $system = "You are the InstallZero probe in a scripted test of the loop: $name (you have $($sc.Turns) turns)."
    $null = Invoke-AgentLoop -Session $session -System $system -User 'Probe this machine.' -Tools $tools -Dispatch $dispatch `
        -MaxTurns $sc.Turns -Transcript $tmp -MaxTokens 8192 -FinishGuard $guard -FinalTools @('write_install_md') -ResultSaved { $script:Saved } `
        -FinalNudge (Get-PromptMessage 'probe_final') -ParseFailureNudge (Get-PromptMessage 'probe_parse_failure') -UnchangedWriteHint (Get-PromptMessage 'probe_unchanged_hint')
    # Seconds vary from run to run.
    $lines = @(Get-Content $tmp -Encoding UTF8 | ForEach-Object { $_ -replace '"seconds":[0-9.]+', '"seconds":0' })
    Remove-Item $tmp
    $text = ($lines -join "`n") + "`n"
    if ($Check) {
        $old = if (Test-Path $file) { [IO.File]::ReadAllText($file) } else { '' }
        if ($old -ne $text) { Write-Host "differs: $file"; $failed++ }
    } else {
        [IO.File]::WriteAllText($file, $text, [Text.UTF8Encoding]::new($false))
        Write-Host "$name : $($lines.Count) events, $(($lines[-1] | ConvertFrom-Json).run.stop_reason)"
    }
}
if ($Check) { if ($failed) { exit 1 }; Write-Host "All $($scenarios.Count) scenario transcripts match." }
