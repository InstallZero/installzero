---
name: comfyui
source: https://github.com/comfyanonymous/ComfyUI
summary: Node-based Stable Diffusion UI, served on http://127.0.0.1:8188
---

# ComfyUI install

Installs ComfyUI from source into `{{REPO_DIR}}` with its own Python virtual
environment and a PyTorch build matching the GPU. Afterwards start it with
`{{REPO_DIR}}\.venv\Scripts\python.exe {{REPO_DIR}}\main.py` and open
http://127.0.0.1:8188. Put model checkpoints in `{{REPO_DIR}}\models\checkpoints`.

## Step 1: git - Git

Why: the source is cloned from GitHub.

```detect powershell
git --version
```

```install powershell os=windows
winget install -e --id Git.Git --accept-package-agreements --accept-source-agreements
```

```verify powershell
git --version
```

## Step 2: python - Python 3.12

Why: ComfyUI and PyTorch support Python 3.10 to 3.13; 3.12 is the tested default.

```requires
min_disk_gb: 20
```

```detect powershell
py -3.12 --version
```

```install powershell os=windows
winget install -e --id Python.Python.3.12 --accept-package-agreements --accept-source-agreements
```

```verify powershell
py -3.12 --version
```

## Step 3: clone - ComfyUI source

Why: the application itself.

```detect powershell
Test-Path '{{REPO_DIR}}\main.py'
if (-not (Test-Path '{{REPO_DIR}}\main.py')) { exit 1 }
```

```install powershell
git clone https://github.com/comfyanonymous/ComfyUI '{{REPO_DIR}}'
```

```verify powershell
git -C '{{REPO_DIR}}' log -1 --format='%h %cs'
```

## Step 4: venv - Python environment with PyTorch

Why: PyTorch must match the GPU: the CUDA 12.8 wheels on NVIDIA, the CPU wheels otherwise.

```detect powershell
& '{{REPO_DIR}}\.venv\Scripts\python.exe' -c "import torch; print(torch.__version__)"
```

```install powershell gpu=nvidia
py -3.12 -m venv '{{REPO_DIR}}\.venv'
& '{{REPO_DIR}}\.venv\Scripts\python.exe' -m pip install --upgrade pip
& '{{REPO_DIR}}\.venv\Scripts\pip.exe' install torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu128
```

```install powershell
py -3.12 -m venv '{{REPO_DIR}}\.venv'
& '{{REPO_DIR}}\.venv\Scripts\pip.exe' install torch torchvision torchaudio
```

```verify powershell gpu=nvidia
& '{{REPO_DIR}}\.venv\Scripts\python.exe' -c "import torch, sys; print(torch.__version__, torch.cuda.is_available()); sys.exit(0 if torch.cuda.is_available() else 1)"
```

```verify powershell
& '{{REPO_DIR}}\.venv\Scripts\python.exe' -c "import torch; print(torch.__version__)"
```

## Step 5: requirements - ComfyUI's Python packages

Why: everything else ComfyUI imports.

```install powershell
& '{{REPO_DIR}}\.venv\Scripts\pip.exe' install -r '{{REPO_DIR}}\requirements.txt'
```

```verify powershell
& '{{REPO_DIR}}\.venv\Scripts\python.exe' -c "import aiohttp, safetensors, einops; print('ok')"
```

## Step 6: serve - Start ComfyUI

Why: proves the install works end to end.

```install powershell
Start-Process -FilePath '{{REPO_DIR}}\.venv\Scripts\python.exe' -ArgumentList 'main.py', '--listen', '127.0.0.1', '--port', '8188' -WorkingDirectory '{{REPO_DIR}}' -WindowStyle Minimized
```

```verify powershell
Invoke-WebRequest http://127.0.0.1:8188 -UseBasicParsing -TimeoutSec 60 | Select-Object -ExpandProperty StatusCode
```
