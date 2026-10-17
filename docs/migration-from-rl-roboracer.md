# Migration from rl-roboracer

InstallZero was built inside [frenb/rl-roboracer](https://github.com/frenb/rl-roboracer)
under `scripts/install/` and `evals/`. This plan moves it here. Four decisions shape it:

- **rl-roboracer fetches a pinned InstallZero at install time.** It stops carrying its own copy.
- **InstallZero starts with a fresh import.** History stays in rl-roboracer; the import commit names the source commit.
- **The eval console gets its own MongoDB.** It no longer shares rl-roboracer's.
- **The code keeps its current folder layout.** `agent/` and `doctor/` stay side by side, so relative paths such as `..\doctor` from the agent and `..\..\doctor` from the eval scripts keep working.

## Status

| Phase | State | Record |
|---|---|---|
| 0. Baseline | Done 2026-10-17 | rl-roboracer branch `installzero-handoff` at `1d5c77e` (local only, not pushed). Mongo backup `%LOCALAPPDATA%\installzero\backups\installzero-20261017-111359.archive.gz`, 9 collections. |
| 1. Import | Done 2026-10-17 | Import commit in this repo, from `frenb/rl-roboracer@1d5c77e`. |
| 2. Path fixes | Done 2026-10-17 | `Test-SecretGuard.ps1 -RealRepo ..\rl-roboracer`: 30 of 30 checks pass, 7 of them new. Hosted-model keys copied to this repo's git-ignored `.env`. |
| 3. Console cutover | Done 2026-10-17 | Console and Mongo from `console/compose.yml` (project `installzero`); all 9 collections match the old database. Worker runs from `agent\eval\worker`. Pre-cutover dump `installzero-cutover-20261017-144534.archive.gz`. Old `iz-evals-test` container stopped, not removed. Drop the old database on or after 2026-10-24. |
| 4. rl-roboracer fetches InstallZero | Release channel ready | `v0.1.0` published by `.github/workflows/release-engine.yml` to `https://storage.googleapis.com/installzero/engine/installzero-engine-v0.1.0.zip`, sha256 `8c356063b4a4a1f3954c1b2748a7fe31f22ba15750bb88ff6ad4de71e1224634`. rl-roboracer changes not started. |
| 5. Follow-ups | Not started | |

## What was found before starting

- **None of the InstallZero work since the 2026-10-08 split plan was committed** in rl-roboracer. The probe, agent, eval harness, eval console, `Remedies.ps1` and `rltest/` were untracked, and the doctor files had uncommitted edits. Phase 0 commits them to a branch first.
- **rl-roboracer's own installer depends on InstallZero code.** `Install.ps1` loads the doctor scripts and the catalog from its own folder, and the `install.ps1` bootstrap downloads the doctor. The doctor can only be removed from rl-roboracer once the installer can fetch it (phase 4).
- **The probe hides InstallZero's own files from itself by path.** `Probe.ps1` and `Tools.ps1` hide anything matching `scripts\install\agent` and `scripts\install\doctor`. After the move those patterns no longer match, so the probe could read `agent\eval\repos.json`, which holds the benchmark's expected answers, and every score would be inflated. This is the most important code change (phase 2).

## What moves where

| rl-roboracer | InstallZero |
|---|---|
| `scripts/install/agent/*` (Probe, Agent, AgentLoop, InstallMd, Policy, LinuxTarget, INSTALL-MD.md, examples) | `agent/` |
| `scripts/install/agent/eval/` (Invoke-ProbeEval, repos.json, bench, WSL recipes, worker) | `agent/eval/` |
| `scripts/install/doctor/` (Doctor, Tools, Llm, Remedies, MODELS.md, kb, eval) | `doctor/` |
| `scripts/install/catalog/*.json`, the 16 generic entries | `catalog/` |
| `evals/` (server, UI, Dockerfile) | `console/`, plus a new `compose.yml` with its own Mongo |
| `docs/install-doctor-plan.md`, `agentic-installer-split-plan.md`, `install-bench-plan.md`, `install-bench-candidates.md` | `docs/` |

**Staying in rl-roboracer:**

- the `install.ps1` and `uninstall.ps1` bootstraps, `Install.ps1`, `Uninstall.ps1`, `Prepare-RlTest.ps1`, `Reset-TestAccount.ps1`, `Start-ClientAtLogon.ps1`, `seed/` and `requirements.json`
- `rltest/` steps 1 to 4 (step 3 will fetch InstallZero)
- the docs `install-script-plan*.md`
- the project-specific catalog entries `demo-data-missing`, `mongo-auth`, `mongo-unreachable` and `mongo-data-permission`

The local models stay outside both repos, in `LATEST\doctor-assets`.

## Phases

### 0. Baseline

- Commit rl-roboracer's InstallZero work to a branch, `installzero-handoff`, and record its hash for the import message.
- Commit or stash pending edits in this repo. (They turned out to be line-ending changes only.)
- `mongodump` the `installzero` database as a backup.

### 1. Import (one commit)

- Copy the files from the `installzero-handoff` commit using the mapping above, with the message "Import from frenb/rl-roboracer@&lt;sha&gt;".
- Add a `.gitignore` covering `.env`, `node_modules` and `doctor/assets`.
- Add `doctor/kb/LICENSE`: CC BY-SA 4.0, as the split plan decided, because one article is adapted from a CC BY-SA source. Update `NOTICE` and the README's layout table to match.
- Leave the 12 rl-roboracer doctor scenarios in `doctor/eval/Scenarios.ps1` for now. Splitting them into a project suite comes later.

Nothing here runs from this repo yet. The worker and console keep running from rl-roboracer until phase 3.

### 2. Path fixes

- **Self-hide patterns.** `Tools.ps1` now hides this repo's `agent`, `doctor`, `console`, `catalog`, `schemas`, `docs` and `.env` by where the running copy lives, in both Windows and `/mnt/<drive>/...` form, so any clone or engine folder name is covered. It also hides them by name (`installzero\agent` and so on). The old `scripts\install\...` patterns stay until phase 4. `Probe.ps1` builds on the same pattern, and its `OwnFilesRegex` reuses it. Before this change, the doctor's pattern did not hide `installzero\agent\eval\repos.json` at all.
- **Secret-guard checks.** `Test-SecretGuard.ps1` now checks that the benchmark answers and this repo's `.env` are refused via `read_file`, `grep`, `list_dir` and `run_command`, with backslash, forward-slash and WSL paths. Its real-project checks take `-RealRepo` (default: the current folder) instead of assuming they sit inside rl-roboracer.
- **Models folder fallback.** The relative fallback to `LATEST\doctor-assets` now climbs two levels from `doctor\` (`Llm.ps1`), four from `agent\eval\worker\` (`Start-EvalWorker.ps1`) and three from `doctor\eval\` (`Measure-LlmRuntime.ps1`).
- **Repo roots.** `Probe.ps1` uses this repo's root in WSL mode. `Doctor.ps1` defaults `-RepoDir` to the current folder, because its old default was rl-roboracer's root; the installer always passes `-RepoDir`. `Select-Bench.ps1` writes to this repo's `docs\`.
- **API keys.** Nothing sets the provider keys in the user or machine environment; they were read from rl-roboracer's `.env` (the probe's WSL mode and the loss analysis via the old repo-root default). `Get-ApiKey` now tries the environment, then the project's `.env`, then this repo's `.env`. The four keys are copied there.
- **Path text** in the console's worker and transcript hints, in `repos.json`'s description and in the docs.

### 3. Console cutover (the only downtime, a few minutes)

- `console/compose.yml` runs the console and its own Mongo. Mongo is internal only, so it can't clash with rl-roboracer's port 27017. The console keeps port 8300.
- Stop the rl-roboracer `evals` container, `mongorestore` a fresh dump into the new Mongo, and start the new console.
- Stop the worker and restart it from `installzero\agent\eval\worker`. It adopts the jobs that are running.
- Check that the counts match: results, transcripts, diagnoses, lesson set L1, and queued jobs.
- `agent\eval\bench\Sync-BenchDb.ps1` runs `docker compose cp/exec mongo` from rl-roboracer's root. Point it at `console\compose.yml` and that Mongo's password variable.

As run: the live console was a standalone `iz-evals-test` container on rl-roboracer's network, not the compose `evals` service (which was never committed and is now removed). No jobs were running or queued. Downtime was about two minutes. The new Mongo is the official `mongo:6.0` image, matching the old server's 6.0.13; its root password is in `console\.env` (git-ignored).

Rollback, until the old database is dropped: `docker compose down` in `console\`, `docker start iz-evals-test`, and restart the worker from rl-roboracer's `scripts\install\agent\eval\worker`. Results recorded after the cutover exist only in the new database.
- Remove the `evals` service from rl-roboracer's `docker-compose.yml`. Drop the old database a week later.

### 4. rl-roboracer fetches InstallZero

This repo stays private. Projects download a published engine archive instead of the source:

- **Release.** Pushing a `v*` tag runs `.github/workflows/release-engine.yml`. It zips `doctor/` (without `eval/`), `catalog/`, `schemas/`, `LICENSE`, `NOTICE` and a `VERSION` file under a top folder `installzero-engine-<tag>/`. It uploads the zip and a `.sha256` to the public bucket `gs://installzero` (Google Cloud project `installzero`, US multi-region).
- **Access.** `allUsers` may read objects. The service account `installzero-serviceaccount@` may only create objects in this bucket, so a published version cannot be overwritten or deleted by CI. GitHub Actions acts as it through Workload Identity Federation (pool `github`, provider `installzero-repo`, limited to `InstallZero/installzero`), with no stored key.
- **Checksums.** The bucket serves `.sha256` files as `application/octet-stream`, so PowerShell's `Invoke-WebRequest` returns bytes; decode them before comparing. Installers should trust the sha256 pinned in the project, not the bucket's `.sha256`.

Then in rl-roboracer:

- A new `scripts/install/installzero.json` holds the engine version, its URL and its sha256.
- The `install.ps1` bootstrap downloads that archive, verifies it, unpacks it to `%LOCALAPPDATA%\installzero\engine\<version>`, and passes the path to `Install.ps1`.
- `Install.ps1` loads the doctor from there. It checks rl-roboracer's own catalog first, then InstallZero's, so the project can override generic entries.
- `Prepare-RlTest.ps1` and `rltest\3-Probe.ps1` stage or fetch the same pinned archive.
- Delete the moved files from rl-roboracer.
- Regression check: `rltest` steps 1 to 4 (the Tier A test), the catalog self-test and the doctor's secret-guard test.

### 5. Follow-ups

- Update the README in both repos.
- Add CI here: catalog self-test, `Test-SecretGuard`, and a syntax check of the console.
- Update the split plan's migration section, which predates the probe and the eval harness.
- The doctor is still written for rl-roboracer: its synopsis, the system-prompt facts about the stack, the GPU requirements lookup in `scripts\install\requirements.json`, and the 12 eval scenarios. Move those behind the project manifest the split plan describes.

## Risks

- **This repo is private.** Resolved: projects download the published engine archive from Cloud Storage, not a GitHub source archive, so no token is needed.
- **The probe could see the answers.** If phase 2's self-hide fix is missed, eval scores silently inflate. The new secret-guard check is there to catch it.
- **Phases 1 to 3 don't affect rl-roboracer users.** Its installer keeps using its own copies until phase 4.
