<#
.SYNOPSIS
    Loss analysis for the eval console: why one probe run missed facts
    (diagnose), and general probe guidance from many diagnosed runs (lessons).

.DESCRIPTION
    diagnose  One scored run. The model gets the missed facts with their
              regexes, the recipe that built the install (ground truth the
              probe never saw), the probe's transcript and the install.md it
              wrote, and gives each miss a cause:
                probe losses     not_explored, out_of_budget, found_not_written
                benchmark bugs   written_unmatched (regex too strict),
                                 not_observable (no probe could learn it)
              with evidence, and for probe losses a lesson phrased so it
              would help on any project.
    lessons   Diagnosed misses across tuning datasets. Recurring probe losses
              become guidance items: kind prompt (the probe can follow it with
              its tools) or tool (a change a developer makes to the probe),
              each backed by misses on at least two repos. Reviewed and
              activated in the console before any campaign uses them.
#>
param(
    [Parameter(Mandatory)][string]$JobFile,
    [Parameter(Mandatory)][string]$Out
)

$ErrorActionPreference = 'Stop'
$job = [IO.File]::ReadAllText($JobFile) | ConvertFrom-Json
$spec = $job.spec
$evalDir = Split-Path $PSScriptRoot
. (Join-Path $evalDir '..\..\doctor\Llm.ps1')
$utf8 = [Text.UTF8Encoding]::new($false)

function Limit([string]$T, [int]$N) { if ($T.Length -le $N) { $T } else { $T.Substring(0, $N) + " [... $($T.Length - $N) more chars]" } }

function Invoke-JsonAnswer([string]$System, [string]$User, [int]$MaxTokens) {
    $model = [string]$job.model
    if (-not (Test-RemoteSpec $model)) { throw "Loss analysis needs a hosted model (provider:model-id); got '$model'." }
    $llm = Get-RemoteLlm $model
    $messages = @(@{ role = 'system'; content = $System }, @{ role = 'user'; content = $User })
    Write-Host "Asking $model ($([math]::Round(($System.Length + $User.Length) / 4000))k tokens in)"
    $resp = if ($llm.Provider -eq 'openai') { Invoke-OpenAIResponses $llm $messages @() $MaxTokens 900 }
    else { $body = New-ChatBody $llm $messages $null 0.2 $MaxTokens; $body.Remove('tools'); Invoke-LlmChat $llm.Url $body 900 (Get-LlmHeaders $llm) }
    $text = [string]$resp.choices[0].message.content
    $start = $text.IndexOf('{'); $end = $text.LastIndexOf('}')
    if ($start -lt 0 -or $end -le $start) { throw "the model's answer has no JSON object: $(Limit $text 500)" }
    @{ Data = ($text.Substring($start, $end - $start + 1) | ConvertFrom-Json)
       Tokens = $(if ($resp.usage) { @{ prompt = $resp.usage.prompt_tokens; completion = $resp.usage.completion_tokens } } else { $null }) }
}

