# InstallZero

An installer engine that any project can drive with a manifest, a shared
catalog of install failures and their fixes, and a local or hosted LLM doctor
for failures the catalog does not know yet.

Status: early. The formats (manifest, catalog entry, failure record, lock
file) are being proven in [rl-roboracer](https://github.com/frenb/rl-roboracer)'s
PowerShell installer first; they move here as JSON schemas, and the Go
engine is built against them. The design is in rl-roboracer's
[`docs/agentic-installer-split-plan.md`](https://github.com/frenb/rl-roboracer/blob/main/docs/agentic-installer-split-plan.md).

First target: Windows 10/11 with WSL 2, Docker Desktop and NVIDIA GPU and ML
stacks. macOS (Apple Silicon) and native Linux follow.

## Planned command line

```text
installzero add <owner/repo | git URL | ./folder>   install a project from its install.json
installzero check <source>                          report what is present or missing; change nothing
installzero list | update | remove <project>        manage installed projects from lock.json
installzero doctor [--prompt | <agent>]             diagnose a failure with a local model, a hosted model or your coding agent
installzero find "<error text>"                     look an error up in the catalog
installzero failures | share | capture              local failure records and opt-in sharing
installzero init                                    write a starter install.json
```

## Layout

| Path | Contents | Licence |
|---|---|---|
| `schemas/` | JSON schemas for the manifest, catalog entries, failure records and the lock file | Apache 2.0 |
| `catalog/` | One JSON file per known failure: signature pattern, diagnosis, named fix action, redacted samples | CC BY 4.0 |

## Licences

Code and schemas: [Apache 2.0](LICENSE). Catalog: [CC BY 4.0](catalog/LICENSE).
The InstallZero name is not licensed for use by other projects (see
[NOTICE](NOTICE)). Contributions need a DCO sign-off (see
[CONTRIBUTING.md](CONTRIBUTING.md)).
