---
name: cellprofiler
source: https://hub.docker.com/r/cellprofiler/cellprofiler
summary: Headless CellProfiler 4.2.8 nightly batch (Docker, systemd timer) with an nginx results browser on port 9900
---

# CellProfiler headless batch + results site

What this installs:

- Docker Engine 29.9.2 with the Compose plugin from download.docker.com.
- The `cellprofiler/cellprofiler:4.2.8` image, run headless by `/usr/local/bin/cellprofiler-batch`
  (config in `/etc/cellprofiler/batch.env`), triggered nightly at 02:30 by `cellprofiler-batch.timer`.
- Data tree `/srv/cellprofiler/{pipelines,input,output,plugins}` with the CellProfiler
  `ExampleHuman` pipeline and its three example images.
- A compose project `cellprofiler` in `{{INSTALL_DIR}}` running `nginx:1.27-alpine` on port 9900,
  serving `index.html` and an autoindex of `/srv/cellprofiler/output` at `/output/`.
- `/etc/hosts` entry `127.0.0.1 cellprofiler.bench.lan`.

Afterwards: browse http://localhost:9900/ (or http://cellprofiler.bench.lan:9900/). Run a batch
on demand with `sudo systemctl start cellprofiler-batch.service`; each run lands in
`/srv/cellprofiler/output/<timestamp>` and `latest` points to the newest one.

## Step 1: docker - Docker Engine and Compose plugin

Why: both the CellProfiler batch and the results web server run in containers.

```requires
os: linux
arch: x64
min_disk_gb: 10
```

```detect bash
docker --version && docker compose version && systemctl is-active docker
```

```install bash
set -e
sudo apt-get update
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

```verify bash
docker --version && docker compose version && systemctl is-active docker
```

## Step 2: images - Pull CellProfiler and nginx images

Why: the batch uses cellprofiler/cellprofiler:4.2.8 (about 5 GB); the results site uses nginx:1.27-alpine.

```detect bash
docker image inspect cellprofiler/cellprofiler:4.2.8 >/dev/null && docker image inspect nginx:1.27-alpine >/dev/null
```

```install bash
sudo docker pull cellprofiler/cellprofiler:4.2.8
sudo docker pull nginx:1.27-alpine
```

```verify bash
docker image inspect cellprofiler/cellprofiler:4.2.8 >/dev/null && docker image inspect nginx:1.27-alpine >/dev/null
```

## Step 3: data - Data tree with the ExampleHuman pipeline and images

Why: the batch reads the pipeline and input images from /srv/cellprofiler and writes results to its output folder (world-writable, since the container user is not root).

```detect bash
test -s /srv/cellprofiler/pipelines/ExampleHuman.cppipe && test -d /srv/cellprofiler/plugins && test -d /srv/cellprofiler/output && [ "$(ls /srv/cellprofiler/input/ExampleHuman/*.tif | wc -l)" -ge 3 ]
```

```install bash
set -e
sudo mkdir -p /srv/cellprofiler/pipelines /srv/cellprofiler/input/ExampleHuman /srv/cellprofiler/plugins /srv/cellprofiler/output
sudo chmod 0777 /srv/cellprofiler/output
base=https://raw.githubusercontent.com/CellProfiler/examples/master/ExampleHuman
sudo curl -fsSL "$base/ExampleHuman.cppipe" -o /srv/cellprofiler/pipelines/ExampleHuman.cppipe
for i in 0 1 2; do
  f=AS_09125_050116030001_D03f00d$i.tif
  sudo curl -fsSL "$base/images/$f" -o /srv/cellprofiler/input/ExampleHuman/$f
