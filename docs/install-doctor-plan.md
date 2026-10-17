# InstallZero doctor: a local open-weights agent for the long tail of install failures

Status: plan, not started. Written 2026-10-07.

## Goal

`Install.ps1` already recognises 20 known failures (the failure catalog) and
fixes most of them. Everything it does not recognise ends in "Install stopped"
and a human (so far, a Cursor agent) reading logs. The doctor replaces that
human for the long tail: a local LLM with a small tool harness that reads the
evidence, finds the cause, and applies or proposes a fix, with no network
access and nothing to install beyond what ships in the bundle.

Every failure we hit while testing in the `rltest` account is the kind of thing
it has to handle, and none was in the catalog when it first happened:

| Failure | What finding it took |
|---|---|
| Docker Desktop already running for another Windows account | process list by session |
| Mongo folder not writable by uid 1001 | container log + folder owner |
| Mongo refusing connections right after first start | timing; container log |
| Unity client never handshaking with a restarted ros-server | process start time vs container start time |
| `saved_models/.../SacAgent` missing on first save | trainer traceback + code read |
| fly-brain "Domain name not found" | compose service list vs what was started |
| fly-brain stubs missing on a fresh clone | `.gitignore` + server import |
| Port 50061 refused | `netsh ... excludedportrange` |
| Corrupted pip wheel (`Bad CRC-32`) | build log |
| Unity client gone after sign-in | process list, startup entries |
| Images missing after a Docker reinstall ("pull access denied") | `docker images` vs compose file |

These become the doctor's evaluation set (see "Evaluation").

## Constraints

1. **Works when the machine is broken.** The doctor runs exactly when Docker,
   WSL or the GPU driver is not working, so it cannot live in Docker or depend
   on CUDA. It runs natively on Windows and falls back from CUDA to Vulkan to
   CPU.
2. **Zero external dependencies after unpacking.** No Python, Node, Ollama
   service, cloud API or model download at run time. PowerShell 5.1 (present
   on every supported Windows) plus files in the bundle.
3. **Never makes things worse.** Read-only investigation is free; anything that
   changes the machine goes through a policy with confirmation, backups and a
   transcript.
4. **Deterministic first.** The catalog stays the first line: instant, tested,
   explainable. The doctor runs only when the catalog has no match or its fix
   did not work, and its successes are turned into new catalog entries.

## Architecture

```
Install.ps1 phase fails
  -> Find-Failure (catalog)            match -> catalogued fix (as today)
  -> no match / fix failed
  -> Invoke-Doctor -Phase -Evidence -Report
       doctor\Llm.ps1     start llama-server.exe on 127.0.0.1, pick backend
       doctor\Doctor.ps1  agent loop: model <-> tools, step/time budget
       doctor\Tools.ps1   tool implementations + policy
       doctor\kb\         offline knowledge pack, searched by the model
  <- diagnosis, actions taken, proposed catalog entry -> install-report.json
```

Also runnable on its own after install, for problems like "the job fails with
X": `powershell -File scripts\install\doctor\Doctor.ps1 -Problem "..."`.

### Runtime: llama.cpp `llama-server.exe`

- Official Windows x64 release zips: CUDA 12.4 (~250 MB + ~370 MB CUDA DLLs),
  Vulkan (~30 MB), CPU (~16 MB). Ship all three; pick at start:
  CUDA if `nvidia-smi` works and the driver supports CUDA 12.4, else Vulkan,
  else CPU. Pin one release build number and verify sha256 like the other
  assets.
- OpenAI-compatible `/v1/chat/completions` with tool calling (`--jinja`), and
  JSON-schema-constrained output as a fallback for models whose tool calls are
  unreliable.
- Bind to 127.0.0.1 on a port checked against
  `netsh interface ipv4 show excludedportrange` (the 50061 lesson).
- Started on demand and stopped when the doctor finishes, so it never holds
  GPU memory while training runs.

### Harness: PowerShell, no dependencies

The agent loop is `Invoke-RestMethod` against llama-server, which is all the
harness needs. Writing it in PowerShell keeps it in the same language and
process model as `Install.ps1`, and it can call the installer's own helpers
(`Invoke-Compose`, `Get-ServiceLogs`, `Find-Failure`, the report).

Tools, roughly in order of how often the failures above needed them:

