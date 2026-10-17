# WSL 2 GPU forwarding

How containers in Docker Desktop reach the NVIDIA GPU, and what breaks it.

## How it works

```
Windows NVIDIA driver -> WSL 2 VM (/usr/lib/wsl/lib/libcuda.so, /dev/dxg) -> docker-desktop -> container
```

- The NVIDIA driver is installed on **Windows only**. Its WSL support exposes
  the GPU to every WSL 2 distro as `/dev/dxg` plus libraries in
  `/usr/lib/wsl/lib/` (`libcuda.so`, `libnvidia-ml.so`, `nvidia-smi`).
- Docker Desktop runs its engine in its own `docker-desktop` WSL 2 distro and
  ships the NVIDIA container runtime itself. There is no
  `nvidia-container-toolkit` to install on Windows.
- Requirements: Windows 10 21H2 or newer, or Windows 11; WSL 2 (not WSL 1); an
  up-to-date WSL (`wsl --update`); Virtual Machine Platform enabled; NVIDIA
  driver 470.76 or newer (this stack needs 551.61, see `nvidia-driver-cuda.md`).
- WSL 1 has no GPU support at all.

## Docker cannot reach the GPU

**Symptoms**
- `could not select device driver "" with capabilities: [[gpu]]`
- `nvidia-container-cli: initialization error: WSL environment detected but no adapters were found: unknown`
- `nvidia-container-cli: initialization error: nvml error: driver not loaded: unknown`
- `Failed to initialize NVML: GPU access blocked by the operating system`
- `sim-controller` or `fly-brain` stays in `Created` or exits immediately while CPU-only services run.

**Cause** In order of likelihood: WSL is out of date; the Windows driver is
missing, too old or mid-update (a driver update while Docker Desktop runs
leaves the VM with a stale adapter); Docker Desktop is not using the WSL 2
backend; the machine has no NVIDIA GPU.

**Check** (doctor can run)
- `nvidia-smi` on Windows. If this fails, fix the Windows driver first
  (`nvidia-driver-cuda.md`).
- `wsl --version` (WSL version and kernel), `wsl --status`
- `wsl -l -v`: `docker-desktop` must show `VERSION 2`.
- `docker info` (look for `Operating System: Docker Desktop` and the kernel
  version ending in `-microsoft-standard-WSL2`)
- `docker compose ps -a sim-controller fly-brain`, then
  `docker inspect <container name> --format '{{.State.Status}} {{.State.Error}}'`

**Check** (ask the user to run; starts a container)
- `docker run --rm --gpus all --entrypoint nvidia-smi fly_brain:latest`
  (prefix the image with the install's `IMAGE_PREFIX` if it sets one) prints
  the GPU table when forwarding works. It uses an image the install already
  has, so nothing is downloaded.

**Fix**
1. `wsl --update` (administrator), then `wsl --shutdown`, then start Docker
   Desktop again. This fixes most cases, including a driver update made while
   Docker Desktop was running.
2. If `nvidia-smi` fails on Windows: reinstall the NVIDIA driver, reboot.
3. Docker Desktop Settings > General: "Use the WSL 2 based engine" must be on.

## NVIDIA driver installed inside a WSL distro

**Symptoms**
- `nvidia-smi` works on Windows but fails inside the user's Ubuntu distro.
- `Missing /usr/lib/wsl/lib/libcuda.so` or `libcuda.so` resolving to
  `/usr/lib/x86_64-linux-gnu/` instead of `/usr/lib/wsl/lib/`.
- GPU code works in Docker containers but not when run directly in the distro.

**Cause** Someone ran `apt install nvidia-driver-*` (or the CUDA `.run`
installer with the driver option) inside the distro. The Linux driver's
libraries shadow the WSL ones and break forwarding in that distro.

This only affects that distro. Containers run in `docker-desktop`, not in the
user's distro, so this does not explain a container failing to see the GPU.

**Fix** In the affected distro: `sudo apt remove --purge 'nvidia-*' 'libnvidia-*'`,
`sudo apt autoremove`, then `wsl --shutdown` from Windows. If CUDA tools are
needed in the distro, install `cuda-toolkit-12-x` from NVIDIA's `wsl-ubuntu`
repository, which contains no driver.

## Distro or Docker running on WSL 1

**Symptoms**
- `wsl -l -v` shows `VERSION 1`.
- `WSL 1 is not supported` from Docker Desktop, or CUDA "not available" inside WSL.

**Fix** `wsl --set-version <distro> 2`; `wsl --set-default-version 2`.

## WSL or virtualization not available

**Symptoms**
- `Please enable the Virtual Machine Platform Windows feature and ensure virtualization is enabled in the BIOS`
- `WslRegisterDistribution failed with error: 0x80370102`
- `The virtual machine could not be started because a required feature is not installed`

**Cause** Virtualization is off in the BIOS/UEFI, or the Virtual Machine
Platform Windows feature is not enabled.

**Check** (doctor can run)
- `systeminfo` (the "Hyper-V Requirements" section; "A hypervisor has been
  detected" means virtualization is on and in use)
- `Get-CimInstance Win32_Processor | Select-Object Name, VirtualizationFirmwareEnabled`
  (reads `False` when a hypervisor is already running, so trust `systeminfo`
  first)

**Fix** Enable virtualization (Intel VT-x / AMD SVM) in the BIOS, then
`wsl --install --no-distribution` as administrator and reboot.
