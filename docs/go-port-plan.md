# Porting the agent, probe and doctor to Go (draft for review)

## Goal

The probe, the installer agent and the doctor run today as about 5,600 lines
of Windows PowerShell 5.1. They have to run on Linux and macOS machines too,
with nothing to install first. Decision 4 in
[agentic-installer-split-plan.md](agentic-installer-split-plan.md) already
chose Go for the engine for this reason; this plan moves the model-driven
parts (agent loop, probe, doctor, their tools and the LLM runtime) into the
same Go module as the existing `installzero` CLI.

Done means: `installzero probe`, `installzero agent` (replay an install.md)
and `installzero doctor` run natively on windows-amd64, linux-amd64,
linux-arm64 and darwin-arm64, and on the `wsl-pilot-11` benchmark the Go
probe matches the PowerShell probe's delivery rate and recall within run to
run noise.

## What exists today

| Area | PowerShell files | Lines | Notes |
|---|---|---|---|
| LLM runtime and client | `doctor/Llm.ps1` | 389 | llama-server download and start, GPU detection, hosted providers, `hf:` servers, streaming with loop detection, per-model settings |
| Agent loop | `agent/AgentLoop.ps1` | 388 | turns, retries, context trimming, duplicate and repeat blocking, escalation, finish guard, transcript |
| Probe | `agent/Probe.ps1` | 478 | prompts, tools, hidden paths, draft checks, best-draft keeping |
| Linux target | `agent/LinuxTarget.ps1` | 228 | runs bash in a WSL distro, read-only checker, secret masking, inventory |
| Install.md | `agent/InstallMd.ps1` | 119 | parse, validate, machine profile, conditions, placeholders |
| Installer agent | `agent/Agent.ps1`, `agent/Policy.ps1` | 295 | replays an install.md with command tiers and consent |
| Doctor | `doctor/Doctor.ps1`, `Tools.ps1`, `Remedies.ps1` | 1,140 | Windows diagnosis tools, secret guard, power and GPU remedies |
| Eval and training tooling | `agent/eval/**` | ~1,900 (plus Python and shell) | worker, WSL snapshots, scoring, SFT export; runs only on the Windows eval host |

The Go module today holds the catalog loader, the CLI verbs (`add`, `check`,
`find`, `doctor --prompt`, ...) and state, about 1,100 lines with tests. It
does not call the PowerShell code.

## What moves and what stays

Moves to Go: everything in the first seven rows above.

Stays as it is for now:

- **Eval worker and WSL tooling** (`agent/eval/worker`, `agent/eval/wsl`).
  They only run on the Windows eval host, where they manage WSL snapshots.
  The worker gets an engine switch and calls the Go binary instead of
  `Probe.ps1`; nothing else changes.
- **Training and serving** (`train.py`, `hf_job.py`, `hf_serve.py`): Python,
  as now. New analysis scripts are Python too.
- **Console** (Node): unchanged. It reads the same transcripts and results.

Contracts both engines keep, so the console, SFT export and scoring work
unchanged across the switch:

- the transcript JSONL events (`start`, `tool`, `server_error`,
  `loop_stopped`, `result`, assistant turns with `usage`)
- the probe result JSON and its `stats` fields
- the install.md format (`INSTALL-MD.md`)
- tool names and argument schemas (the SFT models were trained on them)
- the `<model>.gguf.iz.json` per-model settings file

## Package layout

```text
cmd/installzero            existing entry point; gains probe, agent, doctor verbs
internal/llm               providers (llama-server, hf, OpenAI, Anthropic, xAI, Google),
                           streaming, loop detection, model settings, llama-server lifecycle
internal/agentloop         the loop: retries, trimming, dedupe, escalation, finish guard, transcript
internal/target            interface Target { Run, ReadFile, ListDir, Inventory, Profile }
  target/local_windows     PowerShell commands, CIM/registry inventory   (build tag windows)
  target/local_unix        bash commands, /proc and systemd inventory    (linux, darwin)
  target/wsl               bash inside a WSL distro, driven from Windows (windows)
internal/readonly          bash and PowerShell read-only checkers, quote-aware splitter
internal/secrets           secret detection and masking (shared with failure-record redaction)
internal/installmd         parse, validate, conditions, placeholders, replay
internal/probe             prompts, probe tools, hidden paths, draft checks
internal/installer         install.md replay with policy tiers and consent
internal/doctor            diagnosis tools and remedies (Windows remedies behind a build tag)
prompts/                   system prompts as text files, embedded with go:embed
```

The target interface is what makes it cross-platform: the probe asks a
target to run a command or list an inventory area, and the target decides
how. A Windows target still runs PowerShell commands, because that is the
shell on the machine being examined; only the harness moves to Go. Native
Linux and macOS targets are new; WSL stays for the eval host. An SSH target
can come later behind the same interface.

## Phases

Each phase ends with the checks named in it passing. The PowerShell engine
keeps working throughout.

### 0. Freeze the contracts (about 2 days)

- Move the system prompts and tool descriptions out of `Probe.ps1`,
  `Agent.ps1` and `Doctor.ps1` into `prompts/*.md` and `prompts/tools.json`.
  The PowerShell scripts read them from there, so both engines send the
  model identical text.
- Write the transcript and result formats down as JSON schemas in
  `schemas/`, with a validator run over existing transcripts.
- Collect test fixtures from what we already have: the read-only checker
  cases, the loop detector cases (the looping and good writes from the ft-3
  campaigns), the secret-guard corpus (`doctor/eval/Test-SecretGuard.ps1`),
  and valid and invalid install.md files.