| Tool | Policy |
|---|---|
| `run_command` (PowerShell, captured output, timeout) | read-only commands auto; others by tier |
| `read_file` (line ranges), `list_dir`, `grep` | auto, any path |
| `docker` / `compose` (`ps`, `logs`, `inspect`, `images`) | read-only auto |
| `system_facts` (preflight data: Windows build, RAM, GPU, driver, WSL, Docker, ports, sessions) | auto |
| `search_kb` (offline knowledge pack, BM25) | auto |
| `catalog` (list entries, test a pattern against the evidence) | auto |
| `edit_file` (exact string replace, backup first), `write_file` | install folder: confirm, or auto with `-AutoFix` |
| `restart_service` (compose service, Docker Desktop, Unity client) | confirm, or auto with `-AutoFix` |
| `ask_user` | always available |
| `finish` (diagnosis, evidence, actions, proposed catalog entry) | ends the run |

Policy tiers for `run_command`: classify by verb and target. Read-only (`Get-*`,
`docker ps/logs/inspect`, `wsl -l`, `netsh ... show`) runs without asking.
Changes inside the install folder or the compose project ask unless
`-AutoFix`. System-wide changes (`winget`, `wsl --update`, services, registry,
reboot, anything outside the install folder) always ask, showing the exact
command. A deny list blocks the irreversible (formatting, deleting outside the
install folder, `wsl --unregister`, `docker volume rm` of data volumes,
credential stores).

Context management: tool output is cut to head and tail with a byte cap, long
logs are searched rather than pasted, and the run keeps a transcript in the
state folder. Budgets: about 30 steps and 15 minutes per run.

### Knowledge pack

Offline text the model can search, because a small model's built-in knowledge
of Docker Desktop and WSL versions goes stale:

- This repo's docs, the failure catalog with its samples, `docker-compose.yml`,
  the installer and wrapper scripts.
- Curated troubleshooting pages for Docker Desktop on Windows, WSL 2, the
  NVIDIA container toolkit and driver/CUDA compatibility, saved as text at
  bundle build time.
- Past doctor runs that succeeded (diagnosis, evidence, fix), so a second
  occurrence is a lookup, not a fresh investigation.

### Closing the loop

Every successful run writes a proposed catalog entry (pattern, diagnosis,
advice, fix, sample line) into `install-report.json`. A maintainer reviews and
promotes it, with its sample added to `-SelfTest`. The doctor should get called
less often over time; that is the measure of it working.

## Choosing the model

### What the job needs

- **Reliable tool calls** over 10-30 steps. This matters more than raw coding
  benchmark scores: a model that writes tool calls as prose breaks the loop.
- **Reading long, noisy logs** (build logs, tracebacks, compose output):
  32K context minimum.
- **Windows, PowerShell, Docker and WSL knowledge.**
- **Fits the hardware the installer supports**, with a CPU fallback for when
  the GPU stack is the problem. The bundle has to carry it, so file size
  matters too.
- **A licence that allows redistribution** in the bundle: Apache 2.0 or MIT
  preferred.

### Candidates (from August 2026 public evaluations; verify before pinning)

| Model | Type | Size (4-bit) | Licence | Notes |
|---|---|---|---|---|
| gpt-oss-20b (OpenAI) | MoE, 21B total / 3.6B active | ~12.8 GB (native MXFP4) | Apache 2.0 | Passed 8/8 tasks incl. two-turn tool calling on a 16 GB card at 178-190 tok/s; 128K context; adjustable reasoning effort. Emits tool calls in its own (harmony) format: reliable with parsers that handle it, flaky in some agent frameworks. Few active parameters, so it stays usable when partly offloaded to CPU. |
| Qwen3.5-9B | dense 9B | ~5.7 GB | Apache 2.0 (verify) | Small, long native context (262K), tool calling. The fallback for 8 GB cards and CPU-only. |
| Qwen3-Coder 30B-A3B | MoE, 30B / 3B active | ~19 GB | Apache 2.0 | Best tool-call reliability across agent frameworks in one 30-run study; does not fit 16 GB, runs with CPU offload. |
| Qwen 3.8 27B | dense 27B | ~17 GB (3-bit ~14 GB) | check | Worked reliably in every agent framework tested; needs 24 GB, or a 3-bit quant with about 8K usable context on 16 GB. |
| Gemma 4 26B-A4B | MoE | ~15.8 GB | Apache 2.0 | Strong all-rounder; too big for 16 GB with room for context. |
| Devstral Small 2 24B | dense 24B | ~15 GB | Apache 2.0 | Best published SWE-bench score in this size, but Mistral deprecated it in Feb 2026, and it leaves almost no room for context on 16 GB. Not recommended. |

Sources: threatfrontier.com and localaimaster.com 16 GB roundups,
techfuelhq.com's August 2026 16 GB test, dev.to "six coding agents on seven
local models, 30 times each". These are third-party numbers, which is why the
decision rests on our own bake-off below.

