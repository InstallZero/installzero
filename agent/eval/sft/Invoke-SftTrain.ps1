<#
.SYNOPSIS
    One training run for the eval console's train job (Invoke-EvalJob.ps1):
    LoRA fine-tune on an SFT set, merge, convert to GGUF, quantize, and
    put the model where the doctor and the probe find local models.

.DESCRIPTION
    The spec (job.spec) carries the run's base model, settings and the SFT
    set's folder on this host. Steps, each a PHASE line for the console:
      setup       a Python venv for training under -Root (uv; created once,
                  reused while -Packages are unchanged), and llama.cpp's
                  HF-to-GGUF converter at the tag of the bundled llama.cpp
      training    train.py (adapter, merged bf16 model, metrics.json); with
                  spec.compute.target 'hf', hf_job.py runs it on a Hugging
                  Face Jobs GPU and the results come back from a private
                  model repo (downloading)
      converting  convert_hf_to_gguf.py -> bf16 GGUF
      quantizing  llama-quantize.exe from the doctor assets -> <name>.gguf in
                  the doctor assets folder (the worker then reports it as a
                  local model, so campaigns can probe with it)
    The merged model and the bf16 GGUF are deleted afterwards (tens of GB);
    the adapter stays in the run folder.

    Gated base models (Gemma) need a Hugging Face token: HF_TOKEN in the
    environment or in InstallZero's .env.
#>
param(
    [Parameter(Mandatory)][string]$JobFile,
    [Parameter(Mandatory)][string]$Out,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'installzero\sft'),
    [string]$LlamaTag = 'b9771',
    [string]$TorchIndex = 'https://download.pytorch.org/whl/cu128',
    [string[]]$Packages = @('transformers', 'peft', 'accelerate', 'bitsandbytes', 'datasets', 'sentencepiece', 'protobuf', 'safetensors')
)

$ErrorActionPreference = 'Stop'
$utf8 = [Text.UTF8Encoding]::new($false)
. (Join-Path $PSScriptRoot '..\..\..\doctor\Llm.ps1')
$job = [IO.File]::ReadAllText($JobFile) | ConvertFrom-Json
$spec = $job.spec
$p = $spec.params
$runDir = Join-Path $Root "runs\$($spec.run_id)"
New-Item -ItemType Directory -Force $runDir | Out-Null

function Write-Phase([string]$Phase, [string]$Msg = '') { Write-Host "PHASE: $Phase$(if ($Msg) { " | $Msg" })" }

# Runs a native program with its output on this job's log; stderr is output
# too (Python logs there), so the error preference must not turn it into a throw.
function Invoke-Native([string]$Exe, [string[]]$ArgList, [string]$What) {
    $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    $last = ''
    try { & $Exe @ArgList 2>&1 | ForEach-Object { $s = "$_"; Write-Host $s; if ($s.Trim() -and $s -notmatch '^System\.Management\.Automation') { $last = $s.Trim() } } } finally { $ErrorActionPreference = $old }
    if ($LASTEXITCODE -ne 0) { throw "$What failed (exit $LASTEXITCODE)$(if ($last) { ": $last" })" }
}

function Get-HfToken {
    foreach ($v in 'HF_TOKEN', 'HUGGING_FACE_HUB_TOKEN') { foreach ($s in 'Process', 'User', 'Machine') { $t = [Environment]::GetEnvironmentVariable($v, $s); if ($t) { return $t } } }
    foreach ($f in @((Join-Path $PSScriptRoot '..\..\..\.env'), (Join-Path $PSScriptRoot '..\..\..\console\.env'))) {
        if (Test-Path $f) { foreach ($line in Get-Content $f) { if ($line -match '^\s*(HF_TOKEN|HUGGING_FACE_HUB_TOKEN)\s*=\s*(.+)$') { return $Matches[2].Trim().Trim('"').Trim("'") } } }
    }
    ''
}

$setDir = "$($spec.set.path)"
if (-not $setDir -or -not (Test-Path (Join-Path $setDir 'train.jsonl'))) { throw "SFT set $($spec.set.id) is not on this host (expected $setDir\train.jsonl); build it here first" }