Done. The PowerShell scripts take `-PromptOut` to dump what they send the
model. `schemas/validate.py` passes on every transcript and probe result on
the eval host. `fixtures/Update-Fixtures.ps1` fills in the expected outputs
from the PowerShell code, and with `-Check` it fails when they no longer
match.

### 1. Leaf packages with fixture tests (about 1 week)

`readonly`, `secrets`, `installmd`, and the loop detectors in `llm`. These
are pure functions, so each is ported against the phase 0 fixtures, and the
PowerShell versions run the same fixtures so the two can be compared case by
case.

Done; every fixture case passes in Go. Patterns stay in .NET syntax and run on
`regexp2` (`internal/psrx`), so they behave as in PowerShell. The PowerShell
read-only check is not reimplemented: Go runs `doctor/ReadOnly.ps1` in
Windows PowerShell, because the check walks PowerShell's own syntax tree.

### 2. LLM client and agent loop (about 1 week)

- `llm`: OpenAI-compatible chat with streaming and tool-call assembly, the
  Responses API for OpenAI, per-model settings, `hf:` servers, and
  llama-server download, GPU detection and start (CUDA, Vulkan, CPU on
  Windows and Linux; Metal on macOS).
- `agentloop`: a line-for-line port first, including the character budget
  heuristics, retries and escalation, so behaviour matches before anything
  is improved.
- Test with a replay LLM: a fake server that answers from a recorded
  transcript. Running a recorded run through the Go loop has to produce the
  same tool calls and the same tool outputs as the original.

In progress. Done: the chat client (`internal/llm`: plain and streamed chat
with loop stopping, the Responses API, hosted providers and their keys) and
the loop (`internal/agentloop`). The replay runs inside the test rather than
as a server: the recorded replies and errors stand in for the model, the
recorded outputs for the tools, and every event the Go loop writes must
equal the recorded one, through to the run's counters and stop reason.
`fixtures/transcripts/New-LoopScenarios.ps1` records scripted runs of the
PowerShell loop for the paths real runs rarely take (server errors,
unparseable replies, calls written as text, idle replies, duplicate and
excess calls, the budget running out, looping replies, a hosted model); the
Go loop replays all of them identically, and every probe run on the eval host
since the loop took its current form (31, `IZ_REPLAY_DIR`). Trimming, which
only changes what is sent, has its own fixture (`loops/compress.json`).
Left: per-model settings (`.iz.json`), `hf:` servers, and the llama-server
lifecycle.

### 3. Probe on WSL, then the A/B check (about 1-2 weeks)

- `target/wsl` and `probe`, with the same command-line options as
  `Probe.ps1` (`--target`, `--wsl`, `--model`, `--remote`, `--llm-url`,
  `--context`, `--max-turns`, `--timeout`, `--out`, `--hide`, `--guidance`).
- The eval worker gets an `engine` field per job (`ps` or `go`). Run
  `wsl-pilot-11` with both engines on the same model and settings. Pass
  means delivery rate and macro recall within noise of each other (3 runs
  per repo; differences under about 0.05 recall).

### 4. Native targets (about 2 weeks)

- `target/local_unix`: bash and inventory on Linux (dpkg/rpm, systemd,
  docker, ports via `ss`, `/proc`) and macOS (brew, launchd, `lsof`).
- `target/local_windows`: today's Windows probe tools.
- Linux check: run the probe natively inside the same Ubuntu images the WSL
  benchmark uses (a container or VM built from the snapshot recipes) and
  compare with the WSL results.

### 5. Installer agent and doctor (about 2 weeks)

`installer` (install.md replay with policy tiers and consent) and `doctor`
(tools, secret guard, remedies; Windows power and GPU remedies behind a
build tag, Linux equivalents where they exist). Checks: the doctor
fault-injection scenarios (`doctor/eval`) and an install.md replay on a
clean WSL snapshot and a clean Linux VM.

### 6. Switch and retire (about 2 days)

Releases ship the Go binary for each OS. The worker defaults to the Go
engine, the PowerShell agent, probe and doctor are deleted, and the eval
host keeps only its PowerShell WSL tooling.

Rough total: 7-9 weeks of focused work. Phases 1 and 2 can overlap, and so
can phases 4 and 5.

## Risks

- **Behaviour drift.** The loop has many small heuristics (trimming,
  repeat blocking, nudges) that the ft-3 numbers depend on. Mitigation: a
  line-for-line port first, the replay test in phase 2, the A/B campaign in
  phase 3, and improvements only after parity.
- **Harness work during the port.** Changes to the PowerShell loop while the
  port is under way have to land in both. Mitigation: shared prompts and
  fixtures from phase 0, and porting the loop last among the core pieces.
- **Windows-only remedies.** Power plans and GPU preference have no direct
  Linux equivalent; the doctor reports them as unavailable there.
- **llama-server on more platforms.** Download and GPU detection have to
  cover Linux CUDA, Vulkan and macOS Metal builds; test on real hardware for
  each before release.
- **Unsigned binaries.** Defender may flag a new Go binary (see
  Distribution in the split plan); the bootstrap one-liners avoid most of
  this until signing is set up.

## Questions for review

1. Is an SSH target (probe a remote Linux box from a laptop) needed in the
   first version, or is "run the binary on the box" enough?
2. Which Linux distributions and macOS versions must the first release
   support?
3. Should the Go engine keep the per-turn model settings exactly as now
   (temperature schedule 0.2, 0.6, 0.9, 0.9; 8,192-token replies) or is
   phase 3 the time to revisit them? Decided: keep them as they are; the
   A/B check compares engines on the same settings.
