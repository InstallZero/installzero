# InstallZero doctor: open-weights model comparison

Which local model the install doctor runs, and why. Full design:
[docs/install-doctor-plan.md](../../../docs/install-doctor-plan.md).

## Decision

| Role | Model | File | Licence |
|---|---|---|---|
| Primary (13.5 GB free VRAM, or 24 GB RAM on CPU) | gpt-oss-20b (MXFP4) | 11.5 GB | Apache 2.0 |
| Fallback (smaller GPUs, non-NVIDIA GPUs, small RAM) | Gemma 4 E4B (Q4_K_M) | 4.6 GB | Apache 2.0 |

Runtime: llama.cpp `llama-server.exe` build b9771 (CUDA 12.4, Vulkan, CPU).
`Llm.ps1` picks the backend (CUDA if the NVIDIA driver is 551.61+, else
Vulkan, else CPU) and the first model in that order that fits.

## Fault-injection suite (clean run, 2026-10-07, diagnosis only)

Real faults injected into the running dev stack, the doctor run unattended
(`eval/Invoke-DoctorEval.ps1 -Action Run`), same tools, prompt and 25-call
budget for every model, two runs per fault, every answer graded by hand (the
rubric agrees). Local models on CUDA. Hosted models are an upper bound only:
they never ship, and a run sends this PC's logs and paths to the provider.

| Fault | gpt-oss-20b | Gemma 4 E4B | Opus 5.5 | GPT 5.6 sol | Grok 4.7 | Gemini 3.1 Pro |
|---|---|---|---|---|---|---|
| fly-brain stopped | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| fly-brain gRPC stubs missing | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| Unity client gone | 2/2 | 1/2 | 2/2 | 2/2 | 2/2 | 1/2 |
| Unity client stale after ros-server restart | 0/2 | 0/2 | 2/2 | 2/2 | 1/2 | 2/2 |
| sim_controller image missing | 2/2 | 0/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| dashboard package.json missing | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| **Total** | **10/12** | **7/12** | **12/12** | **12/12** | **11/12** | **11/12** |
| Tool calls per run | 12 | 7 | 12 | 17 | 25 | 21 |
| Time per run | 133 s | 73 s | 51 s | 47 s | 97 s | 201 s |
| Input tokens, whole suite | 1.3 M | 0.5 M | 1.2 M | 0.6 M | 1.4 M | 3.3 M |

- The one fault separating local from hosted is the stale Unity client. It is
  solvable from the evidence (Opus and GPT 5.6 sol compared the client's
  start time with the ros-server restart every time); the local models
  instead invent causes (excluded ports, a plugin missing from the build,
  mismatched library versions). The knowledge pack (phase 4) should state
  the rule outright.
- Gemma 4 E4B also blames the compose file when the image is missing, and
  once blamed Windows excluded ports for a missing client.
- Misses among hosted models: Grok once blamed a Docker port forward;
  Gemini once used its whole budget without reporting.
- Harness fixes mattered more than model size: telling the model when a
  service has no container (13 tool calls down to 4), marking container
  restarts in logs, giving the exact compose command for fixes.
- Earlier runs on this page's history were contaminated: the doctor could
  read its own eval scripts, and planted faults left `.doctor-eval` file
  names (Grok read the scripts in 11 of 13 runs). Its own files are now
  hidden from its tools and faults move files out of the repo; the clean
  run shows no access in any of its 72 transcripts.

## Measured on this project (RTX 4090 Laptop 16 GB, 32 GB RAM, CUDA, 32K context)

| | gpt-oss-20b | Qwen3.5-9B | Granite 4.1 8B | Gemma 4 E4B |
|---|---|---|---|---|
| GPU memory | 12.1 GB | 6.5 GB | 10.4 GB | 3.9 GB |
| Load time | 13-14 s | 7-18 s | 6 s | 6 s |
| Generation | 68-94 tok/s | 29-34 tok/s | 30 tok/s | 40 tok/s |
| Prompt reading | 305-320 tok/s | 100-150 tok/s | 199 tok/s | 247 tok/s |
| Valid tool calls | 3/3 | 3/3 | 4/4 | 5/5 |
| Needed a "call finish" reminder | 1 of 2 runs | yes | no | no |
| Probe time | 6-17 s | 32 s | 7 s | 46 s |
| Diagnosis | "no such service defined" (wrong fix) | "not defined or not running" (half) | "not defined in compose" (wrong fix) | "not among the running services" (**correct**) |

The probe gives the model three tools (`compose_ps`, `service_logs`,
`finish`) and the error `DNS resolution failed for fly-brain:50061`. The logs
tool says fly-brain is defined in `docker-compose.yml` but never created, so
the right diagnosis is "not running" (fix: start it), not "not defined" (fix:
add a service). One probe, temperature 0, one or two runs per model: a smoke
test, not a ranking. The lower gpt-oss speed (68 tok/s) was measured with two
Unity clients rendering on the same GPU. Granite's memory is high for its size
at 32K context.

### Running hosted models

