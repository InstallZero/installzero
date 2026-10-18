<#
.SYNOPSIS
    Uploads probe transcripts (and install documents) to the eval console for
    results that have none there yet, so those runs can be diagnosed.

.DESCRIPTION
    Runs from before transcripts were stored with results left them on the
    host that ran them: <name>.transcript.jsonl next to <name>.install.md in
    %LOCALAPPDATA%\installzero\eval. Run it on that host; results whose files
    are elsewhere are skipped. The console refuses a transcript written by two
    probes that started in the same second (they shared one file).

.EXAMPLE
    .\Push-Transcripts.ps1
#>
param([string]$Server = 'http://127.0.0.1:81')

$ErrorActionPreference = 'Stop'
$missing = @(Invoke-RestMethod "$Server/api/transcripts/missing" | ForEach-Object { $_ })
$tally = @{}
foreach ($m in $missing) {
    $t = if ($m.transcript_path) { $m.transcript_path } elseif ($m.install_md) { $m.install_md -replace '\.install\.md$', '.transcript.jsonl' } else { '' }
    if (-not $t -or -not [IO.File]::Exists($t)) { $tally['no file here']++; continue }
    $body = @{ id = $m._id; jsonl = [IO.File]::ReadAllText($t) }
    if (-not $m.has_install_md -and $m.install_md -and [IO.File]::Exists($m.install_md)) { $body.install_md = [IO.File]::ReadAllText($m.install_md) }
    $bytes = [Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Compress))
    $r = Invoke-RestMethod "$Server/api/transcript" -Method Post -ContentType 'application/json; charset=utf-8' -Body $bytes
    $tally[$r.status]++
}
Write-Host "$($missing.Count) results without a transcript: $(($tally.GetEnumerator() | Sort-Object Name | ForEach-Object { "$($_.Name) $($_.Value)" }) -join ', ')"
