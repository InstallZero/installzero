"""Redraft, accept and rebuild the recipes of repos whose snapshot build failed.

    python fix_recipes.py [--attempts 3] [--drafts 6] [--model anthropic:claude-opus-5-5]
    python fix_recipes.py --status

Each selected Linux repo whose build failed goes round draft -> accept ->
build through the console's API: the draft job sees the previous recipe and
the failed build's error and log, plus the notes below. A build that fails
again starts another round, up to --attempts rounds per repo. Accepting a
draft also replaces the repo's facts; only repos without a ready snapshot are
touched, so no probe results depend on the facts replaced. Progress is kept in
%LOCALAPPDATA%/installzero/fix-recipes.json, so a restart resumes.
"""
import argparse
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path

API = "http://127.0.0.1:81"
STATE = Path(os.environ.get("LOCALAPPDATA", ".")) / "installzero" / "fix-recipes.json"
# Draft errors that say nothing about the repo: retried without counting.
TRANSIENT = ("could not be resolved", "credit balance is too low", "Unable to connect", "429", "overloaded", "timed out")
FEEDBACK = """The snapshot build of the recipe above failed; the error, the end of the log and diagnostics (failed units, containers and their logs, listening ports) are included. Fix the cause, keeping the project's production install path where it can work:
- Packages: Ubuntu 24.04 (noble) names only; check that every package exists there (or in the image a Dockerfile builds from). Drop optional tools the install does not need.
- Service never ready: read the diagnostics. Make the service listen where the ready check looks (host port, 0.0.0.0 or 127.0.0.1), give slow first starts (migrations, JVM, asset builds) a longer ready_timeout_sec, and do not health-check inside the script before the service can be up; leave that to ready.
- Downloads: release asset URLs must exist for the pinned version (asset names change between releases); pin a version whose asset you are sure of, or install from the distribution or the official image.
- Builds that compile large projects or exceed the hour: prefer packages, official binaries or images over building from source.
- Compose projects: pin image tags that exist; set restart policies; keep host ports in the assigned range.
Keep the deliberate non-default choices and facts unless the fix makes them wrong; facts must match the new recipe."""


def call(method, path, body=None):
    req = urllib.request.Request(API + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try:
            return {"error": json.loads(e.read()).get("error", str(e))}
        except Exception:
            return {"error": str(e)}


def load():
    return json.loads(STATE.read_text(encoding="utf-8-sig")) if STATE.exists() else {}


def save(s):
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps(s, indent=1))


def status(s):
    counts = {}
    for v in s.values():
        counts[v["phase"]] = counts.get(v["phase"], 0) + 1
    return ", ".join(f"{k} {n}" for k, n in sorted(counts.items()))


def step(s, a):
    for r in call("GET", "/api/repos"):
        if r["status"] == "selected" and r.get("os") != "windows" and r["stage"] == "build-failed" and r["id"] not in s:
            s[r["id"]] = {"phase": "todo", "attempts": 0, "draft_failures": 0, "job": None, "errors": []}
    drafting = sum(1 for v in s.values() if v["phase"] == "drafting")
    for repo, v in s.items():
        if v["phase"] == "todo" and drafting < a.drafts:
            j = call("POST", "/api/jobs", {"type": "draft", "repo": repo, "model": a.model, "feedback": FEEDBACK, "priority": 1})
            if "error" in j:
                print(f"  {repo}: draft not queued: {j['error']}")
                continue
            v.update(phase="drafting", job=j["_id"]); drafting += 1
        elif v["phase"] == "drafting":
            j = call("GET", f"/api/job?id={v['job']}")
            if j.get("status") in ("queued", "claimed", "running"):
                continue
            r = call("GET", f"/api/repo?id={urllib.request.quote(repo, safe='')}")
            if j.get("status") == "done" and (r.get("draft") or {}).get("status") == "ready":
                acc = call("POST", "/api/repo/accept-draft", {"id": repo})
                b = call("POST", "/api/jobs", {"type": "build", "repo": repo, "priority": 0}) if "error" not in acc else acc
                if "error" in b:
                    print(f"  {repo}: accept/build failed: {b['error']}"); v["phase"] = "todo"; continue
                v.update(phase="building", job=b["_id"], attempts=v["attempts"] + 1)
                print(f"  {repo}: draft accepted, build {v['attempts']} queued")
            else:
                err = str(j.get("error") or (r.get("draft") or {}).get("error"))
                if any(t in err for t in TRANSIENT):
                    v["phase"] = "todo"; print(f"  {repo}: draft hit a transient error, will retry: {err[:100]}")
                    continue
                v["draft_failures"] += 1
                v["errors"].append(f"draft: {err[:200]}")
                v["phase"] = "todo" if v["draft_failures"] < 2 else "gave-up"
                print(f"  {repo}: draft failed ({v['draft_failures']})")
        elif v["phase"] == "building":
            j = call("GET", f"/api/job?id={v['job']}")
            if j.get("status") in ("queued", "claimed", "running"):
                continue
            snap = (call("GET", f"/api/repo?id={urllib.request.quote(repo, safe='')}").get("snapshot") or {})
            if snap.get("status") == "ready":
                v["phase"] = "ready"; print(f"  {repo}: READY after {v['attempts']} rounds")
            else:
                v["errors"].append(str(snap.get("error") or j.get("error"))[:300])
                v["phase"] = "todo" if v["attempts"] < a.attempts else "gave-up"
                print(f"  {repo}: build {v['attempts']} failed: {v['errors'][-1].splitlines()[0][:120]}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--attempts", type=int, default=3, help="draft+build rounds per repo")
    ap.add_argument("--drafts", type=int, default=6, help="draft jobs in flight")
    ap.add_argument("--model", default="anthropic:claude-opus-5-5")
    ap.add_argument("--status", action="store_true", help="print progress and exit")
    a = ap.parse_args()
    s = load()
    if a.status:
        print(status(s))
        for repo, v in sorted(s.items()):
            if v["phase"] in ("gave-up", "ready"):
                print(f"{v['phase']:<8} {repo} ({v['attempts']} rounds){'  last: ' + v['errors'][-1].splitlines()[0][:100] if v['errors'] and v['phase'] == 'gave-up' else ''}")
        return
    while True:
        step(s, a)
        save(s)
        print(time.strftime("%H:%M"), status(s), flush=True)
        if s and all(v["phase"] in ("ready", "gave-up") for v in s.values()):
            break
        time.sleep(60)


if __name__ == "__main__":
    main()
