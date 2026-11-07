# InstallZero: `installzero install` for any repo

Status: plan, not started. Written 2026-11-07. Builds on
`docs/agentic-installer-split-plan.md` (engine, catalog, records),
`docs/go-port-plan.md` (phase 5, the installer agent) and
`agent/INSTALL-MD.md` (the install.md format).

## Goal

A repo owner adds InstallZero to their project once. A user then gets the
binary with one line from the README and, at the project root, runs

```text
installzero install
```

which reads the repo and installs everything the project needs, asking
before any machine-wide change. rl-roboracer is the first project.

Three capabilities make this work:

1. **Review**: read a project's existing install material (scripts,
   Dockerfiles, README) and propose a complete, tested install.md, with a
   report of what was wrong or missing.
2. **Model choice**: the user picks the model the agent uses. Hosted models
   use the user's own API key, kept in the OS credential store.
3. **A default local model**: the fine-tuned Gemma, downloaded on first use
   with llama-server and run on loopback, so the default needs no account,
   no key, and sends nothing off the machine.

## The user's side

First time, one line from the project's README:

```text
irm https://raw.githubusercontent.com/frenb/rl-roboracer/main/install.ps1 | iex       # Windows
curl -fsSL https://raw.githubusercontent.com/frenb/rl-roboracer/main/install.sh | sh  # Linux, macOS
```

The bootstrap downloads the installzero release the project pins, checks its
sha256, puts it in the user's bin folder (`%LOCALAPPDATA%\installzero\bin`,
`~/.local/bin`) on the PATH, and runs `installzero install` in the project.
Later runs (after a pull, or to repair) are just `installzero install`.

What `installzero install` does:

1. **Finds the project**: the git root at or above the working folder, and
   `.installzero/config.json` if the project has one.
2. **Picks the instructions**, most reliable first:
   - an owner-reviewed `install.md` (in `.installzero/` or the root): run
     step by step (detect, install, verify);
   - otherwise the repo itself: the agent reads the README's install
     section, `install.sh` / `setup.ps1`, `Dockerfile` /
     `docker-compose.yml`, `requirements.txt`, `pyproject.toml`,
     `package.json`, and plans its own steps (`Agent.ps1`'s plain mode).
3. **Picks the model**: the user's saved choice, else the owner's
   suggestion from the config, else the default local model (asking before
   the download). A structured install.md whose steps all pass needs no
   model at all: the model is only called when a step fails or when there is
   no install.md.
4. **Installs under the policy tiers** (`agent/Policy.ps1`): read-only and
   install-folder changes run, machine-wide changes ask (default no) showing
   the exact command, destructive commands are refused. Failures go to the
   catalog first, then the agent's doctor tools.
5. **Records** what it did in `lock.json` (commands, tiers, approvals,
   folders, services, images), a report and a transcript, so
   `installzero remove` and `installzero install` (repair) act on facts.

### Changes to the install.md runner

- **In place**: today the agent installs into `%USERPROFILE%\<name>` and
  clones into `{{REPO_DIR}}`. Run from a project, `{{REPO_DIR}}` is the
  project root, the clone step's detect passes, and project-tier paths are
  the repo and its ignored folders (`.venv`, `node_modules`, `data/`).
- **Reboot and sign-out**: WSL and Docker Desktop installs need them
  (rl-roboracer's `Install.ps1` handles both). A block may end with exit
  code 194 (`reboot`) or 195 (`sign-out`); the runner saves its place in
  `lock.json`, tells the user, and continues from the next block on the next
  `installzero install`.
- **Named steps**: the split plan's built-in phase types (`wsl`,
  `docker-desktop`, `docker-gpu`, `winget-package`) become steps an
  install.md can use by name (`uses: installzero/docker-desktop`), so
  rl-roboracer's tested handling is not rewritten as prose. This replaces
  the `install.json` manifest: install.md is the one format a project ships.

## The owner's side

```text
installzero init
```

at the repo root:

1. Runs `installzero review` on the repo (below): a draft install.md,
   test-installed in a throwaway machine and fixed until it passes, plus
   `review.md`.
2. Writes `.installzero/config.json`:

   ```json
   {
     "installzero": { "version": "0.6.0", "sha256": { "windows-amd64": "...", "linux-amd64": "..." } },
     "install_md": ".installzero/install.md",
     "model": { "suggested": "installzero-gemma" },
     "hidden": [".env", "secrets/"],
     "failures": { "owner": "github:frenb/rl-roboracer", "share": ["installzero", "owner"] }
   }
   ```

3. Writes the bootstrap scripts (`install.ps1`, `install.sh`) with the
   pinned version and checksums, and a README snippet.
4. The owner reviews and commits all of it. `installzero init --update`
   moves the pin to a newer release and re-tests.

**Bootstrap scripts, not the binary, are committed.** A binary in git
bloats every clone, needs one per OS and architecture, and an unsigned
`.exe` in a repo draws Defender and reviewer suspicion. The script is a few
lines anyone can read; the binary comes from a versioned InstallZero
release, checked against the sha256 the owner committed. A project that
wants the binary in its own GitHub release can attach it; the bootstrap
then downloads from there with the same check.

**Version pinning**: if the installzero on the PATH is older than the
project's pin, `installzero install` fetches the pinned release (checked the
same way) and hands over to it, so projects pinned to different versions
coexist.

