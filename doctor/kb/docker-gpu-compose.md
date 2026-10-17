# GPU access in docker-compose.yml

How services in this stack request the GPU, and the mistakes that stop a
container from getting it.

## How this stack requests the GPU

`sim-controller` and `fly-brain` each reserve one NVIDIA device:

```yaml
deploy:
  resources:
    reservations:
      devices:
        - driver: nvidia
          count: 1
          capabilities: [gpu]
```

On a machine with one NVIDIA GPU both reservations get the same card and share
its memory (see "GPU busy or out of memory" in
`nvidia-driver-cuda.md`). No other service needs the GPU.

The images supply the CUDA runtime (`nvidia/cuda:*-runtime` bases). Drivers and
the CUDA toolkit are never installed in an image: the container uses the host
driver, and the runtime image is enough unless something compiles CUDA code.

## Service has no GPU

**Symptoms**
- `failed call to cuInit: CUDA_ERROR_NO_DEVICE` in `sim-controller`, or
  `cudaErrorNoDevice` / `CUDARuntimeError` in `fly-brain`, while
  `nvidia-smi` works on Windows and the other GPU service runs.
- TensorFlow logs only `/device:CPU:0`; training is very slow.

**Cause** The service lost its `deploy.resources.reservations.devices` block
(edited compose file, a merge, an override file), or a field in it is wrong.
Each of these silently gives no GPU or fails validation:
- `driver` not `nvidia`
- `capabilities` without `gpu`
- `count` or `device_ids` missing (`count: all` or `count: 1` is fine)
- the block placed under `deploy:` of a different service, or indented under
  `limits:` instead of `reservations:`

**Check** (doctor can run)
- `docker compose config sim-controller` and `docker compose config fly-brain`:
  the rendered config must show the `devices` block above.
- `git diff -- docker-compose.yml` and `Get-ChildItem docker-compose*.yml`
  (an override file such as `docker-compose.override.yml` is merged
  automatically).
- `docker inspect <container> --format '{{json .HostConfig.DeviceRequests}}'`:
  `null` means the running container was created without a GPU request.

**Fix** Restore the block from `git show HEAD:docker-compose.yml`, then
`docker compose up -d --force-recreate <service>`. A container keeps the
device requests it was created with, so editing the file alone changes
nothing until it is recreated.

## Deprecated `runtime: nvidia`

**Symptoms**
- `Error response from daemon: unknown or invalid runtime name: nvidia`
- A service uses `runtime: nvidia` instead of a `deploy` device reservation.

**Cause** `runtime: nvidia` is the old nvidia-docker 2 syntax. It needs an
`nvidia` runtime registered in the daemon, which Docker Desktop does not
always have; the `deploy.resources.reservations.devices` form works everywhere.

**Fix** Replace `runtime: nvidia` with the `deploy` block above and recreate
the service.

## `--gpus` missing on a manual `docker run`

**Symptoms** A container started by hand (not through compose) prints
`no CUDA-capable device is detected` or `nvidia-smi: not found`.

**Cause** `docker run` only passes the GPU with `--gpus all`. Without it,
`nvidia-smi` and `libcuda.so` are not mounted into the container.

**Fix** Add `--gpus all`, or start the service with `docker compose up -d`.

## Images that cannot use the GPU

**Symptoms** `Could not load dynamic library 'libcudart.so.11.0'`,
`libcudart.so: cannot open shared object file`, `CUDA not available` in an
image built from `python:*`, `ubuntu:*` or `alpine:*`.

**Cause** CPU-only base image. Only `sim_controller` and `fly_brain` use the
GPU, and both build from `nvidia/cuda` bases; `madscientist` is deliberately
`python:3.11-slim` and CPU-only.

**Fix** Rebuild the affected image from the repo's Dockerfile
(`docker compose build <service>`); do not change base images to fix a
runtime problem.
