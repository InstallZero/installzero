---
name: chatchat
source: https://pypi.org/project/langchain-chatchat/0.3.1.3/
summary: Langchain-Chatchat 0.3.1.3 RAG chat (API on 9231, Streamlit WebUI on 9230) backed by local Ollama 0.5.7 (qwen2.5:0.5b + nomic-embed-text)
---

# Langchain-Chatchat with Ollama

What this installs:

- **Ollama 0.5.7** at `/usr/local/bin/ollama`, run by the `ollama.service` systemd unit (user `ollama`, listens on 127.0.0.1:11434). It uses the NVIDIA GPU through its bundled CUDA libraries.
- Ollama models: `qwen2.5:0.5b` (chat LLM) and `nomic-embed-text` (embeddings).
- **uv 0.5.14** at `/usr/local/bin/uv`. uv provides a managed CPython 3.11.11 under `{{INSTALL_DIR}}/python` and a venv at `{{INSTALL_DIR}}/venv`.
- **langchain-chatchat 0.3.1.3** from PyPI, installed into that venv.
- Chatchat data root (`CHATCHAT_ROOT`) at `{{REPO_DIR}}`. It holds the YAML settings and `data/` (knowledge base, logs, media, temp).
- Settings changed from the defaults: the API listens on 0.0.0.0:9231 and the WebUI on 0.0.0.0:9230. public_host is `langchain-chatchat.bench.lan`, which has an /etc/hosts entry. The only model platform is Ollama, with LLM `qwen2.5:0.5b` and embedding model `nomic-embed-text`. The `samples` knowledge base is indexed with FAISS.
- `chatchat.service` systemd unit (user `chatchat`) that runs `chatchat start -a`.

After the install, open the WebUI at http://<host>:9230 and the API docs at http://<host>:9231/docs.

No secrets are needed. The Ollama platform's `api_key` is a dummy value; it was masked on the source machine, so `EMPTY` is used here.

## Step 1: apt-prereqs - System packages

Why: the build tools, libGL for opencv/rapidocr, zstd, and curl, all seen on the source machine.

```requires
os: linux
arch: x64
min_ram_gb: 8
min_disk_gb: 15
```

```detect bash
dpkg -s build-essential libgl1 zstd curl >/dev/null 2>&1
```

```install bash
sudo apt-get update
sudo apt-get install -y build-essential libgl1 zstd curl ca-certificates
```

```verify bash
dpkg -s build-essential libgl1 zstd curl >/dev/null 2>&1
```

## Step 2: ollama - Ollama 0.5.7 service

