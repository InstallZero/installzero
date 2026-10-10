# Schemas

JSON schemas for the four InstallZero formats. They are written from what
rl-roboracer's installer actually produces (migration steps 1 and 2 in the
plan), then become the contract the Go engine implements.

| Schema | Describes |
|---|---|
| `manifest-1.json` | A project's `install.json`: requirements and ordered phases |
| `catalog-entry-1.json` | One known failure: pattern, diagnosis, named fix, samples |
| `record-1.json` | One failure record, the only file ever shared |
| `lock-1.json` | What an install created and from which pinned sources |

None are written yet.
