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
        (Join-Path $PSScriptRoot '..\..\doctor-assets')
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
    hf        = @{ Url = ''; KeyVars = @('HF_TOKEN', 'HUGGING_FACE_HUB_TOKEN') }
}

function Test-RemoteSpec([string]$Spec) { $Spec -match '^(anthropic|openai|xai|google|hf):.+' }

# "hf:<file>.gguf": a local GGUF served by llama-server on Hugging Face Jobs
# (agent\eval\sft\hf_serve.py starts one or reuses the running one), for
# evaluation runs that should not wait for this machine's GPU. Requests are
# sent as to a local server; the HF token opens the Jobs proxy.
function Get-HfServedLlm([string]$File) {
    $serve = Join-Path $PSScriptRoot '..\agent\eval\sft\hf_serve.py'
    $py = Join-Path $env:LOCALAPPDATA 'installzero\sft\venv\Scripts\python.exe'
    if (-not (Test-Path $py)) { throw "hf: models need the training venv ($py); run a training job once to create it." }
    $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    try { $out = @(& $py $serve ensure --model $File --assets (Find-DoctorAssets) 2>&1 | ForEach-Object { "$_" }) } finally { $ErrorActionPreference = $old }
    $url = @($out | Where-Object { $_ -match '^https://' })[-1]
    if ($LASTEXITCODE -or -not $url) { throw "No Hugging Face server for ${File}: $(($out | Select-Object -Last 3) -join ' ')" }
    $out | Where-Object { $_ -notmatch '^https://' } | ForEach-Object { Write-Host "  $_" }
    [pscustomobject]@{ Provider = 'hf'; Model = $File; Url = $url.Trim(); ApiKey = (Get-ApiKey 'hf'); Backend = 'remote' }
}

# Environment first, then the project's .env (where the stack keeps its keys),
# then InstallZero's own .env.
function Get-ApiKey([string]$Provider) {
    $vars = $script:RemoteProviders[$Provider].KeyVars
    foreach ($var in $vars) {
        foreach ($scope in 'Process', 'User', 'Machine') { $k = [Environment]::GetEnvironmentVariable($var, $scope); if ($k) { return $k } }
    }
    $envFiles = @($script:DoctorRepoDir, (Join-Path $PSScriptRoot '..')) | Where-Object { $_ } | ForEach-Object { Join-Path $_ '.env' }
    foreach ($envFile in $envFiles) {
        if (-not (Test-Path $envFile)) { continue }
        foreach ($line in Get-Content $envFile) {
            if ($line -match '^\s*([A-Za-z0-9_]+)\s*=\s*(.*)$' -and $vars -contains $Matches[1]) {
                $k = $Matches[2].Trim().Trim('"').Trim("'")
                if ($k) { return $k }
            }
        }
    }
    throw "Set $($vars[0]) (environment, the project's .env or InstallZero's .env) to use $Provider models."
}

