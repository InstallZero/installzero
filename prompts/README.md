# Prompts

Everything the harness writes to the model, shared by the PowerShell engine
(`doctor/Prompts.ps1`) and the Go engine (`prompts.go`), so both send
identical text. The fine-tuned models were trained on this text: change it
deliberately, and expect benchmark numbers to move when you do.

| File | Holds |
|---|---|
| `probe.md`, `agent.md`, `doctor.md` | System prompts |
| `doctor-optimize.md` | The doctor's optimize-mode request |
| `messages.json` | First user messages, reminders the harness sends as user turns, and the loop's notes in tool results ("not run: ...", "[This is call 3 ...]") |
| `tools.json` | Tool definitions (`tools`) and the `purpose` parameter the harness adds to every call (`parameters`) |

What a tool returns from doing its work (command output, "refused: ...")
stays with the tool's code.

## Placeholders

- `{{name}}` is replaced by the value `name`. Inserted values are not scanned
  again.
- `{{#name}}...{{/name}}` is kept when `name` is set (not null, empty or
  false); `{{^name}}...{{/name}}` when it is not. Sections may nest.
- Names are lower case. Upper-case placeholders such as `{{REPO_DIR}}` belong
  to install.md and pass through unchanged.
- A name without a value is an error, not an empty string.
- In `tools.json`, a string that is only `{{name}}` becomes the value itself,
  so a list can fill an `enum`.

A `.md` file's final newline is not part of the text.

## Key order in tools.json

Keys are in the order the definitions were written when they lived in
PowerShell code. The PowerShell engine builds hashtables from them in that
order, which reproduces the requests it sent before; the order a hashtable
serializes in is not the file order. The Go engine sends file order. The two
agree on content, not on key order, until the engines are compared in phase 3
of `docs/go-port-plan.md`.
