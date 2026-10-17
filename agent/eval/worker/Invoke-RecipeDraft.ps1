<#
.SYNOPSIS
    Drafts an install-bench recipe, scoring facts and rubric for one repo
    from its GitHub docs, for review in the eval console.

.DESCRIPTION
    Reads the README and up to eight install, deploy and configuration
    documents from GitHub (GITHUB_TOKEN raises the rate limit), shows the
    model the Harbor recipe and facts as the bar to meet, and asks for one
    JSON object: recipe, facts, rubric factors and notes. On a redraft the
    previous recipe, the reviewer's feedback and the failed build's log go
    along. Regexes that .NET cannot compile are dropped and noted.
#>
param(
    [Parameter(Mandatory)][string]$JobFile,
    [Parameter(Mandatory)][string]$Out
)

$ErrorActionPreference = 'Stop'
$job = Get-Content $JobFile -Raw -Encoding UTF8 | ConvertFrom-Json
$spec = $job.spec
$evalDir = Split-Path $PSScriptRoot
. (Join-Path $evalDir '..\..\doctor\Llm.ps1')
$utf8 = [Text.UTF8Encoding]::new($false)
$repo = $spec.repo
$branch = if ($spec.github -and $spec.github.default_branch) { $spec.github.default_branch } else { 'main' }
$basePort = [int]$spec.base_port

function Get-GitHub([string]$Url, [string]$Accept = 'application/vnd.github+json') {
    $h = @{ 'User-Agent' = 'installzero-evals'; Accept = $Accept }
    if ($env:GITHUB_TOKEN) { $h.Authorization = "Bearer $env:GITHUB_TOKEN" }
    $r = Invoke-WebRequest -UseBasicParsing -Uri $Url -Headers $h -TimeoutSec 60
    [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray())
}
function Limit([string]$T, [int]$N) { if ($T.Length -le $N) { $T } else { $T.Substring(0, $N) + "`n[... truncated]" } }

Write-Host "Reading $repo docs from GitHub"
$docs = [ordered]@{}
try { $docs['README'] = Limit (Get-GitHub "https://api.github.com/repos/$repo/readme" 'application/vnd.github.raw') 20000 } catch { Write-Host "  no README: $($_.Exception.Message)" }
try {
    $tree = (Get-GitHub "https://api.github.com/repos/$repo/git/trees/$($branch)?recursive=1" | ConvertFrom-Json).tree | Where-Object type -eq 'blob'
    $want = '(?i)(install|setup|deploy|getting[-_ ]?started|quick[-_ ]?start|production|configur|requirements|upgrad|docker-compose|compose\.ya?ml|systemd|\.service$|nginx)'
    $picked = @($tree | Where-Object { $_.path -match $want -and $_.path -match '(?i)\.(md|rst|txt|adoc|ya?ml|service|conf|example)$' -and $_.path -notmatch '(?i)(test|spec|\.github/|node_modules|vendor/|changelog|/i18n/|/locale)' -and $_.size -lt 200000 } |
        Sort-Object { $s = 0; if ($_.path -match '(?i)install') { $s -= 4 }; if ($_.path -match '(?i)(deploy|production)') { $s -= 3 }; if ($_.path -match '(?i)compose') { $s -= 2 }; if ($_.path -match '(?i)^docs?/') { $s -= 1 }; $s + ($_.path.Split('/').Count) } |
        Select-Object -First 8)
    foreach ($f in $picked) {
        try { $docs[$f.path] = Limit (Get-GitHub "https://raw.githubusercontent.com/$repo/$branch/$($f.path)" '*/*') 12000; Write-Host "  read $($f.path)" } catch { }
    }
} catch { Write-Host "  no file tree: $($_.Exception.Message)" }
$docText = ($docs.GetEnumerator() | ForEach-Object { "----- $($_.Key) -----`n$($_.Value)" }) -join "`n`n"
$docText = Limit $docText 90000

