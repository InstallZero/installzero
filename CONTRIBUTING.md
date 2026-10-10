# Contributing

Every commit needs a Developer Certificate of Origin sign-off
(<https://developercertificate.org/>): add `-s` to `git commit`, which appends

```text
Signed-off-by: Your Name <you@example.com>
```

By signing off you certify that you wrote the change or otherwise have the
right to submit it under this project's licences: Apache 2.0 for code and
schemas, CC BY 4.0 for anything under `catalog/`.

## Catalog entries

One file per entry, `catalog/<id>.json`. Each entry needs at least one real,
redacted sample line; the self-test requires every sample to match its own
entry and no earlier one. Never include user names, machine names, paths
under a home folder, IP addresses, tokens or keys in a sample.

Fixes can only reference named fix actions implemented by the engine, never
free-form commands.

## Security

Do not report vulnerabilities in public issues. A security policy and
disclosure address will be published before the first release.