# ---- setup ---------------------------------------------------------------------
Write-Phase 'setup' 'training environment'
$uv = (Get-Command uv -ErrorAction SilentlyContinue).Source
if (-not $uv) { throw 'uv is needed to create the training environment: winget install astral-sh.uv' }
$venv = Join-Path $Root 'venv'
$py = Join-Path $venv 'Scripts\python.exe'
$stamp = Join-Path $venv 'installzero-packages.txt'
$want = "torch@$TorchIndex;$($Packages -join ',')"
if (-not (Test-Path $py) -or -not (Test-Path $stamp) -or (Get-Content $stamp -Raw).Trim() -ne $want) {
    Write-Phase 'setup' 'creating the Python 3.12 venv and installing torch (CUDA) and training packages; several GB, first run only'
    if (-not (Test-Path $py)) { Invoke-Native $uv @('venv', '--python', '3.12', $venv) 'uv venv' }
    Invoke-Native $uv @('pip', 'install', '--python', $py, 'torch', '--index-url', $TorchIndex) 'installing torch'
    Invoke-Native $uv (@('pip', 'install', '--python', $py) + $Packages) 'installing training packages'
    Set-Content $stamp $want -Encoding ASCII
}
$llama = Join-Path $Root "llama.cpp-$LlamaTag"
if (-not (Test-Path (Join-Path $llama 'convert_hf_to_gguf.py'))) {
    Write-Phase 'setup' "llama.cpp $LlamaTag converter"
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw 'git is needed to fetch llama.cpp''s GGUF converter' }
    Invoke-Native 'git' @('clone', '--depth', '1', '--branch', $LlamaTag, 'https://github.com/ggml-org/llama.cpp', $llama) 'cloning llama.cpp'
}
if (-not (Test-Path (Join-Path $venv "installzero-gguf-$LlamaTag.txt"))) {
    Invoke-Native $uv @('pip', 'install', '--python', $py, (Join-Path $llama 'gguf-py'), 'mistral-common') 'installing gguf-py'
    Set-Content (Join-Path $venv "installzero-gguf-$LlamaTag.txt") $LlamaTag -Encoding ASCII
}
$ErrorActionPreference = 'Continue'
$versions = "$(& $py -c "import torch, transformers, peft; print(f'torch {torch.__version__} cuda {torch.version.cuda}; transformers {transformers.__version__}; peft {peft.__version__}')" 2>$null)"
$ErrorActionPreference = 'Stop'
Write-Host "Environment: $versions"

$token = Get-HfToken
if ($token) { $env:HF_TOKEN = $token } elseif ("$($spec.base_model)" -match '(?i)gemma|llama') { Write-Host 'No HF_TOKEN found; gated base models will fail to download. Set HF_TOKEN in the environment or InstallZero''s .env.' }
$env:HF_HUB_DISABLE_PROGRESS_BARS = '1'; $env:TRANSFORMERS_VERBOSITY = 'warning'; $env:TOKENIZERS_PARALLELISM = 'false'; $env:PYTHONUNBUFFERED = '1'; $env:PYTHONIOENCODING = 'utf-8'

# ---- training --------------------------------------------------------------------
$trainArgs = @('--base', "$($spec.base_model)", '--epochs', "$($p.epochs)", '--lr', "$($p.lr)", '--lora-r', "$($p.lora_r)", '--lora-alpha', "$($p.lora_alpha)",
    '--max-len', "$($p.max_len)", '--batch', "$($p.batch)", '--grad-accum', "$($p.grad_accum)")
if ($p.qlora) { $trainArgs += '--qlora' }
if ([int]$p.max_examples -gt 0) { $trainArgs += @('--max-examples', "$($p.max_examples)") }
$hfJob = $null
if ("$($spec.compute.target)" -eq 'hf') {
    # Trains on a Hugging Face GPU; this host uploads the set, relays the log,
    # then downloads the merged model for the GGUF steps below. The state file
    # sits in the job's work folder, where the worker looks for it on cancel.
    if (-not $token) { throw 'Hugging Face Jobs need HF_TOKEN (environment or InstallZero''s .env) and credits on that account' }
    if (Test-Path "$($spec.base_model)") { throw "a local base model folder cannot be used on Hugging Face Jobs; use a Hub repo id" }
    $c = $spec.compute
    $state = Join-Path (Split-Path $Out) 'hf-job.json'
    Remove-Item $state -ErrorAction SilentlyContinue
    $timeout = "$([math]::Max(10, [int]([double]$c.timeout_h * 60)))m"
    Invoke-Native $py (@((Join-Path $PSScriptRoot 'hf_job.py'), 'submit', '--state', $state, '--set-dir', $setDir, '--dataset-repo', "installzero-$($spec.set.id)",
            '--model-repo', "$($spec.output_name)", '--flavor', "$($c.flavor)", '--timeout', $timeout, '--price', "$([double]$c.price)", '--run-id', "$($spec.run_id)", '--train') + $trainArgs) 'training on Hugging Face Jobs'
    $hfJob = Get-Content $state -Raw | ConvertFrom-Json
    Write-Phase 'downloading' "merged model from $($hfJob.model_repo)"
    # Plain HTTPS from the CDN: on this network Xet trickled at 0.3 MB/s while
    # HTTPS fetched the same file at 25 MB/s.
    $env:HF_HUB_DISABLE_XET = '1'; $env:HF_HUB_DOWNLOAD_TIMEOUT = '60'
    Invoke-Native $py @('-c', "import sys; from huggingface_hub import snapshot_download; snapshot_download(sys.argv[1], local_dir=sys.argv[2], allow_patterns=['merged/*', 'adapter/*', 'metrics.json'])",
        "$($hfJob.model_repo)", $runDir) 'downloading the trained model'
} else {
    Invoke-Native $py (@((Join-Path $PSScriptRoot 'train.py'), '--data', $setDir, '--out', $runDir) + $trainArgs) 'training'
}
$metrics = Get-Content (Join-Path $runDir 'metrics.json') -Raw | ConvertFrom-Json