if ($job.type -eq 'diagnose') {
    $res = $spec.result
    $turn = 0
    $lines = foreach ($e in @($spec.transcript)) {
        switch ($e.t) {
            'start' { "[task] $(Limit $e.user 600)" }
            'assistant' {
                $turn++
                $t = "[turn $turn]"
                if ($e.reasoning) { $t += " thinking: $(Limit $e.reasoning 400)" }
                if ($e.text) { $t += " says: $(Limit $e.text 400)" }
                foreach ($c in @($e.calls)) { if ($c) { $t += "`n  -> $($c.name) $(Limit $c.args 300)" } }
                $t
            }
            'tool' { "  <- $($e.name): $(Limit $e.out 1500)" }
            'error' { "  !! server error: $($e.text)" }
        }
    }
    $tx = Limit ($lines -join "`n") 70000
    $missed = @($spec.missed | Where-Object { $_ })
    if (-not $missed) { throw 'this run has no missed facts that the repo still defines' }

    $system = @"
You diagnose why an AI probe missed facts in an install benchmark.

The setup: a recipe installed an open-source project in a sealed Ubuntu WSL distro, and a snapshot of it was restored. The probe, blind to READMEs, docs, install scripts, installer logs and shell history, inspected the running system with read-only tools and wrote an install document (install.md) meant to reproduce the installation elsewhere. Facts are regexes, matched case-insensitively and multiline against that document. You get the facts it missed, the recipe that built the install (ground truth the probe never saw), the probe's transcript (its reasoning, tool calls and tool outputs) and the document it wrote.

For each missed fact decide the cause from the transcript and the document, not from what is likely:
- not_explored: no tool output shows the information; the probe never looked where it was visible.
- out_of_budget: as not_explored, but the probe was stopped by its turn or time budget while it still had leads to follow.
- found_not_written: a tool output shows the information, but the document leaves it out, or states it wrongly or too vaguely to match.
- written_unmatched: the document states it correctly, but the regex does not match its wording. A benchmark bug: give fact_fix, a corrected regex (valid in .NET and JavaScript, no lookbehind).
- not_observable: no careful probe could learn it from the running system (the recipe deleted the evidence, it lived only in install files the probe cannot see, or the fact is wrong). A benchmark bug: say in fact_fix whether to drop or reword the fact.

evidence: one short quote or pointer, e.g. "turn 7, cat /etc/hosts shows 'x.bench.lan'" or "document step 3 says 'port 8093'". For not_explored and out_of_budget, where it was visible (a file, command or inventory area, from the recipe).
lesson: for the three probe causes, one imperative sentence: a habit that would have caught this on any project, e.g. "Read systemd drop-ins with systemctl cat <unit>, not only the main unit file." No project names, hostnames, ports, versions or paths specific to this install. Empty for benchmark bugs.
habits: up to three observations about how the probe spent its turns that cost recall (repeated commands, a late first draft, leads it never followed). General, not project-specific.

Answer with only one JSON object, no prose around it:
{"misses":[{"id":"...","cause":"...","evidence":"...","lesson":"...","fact_fix":""}],"habits":["..."],"summary":"one or two sentences"}
"@

    $recipe = if ($spec.recipe) { "target: $($spec.recipe.target)`ndeleted after the install: $(@($spec.recipe.remove) -join ', ')`n$(Limit $spec.recipe.script 15000)" } else { '(recipe not available)' }
    $user = @"
Project: $($spec.repo) (bench id $($spec.bench_id)); serves $($spec.serves). $($spec.why)
Run: model $($res.model), prompt $($res.prompt_version), stopped: $($res.stop_reason), $($res.turns) turns, recall $([math]::Round([double]$res.recall * 100))%.
The probe's own gaps: $($res.gaps)

===== Missed facts =====
$(($missed | ForEach-Object { "- $($_.id) ($($_.category), weight $($_.weight)): /$($_.re)/   [regex matches raw tool output: $(if ($_.seen_in_outputs) { 'yes' } else { 'no' })]" }) -join "`n")

===== Facts it got =====
$(@($spec.hit) -join ', ')

===== Recipe that built the install (ground truth; the probe never saw it) =====
$recipe

===== Probe transcript =====
$tx

===== The install document it wrote =====
$(if ($res.install_md_text) { Limit $res.install_md_text 25000 } else { '(not stored with this run)' })
"@

    $a = Invoke-JsonAnswer $system $user 6000
    $seen = @{}; foreach ($f in $missed) { $seen[$f.id] = [bool]$f.seen_in_outputs }
    $diagnosis = [ordered]@{
        misses = @(@($a.Data.misses) | Where-Object { $_ } | ForEach-Object { [ordered]@{ id = $_.id; cause = $_.cause; evidence = $_.evidence; lesson = $_.lesson; fact_fix = $_.fact_fix; seen_in_outputs = [bool]$seen["$($_.id)"] } })
        habits = @($a.Data.habits | Where-Object { $_ }); summary = $a.Data.summary; tokens = $a.Tokens
    }
    [IO.File]::WriteAllText($Out, ($diagnosis | ConvertTo-Json -Depth 6), $utf8)
    Write-Host "Diagnosis: $(($diagnosis.misses | Group-Object { $_.cause } | ForEach-Object { "$($_.Name) x$($_.Count)" }) -join ', ')"
    return
}

if ($job.type -ne 'lessons') { throw "unknown loss-analysis job '$($job.type)'" }

