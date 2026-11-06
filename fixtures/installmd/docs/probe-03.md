---
name: godot
source: https://github.com/godotengine/godot
summary: Godot 4.4.1 headless bench server on port 9820
---

# Godot 4.4.1 Headless Bench Server

Installs Godot Engine 4.4.1 (`Godot_v4.4.1-stable_linux.x86_64`) into `{{REPO_DIR}}`, links `/usr/local/bin/godot`, configures `/etc/godot-bench/server.cfg` and the project under `/srv/godot-bench/project`, and runs the headless HTTP server as a systemd service (`godot-bench.service`) listening on `http://godot.bench.lan:9820`.

## Secrets

- `ADMIN_TOKEN`: token for the `/admin` endpoint in `/etc/godot-bench/server.cfg` (generate with `openssl rand -hex 16`).

## Step 1: deps - System packages

Why: `unzip` and `curl` are required to download and extract the Godot release archive, and `libfontconfig1` is required by the Godot Linux binary at runtime.

```requires
os: linux
arch: x64
```

```detect bash
dpkg -s unzip libfontconfig1 >/dev/null 2>&1
```

```install bash
sudo apt-get update
sudo apt-get install -y curl ca-certificates unzip libfontconfig1
```

```verify bash
dpkg -s unzip libfontconfig1 >/dev/null 2>&1 && command -v unzip >/dev/null
```

## Step 2: godot-binary - Godot 4.4.1 Engine binary

Why: Downloads the official Godot 4.4.1 Linux x86_64 binary into `{{REPO_DIR}}` and symlinks `/usr/local/bin/godot`.

```detect bash
test -x "{{REPO_DIR}}/Godot_v4.4.1-stable_linux.x86_64" && test -L /usr/local/bin/godot && test -x /usr/local/bin/godot
```

```install bash
sudo mkdir -p "{{REPO_DIR}}"
tmp_zip="$(mktemp /tmp/godot.XXXXXX.zip)"
curl -fsSL "https://github.com/godotengine/godot/releases/download/4.4.1-stable/Godot_v4.4.1-stable_linux.x86_64.zip" -o "$tmp_zip"
sudo unzip -o "$tmp_zip" -d "{{REPO_DIR}}"
rm -f "$tmp_zip"
sudo chmod 0755 "{{REPO_DIR}}/Godot_v4.4.1-stable_linux.x86_64"
sudo ln -sfn "{{REPO_DIR}}/Godot_v4.4.1-stable_linux.x86_64" /usr/local/bin/godot
```

```verify bash
test -x "{{REPO_DIR}}/Godot_v4.4.1-stable_linux.x86_64" && test -L /usr/local/bin/godot && sha256sum "{{REPO_DIR}}/Godot_v4.4.1-stable_linux.x86_64" | grep -q '^54215149d52efb1d653a3dec39d0993587bdf5daa2c56e787b5ee88417fb1339'
```

## Step 3: user-and-hosts - Service user and hosts entry

Why: Creates the dedicated `gdserver` system user/group with home `/var/lib/gdserver` and maps `godot.bench.lan` in `/etc/hosts`.

```detect bash
getent passwd gdserver >/dev/null && getent group gdserver >/dev/null && test -d /var/lib/gdserver && grep -qE '^127\.0\.0\.1[[:space:]]+godot\.bench\.lan(\s|$)' /etc/hosts
```

```install bash
if ! getent group gdserver >/dev/null; then
  sudo groupadd --system gdserver
fi
if ! getent passwd gdserver >/dev/null; then
  sudo useradd --system --gid gdserver --create-home --home-dir /var/lib/gdserver --shell /usr/sbin/nologin gdserver
fi
sudo chmod 0750 /var/lib/gdserver
if ! grep -qE '^127\.0\.0\.1[[:space:]]+godot\.bench\.lan(\s|$)' /etc/hosts; then
  echo "127.0.0.1 godot.bench.lan" | sudo tee -a /etc/hosts >/dev/null
fi
```

```verify bash
getent passwd gdserver >/dev/null && test -d /var/lib/gdserver && getent hosts godot.bench.lan | grep -q '127\.0\.0\.1'
```

## Step 4: config - Server configuration

Why: Writes `/etc/godot-bench/server.cfg` with the host, port, site URL, and `ADMIN_TOKEN` readable by the `gdserver` group.

```detect bash
test -f /etc/godot-bench/server.cfg && grep -q 'port=9820' /etc/godot-bench/server.cfg && grep -q 'host="godot.bench.lan"' /etc/godot-bench/server.cfg
```

```install bash
sudo mkdir -p /etc/godot-bench
TOKEN="${ADMIN_TOKEN:-$(openssl rand -hex 16)}"
sudo tee /etc/godot-bench/server.cfg >/dev/null <<EOF
[server]
host="godot.bench.lan"
port=9820
site_url="http://godot.bench.lan:9820/"
admin_token="${TOKEN}"
EOF
sudo chown root:gdserver /etc/godot-bench/server.cfg
sudo chmod 0640 /etc/godot-bench/server.cfg
```

```verify bash
test -f /etc/godot-bench/server.cfg && grep -q 'host="godot.bench.lan"' /etc/godot-bench/server.cfg && grep -q 'port=9820' /etc/godot-bench/server.cfg
```

