# llama.cpp built from source with CMake (CPU), a small GGUF model, and
# llama-server as a systemd service on 8097 with an API key.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
LLAMA_TAG=v0.7.0
apt-get update -q
apt-get install -y -q build-essential cmake git libcurl4-openssl-dev curl openssl
git clone --depth 1 --branch "$LLAMA_TAG" https://github.com/ggml-org/llama.cpp /opt/llama.cpp
cmake -S /opt/llama.cpp -B /opt/llama.cpp/build -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF
cmake --build /opt/llama.cpp/build --config Release -j 16 --target llama-server llama-cli

mkdir -p /srv/models
curl -fsSL -o /srv/models/qwen2.5-0.5b-instruct-q4_k_m.gguf https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf
adduser --system --group --home /srv/models llama
chown -R llama:llama /srv/models

echo "LLAMA_API_KEY=$(openssl rand -hex 16)" > /etc/default/llama-server
chmod 640 /etc/default/llama-server; chgrp llama /etc/default/llama-server
cat > /etc/systemd/system/llama-server.service <<'EOF'
[Unit]
Description=llama.cpp server
After=network.target

[Service]
User=llama
Group=llama
EnvironmentFile=/etc/default/llama-server
ExecStart=/opt/llama.cpp/build/bin/llama-server -m /srv/models/qwen2.5-0.5b-instruct-q4_k_m.gguf --alias bench-qwen --host 127.0.0.1 --port 8097 -c 4096 -t 8 --api-key ${LLAMA_API_KEY}
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now llama-server
