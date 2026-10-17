# InstallZero: splitting the installer into a standalone agentic installer

Status: plan, not started. Written 2026-10-08; decisions recorded the same
day (see "Decisions"). Command line, extensibility and failure storage and
routing added 2026-10-10, modelled on the `npx skills` CLI
([vercel-labs/skills](https://github.com/vercel-labs/skills)). Follows
`docs/install-doctor-plan.md` (the doctor) and `scripts/install/Install.ps1`.

## Goal

Turn the rl-roboracer installer and its doctor into a separate, reusable
project, **InstallZero**: an installer engine that any project can drive with a manifest, a
shared catalog of install failures and their fixes, and a local or hosted LLM
doctor for failures the catalog does not know yet.

The reason to split it out is a feedback loop. More projects using it means
more machines, more machines means more failures seen, more failures turned
into tested catalog entries means a better installer, which brings more
projects. What compounds is not the code or the agent (models improve for
everyone, and public troubleshooting knowledge is easy to copy) but the
**corpus of verified outcomes**: for a failure signature on a given kind of
machine, which fix was applied and whether the install then passed. Every
design choice below is judged by whether it helps collect that corpus safely.

## Scope of the first version

**Windows 10/11 + WSL 2 + Docker Desktop + GPU and ML stacks** (NVIDIA
drivers and CUDA, container GPU access, local model runtimes). This is where
installs fail most and where existing tools (winget, conda, Homebrew,
devcontainers, Pinokio) help least with diagnosis. It is also exactly what the
current catalog and knowledge pack cover.

Later: macOS (Apple Silicon first; the `m1` feasibility check feeds this) and
native Linux. The manifest and record formats are language-neutral from the
start so those ports do not need a redesign.

Out of scope: package management itself (the engine calls winget, apt, pip,
docker; it does not replace them), and fully offline installs (as decided in
the doctor plan, only the doctor is offline).

## What exists today and where it goes

| Today (rl-roboracer) | Becomes | Notes |
|---|---|---|
| `Install.ps1`: phase runner, state and resume, `install-report.json`, `-DryRun`, `-ExpectClean`, reboot and sign-out handling | **engine** | Project-neutral already in shape; needs the project specifics pulled out. |
| `Install.ps1`: phases 0-3 (preflight, WSL, Git, Docker Desktop) | **engine built-in phase types** | `preflight`, `wsl`, `winget-package`, `docker-desktop`, `docker-gpu`. |
| `Install.ps1`: phases 4-8 (repo, images, Unity gyms, start, seed, first job) | **rl-roboracer manifest** + project phase scripts | Stays in this repo. |
| `$FailureCatalog` (20 entries with samples) and `-SelfTest` | **catalog** (data files) | Generic entries move; rl-roboracer-only ones (`demo-data-missing`, mongo ones tied to our compose file) stay as a project catalog layered on top. |
| Fix kinds (`retry`, `restart-docker`, `rebuild-clean`, `wsl-update`, `port-owner`, `signout`, `mongo-owner`, `recreate`) | **engine fix actions** | Named, parameterised actions with a policy tier, not free-form commands. |
| `doctor/Doctor.ps1`, `Tools.ps1` (incl. read-only command policy), `Llm.ps1` | **doctor** | Hosted-model specs (`anthropic:`, `openai:`, `xai:`, `google:`) stay as an option alongside local models. |
| `doctor/kb/` | **knowledge pack** | Per-article licences are already recorded in `kb/README.md`. |
| `doctor/eval/` (scenarios, `Invoke-DoctorEval.ps1`, `Measure-LlmRuntime.ps1`), `MODELS.md` | **eval harness** + per-project scenario files | The generic harness moves; the 12 rl-roboracer scenarios stay here as the first project suite. |
| `Uninstall.ps1` | **engine** `uninstall` / test-instance reset | Driven by what the manifest declared. |
| `Start-ClientAtLogon.ps1`, `seed/` | stay in rl-roboracer | Project phases. |

## Architecture

```
project repo                         installer (new repo, pinned release)
  install.json  (manifest) ------>   engine     phase runner, state, report
  install/phases/*.ps1               phases     built-in phase types
  install/catalog.json (optional)    catalog    signatures -> diagnosis, fix action
  install/scenarios.ps1 (optional)   fixes      named actions + policy tiers
                                     doctor     agent loop, tools, policy, LLM runtime
                                     kb         offline knowledge pack
                                     records    failure records, redaction, submission
                                     eval       fault-injection harness
```

Run order on a failure stays as today: catalog first (instant, tested,
explainable), then the doctor only when nothing matches or the catalogued fix
did not work.

### Manifest

JSON, validated against a published schema (readable by the PowerShell
engine with no dependencies, and by the Go engine later). A manifest lists
phases in order. Each phase is either a built-in type with parameters or a
project script with a detect step (prints a description and exits 0 if
already done) and an install step. Scripts can be given per OS
(`"script": { "windows": "install/phases/Start.ps1", "linux": "install/phases/start.sh", "macos": "install/phases/start.sh" }`).

```json
{
  "schema": "https://<project>/schema/manifest-1.json",
  "name": "rl-roboracer",
  "requires": { "os": "windows>=10.0.19045", "gpu": "nvidia", "driverMin": "551.61", "ramGB": 16, "diskGB": 80 },
  "phases": [
    { "id": "preflight", "type": "preflight" },
    { "id": "wsl",       "type": "wsl" },
    { "id": "git",       "type": "winget-package", "package": "Git.Git" },
    { "id": "docker",    "type": "docker-desktop", "gpu": true },
    { "id": "workspace", "type": "script", "path": "install/phases/Workspace.ps1" },
    { "id": "images",    "type": "compose-build", "services": ["ros-server", "sim-controller", "fly-brain"] },
    { "id": "unity",     "type": "release-assets", "assets": ["roboracer-gym-*.zip"], "verify": "sha256" },
    { "id": "start",     "type": "script", "path": "install/phases/Start.ps1" }
  ],
  "catalog": "install/catalog.json",
  "doctor": { "kb": ["docs/", "docker-compose.yml"], "hiddenPaths": [".env"] }
}
```

`hiddenPaths` lists files the doctor may not read or search (`read_file`,
`grep`, `run_command`). Today's doctor only hides its own files, not
`.env`; this closes that gap.

### Catalog entries (curated, shipped)

One JSON file per entry so contributions are small, reviewable diffs. Fields
match today's hashtable entries plus what the loop needs:

| Field | Meaning |
|---|---|
| `id` | Stable kebab-case id (`docker-vm-crash`). |
| `version` | Bumped when pattern or fix changes, so field records refer to an exact entry. |
| `phases` | Phase types it applies to, or `*`. |
| `pattern` | .NET regex over the phase's captured output. |
| `requires` | Optional fingerprint conditions (e.g. `gpu.vendor = nvidia`, `docker.version < 4.30`). |
| `diagnosis`, `advice` | Plain text for the user, as today. |
| `fix` | Named fix action and parameters (`{ "action": "restart-docker" }`). |
| `samples` | Real, redacted log lines. The self-test requires each to match its own entry and no earlier one. |
| `kb` | Related knowledge-pack articles. |
| `source` | `curated`, `doctor-proposed` or `field-promoted`, plus the record ids that justified it. |

First match wins, as today. Project catalogs are checked before the shared
one so a project can override a generic entry.

### Fix actions and policy

Fixes are named actions implemented in the engine (`retry`,
`restart-docker`, `wsl-update`, `rebuild-clean`, `free-port`,
`chown-volume`, `recreate-service`, `signout`, `reboot`), each with a tier:

- **auto**: retry, re-read state.
- **ask, or auto with `-AutoFix`**: changes inside the project folder or
  compose project.
- **always ask**: system-wide changes (winget, `wsl --update`, services,
  stopping other programs, reboot), showing the exact command.
- **never**: the doctor plan's deny list (formatting, deleting outside the
  project, `wsl --unregister`, removing data volumes, credential stores).

The doctor may propose free-form commands, but they go through the same
classifier (`Test-ReadOnlyCommand` today) and are always confirmed unless
read-only. Catalog entries can only reference named actions, so a malicious
or mistaken catalog entry cannot run arbitrary code.

## Command line

One verb, one source, a few common flags, as in `npx skills add owner/repo -y`.
A source is `owner/repo`, a git URL, a path inside a repo, a local folder or
an archive URL; private repos use the git credentials, GitHub CLI login or
SSH key already configured, and InstallZero never reads or stores the token.

```text
installzero add frenb/rl-roboracer          install a project
installzero add frenb/rl-roboracer --list   show the phases and what each would do; install nothing
installzero check frenb/rl-roboracer        run only the detect steps; report present / missing
installzero list                            installed projects and the state of each phase
installzero update [project]                re-run against the newer manifest; finished phases are skipped
installzero remove <project> [--data]       uninstall from the lock file; data folders only with --data
installzero doctor [project | <record>]     diagnose the last (or a given) failure with the chosen model
installzero doctor --prompt | claude        print the evidence and instructions for any coding agent
installzero find "<error text>"             search the catalog for a pasted error
installzero init                            write a starter install.json for a new project
```

Common flags: `-y` (no prompts), `--phase <id>` (one phase only),
`--doctor <model>`, and `-g` for machine scope. **Machine scope** is the
prerequisites several projects share (WSL, Docker Desktop, drivers);
**project scope** is the repo, images, assets and data. `remove` only touches
project scope unless `-g` is given and no other installed project needs the
machine-scope item.

`check` is the "look without committing" command (`skills use` in the
skills CLI): every phase already has a detect step, so it costs nothing to
offer. `doctor --prompt` lets the user's own coding agent (Cursor, Claude
Code, Codex) do the reasoning on the same redacted evidence the built-in
doctor gets, with no model download and no API key.

