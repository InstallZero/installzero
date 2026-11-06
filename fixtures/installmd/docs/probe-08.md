---
name: sunshine
source: https://github.com/LizardByte/Sunshine
summary: Sunshine v2025.122.141614 game stream host, web UI on port 9580
---

# Sunshine install

LizardByte Sunshine v2025.122.141614 is a self-hosted GameStream host for Moonlight. It is the Ubuntu 24.04 amd64 `.deb` from GitHub release `v2025.122.141614`, not a package from the Ubuntu archive (it is recorded only in the dpkg status database). A custom system unit runs it as user `streamer`, capturing a headless Xvfb display `:0`.

The service home and config directory is `{{REPO_DIR}}` (this host: `/srv/sunshine`). `{{INSTALL_DIR}}` is the parent of that directory.

What is running:

- `xvfb.service`: `/usr/bin/Xvfb :0 -screen 0 1920x1080x24 -nolisten tcp -ac`
- `sunshine.service`: `/usr/bin/sunshine {{REPO_DIR}}/sunshine.conf` with `HOME={{REPO_DIR}}` and `DISPLAY=:0`
- GameStream HTTP on TCP **9579** (`port` in the config; not the Sunshine default 47989)
- Web configuration UI at **https://localhost:9580** (HTTP basic auth, user `benchadmin`)
- HTTPS GameStream on TCP 9574 (`port - 5`) and RTSP on TCP 9600 (`port + 21`)

Encoding is software libx264 (`encoder = software`, `sw_preset = superfast`, `capture = x11`). An NVIDIA GPU is not required and is not installed in the guest. This machine can see an RTX 4090 through WSL, but Sunshine does not use NVENC and `nvcc` is not installed.

`/dev/uinput` on this host is `root:root` mode 0600. The packaged udev rule only sets `TAG+=uaccess`, which does not grant `streamer` access without a logged-in seat, so the log reports permission denied for the virtual mouse, keyboard, and gamepads. No extra udev rule was installed. The web UI and GameStream ports still serve.

`apps.json` is the file shipped in the package (Desktop, Low Res Desktop, Steam Big Picture). Steam is not installed. The Low Res Desktop prep-cmd calls `xrandr` on `HDMI-1`, which does not exist on this Xvfb screen; `x11-xserver-utils` is installed because that command is in the packaged apps file.

After install, open `https://<host>:9580` and sign in as `benchadmin`. Point Moonlight at port 9579.

`SUNSHINE_PASSWORD`: web UI password for user `benchadmin`. Choose it and export it before the credentials step. Do not write the value into this file. On the observed host it is stored only in `/root/sunshine-admin.txt` (mode 600, `user=benchadmin` and `password=`).

The packaged user unit `/usr/lib/systemd/user/sunshine.service` is not used. Do not enable it. The system units below pass the config path and wait for Xvfb.

## Step 1: packages - Xvfb, xrandr, and a session bus

Why: Sunshine captures X11. This host runs Xvfb on display :0 at 1920x1080x24. `x11-xserver-utils` provides `xrandr`, which the packaged apps.json calls. Sunshine also starts `dbus-launch` (from `dbus-x11`) for a session bus and the system tray. On this host `dbus-x11` 1.14.10-4ubuntu4 was already present via the `ubuntu-wsl` image and was not upgraded; the install only ensures `dbus-launch` exists. Xvfb and x11-xserver-utils were installed explicitly at the versions below (`apt-mark` manual). Xvfb is in Ubuntu universe, which this host's archive sources already enable.

```requires
os: linux
arch: x64
min_ram_gb: 1
min_disk_gb: 2
```

```detect bash
grep -qx 'ID=ubuntu' /etc/os-release && grep -qx 'VERSION_ID="24.04"' /etc/os-release && dpkg --print-architecture | grep -qx amd64 && dpkg -s xvfb | grep -q 'Version: 2:21.1.12-1ubuntu1.9' && dpkg -s x11-xserver-utils | grep -q 'Version: 7.7+10build2' && test -x /usr/bin/Xvfb && test -x /usr/bin/xrandr && test -x /usr/bin/dbus-launch
```

