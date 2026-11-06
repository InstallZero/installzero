<#
.SYNOPSIS
    Builds one SFT training set from probe trajectories: the eval console's
    sft-build job (Invoke-EvalJob.ps1) runs it on the host that holds the
    raw transcripts.

.DESCRIPTION
    The spec (job.spec) carries the candidate results and the set's
    settings. Steps:
      select   drop runs the model should not imitate (Sft.ps1:
               Get-SftRunProblems), providers or models not allowed, and
               transcripts missing on this host or shared by two results
      split    held-out repos (from the console's held-out datasets) are
               never exported; of the rest, a stable hash of the repo name
               puts -ValFraction of repos in val. Splits are by repo, so a
               model is never scored on a repo it trained on.
      tools    the probe's WSL tool definitions (Probe.ps1 -ToolsOut)
      examples every reply of a kept run, with the context the probe would
               have sent a model with context_tokens of context
               (ConvertTo-SftExamples); replies that repeated a call are
               context only
    Writes <OutDir>: train.jsonl, val.jsonl (one example per line: run,
    repo, model, turn, context_chars, messages in OpenAI chat format, the
    last one the target), tools.json, runs.jsonl (every candidate with its
    split or reasons), heldout_repos.txt and manifest.json. Returns the
    manifest, with a few clipped samples, as the job outcome.
#>
param(
    [Parameter(Mandatory)][string]$JobFile,
    [Parameter(Mandatory)][string]$OutDir,
    [Parameter(Mandatory)][string]$Out
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Sft.ps1')
$utf8 = $script:SftUtf8
$job = [IO.File]::ReadAllText($JobFile) | ConvertFrom-Json
$spec = $job.spec
$f = $spec.filter
$minRecall = [double]$f.min_recall; $maxRepeats = [int]$f.max_repeats
$providers = @($f.providers | Where-Object { $_ }); $models = @($f.models | Where-Object { $_ })
$heldout = [Collections.Generic.HashSet[string]]::new([string[]]@($spec.heldout_repos | Where-Object { $_ }))
$valFraction = [double]$spec.val_fraction
$contextTokens = [int]$spec.context_tokens
$maxChars = Get-SftMaxChars $contextTokens

function Write-Phase([string]$Phase, [string]$Msg = '') { Write-Host "PHASE: $Phase$(if ($Msg) { " | $Msg" })" }
function Get-Split([string]$Repo) {
    if ($heldout.Contains($Repo)) { return 'heldout' }
    $h = [Security.Cryptography.SHA1]::Create().ComputeHash($utf8.GetBytes($Repo))
    if ((([int]$h[0] * 256 + [int]$h[1]) % 1000) -lt [int]($valFraction * 1000)) { 'val' } else { 'train' }
}
function Get-Pct($Values, [double]$P) { $v = @($Values | Sort-Object); if (-not $v.Count) { return 0 }; $v[[math]::Min($v.Count - 1, [int][math]::Floor($P * $v.Count))] }

if (Test-Path $OutDir) { Remove-Item $OutDir -Recurse -Force }
New-Item -ItemType Directory -Force $OutDir | Out-Null

Write-Phase 'tools' 'probe tool definitions (WSL)'
$toolsFile = Join-Path $OutDir 'tools.json'
& (Join-Path $PSScriptRoot '..\..\Probe.ps1') -Wsl 'sft-tools' -ToolsOut $toolsFile -Quiet
$toolsHash = (Get-FileHash $toolsFile -Algorithm SHA256).Hash.Substring(0, 16).ToLower()

$cands = @($spec.candidates)
Write-Phase 'selecting' "$($cands.Count) candidates"
$seenPaths = @{}
$runs = [Collections.Generic.List[object]]::new()
$writers = @{ train = [IO.StreamWriter]::new((Join-Path $OutDir 'train.jsonl'), $false, $utf8); val = [IO.StreamWriter]::new((Join-Path $OutDir 'val.jsonl'), $false, $utf8) }
$exStats = @{ train = [Collections.Generic.List[int]]::new(); val = [Collections.Generic.List[int]]::new() }
$turnHist = @{}; $skippedTargets = @{}; $samples = [Collections.Generic.List[object]]::new(); $sampledModels = @{}
$n = 0
try {
    foreach ($c in $cands) {
        $n++
        if ($n % 25 -eq 0) { Write-Phase 'exporting' "$n/$($cands.Count) runs" }
        $reasons = [Collections.Generic.List[string]]::new()
        $provider = Get-SftProvider $c
        $path = "$($c.transcript_path)"
        $split = Get-Split "$($c.repo)"
        $res = $null; $ex = 0
        if ([double]$c.recall -lt $minRecall -or -not $c.delivered) { $reasons.Add('below-recall') }
        if ($providers.Count -and $providers -notcontains $provider) { $reasons.Add('provider-excluded') }
        if ($models.Count -and $models -notcontains "$($c.model)") { $reasons.Add('model-excluded') }
        if (-not $path -or -not [IO.File]::Exists($path)) { $reasons.Add('no-raw-transcript') }
        elseif ($seenPaths[$path]) { $reasons.Add('duplicate') }
        if (-not $reasons.Count) {
            $seenPaths[$path] = $c._id
            $ev = @(Read-SftTranscript $path)
            $res = (@($ev | Where-Object type -eq 'result') | Select-Object -Last 1).run
            foreach ($p in (Get-SftRunProblems $ev $res $maxRepeats)) { $reasons.Add($p) }
            if (-not $reasons.Count -and $split -ne 'heldout') {
                $meta = [ordered]@{ run = "$($c._id)"; repo = "$($c.repo)"; model = "$($c.model)"; provider = $provider; recall = [double]$c.recall }
                $r = ConvertTo-SftExamples $ev $maxChars $meta -IncludeReasoning:([bool]$spec.include_reasoning)
                foreach ($k in $r.Skipped.Keys) { $skippedTargets[$k] = [int]$skippedTargets[$k] + $r.Skipped[$k] }
                foreach ($x in $r.Examples) {
                    $writers[$split].WriteLine($x.Line)
                    $exStats[$split].Add($x.Chars)
                    $b = [math]::Min(40, 5 * [math]::Ceiling($x.Turn / 5)); $turnHist["$b"] = 1 + [int]$turnHist["$b"]
                    if ($split -eq 'train' -and -not $sampledModels[$provider] -and $x.Turn -ge 3 -and $samples.Count -lt 4) {
                        $sampledModels[$provider] = $true
                        $o = $x.Line | ConvertFrom-Json
                        $tail = @($o.messages | Select-Object -Last 4 | ForEach-Object {
                            [ordered]@{ role = $_.role; name = $_.name; content = $(if ($_.content) { $s = "$($_.content)"; if ($s.Length -gt 900) { $s.Substring(0, 900) + " [... $($s.Length - 900) more]" } else { $s } } else { '' })
                                calls = @($_.tool_calls | Where-Object { $_ } | ForEach-Object { $a = "$($_.function.arguments)"; "$($_.function.name) $(if ($a.Length -gt 400) { $a.Substring(0, 400) + ' ...' } else { $a })" }) } })
                        $samples.Add([ordered]@{ run = $o.run; repo = $o.repo; model = $o.model; turn = $o.turn; context_messages = $o.messages.Count - 1; context_chars = $o.context_chars; tail = $tail })
                    }
                }
                $ex = $r.Examples.Count
            }
        }
        $runs.Add([pscustomobject][ordered]@{ result_id = "$($c._id)"; repo = "$($c.repo)"; model = "$($c.model)"; provider = $provider; recall = [double]$c.recall
            split = $split; keep = (-not $reasons.Count); reasons = @($reasons); examples = $ex; turns = $(if ($res) { [int]$res.steps } else { 0 }); transcript = $path })
    }
} finally { foreach ($w in $writers.Values) { $w.Dispose() } }

[IO.File]::WriteAllLines((Join-Path $OutDir 'runs.jsonl'), [string[]]@($runs | ForEach-Object { $_ | ConvertTo-Json -Compress -Depth 5 }), $utf8)
[IO.File]::WriteAllLines((Join-Path $OutDir 'heldout_repos.txt'), [string[]]@($heldout | Sort-Object), $utf8)

$kept = @($runs | Where-Object keep)
$byModel = @($runs | Group-Object model | ForEach-Object {
    $k = @($_.Group | Where-Object keep)
    [ordered]@{ model = $_.Name; candidates = $_.Count; kept = $k.Count; train_runs = @($k | Where-Object split -eq 'train').Count; val_runs = @($k | Where-Object split -eq 'val').Count
        heldout_runs = @($k | Where-Object split -eq 'heldout').Count; examples = ($k | Measure-Object examples -Sum).Sum; repos = @($k.repo | Sort-Object -Unique).Count }
} | Sort-Object { -$_.examples })
$dropped = [ordered]@{}
$runs | Where-Object { -not $_.keep } | ForEach-Object { $_.reasons } | Group-Object | Sort-Object Count -Descending | ForEach-Object { $dropped[$_.Name] = $_.Count }
$lenStats = { param($v) [ordered]@{ examples = $v.Count; p50_chars = (Get-Pct $v 0.5); p90_chars = (Get-Pct $v 0.9); max_chars = (Get-Pct $v 1) } }
$manifest = [ordered]@{
    id = $spec.set_id; built_at = (Get-Date).ToUniversalTime().ToString('o'); host = $env:COMPUTERNAME; path = $OutDir
    filter = $f; context_tokens = $contextTokens; max_chars = $maxChars; val_fraction = $valFraction; include_reasoning = [bool]$spec.include_reasoning
    tools = @{ file = 'tools.json'; sha256_16 = $toolsHash; names = @((Get-Content $toolsFile -Raw | ConvertFrom-Json).function.name) }
    candidates = $runs.Count; kept_runs = $kept.Count
    repos = [ordered]@{ train = @($kept | Where-Object split -eq 'train' | ForEach-Object repo | Sort-Object -Unique).Count; val = @($kept | Where-Object split -eq 'val' | ForEach-Object repo | Sort-Object -Unique).Count; heldout = $heldout.Count }
    val_repos = @($kept | Where-Object split -eq 'val' | ForEach-Object repo | Sort-Object -Unique)
    train = (& $lenStats $exStats.train); val = (& $lenStats $exStats.val)
    by_model = $byModel; dropped_runs = $dropped; skipped_targets = $skippedTargets; turns = $turnHist
}
[IO.File]::WriteAllText((Join-Path $OutDir 'manifest.json'), ($manifest | ConvertTo-Json -Depth 8), $utf8)
$manifest.samples = $samples
[IO.File]::WriteAllText($Out, ($manifest | ConvertTo-Json -Depth 10 -Compress), $utf8)
Write-Phase 'built' "$($manifest.train.examples) train / $($manifest.val.examples) val examples from $($kept.Count) runs"
