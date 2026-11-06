Review this PC's configuration for running rl-roboracer training for hours unattended. Check exactly these items, nothing else:

1. memory: Docker's memory (system_facts, docker info mem) against physical RAM, and the [wsl2] memory= and swap= lines in {{user_profile}}\.wslconfig. Read that file with read_file before judging items 1 and 2 (it may not exist; then WSL gets half of RAM). If Docker's memory does not match memory=, the file is not in effect: recommend saving work and running wsl --shutdown, then restarting Docker Desktop. Otherwise recommend a change only if Docker has less than 8 GB while the PC could give more, if memory= leaves Windows less than 6 GB (the Unity clients run on Windows), or if swap=0.
   Improves: the trainer (TensorFlow and its replay buffer) gets the memory intended for it, so long runs are not killed for running out of memory; or Windows and the Unity clients keep enough memory not to swap.
2. processors: Docker's CPUs against the logical processors (Get-CimInstance Win32_Processor) and any processors= line in .wslconfig. A mismatch with processors= means the file is not in effect, as in item 1. Recommend a change only if a processors= limit gives Docker fewer than half of them.
   Improves: the containers (trainer, ros-server bridges) get the CPUs intended for them, so steps are processed without waiting; or the Unity clients keep the cores they need to simulate at full speed.
3. power, 4. sleep, 5. gpu-driver, 7. disk, 8. unity-gpu: measured by the installer just now (Remedies.ps1). These verdicts are final: do not re-check them, do not contradict them, and leave them out of fix (the installer adds their steps):
{{measured}}
{{apply_offer}}
6. gpu-services: call gpu_drivers (ignore its driver version lines; item 5 covers them). With several NVIDIA GPUs, say which GPU each GPU service sees (gpu_drivers maps what each container sees to the host GPU by UUID) and recommend device_ids only if a larger GPU sits unused; name GPUs in device_ids by UUID ("GPU-..."), never by index, because indexes can differ inside containers. A FAILED in-container probe is a required fix: report its output. Services that are not running are not a finding. Also flag other programs holding most of the GPU memory (nvidia-smi).
   Improves: a working in-container GPU lets the trainer and fly-brain use the GPU at all; moving a service to an unused larger GPU, or closing programs that hold GPU memory, gives training more GPU memory and speed.
9. gpu-load: judge only from the measurement below, taken while the stack ran and before your own model was loaded. If the trainer logged no recent training step (within about 2 minutes), training was not running: UNKNOWN. If GPU memory peaked above 90% of the total, recommend fewer Unity clients (re-run the installer with a smaller -Clients) or closing the GPU programs listed. If average utilization stayed below 40% and memory below 60% while training ran, the GPU has room: recommend one more Unity client (re-run the installer with -Clients <current + 1>, at most 4). Otherwise OK.
   Improves: fewer clients or closed programs keep training from crashing when the GPU runs out of memory; another client collects more environment steps per hour on a GPU that is otherwise idle part of the time.

{{gpu_load}}

An item that already looks fine needs no change; do not suggest optional tweaks. run_command already runs PowerShell: call cmdlets directly, not through powershell -Command or wmic. If a check cannot be done with your tools, say so for that item instead of guessing.
Recommend a change only when the value you observed differs from what the item asks for; if the current value already meets it, the item is OK.
In finish, root_cause has exactly nine lines, one per item in this order, each "<n>. <item>: OK", "<n>. <item>: CHANGE - <reason>" or "<n>. <item>: UNKNOWN - <reason>" (items 3, 4, 5, 7 and 8 copied from the measured verdicts). fix lists only CHANGE items among 1, 2, 6 and 9, numbered by item, each with its own Improves line directly under its steps, based on the item's Improves note and the values you observed. Layout:
1. memory: <exact steps>
   Improves: ...
