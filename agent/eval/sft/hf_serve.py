"""llama-server for a local GGUF on Hugging Face Jobs, for probes that run a
local model without the laptop GPU (model spec "hf:<file>.gguf").

  hf_serve.py ensure --model <file.gguf> --assets <dir> [--flavor h200] [--parallel 6]
      Prints the URL of a healthy server for the model, starting one if none
      runs: uploads the GGUF to a private model repo once, mounts the repo in
      the official llama.cpp server image and exposes the port behind the HF
      token. The same flags as Start-LlmServer (32K context per slot, all
      layers on the GPU, flash attention, jinja), so a probe sees the model it
      would see locally. Concurrent callers share one server per model.
  hf_serve.py stop (--model <file.gguf> | --all)
  hf_serve.py status

State: one JSON per model in --state-dir (default %LOCALAPPDATA%/installzero/hf-serve).
"""
import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from hf_job import env_token, network_error, stage_of  # noqa: E402

# llama.cpp publishes server images for some builds only; the nearest to the
# b9771 Windows build the probes run locally.
IMAGE = "ghcr.io/ggml-org/llama.cpp:server-cuda-b9765"
PORT = 8080
TERMINAL = {"COMPLETED", "CANCELED", "ERROR", "DELETED"}


def state_dir(a):
    d = a.state_dir or os.path.join(os.environ.get("LOCALAPPDATA", os.path.expanduser("~")), "installzero", "hf-serve")
    os.makedirs(d, exist_ok=True)
    return d


def state_path(a, model):
    return os.path.join(state_dir(a), model + ".json")


def load(path):
    try:
        return json.load(open(path, encoding="utf-8"))
    except (OSError, ValueError):
        return {}


class Lock:
    """One starter per model across worker processes. An OS lock, so a caller
    that dies releases it; the first upload of a model can hold it for long."""

    def __init__(self, path, wait=7200):
        self.path, self.wait, self.f = path + ".lock", wait, None

    def __enter__(self):
        self.f = open(self.path, "a+")
        t0 = time.time()
        while True:
            try:
                if os.name == "nt":
                    import msvcrt
                    self.f.seek(0)
                    msvcrt.locking(self.f.fileno(), msvcrt.LK_NBLCK, 1)
                else:
                    import fcntl
                    fcntl.flock(self.f, fcntl.LOCK_EX | fcntl.LOCK_NB)
                return self
            except OSError:
                if time.time() - t0 > self.wait:
                    sys.exit(f"timed out after {self.wait} s waiting for {self.path}")
                time.sleep(3)

    def __exit__(self, *exc):
        if os.name == "nt":
            import msvcrt
            self.f.seek(0)
            msvcrt.locking(self.f.fileno(), msvcrt.LK_UNLCK, 1)
        self.f.close()