GPT 5.6 sol only accepts tools with reasoning on OpenAI's Responses API;
`Llm.ps1` converts to it. Opus 5.5 rejects `temperature`, so hosted models
get no sampling parameters.

```powershell
# keys: environment, or ANTHROPIC_API_KEY / OPENAI_API_KEY / XAI_API_KEY /
# GOOGLE_GEMINI_API_KEY in the repo's .env
.\eval\Invoke-DoctorEval.ps1 -Action RemoteModels -RepoDir <repo>   # exact model ids
.\eval\Invoke-DoctorEval.ps1 -Action Run -RepoDir <repo> -Clients 2 -Runs 2 `
    -Models openai:gpt-5.6-sol,xai:grok-4.7,google:gemini-3.1-pro-preview,anthropic:claude-opus-5-5
```

Still to measure: the CPU and Vulkan backends, Qwen3.5-9B and Granite 4.1 on
the suite, and the scenarios that need admin or a second account.

## Candidates considered

Sizes are 4-bit files. Scores are third-party, from August 2026 roundups on
16 GB cards; ours above take precedence.

| Model | Type | Size | Licence | Verdict |
|---|---|---|---|---|
| gpt-oss-20b | MoE 21B, 3.6B active | 11.5 GB | Apache 2.0 | **Primary.** Fits 16 GB fully; fast; native tool calls; 128K context. Few active parameters, so usable partly on CPU. |
| Qwen3.5-9B | dense 9B | 5.4 GB | Apache 2.0 | Small, 262K context, tool calling; slower and needed a nudge to finish. Not yet run on the suite. |
| Granite 4.1 8B (IBM) | dense 8B | 5.0 GB | Apache 2.0 | Fast, clean tool calls in the probe, but 10.4 GB GPU memory at 32K context and a wrong diagnosis. Not yet run on the suite. |
| Gemma 4 E4B | dense, 4B effective | 4.6 GB | Apache 2.0 | **Fallback.** Smallest footprint, correct on simple faults; see suite above. |
| LFM2.5-2.6B (Liquid) | dense 2.6B | 1.6 GB | LFM Open License | Built for on-device agents; competitive with 9B models on tool use. Candidate for a CPU-only tier; licence needs review before bundling. |
| Gemma 4 26B-A4B | MoE 26B, 3.8B active | 15.8 GB | Apache 2.0 | Strong (#6 open model on Arena); too big for 16 GB with room for context. 24 GB tier. |
| Qwen3.6-35B-A3B | MoE 35B, 3B active | 20.6 GB | Apache 2.0 | Tool-tuned MoE; needs 24 GB+. Scored no better than Gemma 4 E4B in one benchmark. |
| Qwen3-Coder 30B-A3B | MoE 30B, 3B active | ~19 GB | Apache 2.0 | Best tool-call reliability across agent frameworks in one study; needs 24 GB. |
| Qwen 3.8 27B | dense 27B | ~17 GB | check | Reliable in every framework tested; needs 24 GB, or a 3-bit quant with ~8K usable context. |
| Devstral Small 2 24B | dense 24B | ~15 GB | Apache 2.0 | Top coding benchmark at this size, but deprecated by Mistral (Feb 2026) and leaves almost no room for context. |
| Qwen2.5-Coder 7B/14B/32B | dense | 4.7-19 GB | Apache 2.0 | Writes tool calls as prose; fails native tool-calling agents. |

One open question for the primary: a 2026 test of seven local models found
every MoE model failed long multi-step tool workflows (they did not recognise
when to stop) while dense 8-9B models passed. gpt-oss-20b is MoE. Other
tests and our probe disagree, so the fault-injection suite decides: run it
on gpt-oss-20b against the best dense model.

## What matters for this job, in order

1. Reliable tool calls over 10-30 steps.
2. Reading long, noisy logs: 32K context minimum.
3. Windows, PowerShell, Docker and WSL knowledge.
4. Runs on the supported hardware, down to CPU only.
5. Redistributable licence (Apache 2.0 / MIT).

## Files

| File | Source |
|---|---|
| `gpt-oss-20b-MXFP4.gguf` | huggingface.co/ggml-org/gpt-oss-20b-GGUF @ `ef9b12f2` |
| `Qwen3.5-9B-Q4_K_M.gguf` | huggingface.co/unsloth/Qwen3.5-9B-GGUF |
| `gemma-4-E4B-it-Q4_K_M.gguf` | huggingface.co/unsloth/gemma-4-E4B-it-GGUF |
| `granite-4.1-8b-Q4_K_M.gguf` | huggingface.co/unsloth/granite-4.1-8b-GGUF |
| `llama-b9771-bin-win-{cuda-12.4,vulkan,cpu}-x64.zip`, `cudart-llama-bin-win-cuda-12.4-x64.zip` | github.com/ggml-org/llama.cpp/releases/tag/b9771 |

Re-measure a model:

```powershell
.\scripts\install\doctor\eval\Measure-LlmRuntime.ps1 -Model gpt-oss-20b-MXFP4.gguf -Backend cuda
```

Results append to `doctor-assets\runtime-results.jsonl`.
