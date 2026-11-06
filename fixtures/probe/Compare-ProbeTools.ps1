<#
.SYNOPSIS
    Runs the same probe tool calls through Probe.ps1 and the Go probe on a
    WSL distro and reports where their outputs differ.

.DESCRIPTION
    Read-only on the distro. A hidden folder with install-file lines and a
    secret-named variable are set up here, so the content guard and
    redaction are exercised too. Outputs that depend on time (process
    lists, uptimes) can differ between the two runs.
#>
param(
    [string]$Distro = 'Ubuntu-24.04',
    [string]$Target = '/etc/apt',
    [string]$Calls = (Join-Path $PSScriptRoot 'tool-calls.json'),
    [string]$Exe = ''
)
$ErrorActionPreference = 'Stop'
function Format-Brief([string]$s) { if ($s.Length -gt 110) { $s.Substring(0, 110) + '...' } else { $s } }
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
# Not under %TEMP%: the content guard skips files in Temp folders.
$work = Join-Path $env:LOCALAPPDATA "installzero\compare-$PID"
New-Item -ItemType Directory -Force (Join-Path $work 'hidden') | Out-Null
try {
    [IO.File]::WriteAllText((Join-Path $work 'hidden\notes.txt'), (@(
        'docker compose -f stack/compose.yml up -d --build web'
        'export APP_DATA_DIR=/srv/izp-demo/data/storage'
        'systemctl enable --now izp-demo-worker.service'
        'ln -s /srv/izp-demo/releases/current /srv/izp-demo/app'
        'pip install -r requirements.txt --no-cache-dir') -join "`n"))
    $env:IZP_TEST_TOKEN = 'izp-test-value-4711'
    if (-not $Exe) {
        $Exe = Join-Path $work 'installzero.exe'
        Push-Location $root; try { go build -o $Exe ./cmd/installzero; if ($LASTEXITCODE) { throw 'go build failed' } } finally { Pop-Location }
    }
    $out = Join-Path $work 'demo.install.md'
    $hide = Join-Path $work 'hidden'
    Copy-Item $Calls (Join-Path $work 'ps.json'); Copy-Item $Calls (Join-Path $work 'go.json')
    powershell -NoProfile -File (Join-Path $root 'agent\Probe.ps1') -Wsl $Distro -Target $Target -Out $out -HidePaths $hide -ToolCalls (Join-Path $work 'ps.json') -Quiet
    & $Exe probe --wsl $Distro --target $Target --out $out --hide $hide --tool-calls (Join-Path $work 'go.json') --quiet
    if ($LASTEXITCODE) { throw "the Go probe failed ($LASTEXITCODE)" }
    $ps = Get-Content (Join-Path $work 'ps.json.out.json') -Raw | ConvertFrom-Json
    $go = Get-Content (Join-Path $work 'go.json.out.json') -Raw | ConvertFrom-Json
    $callList = Get-Content $Calls -Raw | ConvertFrom-Json
    $diff = 0
    for ($i = 0; $i -lt $callList.Count; $i++) {
        $a = "$($ps.outputs[$i])"; $b = "$($go.outputs[$i])"
        $label = "$i $($callList[$i].name) $($callList[$i].arguments | ConvertTo-Json -Compress -Depth 4)"
        if ($a -ceq $b) { Write-Host "same  $(Format-Brief $label)" -ForegroundColor DarkGray; continue }
        $diff++
        Write-Host "DIFF  $label" -ForegroundColor Yellow
        $al = $a -split "`n"; $bl = $b -split "`n"
        for ($j = 0; $j -lt [math]::Max($al.Count, $bl.Count); $j++) {
            if ($al[$j] -cne $bl[$j]) { Write-Host "   line $($j + 1)`n     ps: $($al[$j])`n     go: $($bl[$j])"; break }
        }
    }
    foreach ($k in 'hidden_attempts', 'withheld_lines', 'guarded_lines') {
        if ($ps.$k -ne $go.$k) { $diff++; Write-Host "DIFF  $($k): ps $($ps.$k), go $($go.$k)" -ForegroundColor Yellow }
    }
    $pd = $ps.draft_checks | ConvertTo-Json -Compress; $gd = $go.draft_checks | ConvertTo-Json -Compress
    if ($pd -ne $gd) { $diff++; Write-Host "DIFF  draft_checks: ps $pd, go $gd" -ForegroundColor Yellow }
    Write-Host "$($callList.Count) calls, $diff difference(s). Outputs: $work" -ForegroundColor $(if ($diff) { 'Yellow' } else { 'Green' })
} finally {
    Remove-Item Env:\IZP_TEST_TOKEN -ErrorAction SilentlyContinue
}
