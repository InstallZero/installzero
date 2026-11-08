# install.md format (InstallZero agent)

An `install.md` is a normal Markdown document a person can follow, with
machine-checkable blocks the InstallZero agent (`Agent.ps1`) runs and the
probe (`Probe.ps1`) writes. A document without `## Step` headings is treated
as plain install instructions: the agent plans its own steps from the prose.

## Layout

````markdown
---
name: comfyui
source: https://github.com/comfyanonymous/ComfyUI
summary: Node-based Stable Diffusion UI on port 8188
---

# ComfyUI install

Any prose: what gets installed, where, and how to use it afterwards.

## Step 1: python - Python 3.12

Why: ComfyUI and PyTorch need Python 3.10-3.12.

```requires
os: windows, linux
min_disk_gb: 20
```

```detect powershell
py -3.12 --version
```

```install powershell os=windows
winget install -e --id Python.Python.3.12 --accept-package-agreements --accept-source-agreements
```

```install bash os=linux
sudo apt-get install -y python3.12 python3.12-venv
```

```verify powershell
py -3.12 -c "import sys; print(sys.version)"
```
````

## Rules

- **Front matter**: `key: value` lines between `---` lines. `name` is
  required; it names the default install folder (`%USERPROFILE%\<name>`).
- **Steps**: `## Step <n>: <id> - <title>`, run in order. `<id>` is
  lowercase letters, digits, `-` and `.`. A `Why:` line says what the step
  is for.
- **Blocks**: fenced code with the info string `<kind> [shell] [key=value ...]`.
  - `requires`: conditions the machine must meet, as `key: value` lines.
    Keys: `os` (windows, linux, macos), `arch` (x64, arm64), `gpu` (nvidia,
    amd, intel, apple, none, any), `min_ram_gb`, `min_disk_gb`,
    `min_driver` (NVIDIA driver version). Lists are comma separated.
  - `detect`: exits 0 when the step is already done (then it is skipped,
    once its `verify` passes too).
  - `install`: does the step. When Windows must restart before the step
    works (WSL, drivers, Windows features), it ends with `exit 3010` (the
    Windows installer code for "restart required"): the run stops there,
    offers to continue at the next sign-in, and picks up from that step.
  - `verify`: exits 0 when the step worked. Required for every step.
  - `uninstall` (optional): undoes the step beyond what InstallZero
    tracks itself (packages, files, services, users), e.g. a database
    the step created. Run by `installzero uninstall`, last step first.
  - Shells: `powershell` (default on Windows) or `bash`.
- **Variants**: `key=value` pairs after the shell (same keys as `requires`)
  limit a block to matching machines. Of the blocks of one kind that match,
  the one with the most conditions is used (the first of them on a tie), so
  a generic block can sit next to specific ones.
- **Block options** (not conditions): `timeout=90m` replaces the time limit
  for a block (5 minutes for detect and verify, 30 for install) and suits
  long builds; `phase=<name>` names the failure-catalog phase the block's
  errors are looked up in, so a known failure is fixed (retried, Docker
  Desktop restarted, `wsl --update`, build cache cleared, each asked first
  when it restarts or clears something) or explained. Phases: `build`,
  `verify`, `git`, `download`, `docker-start`, `docker-gpu`, `compose-up`,
  `container`, `trainer`, `unity-client`. A project's own catalog entries
  go in a folder named in the front matter (`catalog: scripts/install/catalog`,
  relative to the install.md) and take precedence over the bundled ones.
- **Placeholders**: `{{INSTALL_DIR}}` (the install folder) and
  `{{REPO_DIR}}` (`{{INSTALL_DIR}}\<name>`) are replaced before running.
- **Ports**: a service's listening ports go in the front matter as
  `ports: web=8188, api=8189` and in blocks as `{{PORT_WEB}}`,
  `{{PORT_API}}`, never as literal numbers. When another program already
  holds one, InstallZero uses the next free port instead and says so.
  Configure the port explicitly (environment, config file, systemd drop-in,
  `-p`) rather than relying on the software's default.
- **Secrets**: never written into the file. Name the variable and where the
  user gets it (`HF_TOKEN: create at https://huggingface.co/settings/tokens`).

## Safety

Every block is classified before it runs (`Policy.ps1`), whoever wrote the
file:

| Tier | Examples | Runs |
|---|---|---|
| read | `git status`, `docker ps`, `Get-*`, `nvidia-smi` | automatically |
| project | `git clone` into the install folder, `docker compose up`, `pip install` in a venv inside it, files inside it | automatically |
| system | `winget`, `wsl --install`, services, registry outside HKCU, global `pip`/`npm`, anything unrecognised | after the user presses Y (default no) |
| never | formatting disks, `wsl --unregister`, deleting Docker volumes or files outside the install folder | refused |
