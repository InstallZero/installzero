# InstallZero install-bench: plan and difficulty rubric (draft for review)

Goal: a benchmark of 200 GitHub projects. Each project is installed and
running; the InstallZero probe reconstructs an install.md from the live
system (docs hidden), and the result is scored by weighted fact recall, as
`agent/eval/repos.json` does today. Target: at least 55% of
the repositories in the second and third most difficult of four tiers (T2
and T3).

Status: rubric v1; candidate set v1 of 200 repositories with difficulty 7
or more (section 5), to review. Settled so far: four tiers; a 1-10
difficulty score per repository, from 4 factors of 3 components each.

## 1. Terms

- **Difficulty score**: 1 to 10 per repository, from the rubric in
  section 3. Decides the repository's tier.
- **Factor**: one of four broad sources of install difficulty (machine
  footprint, machine-specific adaptation, moving parts, configuration and
  hidden knowledge).
- **Component**: one specific thing within a factor (for example "GPU
  stack" within machine-specific adaptation), scored 0 to 3 in steps of
  0.25.
- **Install behavior / fact**: one thing a correct install must do or know,
  scored for recall as a regex over the probe's install.md, with a weight
  (3 = the install fails without it, 2 = important, 1 = detail). Each fact
  is tagged with the component it belongs to, so recall can be reported per
  factor.

## 2. What the existing runs say about difficulty

51 delivered probes of rl-roboracer on rltest (34 local-model, 17 hosted),
share of runs that recovered each fact:

| Fact | Local models | Hosted models | Why it is hard or easy |
|---|---|---|---|
| docker-desktop | 100% | 100% | In the installed-programs list |
| compose-build | 94% | 100% | Compose file in the project folder |
| unity-client | 85% | 100% | Running process |
| dashboard-port | 85% | 94% | Listening port |
| gpu-driver | 68% | 94% | Minimum driver must be worked out from the CUDA version |
| ros-server | 68% | 94% | One of seven containers |
| wsl | 53% | 100% | Needed by Docker Desktop, but nothing says "install WSL" |
| mongo-password | 53% | 100% | One key among about 40 in .env |
| tensorboard-port | 41% | 94% | One of six listening ports |
| services (sim-controller) | 38% | 94% | `compose up` starts it without naming it |
| num-envs | 12% | 100% | A value in .env and a process argument |
| clone (GitHub URL) | 6% | 76% | rltest's checkout came from a bundle: the remote is not the URL |
| startup | 3% | 88% | Shortcut in the Startup folder, outside the project |

What this says about the factors in section 3:
- Things installed or changed outside the project (startup entries, WSL,
  the driver) are missed most: **machine footprint**.
- Values that depend on the hardware (the driver floor) need knowledge, not
  just looking: **machine-specific adaptation**.
- Many similar parts (seven containers, six ports) cut local recall about in
  half: **moving parts**.
- Settings and steps that leave little trace (num-envs, the clone source)
  are hardest of all: **configuration and hidden knowledge**.
- One fact (`services`) tests the scorer, not the probe: a correct
  install.md need not name it. Facts must pass the validity checks in
  section 4.
- Hosted models are near 100% on rl-roboracer, so the benchmark needs
  harder projects to separate strong models, and a spread of medium ones to
  separate local models.

## 3. Difficulty rubric (v1)

### Structure and arithmetic

Four factors, three components each. Every component is scored 0 to 3 in
steps of 0.25, against the anchors below (0, 1, 2 and 3 are described;
quarter steps fall between them).

**Factor score** (0 to 3): the highest component counts in full; the other
two add a discounted residual:

    factor = min(3, highest + 0.2 x (sum of the other two))

So one hard component makes a factor hard, a second hard component makes it
somewhat harder, but two medium components never equal one hard one. The
discount (0.2) is a parameter to tune in calibration.

**Difficulty score** (1 to 10):

    difficulty = 1 + 9 x (sum of the four factors) / 12

A project with nothing to it scores 1; all four factors at 3 scores 10.

**Tiers**:

| Tier | Difficulty | Typical project |
|---|---|---|
| T1 Basic | 1 to under 3.5 | One binary, installer or container; nothing to configure |
| T2 Intermediate | 3.5 to under 5.5 | A runtime or engine, a data or model download, a few settings |
| T3 Advanced | 5.5 to under 7.5 | GPU stack, several services, or integration outside the project |
| T4 Expert | 7.5 to 10 | Several of the above at once, plus steps that leave no trace |

Rule against double counting: each piece of the install is scored in one
component only, the one it fits best.

### Factor 1: Machine footprint (what the install puts on the machine outside the project)

**1a. System prerequisites**: software installed system-wide before the
project itself (runtimes, container engines, drivers, build tools).
- 0: nothing beyond what the OS has.
- 1: one common tool or runtime (git, Python, Node).
- 2: a heavyweight component (Docker Desktop, a GPU driver, Visual Studio
  build tools, WSL).
- 3: several heavyweight components that depend on each other and must go in
  a set order.

**1b. Integration outside the project**: changes elsewhere on the machine.
- 0: none; everything lives in the project folder.
- 1: data or settings in the user profile (a models folder, a config
  directory).
- 2: starts by itself or is wired in (startup entry, service, scheduled task,
  PATH, shell profile, firewall rule).
- 3: several of those, or system-level changes (registry, kernel features,
  hosts file).

**1c. Privilege and restarts**:
- 0: no admin rights, no restarts.
- 1: admin rights for one step.
- 2: admin rights plus a restart, or virtualization or a Windows feature to
  enable.
- 3: several restarts or BIOS-level settings.

### Factor 2: Machine-specific adaptation (how much the install depends on this machine)

**2a. Accelerator stack**: GPU, CUDA, ROCm, Metal.
- 0: none.
- 1: uses a GPU if present, picks it up automatically.
- 2: must match the hardware explicitly (a CUDA-specific wheel index, a
  minimum driver version, GPU access for containers).
- 3: several matches that must agree (driver, CUDA toolkit and compiled
  extensions), or different stacks per GPU vendor.

**2b. OS and architecture variants**:
- 0: identical everywhere.
- 1: minor differences (paths, a different launcher script).
- 2: different steps per OS or CPU architecture.
- 3: parts run on different OSes at once (Windows host with Linux containers
  or WSL), or a whole branch per platform.

**2c. Version pinning**:
- 0: latest of everything works.
- 1: a version range (Python 3.10 to 3.12).
- 2: an exact version that differs from the usual default (Python 3.10.6).
- 3: several exact versions that conflict with what is usually installed.

### Factor 3: Moving parts (how much there is to get right)

**3a. Processes and services**:
- 0: run a single command or program.
- 1: one long-running server on one port.
- 2: several services or containers that talk to each other.
- 3: many services, several ports, startup order or several client
  processes.

**3b. Toolchains and package managers**:
- 0: none; a prebuilt binary or image.
- 1: one package manager (pip in a venv, npm).
- 2: two ecosystems (Python and Node), or images built locally.
- 3: native code compiled during the install (C++, CUDA extensions, Rust).

**3c. External artifacts**: things downloaded outside the package manager.
- 0: none.
- 1: one artifact from an obvious place (a model with `ollama pull`).
- 2: several artifacts, or one from a release page or a hub (a checkpoint,
  a client build, a compose file from a release).
- 3: large or gated artifacts (license acceptance, sign-in), or ones
  placed in exact folders.

### Factor 4: Configuration and hidden knowledge (what is not obvious from the files)

**4a. Configuration and secrets**:
- 0: works with defaults.
- 1: a few settings with sensible defaults.
- 2: a config file with required values, or secrets the user supplies.
- 3: many interdependent settings (values that must agree across files or
  services).

**4b. Implicit knowledge**: steps nothing on the machine states.
- 0: everything needed is visible.
- 1: one step follows from what is there (a venv implies "create a venv").
- 2: needs outside knowledge (Docker Desktop needs WSL 2; CUDA 12.4 needs
  driver 551.61 or newer).
- 3: several such chains, or project-specific conventions (a setting that
  must equal the number of clients).

**4c. Manual or one-time steps**:
- 0: none.
- 1: one value the user types (a password).
- 2: a step outside the terminal (first-run wizard, account creation, GUI
  setting).
- 3: several such steps, or ones that leave no trace once done (an
  installer run and deleted, an OAuth app created on a website).

### Worked examples

Discount 0.2. Scores are a first pass from what we know of each install.

**rl-roboracer** (Windows, Docker Desktop, GPU, Unity clients):

| Factor | Components (a / b / c) | Factor score |
|---|---|---|
| 1 Machine footprint | 2.5 (Docker Desktop, WSL, driver, git) / 1.75 (startup shortcut, client folder) / 2 (admin, restart, virtualization) | min(3, 2.5 + 0.2 x 3.75) = 3.0 |
| 2 Machine-specific adaptation | 2 (GPU in containers, driver floor) / 2 (Windows host, Linux containers) / 1 (versions pinned inside the images) | 2 + 0.2 x 3 = 2.6 |
| 3 Moving parts | 2.5 (seven services, several ports, two clients) / 1.5 (three images built locally) / 2.5 (Unity client and demos from releases) | min(3, 2.5 + 0.2 x 4) = 3.0 |
| 4 Configuration and hidden knowledge | 2 (.env with secrets) / 2 (WSL, driver floor, NUM_ENVS = clients) / 1 (secrets typed in) | 2 + 0.2 x 3 = 2.6 |

Difficulty = 1 + 9 x 11.2 / 12 = **9.4, T4**.

**ComfyUI** (Python venv, CUDA PyTorch):
1: 1.5 / 0.25 / 0 = 1.55. 2: 2.5 (CUDA wheel index) / 1.5 / 1 = 3.0.
3: 0.5 / 0.75 / 1.5 (checkpoints) = 1.75. 4: 0.25 / 0.5 / 0.75 = 0.9.
Difficulty = 1 + 9 x 7.2 / 12 = **6.4, T3**.

**Ollama** (Windows installer):
1: 1 / 1.5 (tray app at logon, models in profile) / 0.5 = 1.8.
2: 0.75 / 0.5 / 0 = 0.85. 3: 0.5 / 0 / 1 (model pull) = 1.1.
4: 0.25 / 0.25 / 0.25 = 0.35. Difficulty = 1 + 9 x 4.1 / 12 = **4.1, T2**.

**n8n** (one Docker container with a volume):
1: 1.5 (Docker) / 0.5 (volume) / 1 = 1.8. 2: 0.25 / 0 / 0.25 = 0.3.
3: 0.5 / 0 / 0.25 = 0.55. 4: 0.5 / 0.25 / 1 (owner account wizard) = 1.15.
Difficulty = 1 + 9 x 3.8 / 12 = **3.9, T2** (near the T1 line).

The spread (3.9, 4.1, 6.4, 9.4) matches intuition. Open WebUI, Immich and
SD WebUI get scored the same way in calibration.

## 4. Fact validity checks

A fact enters the benchmark only if:

1. **Necessary**: a correct install.md must contain it (a replay fails or
   differs without it). Names that are merely present on the machine fail
   this.
2. **Specific**: its regex matches a gold install.md for the project and
   does not match a generic decoy (a plausible install.md for a different
   project of the same kind). Catches patterns like `\b80\b`.
3. **Single**: one behavior per fact.
4. **Not guessable**: a blind baseline (a model given only the project name
   and no tools, asked for an install.md) does not hit it in most runs.
   Famous projects' installs are memorized; a fact the blind baseline gets
   measures memory, not probing.

Coverage: every component scored 1 or more has at least one fact, so recall
can be reported per factor and the difficulty score is backed by what is
measured.

### Blindness: the probe never sees the install

The task is to recover the procedure from the running system. Install files
are used only to build the environment; the probe is blind to them:

- **Always hidden**: README and docs (any markdown), install, uninstall,
  setup and bootstrap scripts, `scripts/install/` and installer folders,
  shell history, the Windows recent-files list, InstallZero's own files.
- **Hidden per project** (`hide` in the benchmark's project list): test
  harness paths such as rltest's staging folder and the installer's state
  and logs.
- **Content guard**: every line of the hidden files (30 characters or
  longer, and not also in a visible file) is withheld from every tool
  output, however the model reached it (a wildcard, a copy, history). An
  output that is mostly hidden content is withheld whole. git commands that
  print contents, diffs or patches are refused.
- **What stays visible**: what runs and what it left behind, including paths
  that point at install files (a startup shortcut's target, a git remote);
  only contents are hidden. Compose files, Dockerfiles and dependency
  manifests count as part of the running system.
- **Audit**: each run records refused attempts and withheld lines. In the
  Linux containers, install files are also removed before the snapshot is
  committed, so the guard is a second line of defence there.
- **Blind baseline**: an install.md written from the project name alone,
  with no tools. On rl-roboracer it scored 19% (Opus) and 27% (GPT 5.6),
  from the URL in the prompt and generic guesses: the project is not
  memorized.

## 5. Hunting

The rubric is scored from a project's install docs before installing it,
then rescored after the install (scores move once the real install is
seen). Mix target for the 200 repositories: T2 + T3 >= 55% (110+), T1 <=
30%, T4 >= 10%, spread across Python, Node, Go/Rust binaries, compose
stacks, GPU ML apps and native Windows apps. About 400 candidates are
profiled to end with 200 that meet the mix.

### Candidate set v1 (difficulty 7 or more)

`agent/eval/bench/`: `candidates.csv` (294 candidates
with their 12 component scores), `Select-Bench.ps1 -Verify` (computes the
scores, drops difficulty under 7, checks each repository on GitHub, and
selects), `scored.csv` (every candidate and why it was dropped) and
`bench.json` (the selection). The list is in
[install-bench-candidates.md](install-bench-candidates.md).

Tracking is in MongoDB, database `installzero` in the stack's mongo
(`Sync-BenchDb.ps1` loads both, safe to rerun): `bench_repos` (every
candidate, `_id` = owner/name, with rubric components, factors,
difficulty, GitHub metadata, and `status` selected or dropped with the
reason) and `bench_results` (one document per probe run from the
results.jsonl files, deduplicated by repo, model, time and install.md).

- **Filter**: 16 candidates under 7 dropped, including the calibration
  projects ComfyUI (6.4), Open WebUI (6.8), Ollama (4.1) and n8n (3.9);
  6 archived and 9 with no push in two years dropped (text-generation-inference,
  mmdetection, LLaVA, gecko-dev and others). 19 were renamed on GitHub
  and carry their new names.
- **Disk**: installs needing 100 GB or more are dropped (`-MaxDiskGB`):
  AlphaFold 2 and 3, OpenFold, RoseTTAFold All-Atom (genetic databases),
  CARLA (Unreal Engine source build), Security Onion. `disk.csv` holds
  the estimates for every install of about 50 GB or more, for the
  documented default install (map servers with a regional extract, speech
  toolkits with their small example recipes).
- **Selection**: multi-platform and Windows installs first (all 140 that
  pass), then 60 Linux-only ones, filling the least covered of 46
  categories each time. 137 install on two or more OSes; 54 need an
  NVIDIA GPU.
- **This replaces the mix target above**: with a cutoff of 7, every
  project is in upper T3 (17) or T4 (183); there is no T1 or T2, and the
  median is 8.7. Tiers no longer separate the set; recall per factor and
  per fact has to.
- **Scores are desk scores** from the documented install, not yet checked
  by installing. Rescoring after the install will move some under 7.
- **To review before building**:
  - Renamed repositories whose content may have changed with the name:
    NVIDIA/NeMo is now NVIDIA-NeMo/Speech, aphrodite-engine is now
    dphnAI/sonar, calcom/cal.com is now calcom/cal.diy.
  - Gated or signed-in artifacts: Isaac Gym (NVIDIA developer sign-in),
    FLUX and other gated Hugging Face weights.
  - Whole-machine installs that need a VM rather than a container:
    BigBlueButton, DevStack, Kubespray, FreeIPA, Mail-in-a-Box, iRedMail,
    flare-vm.
  - Large but under 100 GB: Wan2.1, HunyuanVideo, TensorRT-LLM, Isaac ROS,
    O3DE, nf-core/sarek (50 to 75 GB each); Nominatim and Pelias imports
    grow past 100 GB if a whole country or the planet is imported.

## 6. Test environments

- **Linux Docker containers** (default for projects that support Linux):
  one image per project, installed from its docs, then committed, so every
  probe runs on an identical snapshot in a fresh container. GPU projects use
  `--gpus all` (Docker Desktop on WSL 2 supports it here). Compose projects
  need Docker inside the container (privileged docker-in-docker, or mounting
  the host's Docker socket with a per-project compose project name).
- **rltest and more Windows accounts** for Windows-only or Windows-specific
  installs (installers, startup entries, WSL), reset between projects with
  `Reset-TestAccount.ps1`.

Engineering needed for Linux (the probe is Windows-only today):
- The model and llama-server stay on the host; tools run inside the
  container through `docker exec`, behind a small executor interface the
  dispatch calls.
- A Linux inventory: `ps`, `ss -ltnp`, `dpkg`/`apt-mark showmanual`, pip and
  node packages, `systemctl`, cron, `/etc`, `~/.config`, and docker inside the
  container. Plus a read-only allowlist for Linux commands.
- Docs hidden in the container (as `-NoDocs` does), and the probe's own files
  absent.

## 7. Phases

1. **Rubric review** (now): agree the factors, components, anchors,
   discount and tier cutoffs.
2. **Calibrate on the seven known projects**: install comfyui, open-webui,
   ollama, n8n, immich and sd-webui (Linux containers where possible,
   rltest otherwise); rl-roboracer is already done. Two independent
   ratings per project (you and a model) to check the anchors are clear
   enough to agree on. Run 3 probes each with gpt-oss-20b, Gemma 4 E4B and
   one hosted model, plus the blind baseline, and check that difficulty
   predicts recall. Tune the discount and cutoffs.
3. **Linux harness**: executor interface, Linux inventory, container
   lifecycle, GPU, snapshot and reset. (Can start in parallel with 2.)
4. **Hunt**: about 400 candidates scored from their docs; select 200 that
   meet the mix target.
5. **Build at scale**: install each project (scripted recipe, or the
   InstallZero agent with a human check), write a gold install.md, draft
   facts from it with a model, run the validity checks, rescore difficulty.
6. **Baselines and release**: probe runs for the model set, reports per tier
   and per factor, versioned facts and images.

## 8. Questions for review

1. The four factors and their twelve components: anything missing, merged
   or misplaced?
2. The residual discount (0.2) and the cap at 3 per factor.
3. Tier cutoffs (3.5, 5.5, 7.5).
4. Famous projects: keep them, with the blind baseline correcting for
   memorized installs, or cap them?
5. Windows share: how many of the 200 should be Windows-specific installs
   (rltest-style accounts) versus Linux containers?
