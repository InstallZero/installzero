<#
.SYNOPSIS
    Writes the eval console's recipes and facts back to the files in git
    (eval\wsl\projects and repos.json) for review and history.

.EXAMPLE
    .\Export-BenchRecipes.ps1; git diff -- ..\wsl\projects ..\repos.json
#>
param([string]$Server = 'http://127.0.0.1:8300')

$ErrorActionPreference = 'Stop'
$evalDir = Split-Path $PSScriptRoot
$projects = Join-Path $evalDir 'wsl\projects'
$reposFile = Join-Path $evalDir 'repos.json'
$utf8 = [Text.UTF8Encoding]::new($false)
$r = Invoke-WebRequest -UseBasicParsing -Uri "$Server/api/export"
$x = [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json

foreach ($s in $x.snippets) { [IO.File]::WriteAllText((Join-Path $projects $s._id), $s.script, $utf8) }
$set = Get-Content $reposFile -Raw | ConvertFrom-Json
foreach ($p in $x.repos) {
    $id = $p.bench_id
    [IO.File]::WriteAllText((Join-Path $projects "$id.sh"), $p.recipe.script, $utf8)
    $def = [ordered]@{ id = $id; repo = $p.id; recipe = "$id.sh" }
    if (@($p.recipe.include).Count) { $def.include = @($p.recipe.include) }
    $def.target = $p.recipe.target; $def.ready = $p.recipe.ready; $def.ready_timeout_sec = $p.recipe.ready_timeout_sec; $def.remove = @($p.recipe.remove)
    [IO.File]::WriteAllText((Join-Path $projects "$id.json"), ($def | ConvertTo-Json -Depth 5), $utf8)
    $entry = $set.repos | Where-Object id -eq $id
    if (-not $entry) {
        $entry = [pscustomobject][ordered]@{ id = $id; source = "https://github.com/$($p.id)"; serves = $p.serves; why = $p.why; wsl = [pscustomobject]@{ project = $id }; facts = @() }
        $set.repos += $entry
    }
    $entry.facts = @($p.facts | ForEach-Object { [ordered]@{ id = $_.id; category = $_.category; weight = $_.weight; re = $_.re } })
}
[IO.File]::WriteAllText($reposFile, ($set | ConvertTo-Json -Depth 10), $utf8)
Write-Host "Exported $(@($x.repos).Count) recipes and $(@($x.snippets).Count) snippets to $projects and $reposFile"
