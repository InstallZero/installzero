# InstallZero: a harness built for installation work

Status: install mode's runner works without a model (see Progress).
Written 2026-11-07. Part of
`docs/project-install-plan.md` (which says what the user and the repo owner
run); this document says how the agent inside does the work.

## Goal

One harness, three modes, all centred on the install.md
(`agent/INSTALL-MD.md`):

| Mode | Command | Input | Output |
|---|---|---|---|
| **Probe** | `installzero probe` | a machine where the project is installed | an install.md that reproduces it |
| **Install** | `installzero install` | an install.md, or a repo with only a README and scripts | the project installed, a report, `lock.json` |
| **Review** | `installzero review`, `installzero init` | a repo's install material | a tested install.md and `review.md` |

The modes feed each other: probe and review write install.md files, install
runs them, and install failures go back to probe or review as refine input.

## Why a different harness

Today's harness (`internal/agentloop`, `agent/AgentLoop.ps1`) is one long
conversation per task: the model drives every action, the whole install.md
is written in one tool call, and the run ends when the model calls finish.
The 2026-11 measurements show where that breaks for a small local model:

- **Long writes loop.** ft-4 loops while writing a whole install.md in one
  reply; loops start a median 7,000 characters in. Higher precision (Q8_0:
  45 of 55 replays looped against 53 for Q4_K_M) and cutting and continuing
  the reply (no usable document in 39 replays) do not fix it.
- **Long contexts overflow and degrade.** Context overflows needed turn
  dropping; later turns repeat earlier calls.
- **The model does work code could do.** In `Agent.ps1` the model calls
  `run_step` for every detect, install and verify block, so a ten-step
  install.md costs thirty-plus model turns even when nothing fails.

A hosted model hides these costs; the default local model cannot.

## Principles

1. **Code does what is deterministic; the model decides.** Running blocks,
   checking exit codes, collecting machine facts, linting documents and
   looking up package names are code. The model chooses what to
   investigate, how to fix a failure and how to write a step.
2. **Short episodes, not one conversation.** Each step to write, failure to
   fix or finding to explain is its own episode with a fresh, compact
   context: the machine facts, the step, the evidence for it, and a summary
   of what came before. An episode has a small turn budget and ends in one
   structured result.
3. **Documents are written a step at a time.** `write_step`,
   `replace_step` and `remove_step`; the harness assembles the document,
   checks its format and runs the step's checks. A loop costs one step,
   retried on its own.
4. **Install-specific tools beside the shell**: `package_info` (does this
   apt, dnf, brew, winget, pip or npm name and version exist), `check_port`,
   `check_service`, `check_http`, `wait_for`, `edit_file` (a patch, not a
   whole file), `download` (with sha256). Cheaper in tokens, harder for a
   small model to get wrong, and simple to tier. `run_command` and
   `run_change` stay, under the policy tiers (`internal/policy`).
5. **Output fit for installs.** Long commands stream; the model sees the
   head, the tail and error-like lines, with progress bars and repeated
   lines collapsed. Timeouts by kind (package installs, builds, downloads,
   checks). Non-interactive by default (`DEBIAN_FRONTEND=noninteractive`,
   `PIP_NO_INPUT=1`, `CI=1`, stdin closed); a command that waits for input
   is reported as such, not left to time out.
6. **The verify check is the judge.** A step is done when its verify
   passes. The model cannot declare a step or an install done.
7. **Known fixes before the model.** A failure's signature is looked up in
   the catalog first (`catalog`); a match runs its named fix action. The
   model is called only for unmatched failures or failed fixes.
8. **Safety and state are the harness's job.** Policy tiers on every
   command; the plan's machine-wide commands shown before the run and
   approved together; secrets redacted and hidden paths never read;
   `lock.json` checkpointed per step so a reboot or a rerun continues where
   it stopped and `remove` knows what to undo.

## Install mode

1. **Plan**: parse the install.md, select blocks for this machine
   (`internal/installmd`), classify every block (`internal/policy`), show
   the plan and ask once for its machine-wide commands.
2. **Run**, per step, in code: detect (skip when it passes), install,
   verify.
3. **On failure**: catalog lookup and named fix; else a **fix episode**:
   the step, the failed block's summarised output, machine facts and
   read-only tools plus `run_change`. It ends with `retry_step` (the fix is
   applied, run the step again), `replace_block` (a corrected block for
   this machine, recorded in the report as a proposed change to the
   install.md) or `give_up` (with what the user must do). At most three
   episodes per step.
4. **Report**: steps and their outcomes, every command with its tier and
   approval, proposed install.md changes, and what needs the user.

A repo without an install.md first gets a **planning episode** series that
turns the README and scripts into an install.md, a step at a time, as
review mode does without the sandbox; then the same runner. With a reviewed
install.md whose steps pass, install mode calls no model at all.

## Probe mode

