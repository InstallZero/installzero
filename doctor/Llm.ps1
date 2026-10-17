# Local LLM runtime for the install doctor: finds the bundled llama.cpp builds
# and models, picks a backend and model for this machine, and runs
# llama-server on loopback. Dot-source this file.
#
# Assets folder layout (zips are unpacked into llama\<backend> on first use):
#   llama-<build>-bin-win-cuda-12.4-x64.zip, cudart-llama-bin-win-cuda-12.4-x64.zip
#   llama-<build>-bin-win-vulkan-x64.zip, llama-<build>-bin-win-cpu-x64.zip
#   *.gguf

$script:LlamaBuild = 'b9771'

# Preference order. MinVramMB: free GPU memory needed to run fully on the GPU
# at 32K context. MinRamMB: system RAM needed to run on the CPU.
$script:DoctorModels = @(
    @{ File = 'gpt-oss-20b-MXFP4.gguf';     MinVramMB = 13500; MinRamMB = 24000 },
    @{ File = 'gemma-4-E4B-it-Q4_K_M.gguf'; MinVramMB = 4800;  MinRamMB = 8000 },
    @{ File = 'Qwen3.5-9B-Q4_K_M.gguf';     MinVramMB = 7000;  MinRamMB = 12000 },
    @{ File = 'granite-4.1-8b-Q4_K_M.gguf'; MinVramMB = 11000; MinRamMB = 14000 }
)

function Find-DoctorAssets {
    $candidates = @(
        $env:RL_DOCTOR_ASSETS,
        (Join-Path $PSScriptRoot 'assets'),
        (Join-Path $PSScriptRoot '..\..\..\..\doctor-assets')
    ) | Where-Object { $_ }
    foreach ($c in $candidates) {
        if ((Test-Path $c) -and (Get-ChildItem $c -Filter *.gguf -ErrorAction SilentlyContinue)) { return (Resolve-Path $c).Path }
    }
    throw "No doctor assets (llama.cpp builds and .gguf models) found. Looked in: $($candidates -join '; '). Set RL_DOCTOR_ASSETS."
}

function Get-GpuInfo {
    $smi = Get-Command nvidia-smi -ErrorAction SilentlyContinue
    if (-not $smi) { return $null }
    try {
        $out = @(& $smi.Source --query-gpu=name,driver_version,memory.total,memory.free --format=csv,noheader,nounits 2>$null)
        if ($LASTEXITCODE -ne 0 -or -not $out) { return $null }
        $line = $out[0]
        $p = $line -split ',\s*'
        [pscustomobject]@{ Name = $p[0]; Driver = [version]$p[1]; TotalMB = [int]$p[2]; FreeMB = [int]$p[3] }
    } catch { $null }
}

# CUDA 12.4 needs driver 551.61 or newer on Windows.
function Select-LlmBackend([string]$AssetsDir) {
    $have = {
        param($b, $zip)
        (Test-Path (Join-Path $AssetsDir "llama\$b\llama-server.exe")) -or (Test-Path (Join-Path $AssetsDir $zip))
    }
    $gpu = Get-GpuInfo
    if ($gpu -and $gpu.Driver -ge [version]'551.61' -and (& $have 'cuda' "llama-$script:LlamaBuild-bin-win-cuda-12.4-x64.zip")) { return 'cuda' }
    $hasVulkan = Test-Path (Join-Path ([Environment]::SystemDirectory) 'vulkan-1.dll')
    if ($hasVulkan -and (& $have 'vulkan' "llama-$script:LlamaBuild-bin-win-vulkan-x64.zip")) { return 'vulkan' }
    'cpu'
}