def healthy(url, token, timeout=10):
    req = urllib.request.Request(url + "/health", headers={"Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.load(r).get("status") == "ok"
    except (urllib.error.URLError, OSError, ValueError):
        return False


def job_url(info, job_id):
    for u in (getattr(info.status, "expose_urls", None) or []):
        u = u if isinstance(u, str) else (u.get("url") if isinstance(u, dict) else getattr(u, "url", ""))
        if u and f"--{PORT}" in u:
            return u.rstrip("/")
    return f"https://{job_id}--{PORT}.hf.jobs"


def ensure(a, api, token):
    # Every probe asks: a server that answers needs no Hub API calls, and a
    # network blip there must not fail the probe.
    path = state_path(a, a.model)
    s = load(path)
    for _ in range(3):
        if s.get("url") and healthy(s["url"], token):
            print(s["url"])
            return
        if not s.get("url"):
            break
        time.sleep(10)
    for attempt in range(5):
        try:
            return start(a, api, token, path)
        except Exception as e:
            if not network_error(e) or attempt == 4:
                raise
            print(f"(network error: {e}; retrying)", file=sys.stderr, flush=True)
            time.sleep(30)


def start(a, api, token, path):
    with Lock(path):
        s = load(path)
        if s.get("id"):
            try:
                info = api.inspect_job(job_id=s["id"], namespace=s["namespace"])
                if stage_of(info) not in TERMINAL:
                    for _ in range(12):
                        if healthy(s["url"], token):
                            print(s["url"])
                            return
                        time.sleep(10)
                    api.cancel_job(job_id=s["id"], namespace=s["namespace"])
            except Exception:
                pass
        ns = api.whoami()["name"]
        repo = f"{ns}/{a.repo}"
        api.create_repo(repo, private=True, exist_ok=True)
        if a.model not in api.list_repo_files(repo):
            src = os.path.join(a.assets, a.model)
            if not os.path.exists(src):
                sys.exit(f"no {src}")
            print(f"uploading {a.model} to {repo} (once)", file=sys.stderr, flush=True)
            api.upload_file(path_or_fileobj=src, path_in_repo=a.model, repo_id=repo, commit_message=f"add {a.model}")

        from huggingface_hub import Volume
        # The job command replaces the image's entrypoint.
        args = ["/app/llama-server", "-m", f"/models/{a.model}", "--host", "0.0.0.0", "--port", str(PORT), "-np", str(a.parallel),
                "-c", str(a.ctx * a.parallel), "--no-kv-unified", "--jinja", "--no-webui", "-ngl", "999", "-fa", "on"]
        job = api.run_job(image=a.image, command=args, flavor=a.flavor, timeout=a.timeout,
                          volumes=[Volume(type="model", source=repo, mount_path="/models")], expose=[PORT],
                          labels={"app": "installzero", "serve": re.sub(r"[^A-Za-z0-9_-]", "_", a.model)[:60]}, namespace=ns, token=token)
        s = {"id": job.id, "namespace": ns, "url": f"https://{job.id}--{PORT}.hf.jobs", "model": a.model, "flavor": a.flavor,
             "price": a.price, "started_at": time.time(), "job_url": job.url}
        json.dump(s, open(path, "w", encoding="utf-8"), indent=2)
        print(f"started {job.url} ({a.flavor}) for {a.model}", file=sys.stderr, flush=True)

        t0 = time.time()
        while time.time() - t0 < a.wait:
            try:
                info = api.inspect_job(job_id=job.id, namespace=ns)
            except Exception:
                time.sleep(10)
                continue
            stage = stage_of(info)
            if stage in TERMINAL:
                sys.exit(f"server job {job.id} ended {stage}: {info.status.message or ''} (see {job.url})")
            if stage == "RUNNING":
                s["url"] = job_url(info, job.id)
                json.dump(s, open(path, "w", encoding="utf-8"), indent=2)
                if healthy(s["url"], token):
                    print(s["url"])
                    return
            time.sleep(10)
        api.cancel_job(job_id=job.id, namespace=ns)
        sys.exit(f"server for {a.model} not healthy after {a.wait} s; cancelled {job.url}")


def stop(a, api):
    names = [f[:-5] for f in os.listdir(state_dir(a)) if f.endswith(".json")] if a.all else [a.model]
    for m in names:
        path = state_path(a, m)
        s = load(path)
        if s.get("id"):
            try:
                api.cancel_job(job_id=s["id"], namespace=s["namespace"])
            except Exception as e:
                print(f"{m}: {e}", file=sys.stderr)
            hours = (time.time() - s.get("started_at", time.time())) / 3600
            print(f"stopped {m} ({s['id']}, {hours:.2f} h, about ${hours * s.get('price', 0):.2f})")
        if os.path.exists(path):
            os.unlink(path)


def status(a, api, token):
    for f in sorted(os.listdir(state_dir(a))):
        if not f.endswith(".json"):
            continue
        s = load(os.path.join(state_dir(a), f))
        try:
            stage = stage_of(api.inspect_job(job_id=s["id"], namespace=s["namespace"]))
        except Exception as e:
            stage = f"? ({e})"
        hours = (time.time() - s.get("started_at", time.time())) / 3600
        print(f"{s.get('model')}: {stage}, healthy={healthy(s['url'], token)}, {hours:.2f} h on {s.get('flavor')}, {s['url']}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["ensure", "stop", "status"])
    ap.add_argument("--model")
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--assets", default="")
    ap.add_argument("--repo", default="installzero-gguf")
    ap.add_argument("--flavor", default="h200", help="l40sx1 often waits for hardware for longer than --wait")
    ap.add_argument("--price", type=float, default=5.0, help="USD per hour of the flavor, for the cost line")
    ap.add_argument("--parallel", type=int, default=6)
    ap.add_argument("--ctx", type=int, default=32768, help="context per slot, as Start-LlmServer")
    ap.add_argument("--timeout", default="6h", help="the server stops after this even if nobody stops it")
    ap.add_argument("--wait", type=int, default=1200, help="seconds to wait for a new server to answer")
    ap.add_argument("--image", default=IMAGE)
    ap.add_argument("--state-dir", default="")
    a = ap.parse_args()
    os.environ.setdefault("HF_HUB_DISABLE_PROGRESS_BARS", "1")
    os.environ.setdefault("HF_HUB_DISABLE_EXPERIMENTAL_WARNING", "1")
    from huggingface_hub import HfApi
    token = env_token()
    if not token:
        sys.exit("HF_TOKEN is not set (environment or InstallZero's .env)")
    api = HfApi(token=token)
    if a.cmd == "ensure":
        if not a.model:
            sys.exit("--model is required")
        ensure(a, api, token)
    elif a.cmd == "stop":
        stop(a, api)
    else:
        status(a, api, token)


if __name__ == "__main__":
    main()
