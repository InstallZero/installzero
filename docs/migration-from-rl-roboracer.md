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
| 2. Path fixes | Not started | |
| 3. Console cutover | Not started | |
| 4. rl-roboracer fetches InstallZero | Not started | |
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

- **Self-hide patterns** in `Probe.ps1` (`HiddenPathRegex` and `OwnFilesRegex`) and `Tools.ps1`: add this repo's `agent`, `doctor`, `console` and `catalog` folders, keeping the old patterns during the transition. Add a check to `Test-SecretGuard.ps1` that reading `agent\eval\repos.json` is refused.
- **Models folder fallback** in `Llm.ps1` and `Start-EvalWorker.ps1`: the folder depth changes, so the relative fallback to `LATEST\doctor-assets` goes from four levels up to two (`Llm.ps1`) and from six to four (`Start-EvalWorker.ps1`).
- **`Probe.ps1` repo root** for WSL runs: `..\..\..` becomes `..`.
- **Path text** shown in the console UI, such as the `Push-Transcripts.ps1` hint, and in the docs.
- **API keys**: confirm where `Llm.ps1` and the worker read hosted-model keys from, and give this repo its own git-ignored `.env`.

### 3. Console cutover (the only downtime, a few minutes)

- `console/compose.yml` runs the console and its own Mongo. Mongo is internal only, so it can't clash with rl-roboracer's port 27017. The console keeps port 8300.
- Stop the rl-roboracer `evals` container, `mongorestore` a fresh dump into the new Mongo, and start the new console.
- Stop the worker and restart it from `installzero\agent\eval\worker`. It adopts the jobs that are running.
- Check that the counts match: results, transcripts, diagnoses, lesson set L1, and queued jobs.
- Remove the `evals` service from rl-roboracer's `docker-compose.yml`. Drop the old database a week later.

### 4. rl-roboracer fetches InstallZero

- A new `scripts/install/installzero.json` in rl-roboracer holds the repo, a pinned commit and its sha256.
- The `install.ps1` bootstrap downloads that archive, verifies it, unpacks it to `%LOCALAPPDATA%\installzero\engine\<sha>`, and passes the path to `Install.ps1`.
- `Install.ps1` loads the doctor from there. It checks rl-roboracer's own catalog first, then InstallZero's, so the project can override generic entries.
- `Prepare-RlTest.ps1` and `rltest\3-Probe.ps1` stage or fetch the same pinned archive.
- Delete the moved files from rl-roboracer.
- Regression check: `rltest` steps 1 to 4 (the Tier A test), the catalog self-test and the doctor's secret-guard test.

### 5. Follow-ups

- Update the README in both repos.
- Add CI here: catalog self-test, `Test-SecretGuard`, and a syntax check of the console.
- Update the split plan's migration section, which predates the probe and the eval harness.

## Risks

- **This repo may be private.** If so, an anonymous download of the archive fails and the rl-roboracer installer breaks for users. Make it public before phase 4; otherwise the bootstrap needs a token, which the split plan rules out.
- **The probe could see the answers.** If phase 2's self-hide fix is missed, eval scores silently inflate. The new secret-guard check is there to catch it.
- **Phases 1 to 3 don't affect rl-roboracer users.** Its installer keeps using its own copies until phase 4.