$wsl = Join-Path $evalDir 'wsl\projects'
$exampleRecipe = Get-Content (Join-Path $wsl 'harbor.sh') -Raw
$exampleDef = Get-Content (Join-Path $wsl 'harbor.json') -Raw
$exampleFacts = ((Get-Content (Join-Path $evalDir 'repos.json') -Raw | ConvertFrom-Json).repos | Where-Object id -eq 'harbor').facts | ConvertTo-Json -Depth 4
$dockerSnippet = Get-Content (Join-Path $wsl '_docker.sh') -Raw
$g = $spec.github

$system = @"
You write install-bench recipes for InstallZero. A recipe installs one open-source project in a fresh, sealed Ubuntu 24.04 WSL 2 distro: root, systemd running, internet access, no Windows interop or drive mounts, no GPU. After the recipe the readiness check must pass; then the files listed in remove are deleted and the distro is exported as a snapshot. Each benchmark run restores the snapshot (a cold boot), waits for the readiness check, and lets an AI probe inspect the running system and write an install document from what it finds. Facts are regexes scored against that document: they measure how much of the real installation the probe recovered.

Recipe rules:
- Bash, run as root, non-interactive (export DEBIAN_FRONTEND=noninteractive), start with set -euxo pipefail.
- Use the project's documented production install path (distribution packages, the official compose file, or a source build), whichever is most representative. Pin versions: latest release is $($g.latest_release); pin it unless the docs say otherwise.
- Everything must come back after a reboot: enable systemd units; compose services need restart policies. The readiness check runs after a cold boot of the restored snapshot.
- Host ports: put the main web or API endpoint on $basePort and any other host-published port in $($basePort + 1)-$($basePort + 9). Never bind 80, 443, 8080, 8081, 8082, 6006, 10000 or 27017 on the host (taken on the Windows side). Internal default ports of databases and caches (5432, 6379, 3306) are fine.
- When apt would start a service on port 80 before you configure it, write /usr/sbin/policy-rc.d that exits 101 around the apt install and remove it afterwards.
- Docker: if the install needs Docker, add "_docker.sh" to include (it installs Docker CE and compose from Docker's apt repo) instead of installing Docker yourself. Its content is below.
- Make three to five deliberate non-default choices that a probe can only learn by inspecting the system: e.g. the hostname $($spec.bench_id).bench.lan added to /etc/hosts and used as the site URL, the custom port, an admin user name, a data directory outside the default, a time zone, one feature flag. Write facts for each.
- Generate passwords in the recipe (openssl rand -hex 16) and store them where the project expects them.
- Stay under 20 GB of disk and 8 GB of RAM; no large model or dataset downloads.
- ready: one quick bash command that exits 0 once the project serves, e.g. curl -fsS http://localhost:$basePort/ >/dev/null. ready_timeout_sec: seconds a cold boot needs to get there (60-600).
- target: the main installation directory the probe is pointed at.
- remove: files only the installer used that would hand the probe the procedure (downloaded archives, install scripts, answer files, the installer's copies of templates). Never config files or data the running system uses.

Fact rules:
- 10 to 16 facts. id in snake_case. category one of: components, version, config, host, network, data, security, operations.
- weight 3: essential to reproduce the install (install method, main components and their versions, the custom port); 2: important configuration; 1: useful detail.
- re: a regex compatible with both .NET and JavaScript, matched case-insensitively and multiline against the install document. Match how a correct document would phrase it, robust to wording (alternation, \b, optional parts); specific enough that a generic document does not match; no lookbehind.
- Cover every deliberate non-default choice and the version pins (major.minor).

Rubric (the benchmark's difficulty rubric), four factors scored 0-3 in steps of 0.25:
 1 machine footprint (disk, RAM, GPU, downloads), 2 machine-specific adaptation (drivers, kernel, arch, OS specifics), 3 moving parts (number of services, databases, queues, proxies), 4 configuration and hidden knowledge (settings that only show on the running system, non-obvious steps).

Answer with only one JSON object, no prose around it:
{"recipe":{"script":"...","target":"...","ready":"...","ready_timeout_sec":300,"include":[],"remove":[],"ports_expected":[$basePort]},
 "facts":[{"id":"...","category":"...","weight":3,"re":"..."}],
 "rubric":{"factors":[0,0,0,0],"why":"..."},
 "notes":"install path chosen, assumptions, risks; short"}
"@

$user = @"
Repository: $repo ($($spec.url))
Description: $($g.description)
Language: $($g.language); stars: $($g.stars); latest release: $($g.latest_release); category: $($spec.category)
Bench id: $($spec.bench_id). Main port: $basePort.

===== The _docker.sh snippet =====
$dockerSnippet

===== Example of the quality expected: Harbor =====
Definition: $exampleDef
Recipe:
$exampleRecipe
Facts:
$exampleFacts

===== $repo documentation =====
$docText
"@
if ($spec.previous) {
    $user += "`n`n===== Your previous draft =====`n$($spec.previous | ConvertTo-Json -Depth 6)"
}
if ($spec.build_error) {
    $user += "`n`n===== Building the snapshot from it failed =====`n$($spec.build_error.error)`n$($spec.build_error.log_tail)"
}
if ($spec.feedback) {
    $user += "`n`n===== Reviewer feedback: revise accordingly =====`n$($spec.feedback)"
}

$model = [string]$job.model
if (-not (Test-RemoteSpec $model)) { throw "Drafting needs a hosted model (provider:model-id); got '$model'." }
$llm = Get-RemoteLlm $model
$messages = @(@{ role = 'system'; content = $system }, @{ role = 'user'; content = $user })
Write-Host "Asking $model ($([math]::Round(($system.Length + $user.Length) / 4000))k tokens in)"
$resp = if ($llm.Provider -eq 'openai') { Invoke-OpenAIResponses $llm $messages @() 8000 900 }
else { $body = New-ChatBody $llm $messages $null 0.2 8000; $body.Remove('tools'); Invoke-LlmChat $llm.Url $body 900 (Get-LlmHeaders $llm) }
$text = [string]$resp.choices[0].message.content
$start = $text.IndexOf('{'); $end = $text.LastIndexOf('}')
if ($start -lt 0 -or $end -le $start) { throw "the model's answer has no JSON object: $(Limit $text 500)" }
$d = $text.Substring($start, $end - $start + 1) | ConvertFrom-Json

$notes = @([string]$d.notes)
$facts = @(foreach ($f in @($d.facts)) {
    try { [void][regex]::new([string]$f.re, 'IgnoreCase, Multiline'); $f }
    catch { $notes += "Dropped fact $($f.id): its regex does not compile ($($_.Exception.Message))." }
})
$factors = @($d.rubric.factors | ForEach-Object { [math]::Min(3, [math]::Max(0, [double]$_)) })
$rubric = if ($factors.Count -eq 4) {
    [ordered]@{ version = 'v1'; factors = [ordered]@{ machine_footprint = $factors[0]; machine_adaptation = $factors[1]; moving_parts = $factors[2]; config_knowledge = $factors[3] }
        difficulty = [math]::Round(1 + 9 * ($factors | Measure-Object -Sum).Sum / 12, 2); scored_from = 'draft'; why = $d.rubric.why }
} else { $null }

$draft = [ordered]@{
    recipe = $d.recipe; facts = $facts; rubric = $rubric; notes = ($notes | Where-Object { $_ }) -join "`n"
    sources = @($docs.Keys); tokens = $(if ($resp.usage) { @{ prompt = $resp.usage.prompt_tokens; completion = $resp.usage.completion_tokens } } else { $null })
}
[IO.File]::WriteAllText($Out, ($draft | ConvertTo-Json -Depth 10), $utf8)
Write-Host "Draft: $($facts.Count) facts, difficulty $($rubric.difficulty), ports $(@($d.recipe.ports_expected) -join ' ')"