```install bash os=linux arch=x64
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
if [ "${ID:-}" != ubuntu ] || [ "${VERSION_ID:-}" != 24.04 ]; then
  echo "These packages were observed on Ubuntu 24.04, found ${ID:-unknown} ${VERSION_ID:-unknown}" >&2
  exit 1
fi
apt-get update
apt-get install -y \
  'xvfb=2:21.1.12-1ubuntu1.9' \
  'x11-xserver-utils=7.7+10build2' \
  dbus-x11 \
  ca-certificates \
  curl
```

```verify bash
dpkg -s xvfb | grep -q 'Version: 2:21.1.12-1ubuntu1.9' && dpkg -s x11-xserver-utils | grep -q 'Version: 7.7+10build2' && test -x /usr/bin/Xvfb && test -x /usr/bin/xrandr && test -x /usr/bin/dbus-launch
```

## Step 2: sunshine - Sunshine 2025.122.141614 deb

Why: The running binary is `/usr/bin/sunshine` -> `sunshine-v2025.122.141614`, package version `2025.122.141614`, publisher LizardByte. It is not in the Ubuntu apt sources. The matching release asset is `sunshine-ubuntu-24.04-amd64.deb` (published 2025-01-22, same date as the binary). The package postinst runs `setcap cap_sys_admin+p` on the real binary and reloads the packaged udev rule `/usr/lib/udev/rules.d/60-sunshine.rules`. The deb is not left on disk after install. `libcap2-bin` (for `setcap`) is already on Ubuntu via `iproute2`; the explicit `setcap` repeats the postinst so the capability is present even if postinst skipped it.

```detect bash
dpkg -s sunshine | grep -q 'Version: 2025.122.141614' && test -x /usr/bin/sunshine && readlink /usr/bin/sunshine | grep -qx 'sunshine-v2025.122.141614' && test -f /usr/lib/udev/rules.d/60-sunshine.rules && test -f /usr/share/sunshine/apps.json && test -x /usr/sbin/setcap
```

```install bash os=linux arch=x64
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
if [ "${ID:-}" != ubuntu ] || [ "${VERSION_ID:-}" != 24.04 ]; then
  echo "sunshine-ubuntu-24.04-amd64.deb requires Ubuntu 24.04" >&2
  exit 1
fi
dpkg --print-architecture | grep -qx amd64
apt-get update
apt-get install -y ca-certificates curl libcap2-bin
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
curl -fsSL -o "$tmpdir/sunshine-ubuntu-24.04-amd64.deb" \
  https://github.com/LizardByte/Sunshine/releases/download/v2025.122.141614/sunshine-ubuntu-24.04-amd64.deb
apt-get install -y "$tmpdir/sunshine-ubuntu-24.04-amd64.deb"
/usr/sbin/setcap cap_sys_admin+p "$(readlink -f /usr/bin/sunshine)"
```

```verify bash
dpkg -s sunshine | grep -q 'Version: 2025.122.141614' && dpkg -s sunshine | grep -q 'Homepage: https://app.lizardbyte.dev/Sunshine' && test -x /usr/bin/sunshine && readlink /usr/bin/sunshine | grep -qx 'sunshine-v2025.122.141614' && test -f /usr/lib/udev/rules.d/60-sunshine.rules && test -f /usr/lib/systemd/user/sunshine.service && test -f /usr/share/sunshine/apps.json
```

## Step 3: user - streamer service account

Why: The units run as `streamer`, a system account with home `{{REPO_DIR}}`, shell `/usr/sbin/nologin`, and an empty GECOS field. Supplementary groups are `video`, `input`, and `render` (standard Sunshine groups for DRM, uinput, and render nodes). On this Ubuntu image `useradd --system` assigned uid 999 and gid 989 because those were the next free system IDs; do not pin them. Home mode 0750 comes from `HOME_MODE` in `/etc/login.defs`. The account password is locked.

```detect bash
getent passwd streamer | grep -q '{{REPO_DIR}}:/usr/sbin/nologin$' && id streamer | grep -q video && id streamer | grep -q input && id streamer | grep -q render && test -d {{REPO_DIR}} && test "$(stat -c %U:%G {{REPO_DIR}})" = streamer:streamer
```

