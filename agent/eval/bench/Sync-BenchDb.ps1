<#
.SYNOPSIS
    Load the install-bench repositories and probe results into MongoDB.

.DESCRIPTION
    Database installzero in the stack's mongo container (the app uses
    robotaxi; nothing there is touched):

      bench_repos     one document per candidate, _id = "owner/name", from
                      scored.csv (run Select-Bench.ps1 first): category,
                      platforms, install variant, rubric components, factors,
                      difficulty, tier, GitHub metadata, and whether it is
                      selected or why it was dropped. Fields are $set, so
                      fields added by hand (notes, build status) survive.
      bench_results   one document per probe run, from results.jsonl files
                      (Invoke-ProbeEval.ps1), _id = hash of repo, model, time
                      and install.md path, so importing twice adds nothing.

    Safe to rerun. Run it on the machine whose stack holds the database
    (your account); rltest's results are read from the shared folder.

.EXAMPLE
    .\Sync-BenchDb.ps1
.EXAMPLE
    .\Sync-BenchDb.ps1 -ResultsFiles C:\Users\Public\rl-roboracer-test\results\results.jsonl -SkipRepos
#>
[CmdletBinding()]
param(
    [string[]]$ResultsFiles = @('C:\Users\Public\rl-roboracer-test\results\results.jsonl', (Join-Path $env:LOCALAPPDATA 'installzero\eval\results.jsonl')),
    [switch]$SkipRepos,
    [string]$Database = 'installzero',
    [string]$BenchVersion = 'v1'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..\..')).Path
$osName = @{ L = 'linux'; W = 'windows'; M = 'macos' }
function Get-Num($v) { if ($null -eq $v -or "$v" -eq '') { $null } else { [double]$v } }

$repos = @()
if (-not $SkipRepos) {
    $scored = Join-Path $PSScriptRoot 'scored.csv'
    if (-not (Test-Path $scored)) { throw "No scored.csv; run Select-Bench.ps1 -Verify first." }
    $repos = @(Import-Csv $scored | ForEach-Object {
        $renamed = if ($_.why -match '\(was ([^)]+)\)') { $Matches[1] } else { $null }
        $comp = [ordered]@{}
        foreach ($k in 'p1a', 'p1b', 'p1c', 'p2a', 'p2b', 'p2c', 'p3a', 'p3b', 'p3c', 'p4a', 'p4b', 'p4c') { $comp[$k] = [double]$_.$k }
        [ordered]@{
            _id = $_.repo; url = "https://github.com/$($_.repo)"; category = $_.category
            os = @($_.os.ToCharArray() | ForEach-Object { $osName["$_"] }); variant = $_.variant
            rubric = [ordered]@{ version = 'v1'; components = $comp
                factors = [ordered]@{ machine_footprint = [double]$_.f1; machine_adaptation = [double]$_.f2; moving_parts = [double]$_.f3; config_knowledge = [double]$_.f4 }
                difficulty = [double]$_.difficulty; tier = $_.tier; scored_from = 'docs' }
            github = [ordered]@{ stars = Get-Num $_.stars; pushed = $_.pushed; license = $_.license; renamed_from = $renamed }
            disk_gb = Get-Num $_.disk_gb
            bench_version = $BenchVersion
            status = if ($_.dropped) { 'dropped' } else { 'selected' }
            dropped_reason = if ($_.dropped) { $_.dropped } else { $null }
            install_notes = ($_.why -replace '\s*\(was [^)]+\)', '')
        }
    })
}

$sha = [Security.Cryptography.SHA1]::Create()
$results = @(foreach ($f in $ResultsFiles) {
    if (-not (Test-Path $f)) { Write-Host "  (no $f)"; continue }
    foreach ($line in (Get-Content $f)) {
        if (-not $line.Trim()) { continue }
        $r = $line | ConvertFrom-Json
        $key = "$($r.repo)|$($r.model)|$($r.time)|$($r.install_md)"
        $id = -join ($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($key)) | ForEach-Object { $_.ToString('x2') })
        $doc = [ordered]@{ _id = $id; run_at = ([datetime]$r.time).ToUniversalTime().ToString('o'); source_file = $f }
        foreach ($p in $r.PSObject.Properties) { if ($p.Name -ne 'time') { $doc[$p.Name] = $p.Value } }
        $doc.provider = if ($r.model -match '^(\w+):') { $Matches[1] } else { 'local' }
        $doc
    }
})

$tmp = Join-Path $env:TEMP "bench-sync-$PID"
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
    # ConvertTo-Json in Windows PowerShell unrolls one-element arrays without the comma.
    ConvertTo-Json -InputObject @($repos) -Depth 12 -Compress | Set-Content (Join-Path $tmp 'repos.json') -Encoding utf8
    ConvertTo-Json -InputObject @($results) -Depth 12 -Compress | Set-Content (Join-Path $tmp 'results.json') -Encoding utf8
    @"
const fs = require('fs');
const d = db.getSiblingDB('$Database');
const now = new Date();
const read = f => JSON.parse(fs.readFileSync(f, 'utf8').replace(/^\uFEFF/, ''));
const repos = read('/tmp/bench_repos.json');
const results = read('/tmp/bench_results.json');
if (repos.length) {
  const r = d.bench_repos.bulkWrite(repos.map(x => {
    const id = x._id; delete x._id; x.updated_at = now;
    return { updateOne: { filter: { _id: id }, update: { `$set: x, `$setOnInsert: { created_at: now } }, upsert: true } };
  }));
  print('bench_repos: ' + r.upsertedCount + ' added, ' + r.modifiedCount + ' updated');
}
if (results.length) {
  const r = d.bench_results.bulkWrite(results.map(x => {
    x.run_at = new Date(x.run_at);
    return { updateOne: { filter: { _id: x._id }, update: { `$setOnInsert: Object.assign(x, { imported_at: now }) }, upsert: true } };
  }));
  print('bench_results: ' + r.upsertedCount + ' added, ' + (results.length - r.upsertedCount) + ' already there');
}
d.bench_repos.createIndex({ status: 1, category: 1 });
d.bench_repos.createIndex({ 'rubric.difficulty': -1 });
d.bench_results.createIndex({ repo: 1, model: 1, run_at: -1 });
d.bench_results.createIndex({ prompt_version: 1, run_at: -1 });
print('totals: bench_repos ' + d.bench_repos.countDocuments() + ' (' + d.bench_repos.countDocuments({ status: 'selected' }) + ' selected), bench_results ' + d.bench_results.countDocuments());
"@ | Set-Content (Join-Path $tmp 'sync.js') -Encoding utf8

    Push-Location $repoRoot
    $ErrorActionPreference = 'Continue'
    try {
        foreach ($n in 'repos', 'results', 'sync') {
            $dest = if ($n -eq 'sync') { '/tmp/bench_sync.js' } else { "/tmp/bench_$n.json" }
            $ext = if ($n -eq 'sync') { 'js' } else { 'json' }
            $out = docker compose cp (Join-Path $tmp "$n.$ext") ("mongo:" + $dest) 2>&1
            if ($LASTEXITCODE -ne 0) { throw "docker compose cp failed: $out. Is the stack's mongo running?" }
        }
        # No inner double quotes: Windows PowerShell 5 strips them from native args.
        docker compose exec -T mongo bash -c 'exec mongosh -u root -p ${MONGODB_ROOT_PASSWORD} --authenticationDatabase admin localhost:27017/admin --quiet --file /tmp/bench_sync.js'
        if ($LASTEXITCODE -ne 0) { throw "mongosh sync failed ($LASTEXITCODE)" }
    } finally { Pop-Location }
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
