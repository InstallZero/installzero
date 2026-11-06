"""Validates run transcripts and probe results against the schemas here.

    python schemas/validate.py [files or folders...]

Without arguments it checks this machine's runs: probe and agent transcripts
and eval results under %LOCALAPPDATA%\\installzero, doctor transcripts under
%LOCALAPPDATA%\\rl-roboracer\\doctor. Exit code 1 when anything fails.
Needs the jsonschema package.
"""
import collections
import json
import os
import pathlib
import sys

from jsonschema import Draft202012Validator

here = pathlib.Path(__file__).parent
event = Draft202012Validator(json.loads((here / "transcript-event-1.json").read_text(encoding="utf-8")))
result = Draft202012Validator(json.loads((here / "probe-result-1.json").read_text(encoding="utf-8")))


def files(args):
    if not args:
        base = pathlib.Path(os.environ.get("LOCALAPPDATA", pathlib.Path.home() / ".local" / "state"))
        args = [base / "installzero" / "probe", base / "installzero" / "agent", base / "installzero" / "eval", base / "rl-roboracer" / "doctor"]
    for a in map(pathlib.Path, args):
        if a.is_dir():
            yield from sorted(p for p in a.iterdir() if p.name.endswith((".transcript.jsonl", ".result.json", ".probe.json"))
                              or (p.suffix == ".jsonl" and p.name[:1].isdigit()))
        elif a.exists():
            yield a


def errors(validator, doc):
    for e in validator.iter_errors(doc):
        # oneOf over event types: report the errors of the branch for this
        # event's type (the branches whose type const failed are the others).
        found = [e]
        if e.validator == "oneOf" and e.context:
            branches = collections.defaultdict(list)
            for c in e.context:
                branches[c.relative_schema_path[0]].append(c)
            mine = [errs for errs in branches.values() if not any(list(c.relative_path) == ["type"] for c in errs)]
            if len(mine) == 1:
                found = mine[0]
        for f in found:
            yield f"{'/'.join(map(str, f.absolute_path)) or '(root)'}: {f.message[:200]}"


counts = collections.Counter()
problems = collections.Counter()
examples = {}
for path in files(sys.argv[1:]):
    kind = "transcript" if path.suffix == ".jsonl" else "result"
    counts[kind + "s"] += 1
    if kind == "result":
        docs = [(0, json.loads(path.read_text(encoding="utf-8-sig")))]
    else:
        docs = [(i, json.loads(l)) for i, l in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1) if l.strip()]
    for line, doc in docs:
        counts[kind == "result" and "results checked" or "events"] += 1
        for msg in errors(result if kind == "result" else event, doc):
            key = f"{kind} {doc.get('type', '') if kind != 'result' else ''} {msg}".replace("  ", " ")
            problems[key] += 1
            examples.setdefault(key, f"{path}:{line}" if line else str(path))

print(", ".join(f"{n} {k}" for k, n in counts.items()))
for key, n in problems.most_common():
    print(f"{n:6}  {key}\n        e.g. {examples[key]}")
sys.exit(1 if problems else 0)
