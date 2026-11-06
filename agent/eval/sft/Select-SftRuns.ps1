<#
.SYNOPSIS
    Step 1 of the SFT training set: picks the probe runs whose trajectories
    are worth imitating and writes one line per candidate, kept or not, with
    the reasons.

.DESCRIPTION
    Candidates are scored results from the eval console that delivered an
    install.md with at least -MinRecall of the facts, on the current probe
    prompt (-PromptVersion, guidance suffixes included), whose raw transcript
    is on this host. Console transcripts are clipped, so only the raw
    AgentLoop.ps1 JSONL is used.

    A candidate is dropped when its transcript shows the model needed the
    harness to get it to the end or went in circles:
      bad-transcript   not exactly one start event, or no result event
      not-finished     the run did not end by calling finish
      nudged           the loop had to prompt it (a reply with no tool call,
                       or the turn budget ran out and it was told to write up)
      finish-rejected  finish was refused (no install.md saved, unreplayable
                       format, or the document was only in a message)
      invalid-calls    a tool call with unparseable arguments or an unknown tool
      looping          -MaxRepeats or more calls repeating an earlier call and result
    Server errors that were retried are kept: the model never saw them.

    Which providers' replies may be used as targets is step 2; this step
    keeps every model and records its provider.

.EXAMPLE
    .\Select-SftRuns.ps1
.EXAMPLE
    .\Select-SftRuns.ps1 -MinRecall 0.9 -Out D:\sft\runs.jsonl
#>
param(
    [string]$Server = 'http://127.0.0.1:81',
    [double]$MinRecall = 0.8,
    [string]$PromptVersion = 'probe-5',
    [int]$MaxRepeats = 3,
    [string]$Out = (Join-Path $env:LOCALAPPDATA 'installzero\sft\runs.jsonl')
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Sft.ps1')
$utf8 = $script:SftUtf8

$all = @(Invoke-RestMethod "$Server/api/results?limit=100000" -TimeoutSec 300 | ForEach-Object { $_ })
$candidates = @($all | Where-Object {
    $_.delivered -and $_.recall -ge $MinRecall -and ("$($_.prompt_version)" -eq $PromptVersion -or "$($_.prompt_version)" -like "$PromptVersion+*")
})
$seenPaths = @{}
$rows = foreach ($r in $candidates) {
    $reasons = [Collections.Generic.List[string]]::new()
    $path = "$($r.transcript_path)"
    $res = $null; $turns = 0; $toolCalls = 0
    if (-not $path -or -not [IO.File]::Exists($path)) { $reasons.Add('no-raw-transcript') }
    elseif ($seenPaths[$path]) { $reasons.Add("duplicate-of:$($seenPaths[$path])") }
    else {
        $seenPaths[$path] = $r._id
        $ev = @(Read-SftTranscript $path)
        $res = (@($ev | Where-Object type -eq 'result') | Select-Object -Last 1).run
        foreach ($p in (Get-SftRunProblems $ev $res $MaxRepeats)) { $reasons.Add($p) }
        if ($res) { $turns = [int]$res.steps; $toolCalls = [int]$res.tool_calls }
    }
    [pscustomobject][ordered]@{
        result_id = $r._id; keep = ($reasons.Count -eq 0); reasons = @($reasons)
        repo = $r.repo; model = $r.model; provider = (Get-SftProvider $r)
        recall = $r.recall; prompt_version = $r.prompt_version; guidance = $(if ($r.guidance) { $r.guidance.id } else { $null })
        campaign = $r.campaign; dataset = $r.dataset; run_at = $r.run_at
        turns = $turns; tool_calls = $toolCalls; server_errors = $(if ($res) { [int]$res.server_errors } else { 0 }); repeats = $(if ($res) { [int]$res.repeats } else { 0 })
        transcript = $path; install_md = $r.install_md
    }
}
$rows = @($rows)

New-Item -ItemType Directory -Force (Split-Path $Out) | Out-Null
[IO.File]::WriteAllLines($Out, [string[]]@($rows | ForEach-Object { $_ | ConvertTo-Json -Compress -Depth 5 }), $utf8)

$kept = @($rows | Where-Object keep)
Write-Host "$($all.Count) results; $($rows.Count) candidates (delivered, recall >= $MinRecall, $PromptVersion); $($kept.Count) kept from $(@($kept.repo | Sort-Object -Unique).Count) repos, $(($kept | Measure-Object turns -Sum).Sum) assistant turns."
Write-Host 'Dropped (a run can have several reasons):'
$rows | Where-Object { -not $_.keep } | ForEach-Object { $_.reasons -replace ':.*$', '' } | Group-Object | Sort-Object Count -Descending | ForEach-Object { Write-Host ("  {0,-18} {1}" -f $_.Name, $_.Count) }
Write-Host 'Kept by model:'
$rows | Group-Object model | Sort-Object { @($_.Group | Where-Object keep).Count } -Descending | ForEach-Object {
    $k = @($_.Group | Where-Object keep)
    Write-Host ("  {0,-34} {1,3} of {2,3}  repos {3,3}  turns {4,5}" -f $_.Name, $k.Count, $_.Count, @($k.repo | Sort-Object -Unique).Count, ($k | Measure-Object turns -Sum).Sum)
}
Write-Host "Wrote $Out"
