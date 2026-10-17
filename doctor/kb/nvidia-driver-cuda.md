# NVIDIA driver, CUDA and GPU generation

How the Windows NVIDIA driver, the CUDA version inside each image, and the GPU
generation have to line up for this stack.

## What this stack needs

| Image | Base | Needs | Minimum Windows driver |
|---|---|---|---|
| `sim_controller` (trainer) | `nvidia/cuda:11.0.3-runtime-ubuntu20.04`, TensorFlow 2.7, cuDNN `8.1.1.33-1+cuda11.2` | CUDA 11.x | 451.82 |
| `fly_brain` | `nvidia/cuda:12.4.1-runtime-ubuntu22.04`, CuPy for CUDA 12 | CUDA 12.4 | 551.61 |
| doctor's own `llama-server.exe` (CUDA build) | CUDA 12.4 | CUDA 12.4 | 551.61 (else the doctor uses its Vulkan or CPU build) |

So the whole stack needs Windows driver **551.61 or newer**. The driver is
installed on Windows only: never inside WSL and never inside an image.

The "CUDA Version" that `nvidia-smi` prints at the top right is the newest CUDA
the driver supports, not an installed toolkit. Containers ship their own CUDA
runtime; only the driver comes from the host.

Driver branch to newest supported CUDA (Linux branch numbers; Windows drivers of
the same branch, e.g. 551.x for 550, support the same CUDA):

| Driver branch | Max CUDA | Driver branch | Max CUDA |
|---|---|---|---|
| 590 | 13.1 | 545 | 12.3 |
| 580 | 13.0 | 535 | 12.2 |
| 575 | 12.9 | 530 | 12.1 |
| 570 | 12.8 | 525 | 12.0 |
| 560 | 12.6 | 520 | 11.8 |
| 555 | 12.5 | 515 | 11.7 |
| 550 | 12.4 | 470 | 11.4 |
| 450 | 11.0 | | |

## Driver too old for an image

**Symptoms**
- `nvidia-container-cli: requirement error: unsatisfied condition: cuda>=12.4, please update your driver to a newer version, or use an earlier cuda container: unknown`
- `CUDA driver version is insufficient for CUDA runtime version`
- `cudaErrorInsufficientDriver`
- `fly-brain` exits at start while `sim-controller` (CUDA 11) still runs.

**Cause** The Windows driver is older than the CUDA version the image was built
for. `fly-brain` (CUDA 12.4) hits this first.

**Check** (doctor can run)
- `nvidia-smi --query-gpu=name,driver_version,compute_cap --format=csv,noheader`
- `nvidia-smi` and read "CUDA Version" in the header.
- `docker compose logs --tail 50 fly-brain`

**Fix** Install the current Game Ready or Studio driver from
https://www.nvidia.com/Download/index.aspx on Windows, reboot, restart Docker
Desktop, then `docker compose up -d`. Do not downgrade the images.

## No GPU visible at all

**Symptoms**
- `nvidia-smi` is not recognized, or prints `NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver`
- `failed call to cuInit: CUDA_ERROR_NO_DEVICE: no CUDA-capable device is detected`
- `Could not load dynamic library 'libcuda.so.1'`
- `Failed to initialize NVML: Unknown Error`

**Cause** In order of likelihood on a laptop: the NVIDIA driver is missing or
broken on Windows; the laptop has no NVIDIA GPU (Intel or AMD only); the GPU is
disabled in Device Manager or BIOS (some laptops have a "hybrid / discrete only"
switch); WSL cannot forward the GPU (see `wsl2-gpu.md`).

**Check** (doctor can run)
- `Get-CimInstance Win32_VideoController | Select-Object Name, DriverVersion, Status`
  (`DriverVersion` is the Windows form: drop the dots and read the last five
  digits, so `32.0.15.6636` is driver 566.36.)
- `nvidia-smi`
- `system_facts` (GPU and driver from the installer preflight)

**Fix** No NVIDIA adapter listed: this machine cannot run the trainer's GPU
images (see `amd-gpu-windows.md` for AMD). Adapter listed with a status other
than `OK`, or `nvidia-smi` fails: reinstall the NVIDIA driver on Windows and
reboot. `nvidia-smi` works on Windows but not in containers: `wsl2-gpu.md`.

