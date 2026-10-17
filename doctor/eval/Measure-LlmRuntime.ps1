<#
.SYNOPSIS
    Starts llama-server with one model on one backend and measures load time,
    generation speed, memory and two-turn tool calling.

.DESCRIPTION
    Phase-1 spike for the install doctor (docs/install-doctor-plan.md).
    AssetsDir holds the llama.cpp release zips and the .gguf files; each zip
    is unpacked once into AssetsDir\llama\<backend>. Results are appended to
    AssetsDir\runtime-results.jsonl.

.EXAMPLE
    .\Measure-LlmRuntime.ps1 -Model gpt-oss-20b-MXFP4.gguf -Backend cuda
    .\Measure-LlmRuntime.ps1 -Model Qwen3.5-9B-Q4_K_M.gguf -Backend cpu -Runs 1
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Model,
    [ValidateSet('cuda', 'vulkan', 'cpu')][string]$Backend = 'cuda',
    [string]$AssetsDir = '',
    [string]$Build = 'b9771',
    [int]$Context = 32768,
    [int]$GpuLayers = 999,
    [int]$Runs = 3,
    [int]$LoadTimeoutSec = 600
)

$ErrorActionPreference = 'Stop'
if (-not $AssetsDir) { $AssetsDir = Join-Path $PSScriptRoot '..\..\..\..\..\doctor-assets' }
$AssetsDir = (Resolve-Path $AssetsDir).Path
$modelPath = if (Test-Path $Model) { (Resolve-Path $Model).Path } else { Join-Path $AssetsDir $Model }
if (-not (Test-Path $modelPath)) { throw "Model not found: $modelPath" }

function Install-Backend([string]$Name) {
    $dir = Join-Path $AssetsDir "llama\$Name"
    if (Test-Path (Join-Path $dir 'llama-server.exe')) { return $dir }
    $zips = switch ($Name) {
        'cuda'   { @("llama-$Build-bin-win-cuda-12.4-x64.zip", 'cudart-llama-bin-win-cuda-12.4-x64.zip') }
        'vulkan' { @("llama-$Build-bin-win-vulkan-x64.zip") }
        'cpu'    { @("llama-$Build-bin-win-cpu-x64.zip") }
    }
    New-Item -ItemType Directory -Force $dir | Out-Null
    foreach ($z in $zips) { Expand-Archive -LiteralPath (Join-Path $AssetsDir $z) -DestinationPath $dir -Force }
    $exe = Get-ChildItem $dir -Recurse -Filter 'llama-server.exe' | Select-Object -First 1
    if (-not $exe) { throw "llama-server.exe not found after unpacking $($zips -join ', ')" }
    if ($exe.DirectoryName -ne $dir) { Get-ChildItem $exe.DirectoryName | Move-Item -Destination $dir -Force }
    $dir
}

# A loopback port outside Windows' excluded (Hyper-V/WSL) ranges.
function Get-FreePort {
    $ranges = @(& netsh interface ipv4 show excludedportrange protocol=tcp | ForEach-Object {
        if ($_ -match '^\s*(\d+)\s+(\d+)') { , @([int]$Matches[1], [int]$Matches[2]) } })
    foreach ($p in 18080..18180) {
        if ($ranges | Where-Object { $p -ge $_[0] -and $p -le $_[1] }) { continue }
        $l = New-Object System.Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $p)
        try { $l.Start(); $l.Stop(); return $p } catch { }
    }
    throw 'no free loopback port in 18080-18180'
}

function Get-GpuMemoryMB {
    try { [int](& nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits | Select-Object -First 1) } catch { $null }
}