done
```

```verify bash
head -1 /srv/cellprofiler/pipelines/ExampleHuman.cppipe | grep -q 'CellProfiler Pipeline' && [ "$(ls /srv/cellprofiler/input/ExampleHuman/*.tif | wc -l)" -ge 3 ]
```

## Step 4: batch - Batch runner script, config and nightly timer

Why: runs the pipeline headless in the CellProfiler container every night at 02:30 and updates output/latest.

```detect bash
test -x /usr/local/bin/cellprofiler-batch && grep -q 'CP_IMAGE=cellprofiler/cellprofiler:4.2.8' /etc/cellprofiler/batch.env && systemctl is-enabled cellprofiler-batch.timer
```

```install bash
set -e
sudo mkdir -p /etc/cellprofiler
sudo tee /etc/cellprofiler/batch.env >/dev/null <<'EOF'
CP_IMAGE=cellprofiler/cellprofiler:4.2.8
CP_DATA=/srv/cellprofiler
CP_PIPELINE=/data/pipelines/ExampleHuman.cppipe
CP_INPUT=/data/input/ExampleHuman
CP_LOG_LEVEL=WARNING
EOF
sudo tee /usr/local/bin/cellprofiler-batch >/dev/null <<'EOF'
#!/bin/bash
set -euo pipefail
. /etc/cellprofiler/batch.env
run=$(date +%Y%m%d-%H%M%S)
mkdir -p "$CP_DATA/output/$run"
chmod 0777 "$CP_DATA/output/$run"
docker run --rm --name cellprofiler-batch \
  -v "$CP_DATA:/data" \
  --entrypoint cellprofiler \
  "$CP_IMAGE" \
  -c -r -p "$CP_PIPELINE" -i "$CP_INPUT" -o "/data/output/$run" \
  --plugins-directory /data/plugins -L "$CP_LOG_LEVEL"
ln -sfn "$run" "$CP_DATA/output/latest"
EOF
sudo chmod 0755 /usr/local/bin/cellprofiler-batch
sudo tee /etc/systemd/system/cellprofiler-batch.service >/dev/null <<'EOF'
[Unit]
Description=CellProfiler headless batch run (ExampleHuman pipeline)
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cellprofiler-batch
EOF
sudo tee /etc/systemd/system/cellprofiler-batch.timer >/dev/null <<'EOF'
[Unit]
Description=Nightly CellProfiler batch run

[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=false

[Install]
WantedBy=timers.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now cellprofiler-batch.timer
```

```verify bash
systemctl is-enabled cellprofiler-batch.timer && systemctl is-active cellprofiler-batch.timer && test -x /usr/local/bin/cellprofiler-batch
```

## Step 5: first-run - Run the batch once

Why: produces the first results run so the results site has something to show (takes a few minutes).

```detect bash
test -s /srv/cellprofiler/output/latest/Image.csv
```

```install bash
sudo systemctl start cellprofiler-batch.service
```

```verify bash
test -s /srv/cellprofiler/output/latest/Image.csv && test -s /srv/cellprofiler/output/latest/Nuclei.csv
```

## Step 6: hosts - Host name cellprofiler.bench.lan

Why: nginx answers for server_name cellprofiler.bench.lan; this maps it to the local machine.

```detect bash
grep -Eq '^127\.0\.0\.1[[:space:]]+cellprofiler\.bench\.lan' /etc/hosts
```

```install bash
echo '127.0.0.1 cellprofiler.bench.lan' | sudo tee -a /etc/hosts >/dev/null
```

```verify bash
getent hosts cellprofiler.bench.lan | grep -q 127.0.0.1
```

## Step 7: results-site - nginx results site (compose project)

Why: serves the batch output on port 9900.

```detect bash
docker compose ls | grep -Eq '^cellprofiler[[:space:]]+running'
```

```install bash
set -e
mkdir -p "{{INSTALL_DIR}}"
cd "{{INSTALL_DIR}}"
cat > docker-compose.yml <<'EOF'
name: cellprofiler
services:
  results:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "9900:80"
    volumes:
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
      - ./index.html:/usr/share/nginx/html/index.html:ro
      - /srv/cellprofiler/output:/srv/output:ro
EOF
cat > nginx.conf <<'EOF'
server {
    listen 80;
    server_name cellprofiler.bench.lan localhost;
    root /usr/share/nginx/html;
    location /output/ {
        alias /srv/output/;
        autoindex on;
    }
}
EOF
cat > index.html <<'EOF'
<html><head><title>CellProfiler results - cellprofiler.bench.lan</title></head>
<body><h1>CellProfiler headless batch results</h1>
<p>Pipeline: ExampleHuman. <a href="/output/">Output runs</a></p></body></html>
EOF
sudo docker compose up -d
```

```verify bash
docker compose ls | grep -Eq '^cellprofiler[[:space:]]+running'
```

## Step 8: serve - Results site answers on port 9900

Why: final check that the results browser serves the batch output.

```detect bash
curl -fsS http://localhost:9900/output/ | grep -q 'latest/'
```

```install bash
cd "{{INSTALL_DIR}}" && sudo docker compose up -d
```

```verify bash
curl -fsS http://localhost:9900/ | grep -q 'CellProfiler headless batch results' && curl -fsS http://localhost:9900/output/ | grep -q 'latest/'
```