**Lock file.** `add` and `update` write `lock.json` in the InstallZero state
folder: the manifest source and commit, every extension pin (source, commit,
sha256), every downloaded asset with its sha256, the images built, and the
shortcuts, services and folders created, each tagged with its scope.
`remove`, `update` and test-account resets act on that list instead of
inferring what was installed (today's `Uninstall.ps1` infers it from the
compose project's working folder).

## Extensibility

An extension is a folder with one small header file, published in any git
repo, found by looking in a few standard places, with no registration step.
That is how skills work (a folder with `SKILL.md`); a search site
(`installzero.dev`, like skills.sh) is an index over what exists, not a
gate.

| Extension | Header | Found at | Used by |
|---|---|---|---|
| Project | `install.json` | repo root or `.installzero/` | `installzero add owner/repo` |
| Phase type | `phase.json` (name, description, OS, parameters, `owner`) plus detect and install scripts | `phases/<name>/` | a manifest phase `"type": "acme/installzero-phases@v1#cuda-toolkit"` |
| Catalog pack | one JSON file per entry | `catalog/<id>.json` | the manifest's `catalog`, or `installzero add-catalog owner/repo` |
| Doctor adapter | `adapter.json` | `adapters/<name>/` | `--doctor <name>` |

- **Built-ins are written the same way.** `wsl`, `docker-desktop`,
  `winget-package`, `compose-build` and `release-assets` ship as the first
  phase pack, so third-party packs have a working model to copy.
- **Doctor adapters** cover local llama.cpp models, the hosted providers
  (`anthropic`, `openai`, `xai`, `google`), and "agent" adapters that hand
  the prompt to an installed coding agent.
- **Catalog order**: the project's pack, then packs the user added, then the
  shared catalog. First match wins.
- **Pinning**: anything outside the project's own repo is pinned to a commit
  and sha256 in `lock.json`; nothing installs from an unpinned branch.
  Catalog packs still reference only named fix actions, so only phase packs
  carry scripts, and those are covered by the pin and by the policy tiers
  above.
- **Unreviewed content** (for example doctor-proposed catalog entries) is
  marked `"internal": true` and is only loaded when
  `INSTALLZERO_INTERNAL=1` is set, for testers.

## Failure records (field data)

Written locally for every failed phase, every catalogued fix and every
doctor run, whether or not anything is ever submitted. This is the raw
material for the corpus.

```json
{
  "record": "1",
  "id": "<random uuid per record>",
  "installer": "0.4.0", "catalog": "2026.10.08", "manifest": "rl-roboracer@<commit>",
  "lock": { "phasePack": "installzero/phases@<commit>", "sha256": "<pack sha256>", "catalogPacks": ["frenb/rl-roboracer@<commit>"] },
  "owner": "github:frenb/rl-roboracer",
  "phase": { "id": "images", "type": "compose-build", "step": "build sim-controller" },
  "fingerprint": {
    "os": "windows 10.0.26200", "arch": "x64", "locale": "en-US",
    "ramGB": "32-63", "diskFreeGB": "100-249",
    "gpu": { "vendor": "nvidia", "family": "ada", "vramGB": "8-11", "driver": "566.36" },
    "wsl": "2.5.9", "dockerDesktop": "4.41.2", "virtualization": "on"
  },
  "signature": { "hash": "sha256:<normalised lines>", "lines": ["failed to receive status: rpc error: code = Unavailable desc = error reading from server: EOF"] },
  "match": { "entry": "docker-vm-crash", "version": 3 },
  "fix": { "action": "restart-docker", "approved": "user", "result": "applied" },
  "outcome": { "phaseRerun": "passed", "installCompleted": true },
  "doctor": { "model": "gpt-oss-20b", "steps": 14, "rootCause": "<text>", "proposedEntry": { } }
}
```

- **Signature**: the matched or most error-like lines, normalised by
  replacing paths, numbers, hashes, ids, timestamps and ports with
  placeholders, then hashed. The same failure on different machines gets the
  same hash, which is what lets reports be counted and clustered.
- **Fingerprint**: coarse buckets, not exact values (RAM and VRAM ranges, GPU
  family rather than serial or exact model where the family decides the
  behaviour). Enough to explain why a fix works on some machines and not
  others, not enough to identify a machine.
- **Verified fix**: `outcome.phaseRerun = passed` after `fix` is what turns a
  suggestion into evidence. Success rate per (signature, fix, fingerprint
  bucket) is the main metric of the corpus.
- **Lock**: the exact pins (from `lock.json`) of the manifest, phase pack and
  catalog packs that were running, so a failure is tied to a version of the
  extension that produced it.
- **Owner**: who should hear about this failure, copied from the header of
  the piece that failed (see "Destinations and routing").

### Storage

A record is a folder, like an extension: one header file that is the only
thing ever sent, and supporting files that never leave the machine.

```text
%LOCALAPPDATA%\installzero\          (~/.local/state/installzero on Linux and macOS)
  lock.json
  records\
    2026-10-10T0812Z-images-3f9c\
      record.json     redacted, structured; the only file any destination reads
      raw.log         full phase output
      doctor.md       doctor transcript
      context.json    installer state at failure time (phase states, versions)
```

Folder names sort by time and carry the phase and the start of the
signature hash. "Raw logs and transcripts are never submitted" (rule 4
under "Redaction") is then a property of the layout: the sender only ever
opens `record.json`.

### Failure commands

```text
installzero failures                        local records, newest first: phase, signature, matched or not
installzero failures show <id>              the record exactly as it would be sent
installzero share <id> | --all              preview the final JSON, confirm, send to the configured destinations
installzero find <id> | sig:<hash>          look a record or signature up in the catalog
installzero doctor <id> [--prompt | claude] diagnose one record
installzero capture -- <command>            run any command and record its failure, even outside an InstallZero install
installzero find --log build.txt            diagnose a pasted or attached log
installzero catalog propose <id>            start a catalog entry folder from a record, with its redacted sample
installzero failures prune --older 90d      local retention
```

`capture` and `find --log` widen what the corpus can learn from: a failed
`docker compose build`, `pip install` or driver installer can be recorded
by someone who never installed through InstallZero. `catalog propose` turns
step 4 of the loop into editing a draft file rather than starting from
nothing.

## Redaction

Records are built from an allow-list of structured fields. Free text enters
only through `signature.lines`, `doctor.rootCause` and `proposedEntry`, and
all of it passes these rules before it is stored:

| Replace | With |
|---|---|
| User profile and home paths (`C:\Users\<name>`, `/home/<name>`, `/mnt/c/Users/<name>`) | `%USERPROFILE%`, `~` |
| Windows user, machine and domain names (read from the environment, matched literally) | `<user>`, `<host>`, `<domain>` |
| Email addresses | `<email>` |
| Public IP addresses, MAC addresses | `<ip>`, `<mac>` (loopback and private ranges kept as their class, e.g. `<private-ip>`) |
| SIDs, GUIDs, container and image ids | `<sid>`, `<guid>`, `<id>` |
| Known secret formats (`sk-`, `sk-ant-`, `xai-`, `AIza`, `ghp_`/`github_pat_`, `glpat-`, AWS keys, JWTs, `Bearer ...`, `password=...`) | `<secret>` |
| Long high-entropy strings (32+ chars, base64 or hex) not already classified | `<secret>` |
| URL query strings and userinfo (`https://user:pass@`) | removed |
| Values of every variable defined in the project's hidden files (`.env`) | `<secret>` (matched literally, so unusual formats are caught too) |

Rules:

1. **Local by default.** Nothing leaves the machine unless the user says yes
   for that record, or opts in once with `-ShareFailures`.
2. **The user sees exactly what would be sent** before sending, as the
   final JSON.
3. **Redaction has its own self-test**: a corpus of lines with planted
   secrets that must all come out redacted, run in CI alongside the catalog
   self-test. A redaction bug is treated as a security bug.
4. **Raw logs and doctor transcripts are never submitted**, only the record.
   A maintainer who needs more asks the reporter.

## Intake

Failure records and successful-install records go to a private MongoDB
instance; failures are also summarised in public GitHub issues.

```
installzero (user machine) --HTTPS--> intake service --> MongoDB (private)
                                        |                records, installs, signatures
                                        +--> GitHub issues (public, one per signature)
```

- **The client never talks to MongoDB or GitHub directly.** Any credential
  shipped in a binary is public. The intake is a small HTTPS service (a
  serverless function is enough at first) that holds the MongoDB and GitHub
  credentials.
- **The intake re-checks everything**: validates the record against the
  schema, runs the same redaction again server-side (a second line of
  defence against a client bug or an old client), rejects oversized records,
  and rate-limits by IP and by an anonymous per-install id.
- **Collections**: `records` (failure records), `installs` (one document per
  completed install: fingerprint, installer, catalog and manifest versions,
  phases run and their outcomes, duration; no free text), `signatures`
  (one per signature hash: counts, first and last seen, projects,
  fingerprint buckets, linked catalog entry and GitHub issue).
- **GitHub issues are one per signature, not one per report.** The first
  record with a new signature opens an issue with the redacted signature
  lines, phase and fingerprint summary; later records update a counts table
  in the issue body instead of adding comments. Fingerprint details stay in
  MongoDB; the issue shows only aggregates. Closing the issue is linked to
  the catalog entry that fixes it.
- **Successful installs need consent like failures do**, but are a single
  yes/no at the end of the install ("Share an anonymous install summary to
  help InstallZero?"), remembered per machine.
- **Retention**: raw records kept for a fixed period (e.g. 12 months),
  `signatures` aggregates kept indefinitely. Stated in the privacy notice
  shown with the consent prompt.
- "Currently private" stays an option: publishing aggregate statistics later
  only needs a read-only export of `signatures` and `installs` counts.

### Destinations and routing

The intake is one destination among several, chosen from a table of
adapters (as the skills CLI has one for agents). Every destination receives
the same redacted `record.json`, never more.

| Destination | Where it goes |
|---|---|
| `local` | Always, and the default; nothing leaves the machine. |
| `installzero` | The shared intake service above (corpus, one public issue per signature). |
| `owner` | Wherever the failing piece's header says: `github:<owner>/<repo>` (an issue per signature on that repo, through the intake, which holds the GitHub credentials) or `https://...` (the owner's own intake, same record format). |
| `file` | A zip of the selected `record.json` files, to attach to a support ticket. |

Each extension names its owner in its header:

```json
// install.json (a project)
{ "name": "rl-roboracer", "failures": { "owner": "github:frenb/rl-roboracer", "share": ["installzero", "owner"] } }

// phase.json (a third-party phase pack)
{ "name": "cuda-toolkit", "owner": "github:acme/installzero-phases" }
```

**The owner follows the code that failed.** A failure inside a pinned
third-party phase (`acme/installzero-phases@<sha>#cuda-toolkit`) is routed to
that pack's owner; a failure in a project script to the project; a failure
in a built-in phase to InstallZero. The shared corpus gets every shared
record regardless. This is what lets extension authors find and fix their
own breakages without InstallZero maintainers in the middle.

- **Consent is per destination.** The user approves each record, or opts in
  once per destination (`-ShareFailures installzero,owner`).
  `DO_NOT_TRACK=1` or `DISABLE_TELEMETRY=1` forces `local` only, whatever the
  manifest asks for.
- **Every receiving service redacts again.** An `https` owner endpoint is
  outside our control, so it gets only what the user previewed.
- **Signatures are the shared key.** `sig:<hash>` is the same on every
  machine and names the signature's public page (`installzero.dev/s/<hash>`):
  aggregate counts, the linked catalog entry and GitHub issue, nothing
  per-machine.

## The loop

1. **Collect**: records written locally; opt-in submission to the intake
   service (see "Intake"), which stores them in MongoDB and keeps one public
   GitHub issue per signature up to date.
2. **Cluster**: group by signature hash; sort by count and by how many
   distinct projects and fingerprints hit it.
3. **Propose**: for a cluster with no catalog entry, the doctor's
   `proposedEntry` from those runs is the starting draft;
   `installzero catalog propose <record>` writes it out as an entry folder
   marked `"internal": true` until reviewed.
4. **Review**: a maintainer turns it into a catalog entry with a redacted
   sample, the self-test passes, and an eval scenario is added when the
   failure can be reproduced.
5. **Release**: catalogs are versioned and released separately from the
   engine (signed, sha256 published), so a new fix reaches users without an
   engine upgrade. The engine fetches the latest catalog at start when online
   and falls back to the bundled one.
6. **Measure**: verified-fix rate per entry from later records. Entries whose
   fix stops working (new Docker Desktop release, new driver) show up as a
   falling rate and get revised.

The measure of the whole thing working: the share of failures resolved by
the catalog keeps rising, and the doctor is called less.

## Trust and safety

An agent that runs commands on strangers' machines is a supply-chain target,
and trust will decide adoption more than model quality.

- Catalog entries reference named fix actions only (see above).
- Catalog and engine releases are signed; the engine refuses an unsigned or
  tampered catalog.
- Every change the installer or doctor makes is recorded in a local
  transcript with what was changed and how to undo it; backups before file
  edits (as planned in doctor phase 3).
- The doctor's read-only policy keeps its current shape: allow-listed
  cmdlets and native read-only subcommands, everything else confirmed.
- Hosted models are opt-in, use the user's own API key, and are sent the same
  redacted evidence a record would contain.
- Security policy, disclosure address and a threat model document from the
  first public release.

## Distribution

- **Engine**: one `installzero` binary per OS and architecture (windows-amd64,
  windows-arm64, linux-amd64, linux-arm64, darwin-arm64, darwin-amd64),
  about 10-20 MB, catalog and kb embedded. One bootstrap line per project
  (`irm <url>/install.ps1 | iex` on Windows, `curl -fsSL <url>/install.sh | sh`
  elsewhere) downloads the pinned release, verifies sha256 and runs it with
  the project's manifest. The generic bootstrap passes its arguments through
  to the binary, so any verb works from one line with no runtime installed:
  `iex "& { $(irm https://installzero.dev/install.ps1) } add frenb/rl-roboracer"`.
  A project's own one-liner (rl-roboracer's `install.ps1`, `uninstall.ps1`)
  is a short form of `add <project>` / `remove <project>`.