# Windows PowerShell decodes responses without a charset as Latin-1, which
# garbles the Unicode hyphens and quotes models like to emit.
function Invoke-Chat($Body) {
    $r = Invoke-WebRequest -UseBasicParsing -Uri "$base/v1/chat/completions" -Method Post -ContentType 'application/json; charset=utf-8' -TimeoutSec 600 `
        -Body ([Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 12)))
    [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json
}

function ConvertTo-PlainText([string]$Text) {
    $Text -replace '[\u2010-\u2015\u2212]', '-' -replace '[\u2018\u2019]', "'" -replace '[\u201C\u201D]', '"'
}

$dir = Install-Backend $Backend
$port = Get-FreePort
$base = "http://127.0.0.1:$port"
$log = Join-Path $AssetsDir "llama-server-$Backend.log"
$serverArgs = @('-m', "`"$modelPath`"", '--host', '127.0.0.1', '--port', $port, '-c', $Context, '--jinja', '--no-webui')
if ($Backend -ne 'cpu') { $serverArgs += @('-ngl', $GpuLayers, '-fa', 'on') }

$gpuBefore = Get-GpuMemoryMB
$sw = [Diagnostics.Stopwatch]::StartNew()
$proc = Start-Process -FilePath (Join-Path $dir 'llama-server.exe') -ArgumentList $serverArgs -PassThru -WindowStyle Hidden `
    -RedirectStandardError $log -RedirectStandardOutput "$log.out"
$result = [ordered]@{ at = (Get-Date).ToString('s'); model = (Split-Path $modelPath -Leaf); backend = $Backend; build = $Build; context = $Context }
try {
    $ready = $false
    while ($sw.Elapsed.TotalSeconds -lt $LoadTimeoutSec) {
        if ($proc.HasExited) { throw "llama-server exited (code $($proc.ExitCode)); see $log" }
        try { if ((Invoke-RestMethod "$base/health" -TimeoutSec 5).status -eq 'ok') { $ready = $true; break } } catch { }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) { throw "llama-server not ready after $LoadTimeoutSec s; see $log" }
    $result.load_seconds = [math]::Round($sw.Elapsed.TotalSeconds, 1)
    $result.gpu_mb_used = if ($null -ne $gpuBefore) { (Get-GpuMemoryMB) - $gpuBefore } else { $null }
    $result.ram_mb = [math]::Round((Get-Process -Id $proc.Id).WorkingSet64 / 1MB)

    # Generation speed: a fixed prompt, a bounded answer.
    $speeds = @(); $promptSpeeds = @()
    for ($i = 0; $i -lt $Runs; $i++) {
        $r = Invoke-Chat @{ messages = @(@{ role = 'user'; content = 'Explain in about 150 words why a Docker container might be unable to resolve the hostname of another service in the same compose project.' }); max_tokens = 300; temperature = 0 }
        if ($r.PSObject.Properties.Name -contains 'timings') { $speeds += $r.timings.predicted_per_second; $promptSpeeds += $r.timings.prompt_per_second }
    }
    if ($speeds) {
        $result.gen_tok_s = [math]::Round(($speeds | Measure-Object -Average).Average, 1)
        $result.prompt_tok_s = [math]::Round(($promptSpeeds | Measure-Object -Average).Average, 1)
    }

    # Two-turn tool calling: the model must pick a tool with valid JSON
    # arguments, then use the tool's result.
    $tools = @(
        @{ type = 'function'; function = @{ name = 'compose_ps'; description = 'List the docker compose services and their state.'; parameters = @{ type = 'object'; properties = @{}; required = @() } } },
        @{ type = 'function'; function = @{ name = 'service_logs'; description = 'Return the last lines of one compose service log.'; parameters = @{ type = 'object'; properties = @{ service = @{ type = 'string' }; lines = @{ type = 'integer' } }; required = @('service') } } },
        @{ type = 'function'; function = @{ name = 'finish'; description = 'Report the root cause and the fix.'; parameters = @{ type = 'object'; properties = @{ root_cause = @{ type = 'string' }; fix = @{ type = 'string' } }; required = @('root_cause', 'fix') } } }
    )
    $messages = [System.Collections.ArrayList]@(
        @{ role = 'system'; content = 'You diagnose a Windows + Docker Compose install. Use the tools to gather evidence before concluding. Call finish when you know the root cause.' },
        @{ role = 'user'; content = 'A training job fails with: DNS resolution failed for fly-brain:50061: Domain name not found. Find the cause.' }
    )
    $toolTurns = 0; $validCalls = 0; $finished = $null; $nudges = 0; $lastText = ''
    $toolSw = [Diagnostics.Stopwatch]::StartNew()
    for ($turn = 0; $turn -lt 8 -and -not $finished; $turn++) {
        $r = Invoke-Chat @{ messages = $messages; tools = $tools; temperature = 0; max_tokens = 2048 }
        $msg = $r.choices[0].message
        $calls = if ($msg.PSObject.Properties.Name -contains 'tool_calls' -and $msg.tool_calls) { @($msg.tool_calls) } else { @() }
        if (-not $calls) {
            $lastText = "$($msg.content)"
            if ($nudges -ge 1) { break }
            $nudges++
            [void]$messages.Add(@{ role = 'assistant'; content = $lastText })
            [void]$messages.Add(@{ role = 'user'; content = 'Report your conclusion by calling the finish tool.' })
            continue
        }
        $echo = @($calls | ForEach-Object { @{ id = $_.id; type = 'function'; function = @{ name = $_.function.name; arguments = $_.function.arguments } } })
        [void]$messages.Add(@{ role = 'assistant'; content = ''; tool_calls = $echo })
        foreach ($c in $calls) {
            $toolTurns++
            $argsOk = $true; $a = $null
            try { $a = $c.function.arguments | ConvertFrom-Json } catch { $argsOk = $false }
            if ($argsOk -and ($tools.function.name -contains $c.function.name)) { $validCalls++ }
            $output = switch ($c.function.name) {
                'compose_ps'   { "SERVICE         STATE`nros-server      running`nmongo           running`ndashboard       running`nsim-controller  running" }
                'service_logs' { if ($a -and $a.service -match 'fly') { 'no such service container: fly-brain (service defined in docker-compose.yml but never created)' } else { '(nothing unusual)' } }
                'finish'       { $finished = $a; 'ok' }
                default        { 'unknown tool' }
            }
            [void]$messages.Add(@{ role = 'tool'; tool_call_id = $c.id; content = $output })
        }
    }
    $result.tool_calls = $toolTurns
    $result.tool_calls_valid = $validCalls
    $result.tool_nudges = $nudges
    $result.tool_last_text = if ($lastText.Length -gt 300) { $lastText.Substring(0, 300) } else { $lastText }
    $result.tool_finished = [bool]$finished
    $cause = if ($finished) { ConvertTo-PlainText "$($finished.root_cause)" } else { '' }
    $result.tool_root_cause = if ($finished) { $cause } else { $null }
    # Correct means "defined but not running" (fix: start it). "Not defined"
    # alone points at the wrong fix (adding a service), so it does not count.
    $result.tool_correct = [bool]($cause -match 'fly-?brain' -and $cause -match 'not running|isn''t running|not (listed )?among the running|never (been )?created|not created|no container|not started|never started|stopped|exited|is down')
    $result.tool_seconds = [math]::Round($toolSw.Elapsed.TotalSeconds, 1)
} catch {
    $result.error = "$_"
} finally {
    if (-not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
}

$json = [pscustomobject]$result | ConvertTo-Json -Compress
Add-Content -Path (Join-Path $AssetsDir 'runtime-results.jsonl') -Value $json
[pscustomobject]$result | Format-List
