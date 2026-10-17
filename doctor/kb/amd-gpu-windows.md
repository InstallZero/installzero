# AMD GPUs on Windows

What works on a machine whose GPU is AMD (Radeon, or a Ryzen APU's integrated
graphics), and the known ROCm / HIP misconfigurations on Windows.

## What this stack can and cannot do on AMD

- **Training stack: needs NVIDIA.** `sim-controller` (TensorFlow 2.7 on CUDA
  11) and `fly-brain` (CuPy on CUDA 12.4) reserve an NVIDIA device. Docker
  Desktop on Windows has no AMD GPU passthrough, so on an AMD-only machine those
  services fail to start with
  `could not select device driver "" with capabilities: [[gpu]]`. That is a
  hardware limit, not a misconfiguration; do not suggest driver or ROCm changes
  for it.
- **The doctor's own model: works.** `Llm.ps1` uses llama.cpp's Vulkan build
  when there is no NVIDIA driver with CUDA 12.4 and a Vulkan driver is present
  (`vulkan-1.dll` in System32), otherwise the CPU build. Vulkan needs only the
  normal AMD Adrenalin driver. If the Vulkan build fails to start, the doctor
  does not yet retry on CPU: `-Backend cpu` forces it.
- ROCm inside WSL 2 is a separate platform (`/dev/dxg` plus the Windows
  driver) with its own AMD guide; it does not help Docker Desktop containers.

**Check** (doctor can run)
- `Get-CimInstance Win32_VideoController | Select-Object Name, DriverVersion, Status`
- `system_facts`

## Known ROCm / HIP failures on Windows

These matter when the user runs PyTorch or llama.cpp against AMD's HIP SDK
outside this stack, or a llama.cpp HIP build. Apps that ship their own runtime
(Ollama, LM Studio, Lemonade) are diagnosed by their own projects.

### HIP SDK not installed

**Symptoms** `'hipInfo' is not recognized as an internal or external command`;
no `AMD\ROCm` folder under `C:\Program Files`; `amdhip64_6.dll was not found`.

**Check** `Test-Path 'C:\Program Files\AMD\ROCm'`; `$env:HIP_PATH`;
`Get-Command hipInfo -ErrorAction SilentlyContinue`.

**Fix** Install the AMD HIP SDK for Windows. The user installs it; it is not
part of this stack.

### Adrenalin driver too old for the HIP SDK

**Symptoms** `hipInfo` cannot enumerate devices; `driver too old`;
HSA `no agents found`; `hipErrorNoDevice`.

**Check** `Get-CimInstance Win32_VideoController | Select-Object Name, DriverVersion`.

**Fix** Update the Adrenalin driver from amd.com to a version listed as
supported for the installed HIP SDK, then reboot.

### HIP or ROCm binaries not on PATH

**Symptoms** `hipInfo` / `rocminfo` not found right after installing the SDK.

**Check** `$env:Path -split ';' | Select-String -SimpleMatch 'ROCm'`; `$env:HIP_PATH`.

**Fix** Add `%HIP_PATH%bin` to the user PATH and open a new terminal.

### Framework built for a different ROCm major version

**Symptoms** `amdhip64_5.dll` or `amdhip64_6.dll` (or `libamdhip64.so.X`) fails
to load while a different major version is installed.

**Fix** Install the framework build that matches the installed HIP SDK major
version, or install the matching SDK.

### GPU architecture not in the build

**Symptoms** `hipErrorNoBinaryForGpu`, `HSA_STATUS_ERROR_INVALID_ISA`,
`invalid device function`, `no kernel image is available`.

**Cause** The GPU's `gfx` target (e.g. `gfx1151`, `gfx1150`, `gfx1103` on
recent Ryzen APUs) is not in the framework's compiled architecture list.

**Fix** Use a build that includes that `gfx` target. Do not set
`HSA_OVERRIDE_GFX_VERSION` by hand as a workaround.

### `HSA_OVERRIDE_GFX_VERSION` left set

**Symptoms** Page faults, `OUT_OF_REGISTERS`, or random crashes with the
variable set, on a GPU that now has native support.

**Check** `[Environment]::GetEnvironmentVariable('HSA_OVERRIDE_GFX_VERSION', 'User')`
and the same for `'Machine'`.

**Fix** Remove the variable (user and machine scope) and open a new terminal.

### Integrated and discrete AMD GPUs both visible

**Symptoms** Crashes or segfaults on a laptop with a Ryzen APU plus a Radeon
dGPU; `HIP_VISIBLE_DEVICES` unset.

**Fix** Set `HIP_VISIBLE_DEVICES` to the dGPU's index (from `hipInfo`).

## Missing Microsoft Visual C++ runtime (any GPU)

**Symptoms**
- `The code execution cannot proceed because VCRUNTIME140_1.dll was not found`
- `vcruntime140.dll` or `msvcp140.dll` missing
- A native program (`llama-server.exe`, `hipInfo.exe`) exits immediately with
  code `-1073741515` (`0xC0000135`, a DLL not found).

**Cause** The Microsoft Visual C++ 2015-2022 Redistributable (x64) is not
installed. Prebuilt llama.cpp and HIP binaries depend on it.

**Check** `Test-Path "$env:SystemRoot\System32\vcruntime140_1.dll"`;
`reg query "HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64" /v Version`.

**Fix** Install it: `winget install Microsoft.VCRedist.2015+.x64`.
