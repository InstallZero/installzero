---
name: rl-roboracer
source: https://github.com/frenb/rl-roboracer
summary: RL stack for the robotaxi Unity gym, built with Docker Compose and NVIDIA GPU support.
---

# rl-roboracer install

This document describes how to install the rl‑roboracer stack on a clean Windows 11 machine. The stack is delivered as a set of Docker Compose services that run a ROS server, a MongoDB database, a Unity simulation controller, a dashboard, and a training brain. The only system‑level requirement is Docker Desktop with WSL 2 and NVIDIA GPU support. All runtime code is contained in the Docker images, so no additional Python or Node installations are needed.

The install proceeds in the following order:
1. Install Docker Desktop with WSL 2 and NVIDIA Container Toolkit.
2. Verify the NVIDIA driver and Docker GPU integration.
3. Build or pull the required Docker images.
4. Create a minimal `.env` file with placeholder values.
5. Bring up the stack with `docker compose`.
6. Verify that the services are running and that the dashboard is reachable.

Feel free to adjust the `.env` values to point to your own data directories or credentials.

## Step 1: docker-desktop - Install Docker Desktop with WSL 2 and NVIDIA support

Why: The stack runs entirely in Docker containers that require GPU access.

```requires
os: windows
arch: x64
gpu: nvidia
min_driver: 617.42
```

```detect powershell
if (Get-Command docker -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }
```

```install powershell
winget install -e --id Docker.DockerDesktop --accept-package-agreements --accept-source-agreements
```

```verify powershell
if (Get-Command docker -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }
```

After installation, ensure that the Docker Desktop settings have **WSL 2** enabled and that the **NVIDIA GPU** support is turned on (under Resources → GPU). Restart Docker Desktop if you changed any settings.

## Step 2: nvidia-driver - Verify NVIDIA driver and Docker GPU integration

Why: The containers use the NVIDIA runtime to access the GPU.

```detect powershell
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }
```

```verify powershell
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }
```

If the driver is missing or too old, install the latest driver from the NVIDIA website or via winget:

```install powershell
winget install -e --id NVIDIA.NVIDIADriver --accept-package-agreements --accept-source-agreements
```

## Step 3: docker-images - Build or pull the required Docker images

Why: The stack uses custom images defined in the `docker/` directory.

```detect powershell
$images = docker images --format "{{.Repository}}:{{.Tag}}" | Select-String -Pattern "^rl-roboracer/"; if ($images) { exit 0 } else { exit 1 }
```

```install powershell
# Build all images defined in the docker-compose file
cd {{REPO_DIR}}
docker compose build
```

```verify powershell
$images = docker images --format "{{.Repository}}:{{.Tag}}" | Select-String -Pattern "^rl-roboracer/"; if ($images) { exit 0 } else { exit 1 }
```

If you prefer to pull prebuilt images from a registry, replace the build step with:

```powershell
# Example: docker compose pull
```

## Step 4: env-file - Create a minimal .env file

Why: The compose file expects a `.env` file for configuration. The repository ships a template that is git‑ignored.

```detect powershell
if (Test-Path {{REPO_DIR}}/.env) { exit 0 } else { exit 1 }
```

```install powershell
# Create a minimal .env with placeholder values
$envContent = @(
    "# Environment variable template for the rl-roboracer stack.",
    "# Copy this file to `.env` (which is git-ignored) and fill in the real values.",
    "# Example values are shown below; replace them with your own.",
    "# DATA_DIR=\"C:\\Users\\benja\\Documents\\rl-roboracer-data\"",
    "# HUGGINGFACE_TOKEN=\"<your-hf-token>\""
)
$envContent | Out-File -FilePath {{REPO_DIR}}/.env -Encoding utf8
```

```verify powershell
if (Test-Path {{REPO_DIR}}/.env) { exit 0 } else { exit 1 }
```

## Step 5: compose-up - Bring up the stack

Why: This starts all services defined in `docker-compose.yml`.

```detect powershell
# Check if any of the expected containers are already running
$running = docker ps --format "{{.Names}}" | Select-String -Pattern "^rl-roboracer"; if ($running) { exit 0 } else { exit 1 }
```

```install powershell
cd {{REPO_DIR}}
docker compose up -d
```

```verify powershell
# Verify that all services are up
$services = @('ros-server','mongo','mongo-express','sim-controller','dashboard','fly-brain','madscientist','tmpdata','flydata')
$allRunning = $true
foreach ($s in $services) {
    if (-not (docker ps --format "{{.Names}}" | Select-String -Pattern "^rl-roboracer_${s}_")) { $allRunning = $false; break }
}
if ($allRunning) { exit 0 } else { exit 1 }
```

## Step 6: verify-dashboard - Check that the dashboard is reachable

Why: The dashboard provides a web UI on port 8080 (this is the default in the compose file). Adjust if your compose file uses a different port.

```detect powershell
if (Test-Path "http://localhost:8080") { exit 0 } else { exit 1 }
```

```verify powershell
try {
    $resp = Invoke-WebRequest -Uri http://localhost:8080 -UseBasicParsing -TimeoutSec 5
    if ($resp.StatusCode -eq 200) { exit 0 } else { exit 1 }
} catch { exit 1 }
```

---

## Summary

The rl‑roboracer stack is now running on your machine. You can access the dashboard at <http://localhost:8080> and interact with the Unity simulation via the `sim-controller` service.

---