# ---- GGUF --------------------------------------------------------------------------
$merged = Join-Path $runDir 'merged'
$bf16 = Join-Path $runDir 'model-bf16.gguf'
Write-Phase 'converting' 'merged model to GGUF (bf16)'
# A LoRA merge keeps the architecture, but newer transformers drop fields on
# save that llama.cpp's converter reads (Gemma 4: text_config.global_head_dim):
# the base model's own config.json describes the merged weights exactly.
if (Test-Path (Join-Path "$($spec.base_model)" 'config.json')) { Copy-Item (Join-Path "$($spec.base_model)" 'config.json') (Join-Path $merged 'config.json') -Force }
else { Invoke-Native $py @('-c', "import shutil, sys; from huggingface_hub import hf_hub_download; shutil.copy(hf_hub_download(sys.argv[1], 'config.json'), sys.argv[2])", "$($spec.base_model)", (Join-Path $merged 'config.json')) 'fetching the base config' }
Invoke-Native $py @((Join-Path $PSScriptRoot 'convert_gguf.py'), $llama, $merged, '--outtype', 'bf16', '--outfile', $bf16) 'GGUF conversion'
$assets = Find-DoctorAssets
$quantize = Join-Path $assets 'llama\cuda\llama-quantize.exe'
if (-not (Test-Path $quantize)) { $quantize = Join-Path $assets 'llama\cpu\llama-quantize.exe' }
if (-not (Test-Path $quantize)) { throw "llama-quantize.exe not found under $assets\llama" }
$name = "$($spec.output_name)-$($spec.quant).gguf"
$final = Join-Path $assets $name
Write-Phase 'quantizing' "$($spec.quant) -> $name"
Invoke-Native $quantize @($bf16, "$final.tmp", "$($spec.quant)") 'quantizing'
Remove-Item $bf16, $merged -Recurse -Force -ErrorAction SilentlyContinue
# The converter takes <eos> as the end token from the tokenizer config, but
# Gemma 4 ends a turn with <turn|>: llama-server then never stops a reply short
# of the output cap. The base GGUF the probes use has the right end token and
# chat template; give the fine-tune the same.
$baseGguf = Get-ChildItem $assets -Filter "$(Split-Path "$($spec.base_model)" -Leaf)-*.gguf" -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -notmatch '-iz-' } | Select-Object -First 1
if ($baseGguf) {
    Write-Phase 'quantizing' "end token and chat template from $($baseGguf.Name)"
    Invoke-Native $py @((Join-Path $PSScriptRoot 'copy_gguf_tokens.py'), $llama, $baseGguf.FullName, "$final.tmp", $final) 'copying the end token'
    Remove-Item "$final.tmp" -Force
} else {
    Write-Host "WARNING: no base GGUF for $($spec.base_model) in $assets; the end token is the converter's"
    Move-Item "$final.tmp" $final -Force
}
# Trained without reasoning, the model is served with thinking off as well
# (llama-server turns it on by default); see Get-LlmModelSettings.
$setManifest = Join-Path $setDir 'manifest.json'
if ((Test-Path $setManifest) -and -not (Get-Content $setManifest -Raw | ConvertFrom-Json).include_reasoning) {
    [IO.File]::WriteAllText("$final.iz.json", (@{ request = @{ chat_template_kwargs = @{ enable_thinking = $false } } } | ConvertTo-Json -Depth 5 -Compress), $utf8)
}
$size = [math]::Round((Get-Item $final).Length / 1GB, 2)
Write-Phase 'trained' "$name ($size GB), train loss $($metrics.train_loss)$(if ($metrics.eval_loss) { ", val loss $($metrics.eval_loss)" })"
$o = [ordered]@{ gguf = $name; size_gb = $size; path = $final; adapter = (Join-Path $runDir 'adapter'); metrics = $metrics; environment = "$versions" }
if ($hfJob) { $o.hf = [ordered]@{ job = $hfJob.id; url = $hfJob.url; flavor = $hfJob.flavor; model_repo = $hfJob.model_repo; dataset_repo = $hfJob.dataset_repo; running_secs = $hfJob.running_secs; cost_usd = $hfJob.cost_usd } }
[IO.File]::WriteAllText($Out, ($o | ConvertTo-Json -Depth 8 -Compress), $utf8)
