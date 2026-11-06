"""Estimate what Hugging Face Jobs have cost, from the job history.

    python hf_spend.py

An API token cannot read the account's credit balance (Hugging Face shows
billing only to a signed-in browser), so this adds up each job's running
time at the flavor's list price. Reads HF_TOKEN from the environment or
InstallZero's .env.
"""
import datetime as dt
import os
from pathlib import Path

from huggingface_hub import HfApi

PRICE_PER_HOUR = {"h200": 5.00, "a100-large": 2.50, "l40sx1": 1.80, "l4x1": 0.80, "t4-small": 0.40, "cpu-basic": 0.0}


def token():
    if os.environ.get("HF_TOKEN"):
        return os.environ["HF_TOKEN"]
    env = Path(__file__).resolve().parents[2] / ".env"
    for line in env.read_text(encoding="utf-8").splitlines():
        if line.startswith("HF_TOKEN="):
            return line.split("=", 1)[1].strip()
    raise SystemExit("HF_TOKEN not found")


def main():
    now = dt.datetime.now(dt.timezone.utc)
    total = 0.0
    for j in sorted(HfApi(token=token()).list_jobs(), key=lambda j: j.created_at):
        start = j.started_at or j.created_at
        end = j.finished_at or (now if j.status.stage == "RUNNING" else None)
        hours = (end - start).total_seconds() / 3600 if start and end else None
        rate = PRICE_PER_HOUR.get(str(j.flavor))
        cost = hours * rate if hours is not None and rate is not None else None
        total += cost or 0
        what = (j.labels or {}).get("serve") or (j.labels or {}).get("run") or j.id
        print(f"{j.created_at:%m-%d %H:%M}  {str(j.flavor):<11} {j.status.stage:<10}"
              f" {'%.2f h' % hours if hours is not None else '?':>8} {'$%.2f' % cost if cost is not None else '?':>8}  {what[:50]}")
    print(f"total about ${total:.2f}")


if __name__ == "__main__":
    main()