## Review: `installzero review`

```text
installzero review [<script | folder | owner/repo | URL>] [--out install.md] [--no-test]
```

1. **Collect** the install material (as in step 2 of `install`) and the
   facts the scripts depend on: languages, package managers, services,
   ports, GPU use.
2. **Static review and draft**: an agent with read-only repo tools writes
   `review.md` and a draft install.md. Findings it looks for: unpinned
   versions, downloads without a checksum, `curl | sudo bash`, steps that
   are not idempotent, missing detect or verify checks, OS and GPU
   coverage, secrets in scripts, global installs where a venv would do.
3. **Package-name check** (read-only): every apt, dnf, brew, winget, pip
   and npm name in the draft is looked up (`apt-cache policy`, PyPI and npm
   JSON APIs, `winget show`). Unknown names and versions go back to the
   model. Today's ft-4 runs show invented names (`python3-venv-dev`,
   `postgresql-16-add-role`) that this catches.
4. **Test install in a throwaway machine**: on Windows a new WSL distro
   (`wsl --install Ubuntu-24.04 --name iz-review-<id> --no-launch`), on
   Linux a container (`ubuntu:24.04`, with systemd where services need
   it). The installer agent runs the draft there; failures go back to the
   reviewer with the probe's refine prompt (`probe_refine`), for up to three
   rounds. The throwaway machine is deleted afterwards. macOS has no
   sandbox here; its variants are reviewed statically only.
5. **Output**: `install.md` and `review.md` (findings, what changed, test
   result per round).

Windows-native installs cannot be test-run this way in the first version;
they are reviewed statically and tested by the owner. A Windows Sandbox
(`.wsb`) target is a later option.

## Models and keys

```text
installzero model                    choose (menu); --list, or set <id>
installzero key set <provider>       paste a key (hidden input), stored in the OS credential store
installzero key status | remove <provider> | open
```

The menu: **InstallZero Gemma (local, default)**, Anthropic, OpenAI, Google,
xAI, or **an OpenAI-compatible server** (Ollama, LM Studio, a llama-server)
by URL. The choice is saved in the user config (`%APPDATA%\installzero\config.json`,
`~/.config/installzero/config.json`); keys never are.

**Key storage**: the OS credential store through
[`zalando/go-keyring`](https://github.com/zalando/go-keyring) (pure Go, no
cgo), driven entirely by installzero so the user never opens it:

- Windows: Credential Manager through the Win32 API; no prompts.
- macOS: the login Keychain through `/usr/bin/security`. The item's access
  list then trusts the system tool, not our binary, so an update of the
  unsigned binary does not trigger "installzero wants to use your
  keychain" prompts.
- Linux: Secret Service over D-Bus (GNOME Keyring, KWallet). Headless
  machines and WSL usually have none; installzero then says so and uses the
  environment variable, or asks once per run and keeps the key in memory.
  It never writes a key to a plain file.

`installzero key open` opens Credential Manager or Keychain Access for users
who want to see or delete the entry themselves.

**Key handling**: typed with echo off (`x/term`), checked with a cheap call
(list models) before it is saved, never logged, never in transcripts,
reports or records (`internal/secrets` redacts known key formats as a second
line). Lookup order: the provider's environment variable (for CI), the
credential store, then a prompt. `.env` files are no longer searched outside
the eval harness.

**Hosted-model consent**: the first time a provider is used, installzero
shows what it sends (the repo's install material, command output, machine
summary; all redacted, hidden paths never read) and asks once per provider.
`DO_NOT_TRACK=1` does not block hosted models (the user chose them) but
does block failure sharing, as in the split plan.