function Select-DoctorModel([string]$AssetsDir, [string]$Backend) {
    $ramMB = [int]((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1MB)
    $gpu = if ($Backend -ne 'cpu') { Get-GpuInfo } else { $null }
    $present = @($script:DoctorModels | Where-Object { Test-Path (Join-Path $AssetsDir $_.File) })
    if (-not $present) { throw "No known model file in $AssetsDir (expected one of: $($script:DoctorModels.File -join ', '))." }
    # A non-NVIDIA GPU (Vulkan) reports no free memory: take the smallest.
    if ($Backend -ne 'cpu' -and -not $gpu) { return ($present | Sort-Object { $_.MinVramMB } | Select-Object -First 1).File }
    foreach ($m in $present) {
        if ($gpu -and $gpu.FreeMB -ge $m.MinVramMB) { return $m.File }
        if (-not $gpu -and $ramMB -ge $m.MinRamMB) { return $m.File }
    }
    # Nothing fits comfortably: the smallest model still runs, slowly.
    ($present | Sort-Object { (Get-Item (Join-Path $AssetsDir $_.File)).Length } | Select-Object -First 1).File
}

function Install-LlamaBackend([string]$AssetsDir, [string]$Backend) {
    $dir = Join-Path $AssetsDir "llama\$Backend"
    if (Test-Path (Join-Path $dir 'llama-server.exe')) { return $dir }
    $zips = switch ($Backend) {
        'cuda'   { @("llama-$script:LlamaBuild-bin-win-cuda-12.4-x64.zip", 'cudart-llama-bin-win-cuda-12.4-x64.zip') }
        'vulkan' { @("llama-$script:LlamaBuild-bin-win-vulkan-x64.zip") }
        'cpu'    { @("llama-$script:LlamaBuild-bin-win-cpu-x64.zip") }
    }
    New-Item -ItemType Directory -Force $dir | Out-Null
    foreach ($z in $zips) { Expand-Archive -LiteralPath (Join-Path $AssetsDir $z) -DestinationPath $dir -Force }
    $exe = Get-ChildItem $dir -Recurse -Filter 'llama-server.exe' | Select-Object -First 1
    if (-not $exe) { throw "llama-server.exe not found after unpacking $($zips -join ', ')" }
    if ($exe.DirectoryName -ne $dir) { Get-ChildItem $exe.DirectoryName | Move-Item -Destination $dir -Force }
    $dir
}

# A loopback port outside Windows' excluded (Hyper-V/WSL) ranges.
function Get-FreeLoopbackPort([int]$From = 18080, [int]$To = 18180) {
    $ranges = @(& netsh interface ipv4 show excludedportrange protocol=tcp | ForEach-Object {
        if ($_ -match '^\s*(\d+)\s+(\d+)') { , @([int]$Matches[1], [int]$Matches[2]) } })
    foreach ($p in $From..$To) {
        if ($ranges | Where-Object { $p -ge $_[0] -and $p -le $_[1] }) { continue }
        $l = New-Object System.Net.Sockets.TcpListener([Net.IPAddress]::Loopback, $p)
        try { $l.Start(); $l.Stop(); return $p } catch { }
    }
    throw "No free loopback port in $From-$To"
}

function Start-LlmServer {
    param(
        [string]$AssetsDir = (Find-DoctorAssets),
        [string]$Model = '',
        [string]$Backend = '',
        [int]$Context = 32768,
        [int]$TimeoutSec = 600
    )
    if (-not $Backend) { $Backend = Select-LlmBackend $AssetsDir }
    if (-not $Model) { $Model = Select-DoctorModel $AssetsDir $Backend }
    $modelPath = if (Test-Path $Model) { (Resolve-Path $Model).Path } else { Join-Path $AssetsDir $Model }
    if (-not (Test-Path $modelPath)) { throw "Model not found: $modelPath" }

    $dir = Install-LlamaBackend $AssetsDir $Backend
    $port = Get-FreeLoopbackPort
    $log = Join-Path $AssetsDir "llama-server-$Backend.log"
    $serverArgs = @('-m', "`"$modelPath`"", '--host', '127.0.0.1', '--port', $port, '-c', $Context, '--jinja', '--no-webui')
    if ($Backend -ne 'cpu') { $serverArgs += @('-ngl', '999', '-fa', 'on') }
    $proc = Start-Process -FilePath (Join-Path $dir 'llama-server.exe') -ArgumentList $serverArgs -PassThru -WindowStyle Hidden `
        -RedirectStandardError $log -RedirectStandardOutput "$log.out"
    $url = "http://127.0.0.1:$port"
    $sw = [Diagnostics.Stopwatch]::StartNew()
    while ($sw.Elapsed.TotalSeconds -lt $TimeoutSec) {
        if ($proc.HasExited) { throw "llama-server exited (code $($proc.ExitCode)); see $log" }
        try { if ((Invoke-RestMethod "$url/health" -TimeoutSec 5).status -eq 'ok') {
            return [pscustomobject]@{ Process = $proc; Url = $url; Model = (Split-Path $modelPath -Leaf); Backend = $Backend; Log = $log; LoadSeconds = [math]::Round($sw.Elapsed.TotalSeconds, 1) }
        } } catch { }
        Start-Sleep -Milliseconds 500
    }
    Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    throw "llama-server not ready after $TimeoutSec s; see $log"
}

function Stop-LlmServer($Server) {
    if ($Server -and $Server.Process -and -not $Server.Process.HasExited) { Stop-Process -Id $Server.Process.Id -Force -ErrorAction SilentlyContinue }
}

# Hosted models, for evaluation only (the shipped doctor runs offline). All
# serve OpenAI-compatible chat completions under Url. Spec: "provider:model-id".
$script:RemoteProviders = @{
    anthropic = @{ Url = 'https://api.anthropic.com/v1'; KeyVars = @('ANTHROPIC_API_KEY') }
    openai    = @{ Url = 'https://api.openai.com/v1';    KeyVars = @('OPENAI_API_KEY') }
    xai       = @{ Url = 'https://api.x.ai/v1';          KeyVars = @('XAI_API_KEY') }
    google    = @{ Url = 'https://generativelanguage.googleapis.com/v1beta/openai'; KeyVars = @('GOOGLE_GEMINI_API_KEY', 'GEMINI_API_KEY', 'GOOGLE_API_KEY') }
}

function Test-RemoteSpec([string]$Spec) { $Spec -match '^(anthropic|openai|xai|google):.+' }

# Environment first, then the repo's .env (where the stack keeps its keys).
function Get-ApiKey([string]$Provider) {
    $vars = $script:RemoteProviders[$Provider].KeyVars
    foreach ($var in $vars) {
        foreach ($scope in 'Process', 'User', 'Machine') { $k = [Environment]::GetEnvironmentVariable($var, $scope); if ($k) { return $k } }
    }
    $repo = if ($script:DoctorRepoDir) { $script:DoctorRepoDir } else { Join-Path $PSScriptRoot '..\..\..' }
    $envFile = Join-Path $repo '.env'
    if (Test-Path $envFile) {
        foreach ($line in Get-Content $envFile) {
            if ($line -match '^\s*([A-Za-z0-9_]+)\s*=\s*(.*)$' -and $vars -contains $Matches[1]) {
                $k = $Matches[2].Trim().Trim('"').Trim("'")
                if ($k) { return $k }
            }
        }
    }
    throw "Set $($vars[0]) (environment or .env) to use $Provider models."
}

function Get-RemoteLlm([string]$Spec) {
    if (-not (Test-RemoteSpec $Spec)) { throw "Remote model spec must be provider:model-id with provider anthropic, openai, xai or google; got '$Spec'." }
    $provider, $model = $Spec -split ':', 2
    [pscustomobject]@{ Provider = $provider; Model = $model; Url = $script:RemoteProviders[$provider].Url; ApiKey = (Get-ApiKey $provider); Backend = 'remote' }
}

function Get-LlmHeaders($Llm) {
    if (-not $Llm -or -not $Llm.ApiKey) { return @{} }
    $h = @{ Authorization = "Bearer $($Llm.ApiKey)" }
    if ($Llm.Provider -eq 'anthropic') { $h['x-api-key'] = $Llm.ApiKey; $h['anthropic-version'] = '2023-06-01' }
    $h
}

function Get-RemoteModelIds([string]$Provider) {
    $llm = [pscustomobject]@{ Provider = $Provider; ApiKey = (Get-ApiKey $Provider) }
    $r = Invoke-WebRequest -UseBasicParsing -Uri "$($script:RemoteProviders[$Provider].Url)/models" -Headers (Get-LlmHeaders $llm) -TimeoutSec 30
    ([Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json).data.id | Sort-Object
}

# Hosted reasoning models reject sampling parameters (OpenAI also rejects
# max_tokens), so they get only a token limit.
function New-ChatBody($Llm, $Messages, $Tools, [double]$Temperature, [int]$MaxTokens) {
    $provider = if ($Llm -and $Llm.Provider) { $Llm.Provider } else { 'local' }
    $body = @{ messages = $Messages; tools = $Tools }
    switch ($provider) {
        'openai'    { $body.model = $Llm.Model; $body.max_completion_tokens = $MaxTokens * 4 }
        'xai'       { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        'anthropic' { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        'google'    { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        default     { $body.temperature = $Temperature; $body.max_tokens = $MaxTokens }
    }
    $body
}

# Windows PowerShell decodes responses without a charset as Latin-1, which
# garbles the Unicode hyphens and quotes models emit; decode as UTF-8.
function Invoke-LlmChat([string]$Url, $Body, [int]$TimeoutSec = 600, [hashtable]$Headers = @{}) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20))
    $endpoint = if ($Url -match '/(v1|openai)$') { "$Url/chat/completions" } else { "$Url/v1/chat/completions" }
    $r = Invoke-WebRequest -UseBasicParsing -Uri $endpoint -Method Post -ContentType 'application/json; charset=utf-8' -TimeoutSec $TimeoutSec -Body $bytes -Headers $Headers
    [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json
}

# OpenAI reasoning models only take tools with reasoning on the Responses API.
# Converts the chat-format conversation to Responses input and the reply back
# to a chat-format message. The reply's raw output items (reasoning and
# function calls) ride along on the message as _responses_items and are sent
# back verbatim, which keeps the model's reasoning across turns.
function Invoke-OpenAIResponses($Llm, $Messages, $Tools, [int]$MaxTokens, [int]$TimeoutSec = 600) {
    $instructions = ''
    $items = [System.Collections.ArrayList]@()
    foreach ($m in $Messages) {
        switch ($m.role) {
            'system' { $instructions = $m.content }
            'user' { [void]$items.Add(@{ role = 'user'; content = $m.content }) }
            'tool' { [void]$items.Add(@{ type = 'function_call_output'; call_id = $m.tool_call_id; output = $m.content }) }
            'assistant' {
                if ($m.ContainsKey('_responses_items')) { foreach ($i in $m._responses_items) { [void]$items.Add($i) } }
                else {
                    if ($m.content) { [void]$items.Add(@{ role = 'assistant'; content = $m.content }) }
                    foreach ($c in @($m.tool_calls)) { if ($c) { [void]$items.Add(@{ type = 'function_call'; call_id = $c.id; name = $c.function.name; arguments = $c.function.arguments }) } }
                }
            }
        }
    }
    $body = @{
        model = $Llm.Model; instructions = $instructions; input = $items; max_output_tokens = $MaxTokens * 4
        tools = @($Tools | ForEach-Object { @{ type = 'function'; name = $_.function.name; description = $_.function.description; parameters = $_.function.parameters } })
    }
    $bytes = [Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Depth 30))
    $r = Invoke-WebRequest -UseBasicParsing -Uri "$($Llm.Url)/responses" -Method Post -ContentType 'application/json; charset=utf-8' -TimeoutSec $TimeoutSec -Body $bytes -Headers (Get-LlmHeaders $Llm)
    $resp = [Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray()) | ConvertFrom-Json
    $text = ''
    $calls = @()
    foreach ($item in @($resp.output)) {
        if ($item.type -eq 'message') { $text += (@($item.content | Where-Object { $_.type -eq 'output_text' }).text -join "`n") }
        if ($item.type -eq 'function_call') { $calls += [pscustomobject]@{ id = $item.call_id; type = 'function'; function = [pscustomobject]@{ name = $item.name; arguments = $item.arguments } } }
    }
    $msg = [pscustomobject]@{ content = $text; _responses_items = @($resp.output) }
    if ($calls) { $msg | Add-Member tool_calls $calls }
    [pscustomobject]@{ choices = @([pscustomobject]@{ message = $msg }); usage = [pscustomobject]@{ prompt_tokens = $resp.usage.input_tokens; completion_tokens = $resp.usage.output_tokens } }
}

# One model turn, whatever the backend.
function Invoke-ChatTurn($Llm, [string]$Url, $Messages, $Tools, [double]$Temperature, [int]$MaxTokens) {
    if ($Llm -and $Llm.Provider -eq 'openai') { return Invoke-OpenAIResponses $Llm $Messages $Tools $MaxTokens }
    Invoke-LlmChat $Url (New-ChatBody $Llm $Messages $Tools $Temperature $MaxTokens) -Headers (Get-LlmHeaders $Llm)
}

function ConvertTo-PlainText([string]$Text) {
    $Text -replace '[\u2010-\u2015\u2212]', '-' -replace '[\u2018\u2019]', "'" -replace '[\u201C\u201D]', '"' -replace '[\u00A0\u2007\u2009\u202F]', ' '
}