- **Code signing**: deferred (Decision 6). Until then, installs go through
  the bootstrap one-liners, which download without the internet-download
  mark, so SmartScreen and Gatekeeper do not prompt. A binary downloaded in
  a browser is blocked on macOS (System Settings > Privacy & Security > Open
  Anyway) and warned about on Windows; the README says to use the
  bootstrap. Microsoft Defender may flag an unsigned Go binary: submit each
  release to Microsoft's false-positive form, and have the bootstrap
  notice when the downloaded binary has been quarantined or deleted and
  explain why. When signing is added: Azure Artifact Signing on Windows,
  Developer ID signing plus notarization on macOS.
- **Doctor local models**: optional, about 19 GB (doctor plan), downloaded
  only when the doctor is first needed and the user agrees, or pointed at an
  existing GGUF. Hosted models need no download.
- **rl-roboracer** keeps `scripts/install/Install.ps1` as a thin bootstrap so
  the README instructions do not change for users.

## Licensing

- **Code**: Apache 2.0 (decided). Chosen over MIT for its explicit patent
  licence from every contributor, with termination for anyone who sues over
  patents in the code, and for its NOTICE and trademark clauses (the licence
  does not grant use of the InstallZero name). Both are permissive and equally
  easy for companies to adopt.
- **Knowledge pack**: `docker-desktop-bind-mounts.md` is adapted from a
  CC BY-SA 4.0 source, so it must stay CC BY-SA. Either license the `kb/`
  folder as CC BY-SA 4.0 as a whole (simplest) or rewrite that article from
  primary sources. The MIT-derived articles keep their notices.