```install bash os=linux
set -euo pipefail
getent group video
getent group input
getent group render
mkdir -p "{{INSTALL_DIR}}"
if ! getent passwd streamer >/dev/null; then
  useradd --system --create-home \
    --home-dir "{{REPO_DIR}}" \
    --shell /usr/sbin/nologin \
    --groups video,input,render \
    streamer
else
  usermod -d "{{REPO_DIR}}" -s /usr/sbin/nologin streamer
  usermod -aG video,input,render streamer
fi
```

```verify bash
getent passwd streamer | grep -q '{{REPO_DIR}}:/usr/sbin/nologin$' && id streamer | grep -q video && id streamer | grep -q input && id streamer | grep -q render && test "$(stat -c %U:%G {{REPO_DIR}})" = streamer:streamer && test "$(stat -c %a {{REPO_DIR}})" = 750
```

## Step 4: config - sunshine.conf and apps

Why: Non-default settings live in `{{REPO_DIR}}/sunshine.conf`, which the service passes on the command line. The name is `sunshine-bench`, the GameStream port is 9579, capture is X11, and the encoder is software x264 at preset `superfast`. Log, state, credentials, apps, and the TLS keypair are explicit paths under `{{REPO_DIR}}`. `credentials_file` and `file_state` are the same file. `apps.json` matches `/usr/share/sunshine/apps.json` both at `{{REPO_DIR}}/apps.json` and at `{{REPO_DIR}}/.config/sunshine/apps.json` (Sunshine's default config directory under `HOME`). `credentials/` and `covers/` exist and are empty of images; Sunshine writes the keypair into `credentials/` on first start. `covers/` is unused by the stock apps file.

```detect bash
grep -qx 'sunshine_name = sunshine-bench' {{REPO_DIR}}/sunshine.conf && grep -qx 'port = 9579' {{REPO_DIR}}/sunshine.conf && grep -qx 'capture = x11' {{REPO_DIR}}/sunshine.conf && grep -qx 'encoder = software' {{REPO_DIR}}/sunshine.conf && grep -qx 'sw_preset = superfast' {{REPO_DIR}}/sunshine.conf && grep -qx 'min_log_level = info' {{REPO_DIR}}/sunshine.conf && grep -qx 'log_path = {{REPO_DIR}}/sunshine.log' {{REPO_DIR}}/sunshine.conf && grep -qx 'file_state = {{REPO_DIR}}/sunshine_state.json' {{REPO_DIR}}/sunshine.conf && grep -qx 'credentials_file = {{REPO_DIR}}/sunshine_state.json' {{REPO_DIR}}/sunshine.conf && grep -qx 'file_apps = {{REPO_DIR}}/apps.json' {{REPO_DIR}}/sunshine.conf && grep -qx 'pkey = {{REPO_DIR}}/credentials/cakey.pem' {{REPO_DIR}}/sunshine.conf && grep -qx 'cert = {{REPO_DIR}}/credentials/cacert.pem' {{REPO_DIR}}/sunshine.conf && cmp -s /usr/share/sunshine/apps.json {{REPO_DIR}}/apps.json && cmp -s /usr/share/sunshine/apps.json {{REPO_DIR}}/.config/sunshine/apps.json && test -d {{REPO_DIR}}/credentials && test -d {{REPO_DIR}}/covers && test "$(stat -c %U:%G {{REPO_DIR}}/sunshine.conf)" = streamer:streamer
```

```install bash os=linux
set -euo pipefail
install -d -o streamer -g streamer -m 755 "{{REPO_DIR}}/.config"
install -d -o streamer -g streamer -m 755 "{{REPO_DIR}}/.config/sunshine"
install -d -o streamer -g streamer -m 750 "{{REPO_DIR}}/credentials"
install -d -o streamer -g streamer -m 750 "{{REPO_DIR}}/covers"
cat > "{{REPO_DIR}}/sunshine.conf" << 'EOF'
sunshine_name = sunshine-bench
port = 9579
capture = x11
encoder = software
sw_preset = superfast
min_log_level = info
log_path = {{REPO_DIR}}/sunshine.log
file_state = {{REPO_DIR}}/sunshine_state.json
credentials_file = {{REPO_DIR}}/sunshine_state.json
file_apps = {{REPO_DIR}}/apps.json
pkey = {{REPO_DIR}}/credentials/cakey.pem
cert = {{REPO_DIR}}/credentials/cacert.pem
EOF
chown streamer:streamer "{{REPO_DIR}}/sunshine.conf"
chmod 644 "{{REPO_DIR}}/sunshine.conf"
cp -f /usr/share/sunshine/apps.json "{{REPO_DIR}}/apps.json"
cp -f /usr/share/sunshine/apps.json "{{REPO_DIR}}/.config/sunshine/apps.json"
chown streamer:streamer "{{REPO_DIR}}/apps.json" "{{REPO_DIR}}/.config/sunshine/apps.json"
chmod 644 "{{REPO_DIR}}/apps.json" "{{REPO_DIR}}/.config/sunshine/apps.json"
```

```verify bash
grep -qx 'sunshine_name = sunshine-bench' {{REPO_DIR}}/sunshine.conf && grep -qx 'port = 9579' {{REPO_DIR}}/sunshine.conf && grep -qx 'encoder = software' {{REPO_DIR}}/sunshine.conf && grep -qx 'capture = x11' {{REPO_DIR}}/sunshine.conf && grep -qx 'sw_preset = superfast' {{REPO_DIR}}/sunshine.conf && cmp -s /usr/share/sunshine/apps.json {{REPO_DIR}}/apps.json && cmp -s /usr/share/sunshine/apps.json {{REPO_DIR}}/.config/sunshine/apps.json && test "$(stat -c %a {{REPO_DIR}}/sunshine.conf)" = 644 && test "$(stat -c %a {{REPO_DIR}}/credentials)" = 750 && test "$(stat -c %a {{REPO_DIR}}/covers)" = 750 && test "$(stat -c %U:%G {{REPO_DIR}}/apps.json)" = streamer:streamer
```

## Step 5: credentials - web UI user benchadmin

Why: The web UI answers HTTP 401 with basic auth once credentials exist. The state file `{{REPO_DIR}}/sunshine_state.json` has username `benchadmin` (salt and password hash are generated; do not copy them). The binary help string is `--creds username password` to set Web manager credentials, and the state file is owned by `streamer`, so credentials were set by running Sunshine as that user with the config path. The plaintext password is not part of this procedure. Export `SUNSHINE_PASSWORD` (single line) before this step. The observed host also wrote `/root/sunshine-admin.txt` mode 600 with `user=benchadmin` and `password=`.

```detect bash
grep -q '"username": "benchadmin"' {{REPO_DIR}}/sunshine_state.json && test "$(stat -c %U:%G {{REPO_DIR}}/sunshine_state.json)" = streamer:streamer && grep -qx 'user=benchadmin' /root/sunshine-admin.txt && test "$(stat -c %a /root/sunshine-admin.txt)" = 600 && test "$(stat -c %U:%G /root/sunshine-admin.txt)" = root:root
```

```install bash os=linux
set -euo pipefail
if [ -z "${SUNSHINE_PASSWORD:-}" ]; then
  echo "Set SUNSHINE_PASSWORD to the web UI password for benchadmin before this step." >&2
  echo "Do not commit it. The observed host stored it only in /root/sunshine-admin.txt." >&2
  exit 1
fi
umask 077
printf 'user=benchadmin\npassword=%s\n' "$SUNSHINE_PASSWORD" > /root/sunshine-admin.txt
chown root:root /root/sunshine-admin.txt
chmod 600 /root/sunshine-admin.txt
runuser -u streamer -- env HOME="{{REPO_DIR}}" DISPLAY=:0 \
  /usr/bin/sunshine "{{REPO_DIR}}/sunshine.conf" --creds benchadmin "$SUNSHINE_PASSWORD"
chown streamer:streamer "{{REPO_DIR}}/sunshine_state.json"
chmod 644 "{{REPO_DIR}}/sunshine_state.json"
```

```verify bash
grep -q '"username": "benchadmin"' {{REPO_DIR}}/sunshine_state.json && test "$(stat -c %U:%G {{REPO_DIR}}/sunshine_state.json)" = streamer:streamer && grep -qx 'user=benchadmin' /root/sunshine-admin.txt && test "$(stat -c %a /root/sunshine-admin.txt)" = 600
```

## Step 6: xvfb - virtual display service

Why: A system unit starts Xvfb as `streamer` on display :0 before Sunshine. Access control is disabled (`-ac`) and TCP listening is off, so no X authority file is used. The unit file is mode 600, enabled in `multi-user.target`.

```detect bash
test -f /etc/systemd/system/xvfb.service && test "$(stat -c %a /etc/systemd/system/xvfb.service)" = 600 && grep -qx 'Description=Xvfb virtual display :0 for Sunshine' /etc/systemd/system/xvfb.service && grep -qx 'After=network.target' /etc/systemd/system/xvfb.service && grep -qx 'User=streamer' /etc/systemd/system/xvfb.service && grep -qx 'ExecStart=/usr/bin/Xvfb :0 -screen 0 1920x1080x24 -nolisten tcp -ac' /etc/systemd/system/xvfb.service && grep -qx 'Restart=always' /etc/systemd/system/xvfb.service && grep -qx 'WantedBy=multi-user.target' /etc/systemd/system/xvfb.service && systemctl is-enabled --quiet xvfb.service && systemctl is-active --quiet xvfb.service
```

```install bash os=linux
set -euo pipefail
umask 077
cat > /etc/systemd/system/xvfb.service << 'EOF'
[Unit]
Description=Xvfb virtual display :0 for Sunshine
After=network.target

[Service]
User=streamer
ExecStart=/usr/bin/Xvfb :0 -screen 0 1920x1080x24 -nolisten tcp -ac
Restart=always

[Install]
WantedBy=multi-user.target
EOF
chmod 600 /etc/systemd/system/xvfb.service
systemctl daemon-reload
systemctl enable --now xvfb.service
```

```verify bash
systemctl is-enabled --quiet xvfb.service && systemctl is-active --quiet xvfb.service && grep -qx 'ExecStart=/usr/bin/Xvfb :0 -screen 0 1920x1080x24 -nolisten tcp -ac' /etc/systemd/system/xvfb.service && test "$(stat -c %a /etc/systemd/system/xvfb.service)" = 600 && test -S /tmp/.X11-unix/X0
```

## Step 7: sunshine-service - Sunshine system unit

Why: `sunshine.service` requires `xvfb.service`, waits 2 seconds, then runs `/usr/bin/sunshine {{REPO_DIR}}/sunshine.conf` as `streamer` with `DISPLAY=:0`. Restart is always, after 5 seconds. The unit is mode 600 and enabled. The running process keeps `CAP_SYS_ADMIN` in its permitted set (`CapPrm` bit 21, `0x200000`) from the file capability set in step 2; the effective set is empty until Sunshine raises it.

```detect bash
test -f /etc/systemd/system/sunshine.service && test "$(stat -c %a /etc/systemd/system/sunshine.service)" = 600 && grep -qx 'Description=Sunshine self-hosted game stream host' /etc/systemd/system/sunshine.service && grep -qx 'After=network-online.target xvfb.service' /etc/systemd/system/sunshine.service && grep -qx 'Requires=xvfb.service' /etc/systemd/system/sunshine.service && grep -qx 'Wants=network-online.target' /etc/systemd/system/sunshine.service && grep -qx 'User=streamer' /etc/systemd/system/sunshine.service && grep -qx 'Group=streamer' /etc/systemd/system/sunshine.service && grep -qx 'WorkingDirectory={{REPO_DIR}}' /etc/systemd/system/sunshine.service && grep -qx 'Environment=HOME={{REPO_DIR}}' /etc/systemd/system/sunshine.service && grep -qx 'Environment=DISPLAY=:0' /etc/systemd/system/sunshine.service && grep -qx 'ExecStartPre=/bin/sleep 2' /etc/systemd/system/sunshine.service && grep -qx 'ExecStart=/usr/bin/sunshine {{REPO_DIR}}/sunshine.conf' /etc/systemd/system/sunshine.service && grep -qx 'Restart=always' /etc/systemd/system/sunshine.service && grep -qx 'RestartSec=5' /etc/systemd/system/sunshine.service && systemctl is-enabled --quiet sunshine.service && systemctl is-active --quiet sunshine.service
```

```install bash os=linux
set -euo pipefail
umask 077
cat > /etc/systemd/system/sunshine.service << 'EOF'
[Unit]
Description=Sunshine self-hosted game stream host
After=network-online.target xvfb.service
Requires=xvfb.service
Wants=network-online.target

[Service]
User=streamer
Group=streamer
WorkingDirectory={{REPO_DIR}}
Environment=HOME={{REPO_DIR}}
Environment=DISPLAY=:0
ExecStartPre=/bin/sleep 2
ExecStart=/usr/bin/sunshine {{REPO_DIR}}/sunshine.conf
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
chmod 600 /etc/systemd/system/sunshine.service
systemctl daemon-reload
systemctl enable --now sunshine.service
```

```verify bash
systemctl is-enabled --quiet sunshine.service && systemctl is-active --quiet sunshine.service && grep -qx 'ExecStart=/usr/bin/sunshine {{REPO_DIR}}/sunshine.conf' /etc/systemd/system/sunshine.service && grep -qx 'Requires=xvfb.service' /etc/systemd/system/sunshine.service && test "$(stat -c %a /etc/systemd/system/sunshine.service)" = 600 && pid=$(systemctl show -p MainPID --value sunshine.service) && test "$pid" != 0 && grep -q 'CapPrm:.*0000000000200000' /proc/$pid/status
```

## Step 8: serve - web UI and GameStream port

Why: A finished install answers the web UI with 401 basic auth (credentials are set, so it is not the first-run welcome page) and the GameStream `serverinfo` endpoint with hostname `sunshine-bench`. Sunshine generates the TLS certificate on first start (`CN = Sunshine Gamestream Host`) at the paths in the config. UDP video, control, and audio ports stay closed until a Moonlight client connects.

```detect bash
curl -ski https://127.0.0.1:9580/ | grep -q '401 Unauthorized' && curl -s http://127.0.0.1:9579/serverinfo | grep -q sunshine-bench && curl -s http://127.0.0.1:9579/serverinfo | grep -q SUNSHINE_SERVER_FREE && test -f {{REPO_DIR}}/credentials/cacert.pem && test -f {{REPO_DIR}}/credentials/cakey.pem && openssl x509 -in {{REPO_DIR}}/credentials/cacert.pem -noout -subject | grep -q 'Sunshine Gamestream Host'
```

```install bash os=linux
set -euo pipefail
systemctl enable --now xvfb.service
systemctl enable --now sunshine.service
ok=0
i=0
while [ "$i" -lt 30 ]; do
  if curl -ski https://127.0.0.1:9580/ | grep -q '401 Unauthorized'; then
    ok=1
    break
  fi
  i=$((i + 1))
  sleep 1
done
test "$ok" = 1
```

```verify bash
curl -ski https://127.0.0.1:9580/ | grep -q '401 Unauthorized' && curl -ski https://127.0.0.1:9580/ | grep -q 'Sunshine Gamestream Host' && curl -s http://127.0.0.1:9579/serverinfo | grep -q sunshine-bench && curl -s http://127.0.0.1:9579/serverinfo | grep -q SUNSHINE_SERVER_FREE && test -f {{REPO_DIR}}/credentials/cacert.pem && test -f {{REPO_DIR}}/credentials/cakey.pem && test "$(stat -c %a {{REPO_DIR}}/credentials/cakey.pem)" = 600 && openssl x509 -in {{REPO_DIR}}/credentials/cacert.pem -noout -subject | grep -q 'Sunshine Gamestream Host' && ss -lnt | grep -q ':9579' && ss -lnt | grep -q ':9580'
```