## The default model

- **Hosting**: a public Hugging Face repo (for example
  `caldorf/installzero-gemma-4-E4B-it-GGUF`) with the GGUF, its `.iz.json`
  request settings, a model card and the Gemma terms notice. The binary pins
  each file's sha256; a new model ships with a new installzero release.
- **Download on first use**: installzero asks first, showing size (about
  5 GB for Q4_K_M) and free disk; downloads with resume and progress into
  the user cache (`%LOCALAPPDATA%\installzero\models`,
  `~/.cache/installzero/models`); checks the sha256. llama-server comes from
  the pinned llama.cpp release as `internal/llm/runtime.go` already does.
- **Run**: llama-server on a free loopback port for the length of the run,
  backend chosen as today (CUDA, Metal, Vulkan, CPU).
- **Quantization**: Q4_K_M by default. The 2026-11-07 replay showed Q8_0
  only slightly better on the looping requests (45 of 55 replays looped
  against 53), so Q8_0 is an option for GPUs with 10 GB or more, not the
  default.
- `FindAssets` stops requiring a developer's assets folder: the user cache
  is the default, `RL_DOCTOR_ASSETS` stays for development.

**The default model's limits**: ft-4 was trained to write an install.md from
an installed machine (the probe). It has not been trained to review scripts
or to run installs, and it loops on long install.md writes. Hosted models
will do better at review and at plain-README installs at first. Making the
local model a good default for these jobs is its own track (an ft-5 trained
on review and install transcripts, step-by-step install.md writing,
repetition-free training targets), measured on the eval console before it is
promoted. A reviewed install.md keeps this from mattering much for users of
projects that ran `init`: there the model only helps when a step fails.

## Releases

Versioned GitHub releases of installzero for windows-amd64, windows-arm64,
linux-amd64, linux-arm64, darwin-arm64 and darwin-amd64, each with a sha256
file, cosign signature and artifact attestation (split plan, Decision 6).
The generic bootstrap (`https://installzero.dev/install.ps1`) and each
project's bootstrap download from there.

## Order of work

| Step | Work | Size |
|---|---|---|
| 1 | Go installer agent: install.md runner, policy tiers (port of `Agent.ps1` and `Policy.ps1`), in-place mode, plain-README mode, `lock.json`, reboot and sign-out resume; `installzero install` | about 2 weeks |
| 2 | Releases and bootstrap: release workflow with checksums, bootstrap scripts, `.installzero/config.json`, version pin hand-over | 3-4 days |
| 3 | Models and keys: `model` and `key` commands, credential store, consent, key lookup order | 3-4 days |
| 4 | Default model: Hugging Face repo, download with resume and sha256, user cache | 3 days |
| 5 | `review` and `init`: static review, package-name check, throwaway WSL distro and container test runs, refine rounds | 2-3 weeks |
| 6 | rl-roboracer on InstallZero: its install.md (with the named WSL and Docker Desktop steps), bootstrap, README | 1 week |
| 7 | Default-model track: evaluate on review and install tasks, ft-5 | ongoing |

Steps 2-4 can run alongside step 1. Step 1 is phase 5 of the Go port plan.

## Risks

- **Model quality without an install.md.** Plain-README installs depend on
  the model; the local default is weakest exactly there. Mitigation:
  `init` produces a reviewed install.md, hosted models are one command away,
  and the default-model track measures before promising.
- **Unsigned binaries.** Defender and Gatekeeper (split plan, Distribution);
  the bootstrap scripts avoid the download mark.
- **Policy gaps.** An install command the classifier misjudges as project
  tier could change the machine without asking. Mitigation: unknown
  commands are machine tier, the classifier has its own fixtures (as the
  read-only checker does), and every command is in the report.
- **Sandbox fidelity.** A container is not a desktop (no GPU, no systemd by
  default); a passing test run does not prove the user's machine will pass.
  The review report says which variants were tested where.
- **Privacy with hosted models.** Command output can contain anything.
  Mitigation: redaction before sending, hidden paths, consent per provider,
  and the local default.

## Open questions

1. Hugging Face repo name and owner for the public GGUF (personal account
   or an InstallZero organization).
2. Whether `installzero install` without a reviewed install.md should
   require an explicit `--from-readme` the first time, given the model risk.
3. Windows-native test runs for `review`: Windows Sandbox, a Hyper-V VM, or
   owner-only testing.
4. Telemetry for `install` outcomes (the split plan's `installs`
   collection) from the first version, or later.