### Recommendation

- **Primary: gpt-oss-20b.** It fits a 16 GB card fully resident, is fast, has
  native tool calling and 128K context, and is Apache 2.0. Because only 3.6B
  parameters are active per token, it degrades gracefully on 8-12 GB cards and
  CPU, which matters for a tool that runs on broken machines. Its one known
  weakness, its own tool-call format, is under our control: llama-server's
  `--jinja` parsing plus a harness-side parser (as the frameworks that ran it
  reliably do).
- **Fallback: Qwen3.5-9B**, picked automatically when free VRAM is under about
  10 GB and there is less than 24 GB of system RAM. About 6 GB to ship.
- **Upgrade path: Qwen3-Coder 30B-A3B** on 24 GB cards, if the bake-off shows a
  clear win.

Confirm with the bake-off before shipping: run the evaluation set on both
primary candidates and pin whichever resolves more cases safely.

## Evaluation

A fault-injection suite that recreates each failure in the table above on a
test install (the `rltest` account), then runs the doctor. Examples: start
Docker Desktop in another account; `chown` the Mongo folder to root; delete
`docker/fly_brain/gen`; remove the `fly-brain` service; reserve the gRPC port;
corrupt a wheel in the build cache; kill the Unity wrapper; `docker rmi` the
project images.

Scored per run:

- correct root cause (graded against the known answer)
- resolved (the phase passes on re-run)
- safe (no denied or unconfirmed changes attempted)
- steps and wall time

Each model is run 5 times per scenario to measure consistency. The suite
re-runs on every model or harness change, like `-SelfTest` does for the
catalog.

## Bundle and distribution

| Item | Size |
|---|---|
| llama.cpp CUDA 12.4 build + CUDA DLLs | ~620 MB |
| llama.cpp Vulkan + CPU builds | ~50 MB |
| gpt-oss-20b GGUF | ~12.8 GB |
| Qwen3.5-9B GGUF (fallback) | ~5.7 GB |
| Knowledge pack | < 50 MB |

About 19 GB with both models. GitHub Releases caps each asset at 2 GB, so the
models ship as split parts that `Get-VerifiedAsset` reassembles and verifies,
or from other hosting, or on a drive. After unpacking, nothing is fetched.

**Open question: what "fully offline" covers.** The doctor and model need no
network. The install itself still does: `winget` for WSL, Git and Docker,
`apt`/`pip` during image builds, base images from Docker Hub. A truly offline
install would also ship the built images as `docker save` archives (roughly
15-20 GB more) and the winget installers. Decide whether that is in scope.

## Phases

1. **Evaluation set and runtime spike (1 week).** Fault-injection scripts for
   the 11 cases. llama-server with both candidate models on this laptop;
   measure load time, tokens/s on CUDA, Vulkan and CPU, and tool-call parsing.
2. **Harness MVP (1-2 weeks).** `Llm.ps1`, `Doctor.ps1`, read-only tools,
   `finish`. Diagnosis only, no changes. Run the suite; pick the model.
   *Done 2026-10-07 (clean run):* gpt-oss-20b 10/12, Gemma 4 E4B 7/12;
   hosted references Opus 5.5 and GPT 5.6 sol 12/12, Grok 4.7 and Gemini
   3.1 Pro 11/12 (results in `scripts/install/doctor/MODELS.md`). Gemma 4
   E4B replaces Qwen3.5-9B as the fallback. The stale-Unity-client case
   fails on both local models and is the first target for the knowledge pack.
3. **Fixing (1-2 weeks).** Write tools, policy tiers, backups, confirmation
   prompts, `-AutoFix`. Wire into `Stop-WithDiagnosis` and add the standalone
   entry point.
4. **Knowledge pack and learning loop (1 week).** BM25 search, curated
   pages, past runs, proposed catalog entries in the report.
5. **Bundle (1 week).** Split assets, backend selection, model selection by
   hardware, `Uninstall` cleanup, README.

## Decisions (2026-10-07)

1. **Hardware floor: any machine, including CPU-only.** Both models ship; the
   doctor picks gpt-oss-20b when it fits (GPU, or enough RAM to run it partly
   on CPU at a usable speed) and Gemma 4 E4B otherwise (was Qwen3.5-9B until
   the phase 2 suite). The CPU llama.cpp build is always in the bundle.
2. **Offline scope: the doctor only.** The doctor and its models need no
   network; the install itself may still download (winget, image builds).
3. **Autonomy: ask before every change.** Read-only investigation runs freely;
   every change is shown and confirmed. `-AutoFix` stays opt-in.
4. Bundle size follows from 1: about 19 GB.
