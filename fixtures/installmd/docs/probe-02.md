---
name: diffdock
source: https://github.com/gcorso/DiffDock
summary: DiffDock web UI on port 9060
---

# DiffDock install

DiffDock is a Python‑based protein‑ligand docking web interface that runs a Gradio server on port **9060**.  The following `install.md` reproduces the current installation on a clean Ubuntu 24.04 LTS x64 machine with an NVIDIA GPU.  All steps are idempotent – the `detect` blocks skip already‑completed work, and the `verify` blocks confirm that the installation succeeded.

> **NOTE**
> The original installation used a CPU‑only PyTorch build (`torch==1.13.1+cpu`).  If you want GPU acceleration you can replace the CPU wheel with the CUDA 11.7 wheel (`torch==1.13.1+cu117`) and install the CUDA toolkit accordingly.  The steps below install the CPU version to match the current state.

---

## Step 1: System prerequisites

Why: DiffDock requires a recent Python 3.9 runtime, build tools, and a few system libraries.

```requires
os: linux
arch: x64
min_ram_gb: 16
min_disk_gb: 20
```

```detect bash
# Are the required packages already installed?
dpkg -s python3.9 python3-venv git build-essential libopenblas-dev liblapack-dev libffi-dev libssl-dev libxml2-dev libxslt1-dev zlib1g-dev libjpeg-dev libpng-dev libtiff-dev > /dev/null 2>&1
```

```install bash
apt-get update
apt-get install -y python3.9 python3-venv git build-essential \
    libopenblas-dev liblapack-dev libffi-dev libssl-dev libxml2-dev \
    libxslt1-dev zlib1g-dev libjpeg-dev libpng-dev libtiff-dev
```

```verify bash
dpkg -s python3.9 python3-venv git build-essential > /dev/null 2>&1
```

---

## Step 2: Create a dedicated install directory

Why: The project lives under `/opt/diffdock` and its virtual environment under `/opt/diffdock-env`.

```detect bash
[ -d "{{INSTALL_DIR}}" ] && exit 0 || exit 1
```

```install bash
mkdir -p "{{INSTALL_DIR}}"
chown diffdock:diffdock "{{INSTALL_DIR}}"
```

```verify bash
[ -d "{{INSTALL_DIR}}" ] && exit 0 || exit 1
```

---

## Step 3: Clone the DiffDock repository

Why: We need the source code and its history.

```detect bash
[ -d "{{REPO_DIR}}" ] && exit 0 || exit 1
```

```install bash
git clone https://github.com/gcorso/DiffDock.git "{{REPO_DIR}}"
chown -R diffdock:diffdock "{{REPO_DIR}}"
```

```verify bash
[ -d "{{REPO_DIR}}" ] && exit 0 || exit 1
```

---

## Step 4: Create a Python 3.9 virtual environment

Why: DiffDock was built against Python 3.9.18.  The current system has Python 3.12, so we create an isolated venv at `/opt/diffdock-env`.

```detect bash
[ -d "{{INSTALL_DIR}}/diffdock-env" ] && exit 0 || exit 1
```

```install bash
python3.9 -m venv "{{INSTALL_DIR}}/diffdock-env"
chown -R diffdock:diffdock "{{INSTALL_DIR}}/diffdock-env"
```

```verify bash
[ -d "{{INSTALL_DIR}}/diffdock-env" ] && exit 0 || exit 1
```

---

## Step 5: Install Python dependencies

Why: All required packages are listed in `requirements.txt`.  The file contains a pinned PyTorch wheel for CUDA 11.7, but the current environment uses the CPU wheel – this is fine for the existing installation.

```detect bash
# Check that the venv has a PyTorch 1.13.1 package (CPU or cu117)
{{INSTALL_DIR}}/diffdock-env/bin/python -m pip freeze | grep -q '^torch==1.13.1' && exit 0 || exit 1
```

```install bash
# Activate the venv and install everything
{{INSTALL_DIR}}/diffdock-env/bin/pip install --upgrade pip
{{INSTALL_DIR}}/diffdock-env/bin/pip install -r "{{REPO_DIR}}/requirements.txt"
```

```verify bash
{{INSTALL_DIR}}/diffdock-env/bin/python -m pip freeze | grep -q '^torch==1.13.1'
```

---

## Step 6: (Optional) Install GPU‑enabled PyTorch

Why: If you want GPU acceleration you can replace the CPU wheel with the CUDA 11.7 wheel and install the CUDA toolkit.

```detect bash
# This step is optional – always pass
exit 0
```

```install bash
# No-op on the current machine
```

```verify bash
# No-op – always pass
exit 0
```

---

## Step 7: Create a systemd service

Why: Running DiffDock as a service keeps it alive and exposes it on port 9060.

```detect bash
[ -f /etc/systemd/system/diffdock.service ] && exit 0 || exit 1
```

```install bash
cat > /etc/systemd/system/diffdock.service <<'EOF'
[Unit]
Description=DiffDock Gradio server
After=network.target

[Service]
User=diffdock
WorkingDirectory={{REPO_DIR}}
Environment=GRADIO_SERVER_PORT=9060
ExecStart={{INSTALL_DIR}}/diffdock-env/bin/python {{REPO_DIR}}/app/main.py
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable diffdock
```

```verify bash
systemctl is-enabled diffdock && systemctl is-active diffdock
```

---

## Step 8: Start the service

Why: Bring the server online.

```detect bash
systemctl is-active diffdock > /dev/null 2>&1 && exit 0 || exit 1
```

```install bash
systemctl start diffdock
```

```verify bash
systemctl is-active diffdock > /dev/null 2>&1
```

---

## Step 9: Verify the web UI

Why: Ensure the server is listening on the expected port and returns a page.

```detect bash
# Check that port 9060 is listening
ss -tlnp | grep -q ':9060 ' && exit 0 || exit 1
```

```verify bash
# Simple HTTP check
curl -s -o /dev/null -w "%{http_code}" http://localhost:9060/ | grep -q 200
```

---

## Summary

After completing these steps, DiffDock is running as a systemd service and is reachable at `http://<host>:9060/`.  The installation uses the CPU‑only PyTorch build that matches the current state of the machine.

---

## Gaps & Assumptions

* **GPU acceleration** – The current installation uses a CPU‑only PyTorch wheel.  If GPU support is required, the user must install the CUDA 11.7 toolkit and replace the wheel in `requirements.txt`.
* **Secrets** – No secrets are present in the repository.  If the user needs to provide Hugging Face or other API keys, they should set the corresponding environment variables before starting the service.
* **System user** – The script assumes a `diffdock` user exists.  If not, the user should create it (`useradd -m -s /usr/sbin/nologin diffdock`) before running the install.
* **Python version** – The install uses Python 3.9.18.  If the target machine has a different Python 3.9 version, the venv creation will still succeed.
* **Disk space** – The install requires at least 20 GB free space for the repository, venv, and data.

---