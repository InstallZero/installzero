# InstallZero

An installer engine that any project can drive with a manifest, a shared
catalog of install failures and their fixes, and a local or hosted LLM doctor
for failures the catalog does not know yet.

Status: early. A project describes its install in an `install.md` (steps
with detect, install and verify blocks); `installzero install` runs it,
records what it changed so `installzero uninstall` can undo it, and hands a
failure the catalog cannot fix to the model you choose. First in use in
[rl-roboracer](https://github.com/frenb/rl-roboracer). First target: Windows
10/11 with WSL 2, Docker Desktop and NVIDIA GPU and ML stacks; macOS and
Linux builds are published too.

## Install a project

Binaries for Windows, macOS and Linux are on the
[releases page](https://github.com/InstallZero/installzero/releases).

```text
installzero install                          run the install.md in this folder
installzero install <owner/repo[@ref]>       get the project into ~/<repo>/<repo> (git clone, or GitHub's
                                             archive when git is not installed yet) and run its install.md
installzero install ... --plan               list the steps for this machine and run nothing
installzero install ... --model <choice>     the model that fixes what the catalog cannot (asked once)
installzero install ... --agent <model>      let a model carry out the install.md (judged by each verify)
installzero uninstall <project> [--data]     undo what a recorded install changed
installzero key set|remove|status <provider> hosted-model API keys, kept in the OS credential store
```

Machine-wide steps are listed first and need your yes. A step that needs a
restart of Windows stops the install and offers to continue at the next
sign-in.

## Publish installzero with your project

Attach a copy tagged with your repository to a release: opened without
arguments (double-clicked from Downloads), it asks to install your project
and then runs its install.md.

```text
installzero tag <owner/repo[@ref]> -i installzero-windows-amd64.exe -o installzero.exe
installzero tag --show installzero.exe
```

Tagging writes the project into a fixed slot in the executable; any build
can tag any platform's binary, so a Linux CI runner can tag the Windows one.
Until InstallZero's binaries are code-signed, Windows SmartScreen asks the
user to confirm (More info, Run anyway).

## Layout

| Path | Contents | Licence |
|---|---|---|
| `schemas/` | JSON schemas for the manifest, catalog entries, failure records and the lock file | Apache 2.0 |
| `catalog/` | One JSON file per known failure: signature pattern, diagnosis, named fix action, redacted samples | CC BY 4.0 |
| `doctor/` | Diagnoses a failed install from its logs with a local or hosted model, using the catalog and a fixed set of read-only tools | Apache 2.0 |
| `doctor/kb/` | Short articles the doctor can look up | CC BY-SA 4.0 |
| `agent/` | The probe and agent loop that work out how to install a repo and write its `install.md` | Apache 2.0 |
| `agent/eval/` | Benchmark of hard-to-install repos, WSL recipes, and the worker that runs eval jobs | Apache 2.0 |
| `console/` | Web console for queuing evals, comparing models, and reviewing losses and lessons | Apache 2.0 |
| `docs/` | Plans and design notes | Apache 2.0 |

## Licences

Code and schemas: [Apache 2.0](LICENSE). Catalog: [CC BY 4.0](catalog/LICENSE).
The InstallZero name is not licensed for use by other projects (see
[NOTICE](NOTICE)). Contributions need a DCO sign-off (see
[CONTRIBUTING.md](CONTRIBUTING.md)).
