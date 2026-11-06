"""Compare eval campaigns: delivery, recall, failures, cost per run.

    python compare_campaigns.py <campaign id>[@model text][=label] ...
    python compare_campaigns.py cmp-a@E4B-it-Q4=base cmp-b="ft-3 no DRY" --repos

Reads bench_results from the console's MongoDB through `docker exec`, so it
needs no database driver. "@model text" keeps only that campaign's results
whose model name contains the text (a campaign can hold several models);
--model applies one such filter to every campaign. Recall per repo is
the mean over its runs; "recall, all runs" counts an undelivered run as 0.
"""
import argparse
import json
import subprocess
from collections import defaultdict
from statistics import mean

MONGO_CONTAINER = "installzero-mongo-1"
CONSOLE_CONTAINER = "installzero-console-1"


def load_results(campaign):
    url = subprocess.run(["docker", "exec", CONSOLE_CONTAINER, "printenv", "MONGO_URL"],
                         capture_output=True, text=True, check=True).stdout.strip()
    query = ("const d=db.getSiblingDB('installzero');"
             f"print(EJSON.stringify(d.bench_results.find({{campaign:'{campaign}'}},"
             "{repo:1,model:1,delivered:1,recall:1,stop_reason:1,format_problems:1,'probe.stats':1}).toArray()))")
    out = subprocess.run(["docker", "exec", MONGO_CONTAINER, "mongosh", url, "--quiet", "--eval", query],
                         capture_output=True, text=True, check=True, encoding="utf-8").stdout
    return json.loads(out)


def summarize(rows):
    by_repo = defaultdict(list)
    for r in rows:
        by_repo[r["repo"]].append(r)
    delivered = [r for r in rows if r.get("delivered")]
    stats = [(r.get("probe") or {}).get("stats") or {} for r in rows]
    recall_delivered, recall_all = [], []
    for runs in by_repo.values():
        got = [r["recall"] for r in runs if r.get("delivered") and r.get("recall") is not None]
        if got:
            recall_delivered.append(mean(got))
        recall_all.append(sum(got) / len(runs))
    return {
        "runs": len(rows),
        "delivered": len(delivered),
        "format problems": sum(1 for r in delivered if r.get("format_problems")),
        "timeouts": sum(1 for r in rows if r.get("stop_reason") == "timeout"),
        "model errors": sum(1 for r in rows if r.get("stop_reason") == "model-errors"),
        "recall, delivered": mean(recall_delivered) if recall_delivered else 0.0,
        "recall, all runs": mean(recall_all) if recall_all else 0.0,
        "output tokens/run": round(mean(s.get("completion_tokens", 0) for s in stats)) if stats else 0,
        "minutes/run": mean(s.get("seconds", 0) for s in stats) / 60 if stats else 0,
    }, by_repo


def fmt(v):
    return f"{v:.3f}" if isinstance(v, float) and v < 1 else (f"{v:,.1f}" if isinstance(v, float) else str(v))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("campaigns", nargs="+", help="campaign id, optionally =label")
    ap.add_argument("--model", default="", help="keep results whose model contains this text")
    ap.add_argument("--repos", action="store_true", help="also print recall per repo and run")
    a = ap.parse_args()

    columns, repo_tables = [], []
    for spec in a.campaigns:
        cid, _, label = spec.partition("=")
        cid, _, model = cid.partition("@")
        rows = [r for r in load_results(cid) if (model or a.model) in (r.get("model") or "")]
        summary, by_repo = summarize(rows)
        columns.append((label or cid, summary))
        repo_tables.append((label or cid, by_repo))

    width = max(len(k) for k in columns[0][1]) + 2
    print("".ljust(width) + "".join(label[:22].ljust(24) for label, _ in columns))
    for key in columns[0][1]:
        print(key.ljust(width) + "".join(fmt(s[key]).ljust(24) for _, s in columns))

    if a.repos:
        repos = sorted({repo for _, t in repo_tables for repo in t})
        print("\nrecall per run ('--' = no install.md delivered)")
        print("repo".ljust(14) + "".join(label[:22].ljust(24) for label, _ in repo_tables))
        for repo in repos:
            cells = []
            for _, t in repo_tables:
                runs = t.get(repo, [])
                cells.append(" ".join(f"{r['recall']:.2f}" if r.get("delivered") and r.get("recall") is not None else "--" for r in runs))
            print(repo.ljust(14) + "".join(c.ljust(24) for c in cells))


if __name__ == "__main__":
    main()
