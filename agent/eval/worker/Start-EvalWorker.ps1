<#
.SYNOPSIS
    Eval-console worker for this Windows host: takes jobs from the console
    and runs up to -Slots of them in parallel.

.DESCRIPTION
    Polls the console (default http://127.0.0.1:8300) the way the Unity
    client wrapper polls the dashboard. Each job runs in its own PowerShell
    process (Invoke-EvalJob.ps1); this loop tails its output to the console
    as live log and progress, posts the outcome when it exits, and kills it
    (and deletes its WSL distro) when the job is cancelled.

    The console decides what may run together (ports, GPU, builds); this
    worker only says how many slots and GPU slots it has free. Every 10 s it
    reports its snapshots (with their listening ports from <id>.meta.json)
    and the local .gguf models it can run.

    Ctrl+C stops the worker and its running jobs; the console requeues them.
    A worker killed outright leaves its jobs running; the next worker started
    on this host adopts them.

.EXAMPLE
    .\Start-EvalWorker.ps1 -Slots 3
.EXAMPLE
    .\Start-EvalWorker.ps1 -Slots 4 -GpuSlots 0 -Types probe
#>
[CmdletBinding()]
param(
    [string]$Server = 'http://127.0.0.1:8300',
    [int]$Slots = 3,
    [int]$GpuSlots = 1,
    [string]$Name = $env:COMPUTERNAME.ToLower(),
    [string[]]$Types = @(),
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'installzero\bench'),
    [int]$PollSeconds = 2
)

$ErrorActionPreference = 'Stop'
$jobScript = Join-Path $PSScriptRoot 'Invoke-EvalJob.ps1'
$jobsDir = Join-Path $Root 'jobs'
New-Item -ItemType Directory -Force $jobsDir, (Join-Path $Root 'snapshots') | Out-Null
$utf8 = [Text.UTF8Encoding]::new($false)
$running = @{}
function Write-Line([string]$m, [string]$c = 'Gray') { Write-Host "[$(Get-Date -Format HH:mm:ss)] $m" -ForegroundColor $c }

function Invoke-Api([string]$Path, $Body) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20 -Compress))
    $r = Invoke-WebRequest -UseBasicParsing -Uri "$Server$Path" -Method Post -ContentType 'application/json; charset=utf-8' -Body $bytes -TimeoutSec 60
    [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json
}

function Get-LocalModels {
    $dirs = @($env:RL_DOCTOR_ASSETS, (Join-Path $PSScriptRoot '..\..\..\doctor\assets'), (Join-Path $PSScriptRoot '..\..\..\..\doctor-assets')) | Where-Object { $_ -and (Test-Path $_) }
    @($dirs | ForEach-Object { Get-ChildItem $_ -Filter *.gguf -ErrorAction SilentlyContinue } | ForEach-Object { $_.Name } | Sort-Object -Unique)
}

function Get-Snapshots {
    @(Get-ChildItem (Join-Path $Root 'snapshots') -Filter *.tar -ErrorAction SilentlyContinue | ForEach-Object {
        $id = $_.BaseName
        $metaFile = Join-Path $_.DirectoryName "$id.meta.json"
        $meta = if (Test-Path $metaFile) { Get-Content $metaFile -Raw | ConvertFrom-Json } else { $null }
        @{ bench_id = $id; size_gb = [math]::Round($_.Length / 1GB, 2); built_at = $_.LastWriteTimeUtc.ToString('o'); measured = [bool]$meta; ports = @(if ($meta) { $meta.ports | Where-Object { $_ } }) }
    })
}

# New complete lines since the last read; the process keeps the file open.
function Read-NewLines($J) {
    if (-not (Test-Path $J.Log)) { return @() }
    $fs = [IO.File]::Open($J.Log, 'Open', 'Read', 'ReadWrite')
    try {
        if ($fs.Length -le $J.Offset) { return @() }
        $fs.Seek($J.Offset, 'Begin') | Out-Null
        $buf = New-Object byte[] ($fs.Length - $J.Offset)
        $n = $fs.Read($buf, 0, $buf.Length)
        $J.Offset += $n
    } finally { $fs.Dispose() }
    $text = $J.Partial + $utf8.GetString($buf, 0, $n)
    $parts = $text -split "`r?`n"
    $J.Partial = $parts[-1]
    @($parts[0..($parts.Count - 2)] | Where-Object { $_ -ne '' })
}

