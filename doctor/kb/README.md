# InstallZero doctor knowledge pack

Offline pages the doctor's model searches (`search_kb`, BM25) when its built-in
knowledge of drivers, WSL and Docker Desktop is stale or missing. One topic per
page. Each failure section has the same parts, so a search on an error string
lands on the section that explains it:

- **Symptoms**: exact error text as it appears in logs. Keep these verbatim;
  they are what the search matches.
- **Cause**
- **Check**: what the doctor can run itself with its read-only tools, and what
  it has to ask the user to run (anything that starts a container or changes
  state).
- **Fix**: what to propose. The doctor asks before every change.

Pages state facts for this stack (Windows, Docker Desktop on WSL 2, one NVIDIA
GPU shared by `sim-controller` and `fly-brain`) rather than general advice.

## Sources and licences

| Page | Adapted from | Licence |
|---|---|---|
| `nvidia-driver-cuda.md` | [env-doctor](https://github.com/mitulgarg/env-doctor) `data/compatibility.json`, `data/compute_capability.json`, `docs/CONTAINER_BEST_PRACTICES.md`; NVIDIA CUDA release notes | MIT (env-doctor, Copyright (c) 2025 Mitul Garg) |
| `wsl2-gpu.md` | env-doctor `docs/guides/wsl2.md`, `docs/commands/wsl.md`, `detectors/wsl2.py` | MIT |
| `docker-gpu-compose.md` | env-doctor `docs/CONTAINER_BEST_PRACTICES.md`, `validators/compose_validator.py` | MIT |
| `docker-desktop-bind-mounts.md` | [docker-development-skill](https://github.com/netresearch/docker-development-skill) `docker-via-wsl` and `references/bind-mount-ownership.md`, Netresearch DTT GmbH | CC BY-SA 4.0 (this page only) |
| `amd-gpu-windows.md` | [amd/skills](https://github.com/amd/skills) `staging/rocm-doctor/reference.md` | MIT (Copyright (c) 2026 Advanced Micro Devices, Inc.) |

All pages were rewritten for this stack; none is a verbatim copy. The MIT
licences require keeping the copyright notices above with the bundle.
`docker-desktop-bind-mounts.md` is a derivative of CC BY-SA 4.0 text and stays
under CC BY-SA 4.0.