- **Catalog**: CC BY 4.0 (decided). Anyone may copy and adapt it, including
  commercially, but must credit InstallZero and link the licence wherever they
  share it, so reuse points back to the project. Publishing it openly is
  acceptable because the catalog is the visible output; the field corpus
  behind it (counts, fingerprints, verified-fix rates) is not published raw
  and is what others cannot copy. The catalog files embedded in the Apache
  2.0 binary are listed with their licence in the NOTICE file. Contributors
  agree that catalog contributions are licensed CC BY 4.0. Whether
  aggregate statistics are published is still open.
- **Contributor terms**: DCO sign-off for code and catalog contributions;
  submitted records covered by the intake's terms (what is collected, how
  long it is kept, who can see it).

## Migration path

rl-roboracer's installer keeps working at every step; the Tier A test
(`t1`) is the regression check after each one.

1. **Catalog as data, in this repo.** Move `$FailureCatalog` into
   `scripts/install/catalog/*.json` with the fields above; `Find-Failure` and
   `-SelfTest` read the files. No behaviour change.
2. **Failure records and lock file, local only.** Write records (one folder
   each, as under "Storage") and redaction with its self-test; attach them
   to `install-report.json`. Write `lock.json` from what the phases did and
   make `Uninstall.ps1` read it. No submission yet. The
   catalog, record and manifest formats defined in steps 1-2 are the
   contract the Go engine implements, so nothing written here is thrown away.