function Update-Progress($J, [string[]]$Lines) {
    foreach ($l in $Lines) {
        if ($l -match '^PHASE: ([^|]+?)(?: \| (.*))?$') { $J.Progress.phase = $Matches[1]; $J.Progress.msg = $Matches[2]; if ($Matches[1] -ne 'probing') { $J.Progress.Remove('turn'); $J.Progress.Remove('max_turns') } }
        elseif ($l -match 'Turn (\d+) of (\d+)') { $J.Progress.turn = [int]$Matches[1]; $J.Progress.max_turns = [int]$Matches[2] }
    }
}

function Stop-JobTree($J) {
    if ($J.Proc -and -not $J.Proc.HasExited) { & taskkill.exe /T /F /PID $J.Proc.Id 2>&1 | Out-Null }
    $df = Join-Path $J.Work 'distro.txt'
    if (Test-Path $df) {
        $d = (Get-Content $df -Raw).Trim()
        if ($d) { wsl.exe --unregister $d 2>&1 | Out-Null; Remove-Item (Join-Path $Root "distros\$d") -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

function Start-EvalJob($Job) {
    $work = Join-Path $jobsDir $Job._id
    New-Item -ItemType Directory -Force $work | Out-Null
    $jobFile = Join-Path $work 'job.json'
    [IO.File]::WriteAllText($jobFile, ($Job | ConvertTo-Json -Depth 25), $utf8)
    $log = Join-Path $work 'out.log'; $err = Join-Path $work 'err.log'
    $p = Start-Process powershell.exe -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$jobScript`"", '-JobFile', "`"$jobFile`"", '-Root', "`"$Root`"") `
        -RedirectStandardOutput $log -RedirectStandardError $err -WindowStyle Hidden -PassThru
    $running[$Job._id] = @{ Id = $Job._id; Proc = $p; Log = $log; Err = $err; Work = $work; Offset = 0L; Partial = ''; Gpu = [bool]$Job.gpu; Started = $false; Progress = @{ phase = 'starting' }; Label = "$($Job.type) $($Job.bench_id) $($Job.model)".Trim() }
    Write-Line "started $($Job._id): $($running[$Job._id].Label)" 'Cyan'
}

function Complete-EvalJob($J) {
    $lines = @(Read-NewLines $J); if ($J.Partial) { $lines += $J.Partial; $J.Partial = '' }
    $outcomeFile = Join-Path $J.Work 'outcome.json'
    $o = if (Test-Path $outcomeFile) { Get-Content $outcomeFile -Raw -Encoding UTF8 | ConvertFrom-Json } else { $null }
    if (-not $o) {
        $errTail = if (Test-Path $J.Err) { (Get-Content $J.Err -Tail 20) -join "`n" } else { '' }
        $code = try { $J.Proc.ExitCode } catch { '?' }
        $o = [pscustomobject]@{ status = 'failed'; error = "job process exited ($code) without an outcome. $errTail".Trim() }
    }
    Update-Progress $J $lines
    $body = @{ id = $J.Id; status = $o.status; error = $o.error; log = $lines; progress = $J.Progress }
    foreach ($k in 'result', 'snapshot', 'draft', 'ports', 'transcript', 'diagnosis', 'lessons') { if ($o.PSObject.Properties[$k]) { $body[$k] = $o.$k } }
    for ($i = 0; $i -lt 5; $i++) { try { Invoke-Api '/api/worker/complete' $body | Out-Null; break } catch { Start-Sleep 3 } }
    Stop-JobTree $J
    Write-Line "finished $($J.Id): $($o.status)$(if ($o.error) { " - $($o.error)" })" $(if ($o.status -eq 'done') { 'Green' } else { 'Yellow' })
}

# One worker per name: two would report each other's jobs as orphans.
$lock = Join-Path $Root "worker-$Name.pid"
if (Test-Path $lock) {
    $other = Get-CimInstance Win32_Process -Filter "ProcessId=$([int](Get-Content $lock -Raw))" -ErrorAction SilentlyContinue
    if ($other -and $other.CommandLine -match 'Start-EvalWorker') { throw "worker $Name is already running (PID $($other.ProcessId)); stop it first or pass -Name" }
}
Set-Content $lock $PID

# Job processes of an earlier worker that was killed (not stopped with
# Ctrl+C) keep running with their output in out.log: adopt them, so a worker
# restart loses no work. Jobs whose process is gone failed.
$adopted = @{}
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object { $_.CommandLine -match 'Invoke-EvalJob\.ps1' -and -not (Get-Process -Id $_.ParentProcessId -ErrorAction SilentlyContinue) } | ForEach-Object {
    $proc = Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue
    $jobFile = if ($_.CommandLine -match '-JobFile\s+"([^"]+)"') { $Matches[1] } else { '' }
    if (-not $proc -or -not $jobFile -or -not (Test-Path $jobFile)) { Write-Line "killing orphaned job process $($_.ProcessId)" 'Yellow'; & taskkill.exe /T /F /PID $_.ProcessId 2>&1 | Out-Null; return }
    $job = [IO.File]::ReadAllText($jobFile) | ConvertFrom-Json
    $work = Split-Path $jobFile
    $J = @{ Id = $job._id; Proc = $proc; Log = (Join-Path $work 'out.log'); Err = (Join-Path $work 'err.log'); Work = $work; Offset = 0L; Partial = ''; Gpu = [bool]$job.gpu; Started = $true; Progress = @{ phase = 'running' }; Label = "$($job.type) $($job.bench_id) $($job.model)".Trim() }
    Update-Progress $J @(Read-NewLines $J)
    $running[$job._id] = $J; $adopted[$work] = $true
    Write-Line "adopted $($job._id): $($J.Label) (still running from the previous worker)" 'Cyan'
}
foreach ($d in Get-ChildItem $jobsDir -Directory -ErrorAction SilentlyContinue) {
    if (-not $adopted[$d.FullName] -and (Test-Path (Join-Path $d.FullName 'distro.txt')) -and -not (Test-Path (Join-Path $d.FullName 'outcome.json'))) {
        Stop-JobTree @{ Proc = $null; Work = $d.FullName }
        Set-Content (Join-Path $d.FullName 'outcome.json') '{"status":"failed","error":"worker restarted"}'
    }
}

Write-Line "worker $Name -> $Server, $Slots slots ($GpuSlots for local models)$(if ($Types) { ", types $($Types -join ',')" })" 'Cyan'
$lastBeat = [datetime]::MinValue
try {
    while ($true) {
        try {
            foreach ($J in @($running.Values)) {
                if ($J.Proc.HasExited) { Complete-EvalJob $J; $running.Remove($J.Id); continue }
                $lines = @(Read-NewLines $J)
                Update-Progress $J $lines
                $body = @{ id = $J.Id; progress = $J.Progress; log = $lines }
                if (-not $J.Started) { $body.status = 'running'; $J.Started = $true }
                $df = Join-Path $J.Work 'distro.txt'
                if (Test-Path $df) { $body.distro = (Get-Content $df -Raw).Trim() }
                $resp = Invoke-Api '/api/worker/progress' $body
                if ($resp.cancel) { Write-Line "cancelling $($J.Id)" 'Yellow'; Stop-JobTree $J }
            }

            if (((Get-Date) - $lastBeat).TotalSeconds -ge 10) {
                $beat = Invoke-Api '/api/worker/heartbeat' @{ worker = $Name; host = $env:COMPUTERNAME; slots = $Slots; gpu_slots = $GpuSlots; running = @($running.Keys)
                    local_models = @(Get-LocalModels); snapshots = @(Get-Snapshots); snapshot_root = $Root }
                foreach ($id in @($beat.cancel)) { if ($id -and $running[$id]) { Write-Line "cancelling $id" 'Yellow'; Stop-JobTree $running[$id] } }
                $lastBeat = Get-Date
            }

            while ($running.Count -lt $Slots) {
                $gpuUsed = @($running.Values | Where-Object { $_.Gpu }).Count
                $claim = Invoke-Api '/api/worker/claim' @{ worker = $Name; free_slots = $Slots - $running.Count; gpu_free = ($gpuUsed -lt $GpuSlots); types = $Types }
                if (-not $claim.job) { break }
                Start-EvalJob $claim.job
            }
        } catch {
            Write-Line "console unreachable or error: $($_.Exception.Message)" 'Red'
            Start-Sleep 5
        }
        Start-Sleep $PollSeconds
    }
} finally {
    if ($running.Count) { Write-Line "stopping $($running.Count) running jobs" 'Yellow' }
    foreach ($J in @($running.Values)) { Stop-JobTree $J }
    Remove-Item $lock -ErrorAction SilentlyContinue
}