## Step 5: project - Godot bench project files

Why: Creates the headless HTTP server Godot project in `/srv/godot-bench/project` and initializes its `.godot` metadata cache.

```detect bash
test -f /srv/godot-bench/project/project.godot && test -f /srv/godot-bench/project/main.tscn && test -f /srv/godot-bench/project/server.gd && test -d /srv/godot-bench/project/.godot
```

```install bash
sudo mkdir -p /srv/godot-bench/project

sudo tee /srv/godot-bench/project/project.godot >/dev/null <<'EOF'
config_version=5

[application]

config/name="Godot Bench Server"
run/main_scene="res://main.tscn"
run/max_fps=30
run/low_processor_mode=true
config/features=PackedStringArray("4.4")
EOF

sudo tee /srv/godot-bench/project/main.tscn >/dev/null <<'EOF'
[gd_scene load_steps=2 format=3]

[ext_resource type="Script" path="res://server.gd" id="1"]

[node name="Server" type="Node"]
script = ExtResource("1")
EOF

sudo tee /srv/godot-bench/project/server.gd >/dev/null <<'EOF'
extends Node

var server = TCPServer.new()
var clients = []
var port = 9820
var host = "localhost"
var admin_token = ""

func _ready():
    var cfg = ConfigFile.new()
    if cfg.load("/etc/godot-bench/server.cfg") == OK:
        port = int(cfg.get_value("server", "port", 9820))
        host = str(cfg.get_value("server", "host", "localhost"))
        admin_token = str(cfg.get_value("server", "admin_token", ""))
    var err = server.listen(port, "*")
    if err != OK:
        push_error("listen failed: %s" % err)
        get_tree().quit(1)
        return
    print("godot-bench listening on %s:%d" % [host, port])

func _process(_delta):
    while server.is_connection_available():
        clients.append({"peer": server.take_connection(), "buf": ""})
    for cl in clients.duplicate():
        var peer = cl["peer"]
        peer.poll()
        if peer.get_status() != StreamPeerTCP.STATUS_CONNECTED:
            clients.erase(cl)
            continue
        var n = peer.get_available_bytes()
        if n > 0:
            var res = peer.get_data(n)
            cl["buf"] += res[1].get_string_from_utf8()
        if cl["buf"].find("\r\n\r\n") != -1:
            _respond(peer, cl["buf"])
            peer.disconnect_from_host()
            clients.erase(cl)

func _respond(peer, req):
    var lines = req.split("\r\n")
    var parts = lines[0].split(" ")
    var path = parts[1] if parts.size() > 1 else "/"
    var token = ""
    for l in lines:
        if l.to_lower().begins_with("x-admin-token:"):
            token = l.substr(14).strip_edges()
    var code = "200 OK"
    var ctype = "application/json"
    var body = ""
    if path == "/status":
        body = JSON.stringify({"engine": "godot", "version": Engine.get_version_info()["string"], "host": host, "port": port, "max_fps": Engine.max_fps})
    elif path == "/admin":
        if admin_token != "" and token == admin_token:
            body = JSON.stringify({"admin": true, "uptime_ms": Time.get_ticks_msec()})
        else:
            code = "401 Unauthorized"
            body = JSON.stringify({"error": "missing or bad X-Admin-Token"})
    elif path == "/":
        ctype = "text/html"
        body = "<html><body><h1>Godot bench server</h1><p>Godot %s headless on %s</p></body></html>" % [Engine.get_version_info()["string"], host]
    else:
        code = "404 Not Found"
        body = JSON.stringify({"error": "not found"})
    var data = body.to_utf8_buffer()
    var head = "HTTP/1.1 %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n" % [code, ctype, data.size()]
    peer.put_data(head.to_utf8_buffer())
    peer.put_data(data)
EOF

sudo chown -R gdserver:gdserver /srv/godot-bench
sudo -u gdserver env HOME=/var/lib/gdserver /usr/local/bin/godot --headless --import --path /srv/godot-bench/project
```

```verify bash
test -f /srv/godot-bench/project/project.godot && test -f /srv/godot-bench/project/main.tscn && test -f /srv/godot-bench/project/server.gd && test -d /srv/godot-bench/project/.godot
```

## Step 6: service - Systemd service and HTTP verification

Why: Installs and starts `godot-bench.service` and verifies that the server responds on `http://godot.bench.lan:9820/status`.

```detect bash
systemctl is-active --quiet godot-bench.service && curl -fsS http://godot.bench.lan:9820/status | grep -q '"engine":"godot"'
```

```install bash
sudo tee /etc/systemd/system/godot-bench.service >/dev/null <<'EOF'
[Unit]
Description=Godot headless bench server
After=network-online.target
Wants=network-online.target

[Service]
User=gdserver
Group=gdserver
Environment=HOME=/var/lib/gdserver
WorkingDirectory=/srv/godot-bench/project
ExecStart=/usr/local/bin/godot --headless --path /srv/godot-bench/project
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now godot-bench.service
for i in $(seq 1 15); do
  if curl -fsS http://godot.bench.lan:9820/status >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
```

```verify bash
systemctl is-enabled --quiet godot-bench.service && systemctl is-active --quiet godot-bench.service && curl -fsS http://godot.bench.lan:9820/status | grep -q '"version":"4.4.1-stable (official)"'
```
