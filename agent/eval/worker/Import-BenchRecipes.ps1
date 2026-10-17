<#
.SYNOPSIS
    Loads the recipes in eval\wsl\projects and their facts from repos.json
    into the eval console (MongoDB is the source of truth from then on).

.EXAMPLE
    .\Import-BenchRecipes.ps1
#>
param(
    [string]$Server = 'http://127.0.0.1:8300',
    [string[]]$Project = @()
)

$ErrorActionPreference = 'Stop'
$evalDir = Split-Path $PSScriptRoot
$projects = Join-Path $evalDir 'wsl\projects'
$facts = @{}; foreach ($r in (Get-Content (Join-Path $evalDir 'repos.json') -Raw | ConvertFrom-Json).repos) { $facts[$r.id] = $r }

$snippets = @(Get-ChildItem $projects -Filter '_*.sh' | ForEach-Object { @{ id = $_.Name; script = (Get-Content $_.FullName -Raw) -replace "`r`n", "`n" } })
$repos = @(Get-ChildItem $projects -Filter '*.json' | Where-Object { -not $Project -or $_.BaseName -in $Project } | ForEach-Object {
    $d = Get-Content $_.FullName -Raw | ConvertFrom-Json
    $f = $facts[$d.id]
    if (-not $d.repo) { Write-Warning "$($_.Name) names no GitHub repo; skipped"; return }
    if (-not $f) { Write-Warning "$($d.id) has no facts in repos.json; importing the recipe only" }
    @{ id = $d.repo; bench_id = $d.id; serves = $f.serves; why = $f.why; facts = @($f.facts)
        recipe = @{ script = (Get-Content (Join-Path $projects $d.recipe) -Raw) -replace "`r`n", "`n"; target = $d.target; ready = $d.ready; ready_timeout_sec = $d.ready_timeout_sec
            include = @($d.include | Where-Object { $_ }); remove = @($d.remove) } }
})
$bytes = [Text.Encoding]::UTF8.GetBytes((@{ snippets = $snippets; repos = $repos } | ConvertTo-Json -Depth 10 -Compress))
$r = Invoke-WebRequest -UseBasicParsing -Uri "$Server/api/import" -Method Post -ContentType 'application/json; charset=utf-8' -Body $bytes
Write-Host "Imported $(($r.Content | ConvertFrom-Json).imported) recipes and $($snippets.Count) snippets: $($repos.bench_id -join ', ')"