3. **InstallZero repository, Go engine.** New `installzero` repo (Apache 2.0). Port
   the phase runner, state and resume, report, built-in phases, fix actions,
   policy classifier, redaction and catalog matcher to Go, with the JSON
   catalog and the redaction corpus as shared test fixtures. The binary
   exposes the verbs under "Command line" from the start; the built-in
   phases ship as the first phase pack. Windows first,
   with parity checked by running the rl-roboracer Tier A test against both
   engines.
4. **rl-roboracer on InstallZero.** rl-roboracer gets a manifest plus its
   project phase scripts; `Install.ps1` becomes the bootstrap that fetches a
   pinned `installzero` release. The PowerShell engine is retired.
5. **Doctor in Go.** Port the agent loop and tools (`Doctor.ps1`,
   `Tools.ps1`, `Llm.ps1`); llama-server stays an external binary. Move the
   kb and eval harness; rl-roboracer's 12 scenarios stay as its project
   suite.
6. **Intake.** The HTTPS intake service, MongoDB collections, per-signature
   GitHub issues, consent prompts, clustering and review workflow, signed
   catalog releases, and owner routing (the `owner` and `file`
   destinations).
7. **A second project.** Write a manifest for a different project with a
   GPU/ML install (ideally one with an active user base and its own install
   issues) to prove the engine is general and find what the manifest format
   is missing.