1. **Inventory, in code**, handed to the model as evidence: packages and
   their install history (`/var/log/apt/history.log`, `dnf history`,
   winget and brew lists), virtual environments and their `pip freeze`,
   systemd units, Docker images, containers and compose files, listening
   ports and their processes, config files under the project, shell
   history, cron entries. (`internal/target` already collects part of
   this.)
2. **Investigation episodes** with read-only tools, scoped by topic (the
   runtime, the database, the web server, the services), each ending in
   notes, not a document.
3. **Writing episodes**, one per step, from the notes. The harness runs the
   new step's detect and verify at once and checks its package names; a
   failing check goes straight back to the same episode.
4. **Refine**: an install report from a clean replay sends only the failing
   steps back to writing episodes.

## Review mode

1. **Collect** the install material: README install sections, install
   scripts, Dockerfiles and compose files, Makefiles, dependency files.
2. **Lint, in code**: unpinned versions, `curl | sh`, downloads without a
   checksum, missing detect or verify, global installs where a virtual
   environment would do, steps that are not idempotent, secrets in
   scripts, unknown package names and versions, OS and GPU coverage.
3. **Finding episodes**: the model explains each finding and writes the
   corrected step. Steps without findings are converted as they are.
4. **Sandbox test**: install mode runs the proposed install.md in a
   throwaway WSL distro or container; failing steps go back to writing
   episodes, up to three rounds.
5. **Output**: install.md and `review.md` (findings, changes, test results
   per round).

## Code

| Package | What |
|---|---|
| `internal/agentloop` | stays: runs one episode (tools, retries, loop detection, trimming) |
| `internal/harness` | new: runs a mode as a series of episodes, carries the summary between them, budgets turns and time |
| `internal/runner` | new: the step runner (plan, tiers, consent, detect/install/verify, `lock.json`, resume) |
| `internal/output` | new: streaming, summarising and timing out command output |
| `internal/policy` | done: the safety tiers (bash rules to be tightened for Linux) |
| `internal/installmd` | exists: parse, select, placeholders; gains step-level editing and assembly |
| `internal/inventory` | new, from `internal/target`: the probe's evidence |
| `internal/lint` | new: review's deterministic checks and the package-name lookups |
| `prompts/` | one system prompt and tool set per episode type |

## Models

Every episode is short and ends in one structured call, which suits a 4B
local model and makes each episode type its own training set: ft-5 learns
`write_step`, `fix_step` and `explain_finding` episodes instead of whole
transcripts. Hosted models use the same episodes. Per-episode settings
(temperature, reply cap) can differ: a fix episode needs a short reply, a
writing episode a step's worth.

## Evaluation

Each mode gets a benchmark on the eval console, on the WSL snapshots the
probe benchmark already uses:

- **Probe**: today's benchmark (recall against the reference recipe).
- **Install**: replay the reference install.md files on clean snapshots,
  and the same with injected faults (the doctor's scenarios: a missing
  package, a taken port, a stopped service, a wrong version). Metrics:
  installed or not, time, model calls, tokens, consent prompts.
- **Review**: the benchmark projects' real install docs, as they are and
  deliberately degraded (pins removed, a step dropped, a wrong package
  name). Metric: does the proposed install.md install cleanly in the
  sandbox, and how many rounds it took.

## Order of work

1. **Install mode**: `internal/runner` and `internal/output` (no model),
   then fix episodes, then the planning episodes for README-only repos.
   Tighten the bash policy rules for Linux first.
2. **Step-at-a-time writing** in `internal/installmd` and the writing
   episodes; switch the probe to them and compare on the probe benchmark.
3. **Inventory** for the probe.
4. **Review**: lint, package-name lookups, finding episodes, sandbox test.
5. **Install and review benchmarks** on the eval console.
6. **ft-5** on episode data from all three modes.

## Progress

- 2026-11-07: bash policy rules for Linux (`fixtures/policy/bash.json`).
  `internal/runner`, `internal/output` and `installzero install
  [install.md] [--wsl <distro>] [--plan] [-y] [--env NAME=VALUE]`: plan
  with tiers, one approval for machine-wide steps, detect, install and
  verify in code, required variables (`${NAME:?}`) asked for and hidden in
  the output, `lock.json` per step, rerun skips steps whose detect and
  verify pass. No fixer yet.
- First replay: gemini-4-argon's Jenkins install.md on a clean Ubuntu
  24.04 distro. Steps 1 to 5 installed and verified in about three
  minutes. Step 6 failed: the apt package starts Jenkins on port 8080,
  which was taken, so it failed five times and systemd refused the
  restart on port 8095 (start limit). `systemctl reset-failed` fixed it;
  the journal held the cause. This is the case fix episodes are for:
  the runner's summary of the failed block said only "see journalctl", so
  a fix episode needs read tools (journalctl, ss) and not just the
  block's output.
