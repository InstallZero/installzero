"""train.py on Hugging Face Jobs, for Invoke-SftTrain.ps1.

  hf_job.py submit --state <file> --set-dir <dir> --dataset-repo <name> --model-repo <name>
                   --flavor a100-large --timeout 6h [--price 2.5] [--run-id ft-3] --train <train.py args>
      Uploads the SFT set to a private dataset repo, submits train.py with the
      package versions of this venv (what was tested locally), then relays the
      job's log, so PHASE/STEP/PROGRESS/METRIC lines reach the console as if
      training ran here. Exits non-zero unless the job completed. The state
      file (job id, namespace) lets the eval worker cancel the job.
  hf_job.py cancel --state <file>

Needs HF_TOKEN (environment or InstallZero's .env); it is passed to the job
as a secret.
"""
import argparse
import json
import os
import sys
import time
from importlib.metadata import PackageNotFoundError, version

TERMINAL = {"COMPLETED", "CANCELED", "ERROR", "DELETED"}
PACKAGES = ["torch", "transformers", "peft", "accelerate", "bitsandbytes", "sentencepiece", "protobuf", "safetensors", "huggingface_hub"]


def phase(name, msg=""):
    print(f"PHASE: {name}" + (f" | {msg}" if msg else ""), flush=True)


def metric(**kv):
    print("METRIC: " + json.dumps(kv), flush=True)


def pinned():
    out = []
    for p in PACKAGES:
        try:
            out.append(f"{p}=={version(p).split('+')[0]}")
        except PackageNotFoundError:
            out.append(p)
    return out


def env_token():
    t = os.environ.get("HF_TOKEN") or os.environ.get("HUGGING_FACE_HUB_TOKEN")
    if t:
        return t
    here = os.path.dirname(os.path.abspath(__file__))
    for f in (os.path.join(here, "..", "..", "..", ".env"), os.path.join(here, "..", "..", "..", "console", ".env")):
        if os.path.exists(f):
            for line in open(f, encoding="utf-8"):
                k, _, v = line.partition("=")
                if k.strip() in ("HF_TOKEN", "HUGGING_FACE_HUB_TOKEN") and v.strip():
                    return v.strip().strip("\"'")
    return None


def stage_of(info):
    s = info.status.stage
    return str(getattr(s, "value", s))


def save_state(path, **kv):
    state = json.load(open(path, encoding="utf-8")) if os.path.exists(path) else {}
    state.update(kv)
    json.dump(state, open(path, "w", encoding="utf-8"), indent=2)
    return state


def trained_with(api, model_repo, train_args):
    """True if model_repo already holds a merged model trained with these
    train.py arguments, e.g. when only the download failed: retrying the run
    then skips the paid job."""
    try:
        files = api.list_repo_files(model_repo)
        if "metrics.json" not in files or not any(f.startswith("merged/") and f.endswith(".safetensors") for f in files):
            return False
        done = json.load(open(api.hf_hub_download(model_repo, "metrics.json"), encoding="utf-8")).get("args", {})
    except Exception:
        return False
    want, i = {}, 0
    while i < len(train_args):
        k = train_args[i].lstrip("-").replace("-", "_")
        if i + 1 < len(train_args) and not train_args[i + 1].startswith("--"):
            want[k], i = train_args[i + 1], i + 2
        else:
            want[k], i = True, i + 1
    for k, v in want.items():
        have = done.get(k)
        if v is True:
            if have is not True:
                return False
        elif str(have) != v and not (isinstance(have, (int, float)) and _num(v) == have):
            return False
    return all(not done.get(k) for k in ("qlora", "reasoning") if k not in want)


def _num(v):
    try:
        return float(v)
    except ValueError:
        return None


def submit(a, api, token):
    ns = api.whoami()["name"]
    ds, model_repo = f"{ns}/{a.dataset_repo}", f"{ns}/{a.model_repo}"
    if trained_with(api, model_repo, a.train_args):
        print(f"{model_repo} already holds a model trained with these settings; reusing it (delete the repo to train again)", flush=True)
        save_state(a.state, id="", namespace=ns, url=f"https://huggingface.co/{model_repo}", flavor=a.flavor, dataset_repo=ds, model_repo=model_repo,
                   done=True, stage="COMPLETED", reused=True, python=sys.executable, script=os.path.abspath(__file__))
        return None, ns, model_repo
    phase("uploading", f"SFT set to {ds} (private)")
    api.create_repo(ds, repo_type="dataset", private=True, exist_ok=True)
    api.upload_folder(repo_id=ds, repo_type="dataset", folder_path=a.set_dir, commit_message=f"InstallZero SFT set {a.dataset_repo}",
                      allow_patterns=["train.jsonl", "val.jsonl", "tools.json", "manifest.json"])
    api.create_repo(model_repo, private=True, exist_ok=True)

    train_args = a.train_args
    deps = pinned()
    phase("setup", f"submitting to Hugging Face Jobs ({a.flavor}, timeout {a.timeout})")
    print("Dependencies: " + " ".join(deps), flush=True)
    job = api.run_uv_job(
        script=os.path.join(os.path.dirname(os.path.abspath(__file__)), "train.py"),
        script_args=["--data", ds, "--out", "/tmp/out", "--push-to", model_repo] + train_args,
        dependencies=deps, flavor=a.flavor, timeout=a.timeout, secrets={"HF_TOKEN": token},
        env={"HF_HUB_DISABLE_PROGRESS_BARS": "1", "TRANSFORMERS_VERBOSITY": "warning", "TOKENIZERS_PARALLELISM": "false", "PYTHONUNBUFFERED": "1"},
        labels={"app": "installzero", "run": a.run_id or a.model_repo}, namespace=ns, token=token)
    save_state(a.state, id=job.id, namespace=ns, url=job.url, flavor=a.flavor, dataset_repo=ds, model_repo=model_repo, done=False,
               python=sys.executable, script=os.path.abspath(__file__))
    print(f"HFJOB: {job.url}", flush=True)
    metric(hf_job=job.id, hf_url=job.url, flavor=a.flavor, dataset_repo=ds, model_repo=model_repo)
    return job.id, ns, model_repo