8. **Linux and macOS** phase types (apt/dnf, Homebrew, Docker Engine and
   NVIDIA Container Toolkit on Linux; Apple Silicon on macOS, informed by
   the `m1` feasibility check).

## Decisions (2026-10-08)

1. **Name: InstallZero** (2026-10-09; first chosen as ZeroRig). The
   rl-roboracer installer already carries the name; its file names and
   paths change when the engine moves to its own repo. The repo exists
   (2026-10-10) at [InstallZero/installzero](https://github.com/InstallZero/installzero),
   owned by the `InstallZero` GitHub organization, holding the licences,
   README and contributor terms; schemas and the Go engine follow steps 1-3.
2. **Licences: code Apache 2.0, catalog CC BY 4.0**; kb folder CC BY-SA 4.0
   (or rewrite the one CC BY-SA article and use CC BY 4.0 there too).
3. **Shared failure reports: GitHub issues (public, one per signature) and
   a private MongoDB**, both written by the intake service (see "Intake").
4. **Engine language: Go.** It has to run on Windows, Linux and macOS
   without asking users to install a runtime first, which rules out Python,
   Node and PowerShell 7 outside Windows (an installer that needs an
   install). Go builds one static binary per OS and architecture from any
   machine with no C toolchain, its standard library covers what the engine
   needs (processes, HTTP, JSON, file system, Windows registry and services
   via `x/sys`), and it is the language of the tools InstallZero works alongside
   (Docker, Ollama, gh, Kubernetes tooling), so contributors from that world
   can read it. Rust would give smaller binaries and stronger guarantees but
   slower contribution and more involved cross-compiling for Windows and
   macOS; the engine is process orchestration and text matching, where
   those advantages matter little.
5. **Successful installs: recorded in the private MongoDB** (`installs`
   collection, with consent), so failure counts become failure rates.
6. **No OS code signing for now** (rl-roboracer and the second project).
   Binaries ship without Authenticode or Apple notarization. Kept, because
   they are free and need no certificates: sha256 checksums pinned in the
   bootstrap scripts, cosign keyless signatures and GitHub artifact
   attestations on releases, and the Ed25519 signature on catalog releases.
   The release pipeline keeps a placeholder step for Windows and macOS
   signing so adding it later is a configuration change. Revisit before
   promoting InstallZero beyond these two projects, and decide by then whether
   to sign as an individual or an organization.

## Open decisions

1. **Which second project** to onboard in step 7.
2. **Intake hosting** (serverless function provider, MongoDB hosting) and
   the retention period.
3. **Code signing** (deferred, Decision 6): individual or organization
   identity, Azure Artifact Signing and an Apple Developer account.
4. **Domain.** `installzero.dev` is a placeholder for the bootstrap, the
   search index and the per-signature pages.
