<#
.SYNOPSIS
    Score install-bench candidates with the difficulty rubric, keep those at
    or above a minimum, check them on GitHub, and select the benchmark set.

.DESCRIPTION
    candidates.csv holds the 12 component scores (0-3) per repository, in
    the order of docs/install-bench-plan.md section 3 (p1a..p4c).
    factor = min(3, highest + Discount x sum of the other two)
    difficulty = 1 + 9 x (sum of the four factors) / 12

    With -Verify, each repository is looked up on GitHub (gh api graphql):
    missing, archived, or idle for more than -MaxIdleMonths are dropped,
    and renamed ones take their current name. The selection then takes up
    to -Count repositories: multi-platform and Windows installs first, then
    Linux-only ones, each time filling the least covered category with its
    hardest remaining repository, so no category crowds out the rest.

    Writes scored.csv (every candidate, with its scores and why it was
    dropped) and bench.json (the selection) next to the script.

.EXAMPLE
    .\Select-Bench.ps1 -Verify
#>
[CmdletBinding()]
param(
    [double]$MinDifficulty = 7,
    [int]$Count = 200,
    [double]$Discount = 0.2,
    [switch]$Verify,
    [int]$MaxIdleMonths = 24,
    [int]$MaxDiskGB = 100
)

$ErrorActionPreference = 'Stop'
$rows = @(Import-Csv (Join-Path $PSScriptRoot 'candidates.csv'))
$dupes = @($rows | Group-Object repo | Where-Object Count -gt 1)
if ($dupes) { throw "Listed twice: $($dupes.Name -join ', ')" }

function Get-Factor([double[]]$c) {
    $s = @($c | Sort-Object -Descending)
    [math]::Min(3.0, $s[0] + $Discount * ($s[1] + $s[2]))
}
function Get-Tier([double]$d) { if ($d -lt 3.5) { 'T1' } elseif ($d -lt 5.5) { 'T2' } elseif ($d -lt 7.5) { 'T3' } else { 'T4' } }

foreach ($r in $rows) {
    $c = foreach ($k in 'p1a', 'p1b', 'p1c', 'p2a', 'p2b', 'p2c', 'p3a', 'p3b', 'p3c', 'p4a', 'p4b', 'p4c') {
        $v = [double]$r.$k
        if ($v -lt 0 -or $v -gt 3 -or ($v * 4) % 1) { throw "$($r.repo): $k = $v is not 0-3 in steps of 0.25" }
        $v
    }
    $f = 0..3 | ForEach-Object { Get-Factor $c[(3 * $_)..(3 * $_ + 2)] }
    $d = 1 + 9 * ($f | Measure-Object -Sum).Sum / 12
    $r | Add-Member -NotePropertyMembers ([ordered]@{
        f1 = [math]::Round($f[0], 2); f2 = [math]::Round($f[1], 2); f3 = [math]::Round($f[2], 2); f4 = [math]::Round($f[3], 2)
        difficulty = [math]::Round($d, 2); tier = Get-Tier $d
        stars = $null; pushed = $null; license = $null; dropped = ''
    })
    if ($d -lt $MinDifficulty) { $r.dropped = "difficulty under $MinDifficulty" }
}

# disk.csv lists installs estimated at 50 GB or more; the rest are taken as smaller.
$disk = @{}
foreach ($x in (Import-Csv (Join-Path $PSScriptRoot 'disk.csv'))) { $disk[$x.repo] = $x }
foreach ($r in $rows) {
    $x = $disk[$r.repo]
    $r | Add-Member -NotePropertyName disk_gb -NotePropertyValue $(if ($x) { [int]$x.disk_gb } else { $null })
    if ($x -and [int]$x.disk_gb -ge $MaxDiskGB -and -not $r.dropped) { $r.dropped = "needs about $($x.disk_gb) GB of disk ($($x.basis -replace ';', ','))" }
}

if ($Verify) {
    $cutoff = (Get-Date).AddMonths(-$MaxIdleMonths)
    for ($i = 0; $i -lt $rows.Count; $i += 40) {
        $batch = $rows[$i..([math]::Min($i + 39, $rows.Count - 1))]
        $q = 'query {' + ((0..($batch.Count - 1) | ForEach-Object {
            $o, $nm = $batch[$_].repo -split '/', 2
            "r${_}: repository(owner: `"$o`", name: `"$nm`") { nameWithOwner isArchived isFork stargazerCount pushedAt licenseInfo { spdxId } }"
        }) -join ' ') + ' }'
        $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
        $json = @{ query = $q } | ConvertTo-Json -Compress | gh api graphql --input - 2>$null
        $ErrorActionPreference = $old
        $data = ("$json" | ConvertFrom-Json).data
        if (-not $data) { throw "GitHub query failed: $json" }
        for ($k = 0; $k -lt $batch.Count; $k++) {
            $r = $batch[$k]; $g = $data."r$k"
            if (-not $g) { if (-not $r.dropped) { $r.dropped = 'not found on GitHub' }; continue }
            if ($g.nameWithOwner -ne $r.repo) { $r.why = "$($r.why) (was $($r.repo))"; $r.repo = $g.nameWithOwner }
            $r.stars = $g.stargazerCount; $r.pushed = ([datetime]$g.pushedAt).ToString('yyyy-MM-dd'); $r.license = $g.licenseInfo.spdxId
            if ($r.dropped) { continue }
            if ($g.isArchived) { $r.dropped = 'archived' }
            elseif ([datetime]$g.pushedAt -lt $cutoff) { $r.dropped = "no push since $($r.pushed)" }
        }
    }
}