def network_error(e):
    return isinstance(e, (OSError, TimeoutError)) or any(
        c.__name__ in ("TransportError", "TimeoutException", "NetworkError", "ProtocolError") for c in type(e).__mro__)


# Exiting before the job ends makes the eval worker cancel it, so a network
# outage here must not end the follow: keep asking until the job answers. Its
# own timeout bounds the bill. HTTP errors other than 429/5xx are real.
def inspect(api, job_id, ns):
    from huggingface_hub.errors import HfHubHTTPError
    down = None
    while True:
        try:
            info = api.inspect_job(job_id=job_id, namespace=ns)
            if down:
                print(f"(reached Hugging Face again after {int(time.time() - down)} s)", flush=True)
            return info
        except HfHubHTTPError as e:
            code = e.response.status_code if e.response is not None else 0
            if code and code != 429 and code < 500:
                raise
            err = e
        except Exception as e:
            if not network_error(e):
                raise
            err = e
        if down is None:
            down = time.time()
            print(f"(cannot reach Hugging Face: {type(err).__name__}: {err}; the job keeps running there, retrying)", flush=True)
        time.sleep(30)


# The log stream drops now and then and replays from the start on reconnect:
# count lines so each is relayed once.
def follow(api, job_id, ns):
    seen, last = 0, None
    while True:
        info = inspect(api, job_id, ns)
        stage = stage_of(info)
        if stage != last:
            print(f"Hugging Face job {job_id}: {stage}" + (f" ({info.status.message})" if info.status.message else ""), flush=True)
            if stage == "SCHEDULING":
                phase("setup", "waiting for the GPU on Hugging Face")
            last = stage
        if stage in TERMINAL:
            break
        if stage != "RUNNING":
            time.sleep(10)
            continue
        try:
            n = 0
            for line in api.fetch_job_logs(job_id=job_id, namespace=ns, follow=True):
                n += 1
                if n > seen:
                    print(line, flush=True)
                    seen = n
        except Exception as e:
            print(f"(log stream interrupted: {e}; reconnecting)", flush=True)
            time.sleep(10)
    try:
        n = 0
        for line in api.fetch_job_logs(job_id=job_id, namespace=ns):
            n += 1
            if n > seen:
                print(line, flush=True)
    except Exception:
        pass
    return info, stage


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["submit", "cancel"])
    ap.add_argument("--state", required=True)
    ap.add_argument("--set-dir")
    ap.add_argument("--dataset-repo")
    ap.add_argument("--model-repo")
    ap.add_argument("--flavor", default="a100-large")
    ap.add_argument("--timeout", default="6h")
    ap.add_argument("--price", type=float, default=0.0, help="USD per hour of the flavor, for the cost estimate")
    ap.add_argument("--run-id", default="")
    argv = sys.argv[1:]
    cut = argv.index("--train") if "--train" in argv else len(argv)
    a = ap.parse_args(argv[:cut])
    a.train_args = argv[cut + 1:]

    os.environ.setdefault("HF_HUB_DISABLE_PROGRESS_BARS", "1")
    os.environ.setdefault("HF_HUB_DISABLE_EXPERIMENTAL_WARNING", "1")
    from huggingface_hub import HfApi
    from huggingface_hub.errors import HfHubHTTPError
    token = env_token()
    if not token:
        sys.exit("HF_TOKEN is not set (environment or InstallZero's .env)")
    api = HfApi(token=token)

    if a.cmd == "cancel":
        s = json.load(open(a.state, encoding="utf-8"))
        if not s.get("done"):
            api.cancel_job(job_id=s["id"], namespace=s["namespace"])
            save_state(a.state, done=True, cancelled=True)
            print(f"cancelled Hugging Face job {s['id']}")
        return

    try:
        job_id, ns, model_repo = submit(a, api, token)
    except HfHubHTTPError as e:
        code = e.response.status_code if e.response is not None else 0
        msg = str(e).splitlines()[-2] if len(str(e).splitlines()) > 1 else str(e)
        if "job.write" in str(e):
            msg = "the HF_TOKEN lacks the Jobs permission: edit the token at https://huggingface.co/settings/tokens and tick 'Start and manage Jobs on your behalf'"
        elif code == 402 or "credit" in str(e).lower():
            msg = "no Hugging Face credits: add some at https://huggingface.co/settings/billing"
        sys.exit(f"Hugging Face refused the job ({code}): {msg}")
    if job_id is None:
        return
    info, stage = follow(api, job_id, ns)
    secs = getattr(getattr(info, "durations", None), "running_secs", None)
    cost = round(secs / 3600 * a.price, 2) if secs and a.price else None
    save_state(a.state, done=True, stage=stage, running_secs=secs, cost_usd=cost)
    metric(hf_job=job_id, stage=stage, running_secs=secs, cost_usd=cost)
    if stage != "COMPLETED":
        sys.exit(f"Hugging Face job {job_id} ended {stage}" + (f": {info.status.message}" if info.status.message else ""))
    print(f"Hugging Face job {job_id} completed in {secs or '?'} s" + (f", about ${cost}" if cost else "") + f"; model in {model_repo}", flush=True)


if __name__ == "__main__":
    main()