function Get-RemoteLlm([string]$Spec) {
    if (-not (Test-RemoteSpec $Spec)) { throw "Remote model spec must be provider:model-id with provider anthropic, openai, xai, google or hf; got '$Spec'." }
    $provider, $model = $Spec -split ':', 2
    if ($provider -eq 'hf') { return Get-HfServedLlm $model }
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

# Request settings a GGUF needs, from <file>.iz.json next to it in the doctor
# assets ({"request": {...}}, merged into every chat request); $null if none.
# A fine-tune trained without reasoning carries
# {"request": {"chat_template_kwargs": {"enable_thinking": false}}}.
function Get-LlmModelSettings([string]$Model) {
    if (-not $Model) { return $null }
    $path = if (Test-Path $Model -PathType Leaf) { "$Model.iz.json" } else { Join-Path (Find-DoctorAssets) "$(Split-Path $Model -Leaf).iz.json" }
    if (-not (Test-Path $path)) { return $null }
    (Get-Content $path -Raw | ConvertFrom-Json).request
}

# Hosted reasoning models reject sampling parameters (OpenAI also rejects
# max_tokens), so they get only a token limit. $Extra (an object) adds or
# overrides request fields. llama-server's DRY sampling is left off: an
# install.md repeats {{REPO_DIR}} paths, block fences and commands by design,
# and penalizing repeats bent them into variants ({{REPO_DIR}}xxx) that the
# model then looped on; Invoke-LlmChatStream stops real loops instead.
function New-ChatBody($Llm, $Messages, $Tools, [double]$Temperature, [int]$MaxTokens, $Extra = $null) {
    $provider = if ($Llm -and $Llm.Provider) { $Llm.Provider } else { 'local' }
    $body = @{ messages = @($Messages); tools = @($Tools) }
    switch ($provider) {
        'openai'    { $body.model = $Llm.Model; $body.max_completion_tokens = $MaxTokens * 4 }
        'xai'       { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        'anthropic' { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        'google'    { $body.model = $Llm.Model; $body.max_tokens = $MaxTokens * 4 }
        default     { $body.temperature = $Temperature; $body.max_tokens = $MaxTokens }
    }
    if ($Extra) { foreach ($p in $Extra.PSObject.Properties) { $body[$p.Name] = $p.Value } }
    $body
}

# The repeating unit when $Text ends in one chunk (up to 400 characters)
# repeated back to back over at least 400 characters and 6 repetitions; else $null.
function Find-RepeatLoop([string]$Text) {
    $n = $Text.Length
    for ($p = 1; $p -le 400; $p++) {
        $span = [math]::Max(400, 6 * $p)
        if ($span -gt $n) { break }
        if ([string]::CompareOrdinal($Text, $n - $span, $Text, $n - $span + $p, $span - $p) -eq 0) { return $Text.Substring($n - $p) }
    }
    $null
}

# The last line when $Text ends in lines that follow one template: all but
# 10% of the last max(40, 4k) lines (of 3+ words; JSON-escaped \n counts as a
# line break) share at least 60% of their words with the line k before, for
# some k up to 24. This catches loops that vary a word each time (a counter,
# a path segment), which Find-RepeatLoop misses; a real list of near-identical
# lines (a verify block checking 25 packages) stays under 40. Else $null.
function Find-TemplateLoop([string]$Text, [int]$MinLines = 40, [int]$MaxBlock = 24) {
    $lines = @($Text -split '\r?\n|\\n' | ForEach-Object { , @($_ -split '[^\p{L}\p{N}]+' | Where-Object { $_ }) } | Where-Object { $_.Count -ge 3 })
    $n = $lines.Count
    if ($n -lt $MinLines) { return $null }
    $sim = {
        param($a, $b)
        $counts = @{}; foreach ($w in $a) { $counts[$w] = 1 + [int]$counts[$w] }
        $common = 0; foreach ($w in $b) { if ([int]$counts[$w] -gt 0) { $counts[$w]--; $common++ } }
        $common / [math]::Max($a.Count, $b.Count)
    }
    for ($k = 1; $k -le $MaxBlock; $k++) {
        $m = [math]::Max($MinLines, 4 * $k)
        if ($m + $k -gt $n) { break }
        $miss = 0; $allowed = [math]::Floor($m * 0.1)
        for ($i = $n - 1; $i -ge $n - $m; $i--) {
            if ((& $sim $lines[$i] $lines[$i - $k]) -lt 0.6) { if (++$miss -gt $allowed) { break } }
        }
        if ($miss -le $allowed) { return ($lines[$n - 1] -join ' ') }
    }
    $null
}

# The content argument of a tool call cut off while it looped ($Partial from
# $script:LlmLoopPartial), without the repeated tail; $null if there is none.
function Get-LoopSalvage($Partial) {
    if (-not $Partial -or $Partial.args -notmatch '"content"\s*:\s*"') { return $null }
    $raw = $Partial.args.Substring($Partial.args.IndexOf($Matches[0]) + $Matches[0].Length)
    if ($Partial.unit) {
        $cut = 0; $u = $Partial.unit
        while ($raw.Length - $cut -ge 2 * $u.Length -and [string]::CompareOrdinal($raw, $raw.Length - $cut - $u.Length, $u, 0, $u.Length) -eq 0) { $cut += $u.Length }
        $raw = $raw.Substring(0, $raw.Length - $cut)
    }
    $body = [regex]::Match($raw, '^(?:[^"\\]|\\.)*').Value -replace '\\u[0-9a-fA-F]{0,3}$', ''
    try { $text = ConvertFrom-Json ('"' + $body + '"') } catch { return $null }
    $lines = [Collections.Generic.List[string]]@($text -split "`n")
    if (-not $Partial.unit) {
        # A templated loop: cut where the trailing run of lines that echo one of
        # the 24 before them begins (short lines inside it go too, not before it).
        $words = { param($l) @($l -split '[^\p{L}\p{N}]+' | Where-Object { $_ }) }
        $start = $lines.Count
        for ($i = $lines.Count - 1; $i -ge 1 -and $lines.Count - $i -le 400; $i--) {
            $w = & $words $lines[$i]
            if ($w.Count -lt 3) { continue }
            $echo = $false
            for ($k = 1; $k -le 24 -and $i - $k -ge 0 -and -not $echo; $k++) {
                $prev = & $words $lines[$i - $k]
                if ($prev.Count -lt 3) { continue }
                $common = @($w | Where-Object { $prev -contains $_ }).Count
                if ($common / [math]::Max($w.Count, $prev.Count) -ge 0.6) { $echo = $true }
            }
            if (-not $echo) { break }
            $start = $i
        }
        if ($start -lt $lines.Count) { $lines.RemoveRange($start, $lines.Count - $start); $lines.Add('') }
    }
    if ($lines.Count -gt 1) { $lines.RemoveAt($lines.Count - 1) }
    $doc = ($lines -join "`n").TrimEnd()
    if ($doc.Length -lt 200) { return $null }
    $doc
}

# llama-server reply, streamed so it can be stopped as soon as its text,
# reasoning or a tool call's arguments start repeating one chunk over and over
# (such a reply would run to the token limit and be cut off). Returns the same
# shape as a non-streamed reply.
function Invoke-LlmChatStream([string]$Url, $Body, [int]$TimeoutSec = 600, [hashtable]$Headers = @{}) {
    $Body.stream = $true; $Body.stream_options = @{ include_usage = $true }
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20))
    $endpoint = if ($Url -match '/(v1|openai)$') { "$Url/chat/completions" } else { "$Url/v1/chat/completions" }
    try {
        $req = [Net.HttpWebRequest]::Create($endpoint)
        $req.Method = 'POST'; $req.ContentType = 'application/json; charset=utf-8'; $req.Timeout = $TimeoutSec * 1000; $req.ReadWriteTimeout = $TimeoutSec * 1000
        foreach ($k in $Headers.Keys) { $req.Headers[$k] = $Headers[$k] }
        $s = $req.GetRequestStream(); $s.Write($bytes, 0, $bytes.Length); $s.Close()
        $resp = $req.GetResponse()
    } catch {
        $ex = $_.Exception; while ($ex.InnerException -and $ex -isnot [Net.WebException]) { $ex = $ex.InnerException }
        $detail = ''
        if ($ex -is [Net.WebException] -and $ex.Response) { try { $detail = (New-Object IO.StreamReader($ex.Response.GetResponseStream(), [Text.Encoding]::UTF8)).ReadToEnd() } catch { } }
        throw "$($ex.Message) $("$detail".Trim())".Trim()
    }
    $reader = New-Object IO.StreamReader($resp.GetResponseStream(), [Text.Encoding]::UTF8)
    $content = New-Object Text.StringBuilder; $reasoning = New-Object Text.StringBuilder
    $calls = [ordered]@{}; $usage = $null; $finish = $null; $sinceCheck = 0; $checks = 0
    $tail = { param($sb, $len) $sb.ToString([math]::Max(0, $sb.Length - $len), [math]::Min($len, $sb.Length)) }
    try {
        while ($null -ne ($line = $reader.ReadLine())) {
            if (-not $line.StartsWith('data: ')) { continue }
            $data = $line.Substring(6)
            if ($data -eq '[DONE]') { break }
            $j = $data | ConvertFrom-Json
            if ($j.error) { throw "The remote server returned an error in the stream: $data" }
            if ($j.usage) { $usage = $j.usage }
            foreach ($ch in @($j.choices)) {
                if (-not $ch) { continue }
                if ($ch.finish_reason) { $finish = $ch.finish_reason }
                $d = $ch.delta
                if (-not $d) { continue }
                if ($d.content) { [void]$content.Append($d.content); $sinceCheck += $d.content.Length }
                if ($d.reasoning_content) { [void]$reasoning.Append($d.reasoning_content); $sinceCheck += $d.reasoning_content.Length }
                foreach ($tc in @($d.tool_calls)) {
                    if (-not $tc) { continue }
                    $i = "$([int]$tc.index)"
                    if (-not $calls.Contains($i)) { $calls[$i] = @{ id = ''; name = ''; args = New-Object Text.StringBuilder } }
                    if ($tc.id) { $calls[$i].id = $tc.id }
                    if ($tc.function.name) { $calls[$i].name += $tc.function.name }
                    if ($tc.function.arguments) { [void]$calls[$i].args.Append($tc.function.arguments); $sinceCheck += $tc.function.arguments.Length }
                }
            }
            if ($sinceCheck -ge 300) {
                $sinceCheck = 0; $checks++
                foreach ($src in @(@{ sb = $content }, @{ sb = $reasoning }) + @($calls.Values | ForEach-Object { @{ sb = $_.args; call = $_.name } })) {
                    $sb = $src.sb
                    if ($sb.Length -lt 400) { continue }
                    $unit = Find-RepeatLoop (& $tail $sb 2400); $exact = $null -ne $unit
                    if ($null -eq $unit -and $checks % 3 -eq 0 -and $sb.Length -ge 3000) { $unit = Find-TemplateLoop (& $tail $sb 8000) }
                    if ($null -ne $unit) {
                        $req.Abort()
                        # Kept for the caller: a document that loops near its end is
                        # still worth saving if nothing better arrives.
                        if ($src.call -and (-not $script:LlmLoopPartial -or $script:LlmLoopPartial.args.Length -lt $sb.Length)) {
                            $script:LlmLoopPartial = @{ name = $src.call; args = $sb.ToString(); unit = $(if ($exact) { $unit } else { '' }) }
                        }
                        $unit = ($unit -replace '\s+', ' ').Trim(); if ($unit.Length -gt 80) { $unit = $unit.Substring(0, 80) + '...' }
                        throw "the reply started repeating itself (`"$unit`" over and over) and was stopped"
                    }
                }
            }
        }
    } finally { $reader.Dispose(); $resp.Close() }
    $msg = [pscustomobject]@{ role = 'assistant'; content = $content.ToString() }
    if ($reasoning.Length) { $msg | Add-Member reasoning_content $reasoning.ToString() }
    if ($calls.Count) {
        $msg | Add-Member tool_calls @($calls.Values | ForEach-Object { [pscustomobject]@{ id = $_.id; type = 'function'; function = [pscustomobject]@{ name = $_.name; arguments = $_.args.ToString() } } })
    }
    [pscustomobject]@{ choices = @([pscustomobject]@{ message = $msg; finish_reason = $finish }); usage = $usage }
}

# Windows PowerShell decodes responses without a charset as Latin-1, which
# garbles the Unicode hyphens and quotes models emit; decode as UTF-8.
function Invoke-LlmChat([string]$Url, $Body, [int]$TimeoutSec = 600, [hashtable]$Headers = @{}) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20))
    $endpoint = if ($Url -match '/(v1|openai)$') { "$Url/chat/completions" } else { "$Url/v1/chat/completions" }
    try { $r = Invoke-WebRequest -UseBasicParsing -Uri $endpoint -Method Post -ContentType 'application/json; charset=utf-8' -TimeoutSec $TimeoutSec -Body $bytes -Headers $Headers }
    catch {
        $detail = if ($_.ErrorDetails -and $_.ErrorDetails.Message) { $_.ErrorDetails.Message } else { '' }
        if (-not $detail -and $_.Exception.Response) {
            try { $detail = (New-Object IO.StreamReader($_.Exception.Response.GetResponseStream(), [Text.Encoding]::UTF8)).ReadToEnd() } catch { }
        }
        if ($detail) { throw "$($_.Exception.Message) $($detail.Trim())" }
        throw
    }
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

# One model turn, whatever the backend. -Stream (llama-server only) stops a
# reply that starts repeating itself.
function Invoke-ChatTurn($Llm, [string]$Url, $Messages, $Tools, [double]$Temperature, [int]$MaxTokens, $Extra = $null, [switch]$Stream) {
    if ($Llm -and $Llm.Provider -eq 'openai') { return Invoke-OpenAIResponses $Llm $Messages $Tools $MaxTokens }
    $body = New-ChatBody $Llm $Messages $Tools $Temperature $MaxTokens $Extra
    if ($Stream) { return Invoke-LlmChatStream $Url $body -Headers (Get-LlmHeaders $Llm) }
    Invoke-LlmChat $Url $body -Headers (Get-LlmHeaders $Llm)
}

function ConvertTo-PlainText([string]$Text) {
    $Text -replace '[\u2010-\u2015\u2212]', '-' -replace '[\u2018\u2019]', "'" -replace '[\u201C\u201D]', '"' -replace '[\u00A0\u2007\u2009\u202F]', ' '
}