# Multi-platform and Windows installs first, then Linux-only ones; each
# round-robin across categories, hardest first within each.
$pool = @($rows | Where-Object { -not $_.dropped })
$picked = [Collections.Generic.List[object]]::new()
foreach ($part in @(@($pool | Where-Object { $_.os -ne 'L' }), @($pool | Where-Object { $_.os -eq 'L' }))) {
    $queues = @{}
    foreach ($g in ($part | Group-Object category)) { $queues[$g.Name] = [Collections.Generic.Queue[object]]::new([object[]]@($g.Group | Sort-Object { [double]$_.difficulty } -Descending)) }
    while ($picked.Count -lt $Count -and @($queues.Values | Where-Object Count).Count) {
        foreach ($cat in @($queues.Keys | Sort-Object { $n = $_; @($picked | Where-Object category -eq $n).Count }, { $_ })) {
            if ($picked.Count -ge $Count) { break }
            if ($queues[$cat].Count) { $picked.Add($queues[$cat].Dequeue()); break }
        }
    }
}
foreach ($r in $pool) { if (-not $picked.Contains($r)) { $r.dropped = "over the $Count (category already well covered)" } }

$rows | Sort-Object difficulty -Descending | Select-Object repo, category, os, variant, difficulty, tier, f1, f2, f3, f4, p1a, p1b, p1c, p2a, p2b, p2c, p3a, p3b, p3c, p4a, p4b, p4c, disk_gb, stars, pushed, license, dropped, why |
    Export-Csv (Join-Path $PSScriptRoot 'scored.csv') -NoTypeInformation -Encoding utf8
@($picked | Sort-Object category, { - $_.difficulty } | ForEach-Object {
    [ordered]@{ repo = $_.repo; category = $_.category; os = $_.os; variant = $_.variant; difficulty = $_.difficulty; tier = $_.tier
        factors = @($_.f1, $_.f2, $_.f3, $_.f4); stars = $_.stars; pushed = $_.pushed; license = $_.license; why = $_.why }
}) | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $PSScriptRoot 'bench.json') -Encoding utf8

$osName = @{ L = 'Linux'; W = 'Windows'; M = 'macOS' }
$md = @("# Install-bench candidate set", '',
    "Generated by ``agent/eval/bench/Select-Bench.ps1`` from ``candidates.csv``; do not edit by hand.",
    "$($picked.Count) repositories with difficulty $MinDifficulty or more (rubric v1, discount $Discount), grouped by category, hardest first.",
    'Scores are from each project''s documented install, before installing it; they are rescored after the install.',
    'Factors: 1 machine footprint, 2 machine-specific adaptation, 3 moving parts, 4 configuration and hidden knowledge (each 0 to 3).', '')
foreach ($g in ($picked | Group-Object category | Sort-Object Name)) {
    $md += "## $($g.Name) ($($g.Count))", ''
    foreach ($r in ($g.Group | Sort-Object { [double]$_.difficulty } -Descending)) {
        $os = ($r.os.ToCharArray() | ForEach-Object { $osName["$_"] }) -join ', '
        $md += "- [$($r.repo)](https://github.com/$($r.repo)): **$('{0:N1}' -f [double]$r.difficulty)** ($($r.f1) / $($r.f2) / $($r.f3) / $($r.f4)); $os; $($r.variant). $($r.why -replace ';', ',')."
    }
    $md += ''
}
$md | Set-Content (Join-Path $PSScriptRoot '..\..\..\docs\install-bench-candidates.md') -Encoding utf8

Write-Host "Candidates: $($rows.Count). Selected: $($picked.Count) of $Count wanted." -ForegroundColor Cyan
$rows | Where-Object dropped | Group-Object { $_.dropped -replace ' since .*', '' -replace '\(.*\)', '' -replace 'about \d+ GB', "$MaxDiskGB GB or more" } | ForEach-Object { Write-Host ("  dropped, {0}: {1}" -f $_.Name.Trim(), $_.Count) }
if ($picked.Count) {
    $d = @($picked | ForEach-Object { [double]$_.difficulty })
    Write-Host ("Selected difficulty: {0:N1} to {1:N1}, median {2:N1}; T3 (7 to 7.5): {3}, T4: {4}" -f ($d | Measure-Object -Minimum).Minimum, ($d | Measure-Object -Maximum).Maximum, ($d | Sort-Object)[[int][math]::Floor($d.Count / 2)], @($picked | Where-Object tier -eq 'T3').Count, @($picked | Where-Object tier -eq 'T4').Count)
    Write-Host ("Platforms: {0} on two or more OSes, {1} Linux only, {2} Windows only" -f @($picked | Where-Object { $_.os.Length -ge 2 }).Count, @($picked | Where-Object os -eq 'L').Count, @($picked | Where-Object os -eq 'W').Count)
    $picked | Group-Object category | Sort-Object Count -Descending | ForEach-Object { '{0} {1}' -f $_.Count, $_.Name } | Write-Host
}