## GPU newer than the image's CUDA build

**Symptoms**
- `TensorFlow was not built with CUDA kernel binaries compatible with compute capability 12.0. CUDA kernels will be jit-compiled from PTX, which could take 30 minutes or longer.`
- The first training step takes many minutes, then runs (PTX JIT, cached afterwards).
- `no kernel image is available for execution on the device`
- `CUDA error: invalid device function`
- `CUDNN_STATUS_ARCH_MISMATCH` or `CUDNN_STATUS_EXECUTION_FAILED` on the first Conv2D (camera-based jobs); MLP-only jobs run.

**Cause** The trainer's TensorFlow 2.7 and cuDNN 8.1 predate Ada (RTX 40) and
Blackwell (RTX 50). TensorFlow falls back to compiling PTX at start, which is
slow but usually works; libraries without PTX for the GPU (cuDNN kernels) fail
outright. Not yet tested on this stack with an RTX 40 or 50 GPU.

**Check**
- `nvidia-smi --query-gpu=name,compute_cap --format=csv,noheader`
- `docker compose logs sim-controller` and the trainer log `rl_agent\robotaxi.out`
  for the strings above.

Compute capability by generation:

| Compute capability | Generation | Example GPUs |
|---|---|---|
| 6.0, 6.1 | Pascal | GTX 10 series |
| 7.0 | Volta | V100 |
| 7.5 | Turing | RTX 20, GTX 16 |
| 8.0, 8.6, 8.7 | Ampere | A100, RTX 30 |
| 8.9 | Ada Lovelace | RTX 40 |
| 9.0 | Hopper | H100 |
| 10.0, 10.3, 12.0 | Blackwell | B200, RTX 50 (12.0) |

**Fix** A slow first step on an RTX 40/50 is expected: let it finish once. If
it fails with one of the kernel errors above, the trainer image needs a newer
TensorFlow/CUDA base; that is a code change, so report it with the GPU name and
the error rather than editing the Dockerfile.

## cuDNN does not match TensorFlow

**Symptoms**
- `Could not load library libcudnn_cnn_infer.so.8`
- `libcublasLt.so.12: cannot open shared object file`
- `Loaded runtime CuDNN library: 8.9.7 but source was compiled with: 8.1.0`
- Abort on the first Conv2D (`donut_camera` and other camera jobs) while MLP jobs work.

**Cause** The `sim_controller` image was built without the cuDNN pin, or from
an older Dockerfile: unpinned `libcudnn8` resolves to a CUDA 12 build that TF
2.7 cannot load.

**Check** `docker image history sim_controller:latest` (look for
`libcudnn8=8.1.1.33-1+cuda11.2`); `git log -1 -- docker/sim_controller/Dockerfile`.

**Fix** Rebuild the image from the current Dockerfile:
`docker compose build sim-controller`, then `docker compose up -d sim-controller`.

## GPU busy or out of memory

**Symptoms**
- `CUDA_ERROR_OUT_OF_MEMORY`, `OOM when allocating tensor`, `cudaErrorMemoryAllocation`
- `nvidia-smi` shows most memory used before training starts.

**Cause** `sim-controller` and `fly-brain` share the one GPU. TensorFlow in
`sim-controller` reserves nearly all GPU memory at start unless
`TF_FORCE_GPU_ALLOW_GROWTH=true` is set, which leaves `fly-brain` (about
210 MB) nothing; or the trainer starts second and gets nothing. Another stack,
a game, or the doctor's own model can also hold memory. On Windows,
`nvidia-smi` lists processes but usually shows `N/A` for per-process memory.

**Check** `nvidia-smi`; `docker compose config sim-controller` (is
`TF_FORCE_GPU_ALLOW_GROWTH` set?); `docker ps` (a second training stack or old
containers); `Get-Process llama-server -ErrorAction SilentlyContinue`.

**Fix** Set `TF_FORCE_GPU_ALLOW_GROWTH=true` in `sim-controller`'s
`environment` in `docker-compose.yml` and recreate it; close other GPU users;
stop the doctor's model before retrying training.