Why: Chatchat uses Ollama (http://127.0.0.1:11434/v1) for both the LLM and the embeddings.

```detect bash
test -x /usr/local/bin/ollama && systemctl is-enabled ollama >/dev/null 2>&1 && curl -fsS http://127.0.0.1:11434/api/version | grep -q '"0.5.7"'
```

```install bash
curl -fsSL https://ollama.com/install.sh | OLLAMA_VERSION=0.5.7 sh
sudo systemctl enable --now ollama
for i in $(seq 1 30); do curl -fsS http://127.0.0.1:11434/api/version && break; sleep 2; done
```

```verify bash
curl -fsS http://127.0.0.1:11434/api/version | grep -q '"0.5.7"'
```

## Step 3: ollama-models - Pull qwen2.5:0.5b and nomic-embed-text

Why: these are the default LLM and embedding model set in model_settings.yaml.

```detect bash
tags=$(curl -fsS http://127.0.0.1:11434/api/tags) && echo "$tags" | grep -q '"qwen2.5:0.5b"' && echo "$tags" | grep -q '"nomic-embed-text:latest"'
```

```install bash
ollama pull qwen2.5:0.5b
ollama pull nomic-embed-text
```

```verify bash
tags=$(curl -fsS http://127.0.0.1:11434/api/tags) && echo "$tags" | grep -q '"qwen2.5:0.5b"' && echo "$tags" | grep -q '"nomic-embed-text:latest"'
```

## Step 4: uv - uv 0.5.14

Why: uv created the Python 3.11.11 venv that Chatchat runs from.

```detect bash
test -x /usr/local/bin/uv && /usr/local/bin/uv --version | grep -q '0.5.14'
```

```install bash
curl -LsSf https://astral.sh/uv/0.5.14/install.sh | sudo env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
```

```verify bash
/usr/local/bin/uv --version | grep -q '0.5.14'
```

## Step 5: chatchat-user - Service user and folders

Why: chatchat.service runs as the system user `chatchat` (home /var/lib/chatchat, no login shell).

```detect bash
id chatchat >/dev/null 2>&1 && test -d "{{INSTALL_DIR}}" && test -d "{{REPO_DIR}}"
```

```install bash
id chatchat >/dev/null 2>&1 || sudo useradd --system --create-home --home-dir /var/lib/chatchat --shell /usr/sbin/nologin chatchat
sudo mkdir -p "{{INSTALL_DIR}}" "{{REPO_DIR}}"
sudo chown chatchat:chatchat "{{INSTALL_DIR}}" "{{REPO_DIR}}"
```

```verify bash
id chatchat >/dev/null 2>&1 && test -d "{{INSTALL_DIR}}" && test -d "{{REPO_DIR}}"
```

## Step 6: venv - Python 3.11.11 venv with langchain-chatchat 0.3.1.3

Why: the `chatchat` CLI (API server and Streamlit WebUI) comes from the PyPI package langchain-chatchat==0.3.1.3.

```detect bash
test -x "{{INSTALL_DIR}}/venv/bin/chatchat" && ls "{{INSTALL_DIR}}"/venv/lib/python3.11/site-packages | grep -q '^langchain_chatchat-0.3.1.3.dist-info$'
```

```install bash
cd "{{INSTALL_DIR}}"
sudo -u chatchat env UV_PYTHON_INSTALL_DIR="{{INSTALL_DIR}}/python" UV_CACHE_DIR=/var/lib/chatchat/.cache/uv \
  /usr/local/bin/uv python install 3.11.11
sudo -u chatchat env UV_PYTHON_INSTALL_DIR="{{INSTALL_DIR}}/python" UV_CACHE_DIR=/var/lib/chatchat/.cache/uv \
  /usr/local/bin/uv venv --python 3.11.11 "{{INSTALL_DIR}}/venv"
sudo -u chatchat env UV_CACHE_DIR=/var/lib/chatchat/.cache/uv VIRTUAL_ENV="{{INSTALL_DIR}}/venv" \
  /usr/local/bin/uv pip install --python "{{INSTALL_DIR}}/venv/bin/python" "langchain-chatchat==0.3.1.3"
```

```verify bash
"{{INSTALL_DIR}}/venv/bin/python" --version | grep -q '3.11.11' && test -x "{{INSTALL_DIR}}/venv/bin/chatchat" && ls "{{INSTALL_DIR}}"/venv/lib/python3.11/site-packages | grep -q '^langchain_chatchat-0.3.1.3.dist-info$'
```

## Step 7: chatchat-config - Init the data root and apply settings

Why: `chatchat init` writes the YAML settings and data/ (with the samples KB) into CHATCHAT_ROOT. This step then sets the ports, public host and Ollama platform seen on the source machine.

```detect bash
grep -q 'port: 9231' "{{REPO_DIR}}/basic_settings.yaml" && grep -q 'port: 9230' "{{REPO_DIR}}/basic_settings.yaml" && grep -q 'DEFAULT_LLM_MODEL: qwen2.5:0.5b' "{{REPO_DIR}}/model_settings.yaml" && grep -q 'platform_type: ollama' "{{REPO_DIR}}/model_settings.yaml"
```

```install bash
cd "{{REPO_DIR}}"
sudo -u chatchat env CHATCHAT_ROOT="{{REPO_DIR}}" "{{INSTALL_DIR}}/venv/bin/chatchat" init -l qwen2.5:0.5b -e nomic-embed-text || \
  sudo -u chatchat env CHATCHAT_ROOT="{{REPO_DIR}}" "{{INSTALL_DIR}}/venv/bin/chatchat" init
sudo -u chatchat env CHATCHAT_ROOT="{{REPO_DIR}}" "{{INSTALL_DIR}}/venv/bin/python" - <<'PY'
import os, yaml
root = os.environ["CHATCHAT_ROOT"]
def load(n):
    with open(os.path.join(root, n), encoding="utf-8") as f: return yaml.safe_load(f) or {}
def save(n, d):
    with open(os.path.join(root, n), "w", encoding="utf-8") as f: yaml.safe_dump(d, f, allow_unicode=True, sort_keys=False)
b = load("basic_settings.yaml")
b["DEFAULT_BIND_HOST"] = "0.0.0.0"
b["API_SERVER"] = {"host": "0.0.0.0", "port": 9231, "public_host": "langchain-chatchat.bench.lan", "public_port": 9231}
b["WEBUI_SERVER"] = {"host": "0.0.0.0", "port": 9230}
save("basic_settings.yaml", b)
m = load("model_settings.yaml")
m["DEFAULT_LLM_MODEL"] = "qwen2.5:0.5b"
m["DEFAULT_EMBEDDING_MODEL"] = "nomic-embed-text"
m["MODEL_PLATFORMS"] = [{
    "platform_name": "ollama", "platform_type": "ollama",
    "api_base_url": "http://127.0.0.1:11434/v1", "api_key": "EMPTY", "api_proxy": "",
    "api_concurrencies": 5, "auto_detect_model": False,
    "llm_models": ["qwen2.5:0.5b"], "embed_models": ["nomic-embed-text"],
    "text2image_models": [], "image2text_models": [], "rerank_models": [],
    "speech2text_models": [], "text2speech_models": []}]
save("model_settings.yaml", m)
k = load("kb_settings.yaml")
k["DEFAULT_KNOWLEDGE_BASE"] = "samples"; k["DEFAULT_VS_TYPE"] = "faiss"
save("kb_settings.yaml", k)
PY
```

```verify bash
grep -q 'port: 9231' "{{REPO_DIR}}/basic_settings.yaml" && grep -q 'port: 9230' "{{REPO_DIR}}/basic_settings.yaml" && grep -q 'DEFAULT_LLM_MODEL: qwen2.5:0.5b' "{{REPO_DIR}}/model_settings.yaml" && grep -q 'platform_type: ollama' "{{REPO_DIR}}/model_settings.yaml" && test -d "{{REPO_DIR}}/data/knowledge_base/samples/content"
```

## Step 8: hosts - public_host entry

Why: the API public_host `langchain-chatchat.bench.lan` resolves to 127.0.0.1 through /etc/hosts.

```detect bash
grep -qE '^127\.0\.0\.1[[:space:]]+langchain-chatchat\.bench\.lan' /etc/hosts
```

```install bash
echo '127.0.0.1 langchain-chatchat.bench.lan' | sudo tee -a /etc/hosts >/dev/null
```

```verify bash
grep -qE '^127\.0\.0\.1[[:space:]]+langchain-chatchat\.bench\.lan' /etc/hosts
```

## Step 9: knowledge-base - Index the samples KB with nomic-embed-text

Why: the samples knowledge base has a FAISS vector store under vector_store/nomic-embed-text and is recorded in info.db.

```detect bash
test -f "{{REPO_DIR}}/data/knowledge_base/samples/vector_store/nomic-embed-text/index.faiss" && test -f "{{REPO_DIR}}/data/knowledge_base/info.db"
```

```install bash
cd "{{REPO_DIR}}"
sudo -u chatchat env CHATCHAT_ROOT="{{REPO_DIR}}" "{{INSTALL_DIR}}/venv/bin/chatchat" kb -r
```

```verify bash
test -f "{{REPO_DIR}}/data/knowledge_base/samples/vector_store/nomic-embed-text/index.faiss" && test -f "{{REPO_DIR}}/data/knowledge_base/info.db"
```

## Step 10: service - chatchat.service

Why: starts the API (9231) and WebUI (9230) at boot, after Ollama.

```detect bash
systemctl is-enabled chatchat >/dev/null 2>&1 && systemctl is-active chatchat >/dev/null 2>&1
```

```install bash
sudo tee /etc/systemd/system/chatchat.service >/dev/null <<EOF
[Unit]
Description=Langchain-Chatchat (API 9231 + Streamlit WebUI 9230)
After=network-online.target ollama.service
Wants=network-online.target ollama.service

[Service]
Type=simple
User=chatchat
Group=chatchat
WorkingDirectory={{REPO_DIR}}
Environment=CHATCHAT_ROOT={{REPO_DIR}}
Environment=STREAMLIT_BROWSER_GATHER_USAGE_STATS=false
Environment=STREAMLIT_SERVER_HEADLESS=true
ExecStart={{INSTALL_DIR}}/venv/bin/chatchat start -a
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now chatchat
```

```verify bash
systemctl is-active chatchat >/dev/null 2>&1
```

## Step 11: serve-check - API and WebUI respond

Why: shows that the install serves on its ports.

```detect bash
curl -fsS http://127.0.0.1:9231/docs | grep -qi swagger && curl -fsS http://127.0.0.1:9230/ | grep -qi '<html'
```

```install bash
for i in $(seq 1 60); do
  curl -fsS http://127.0.0.1:9231/docs >/dev/null 2>&1 && curl -fsS http://127.0.0.1:9230/ >/dev/null 2>&1 && break
  sleep 3
done
```

```verify bash
curl -fsS http://127.0.0.1:9231/docs | grep -qi swagger && curl -fsS http://127.0.0.1:9230/ | grep -qi '<html'
```