$misses = @($spec.misses | Where-Object { $_ })
if (-not $misses) { throw 'no diagnosed misses to learn from' }
$probeLosses = @($misses | Where-Object { $_.cause -in 'not_explored', 'out_of_budget', 'found_not_written' })
$byCause = ($misses | Group-Object cause | ForEach-Object { "$($_.Name) $($_.Count) (weight $(($_.Group | Measure-Object weight -Sum).Sum))" }) -join '; '
$byCat = ($probeLosses | Group-Object category | Sort-Object { - ($_.Group | Measure-Object weight -Sum).Sum } | ForEach-Object { "$($_.Name) $(($_.Group | Measure-Object weight -Sum).Sum)" }) -join '; '

$system = @"
You turn diagnosed benchmark losses into guidance for the InstallZero probe: an AI agent that inspects a running install on a Linux machine with read-only tools (run_command, read_file, list_dir, grep, inventory, test_check, write_install_md) and writes an install.md that reproduces it, blind to docs, install scripts and shell history.

You get the probe's current system prompt, the accepted guidance from the previous round (if any), and every diagnosed miss from a tuning set of runs: repo, model, fact category and weight, cause, evidence and the per-miss lesson. not_explored, out_of_budget and found_not_written are probe losses; written_unmatched and not_observable are benchmark bugs and only context.

Write the guidance that would recover the most weighted misses on projects the probe has never seen:
- Each item is one or two imperative sentences: a concrete habit, naming the command or place to look where that helps (e.g. "Before writing, list enabled units with systemctl list-unit-files --state=enabled and read each with systemctl cat, drop-ins included; record non-default ExecStart flags and Environment lines.").
- General: no project names, hostnames, ports, versions or paths that only one install has. An item must be backed by probe-loss misses on at least two different repos; list those repos (bench ids as given) in repos.
- Do not repeat what the current prompt already says unless the misses show it is ignored; then make it sharper and say so in rationale.
- found_not_written losses call for writing habits (what the document must record), not more exploration.
- kind "prompt" for guidance the probe can follow with its current tools; kind "tool" for a change to its tools or inventory (a new inventory area, more in a tool's output) that a developer would implement.
- At most 12 items, the ones recovering the most weighted misses first. Merge overlapping lessons.
- Keep prior accepted items that the new misses still support; drop ones they contradict.

Answer with only one JSON object, no prose around it:
{"items":[{"text":"...","kind":"prompt","categories":["..."],"repos":["..."],"rationale":"which misses it recovers and why it generalizes"}],"notes":"what the losses look like overall; short"}
"@

$user = @"
$($spec.runs) diagnosed runs on $(@($spec.repos).Count) repos. Misses by cause: $byCause. Probe-loss weight by category: $byCat.

===== Diagnosed misses (repo | model | category, weight | cause | lesson | evidence) =====
$(Limit (($misses | Sort-Object cause, category, repo | ForEach-Object { "- $($_.repo) | $($_.model) | $($_.category) w$($_.weight) | $($_.cause) | $($_.lesson) | $($_.evidence)" }) -join "`n") 60000)

===== How the probe spent its turns (per run) =====
$(Limit ((@($spec.habits) | Where-Object { $_ } | ForEach-Object { "- $($_.repo): $($_.text)" }) -join "`n") 12000)

===== Accepted guidance from the previous round =====
$(if (@($spec.parent_items | Where-Object { $_ }).Count) { (@($spec.parent_items) | ForEach-Object { "- [$($_.kind)] $($_.text)" }) -join "`n" } else { '(none)' })

===== The probe's current system prompt =====
$(if ($spec.probe_prompt) { $spec.probe_prompt } else { '(not available)' })
"@

$a = Invoke-JsonAnswer $system $user 8000
$lessonSet = [ordered]@{ items = @($a.Data.items | Where-Object { $_ -and $_.text }); notes = $a.Data.notes; tokens = $a.Tokens }
[IO.File]::WriteAllText($Out, ($lessonSet | ConvertTo-Json -Depth 6), $utf8)
Write-Host "Lessons: $(@($lessonSet.items).Count) items ($(@($lessonSet.items | Where-Object kind -eq 'tool').Count) tool changes)"
