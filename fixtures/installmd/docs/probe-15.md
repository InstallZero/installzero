---
name: rpcs3
source: https://github.com/RPCS3/rpcs3
summary: RPCS3 PlayStation 3 emulator running headless on Xvfb :1 with x11vnc (port 8741) and noVNC web client on port 8740
---

# RPCS3 Headless Web/VNC Install

This document installs the RPCS3 PlayStation 3 emulator (Linux x64 AppImage extracted to `{{REPO_DIR}}`) running headlessly under a dedicated `rpcs3` system user on a virtual X11 display (`:1` via `Xvfb`), exported locally over VNC (`127.0.0.1:8741` via `x11vnc`) and served over HTTP via noVNC/websockify on port `8740`.

## Step 1: system-packages - Install Xvfb, VNC, noVNC, and RPCS3 runtime libraries

Why: RPCS3 requires X11/XCB, OpenGL/Vulkan, SDL2, and audio runtime libraries, while headless browser access requires Xvfb, x11vnc, and noVNC with websockify.

```requires
os: linux
arch: x64
min_ram_gb: 4
min_disk_gb: 5
```

```detect bash
dpkg -s xvfb x11vnc xauth novnc python3-websockify jq file fonts-dejavu-core libfontconfig1 libgl1 libglu1-mesa libegl1 libvulkan1 libasound2t64 libopenal1 libpulse0 libsdl2-2.0-0 libxcb-cursor0 libxcb-icccm4 libxcb-image0 libxcb-keysyms1 libxcb-randr0 libxcb-render-util0 libxcb-shape0 libxcb-xinerama0 libxcb-xkb1 libxkbcommon-x11-0 >/dev/null
```

```install bash
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y \
  ca-certificates \
  curl \
  dbus-x11 \
  file \
  fonts-dejavu-core \
  jq \
  libasound2t64 \
  libegl1 \
  libfontconfig1 \
  libgl1 \
  libgl1-mesa-dri \
  libglu1-mesa \
  libopenal1 \
  libpulse0 \
  libsdl2-2.0-0 \
  libvulkan1 \
  libxcb-cursor0 \
  libxcb-icccm4 \
  libxcb-image0 \
  libxcb-keysyms1 \
  libxcb-randr0 \
  libxcb-render-util0 \
  libxcb-shape0 \
  libxcb-xinerama0 \
  libxcb-xkb1 \
  libxkbcommon-x11-0 \
  mesa-vulkan-drivers \
  novnc \
  python3-websockify \
  x11vnc \
  xauth \
  xvfb
```

```verify bash
command -v Xvfb >/dev/null && command -v x11vnc >/dev/null && command -v websockify >/dev/null && test -f /usr/share/novnc/vnc.html
```

## Step 2: user-and-dirs - Create rpcs3 user, runtime directories, and host entry

Why: RPCS3 runs as an unprivileged `rpcs3` service user with state and configuration stored under `/srv/rpcs3` and VNC credentials in `/etc/rpcs3`.

```detect bash
id -u rpcs3 >/dev/null && test -d /srv/rpcs3/config && test -d /srv/rpcs3/games && test -d /etc/rpcs3 && grep -q "rpcs3.bench.lan" /etc/hosts
```

```install bash
if ! id -u rpcs3 >/dev/null 2>&1; then
  useradd --system --user-group --home-dir /srv/rpcs3 --create-home --shell /usr/sbin/nologin rpcs3
fi
install -d -m 0755 -o rpcs3 -g rpcs3 /srv/rpcs3 /srv/rpcs3/config /srv/rpcs3/games
install -d -m 0755 -o root -g root /etc/rpcs3
if ! grep -q "rpcs3.bench.lan" /etc/hosts; then
  echo "127.0.0.1 rpcs3.bench.lan" >> /etc/hosts
fi
```

```verify bash
id -u rpcs3 >/dev/null && test -d /srv/rpcs3/config && test -d /srv/rpcs3/games && test -d /etc/rpcs3 && grep -q "rpcs3.bench.lan" /etc/hosts
```

## Step 3: rpcs3-appimage - Download and extract RPCS3 Linux AppImage

Why: Downloads the RPCS3 Linux x64 AppImage (`v0.0.43-20336-bd179ffd`), extracts its `squashfs-root` to `{{REPO_DIR}}/AppDir`, and links `/usr/local/bin/rpcs3` to `{{REPO_DIR}}/app/AppRun`.

```detect bash
test -x "{{REPO_DIR}}/AppDir/usr/bin/rpcs3" && test -L "{{REPO_DIR}}/app" && test -f "{{REPO_DIR}}/VERSION" && test -L /usr/local/bin/rpcs3
```

```install bash
RPCS3_VERSION="v0.0.43-20336-bd179ffd"
ASSET_NAME="rpcs3-${RPCS3_VERSION}_linux64.AppImage"
DOWNLOAD_URL="https://github.com/RPCS3/rpcs3-binaries-linux/releases/download/build-bd179ffda046c318fa4bfdc1be1cc5fd7dc5b3a2/${ASSET_NAME}"

mkdir -p "{{REPO_DIR}}"
TMP_DIR="$(mktemp -d)"
if ! curl -fsSL -o "${TMP_DIR}/${ASSET_NAME}" "${DOWNLOAD_URL}"; then
  RELEASE_JSON="$(curl -fsSL https://api.github.com/repos/RPCS3/rpcs3-binaries-linux/releases/latest)"
  ASSET_NAME="$(printf '%s' "${RELEASE_JSON}" | jq -r '.assets[] | select(.name | endswith("_linux64.AppImage")) | .name' | head -n1)"
  DOWNLOAD_URL="$(printf '%s' "${RELEASE_JSON}" | jq -r '.assets[] | select(.name | endswith("_linux64.AppImage")) | .browser_download_url' | head -n1)"
  RPCS3_VERSION="$(printf '%s' "${ASSET_NAME}" | sed -E 's/^rpcs3-(.*)_linux64\.AppImage$/\1/')"
  curl -fsSL -o "${TMP_DIR}/${ASSET_NAME}" "${DOWNLOAD_URL}"
fi

chmod +x "${TMP_DIR}/${ASSET_NAME}"
(cd "${TMP_DIR}" && "./${ASSET_NAME}" --appimage-extract >/dev/null)
rm -rf "{{REPO_DIR}}/AppDir"
mv "${TMP_DIR}/squashfs-root" "{{REPO_DIR}}/AppDir"
ln -sfn ./AppDir "{{REPO_DIR}}/app"
cat > "{{REPO_DIR}}/VERSION" <<EOF
${RPCS3_VERSION}
asset=${ASSET_NAME}
EOF
ln -sfn "{{REPO_DIR}}/app/AppRun" /usr/local/bin/rpcs3
rm -rf "${TMP_DIR}"
```

```verify bash
test -x "{{REPO_DIR}}/app/AppRun" && test -x "{{REPO_DIR}}/AppDir/usr/bin/rpcs3" && test -f "{{REPO_DIR}}/VERSION" && test -L /usr/local/bin/rpcs3
```

## Step 4: configuration - Configure RPCS3 defaults and VNC password

Why: Pre-populates the headless RPCS3 configuration under `/srv/rpcs3/config/rpcs3/config/config.yml` and generates the `x11vnc` password files in `/etc/rpcs3` (provide `RPCS3_VNC_PASSWORD` in the environment to set a custom password, otherwise a random password is generated and saved to `/etc/rpcs3/vnc-password.txt`).

```detect bash
test -f /srv/rpcs3/config/rpcs3/config/config.yml && test -s /etc/rpcs3/vncpasswd && test -s /etc/rpcs3/vnc-password.txt
```

```install bash
install -d -m 0755 -o rpcs3 -g rpcs3 /srv/rpcs3/config/rpcs3/config
cat > /srv/rpcs3/config/rpcs3/config/config.yml <<'EOF'
Core:
  PPU Decoder: Recompiler (LLVM)
  SPU Decoder: Recompiler (LLVM)
Video:
  Renderer: "Null"
  Frame limit: 30
Audio:
  Renderer: "Null"
Miscellaneous:
  Automatically start games after boot: false
EOF
chown -R rpcs3:rpcs3 /srv/rpcs3/config

if [ ! -s /etc/rpcs3/vnc-password.txt ]; then
  VNC_PASS="${RPCS3_VNC_PASSWORD:-$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 8)}"
  printf '%s\n' "${VNC_PASS}" > /etc/rpcs3/vnc-password.txt
  chmod 0600 /etc/rpcs3/vnc-password.txt
fi
x11vnc -storepasswd "$(head -n1 /etc/rpcs3/vnc-password.txt)" /etc/rpcs3/vncpasswd
chmod 0600 /etc/rpcs3/vncpasswd
```

```verify bash
test -f /srv/rpcs3/config/rpcs3/config/config.yml && test -s /etc/rpcs3/vncpasswd && test -s /etc/rpcs3/vnc-password.txt
```

## Step 5: systemd-services - Configure and start RPCS3, Xvfb, x11vnc, and noVNC services

Why: Creates and starts the systemd units `rpcs3-xvfb.service`, `rpcs3.service`, `rpcs3-vnc.service`, and `rpcs3-novnc.service` so RPCS3 runs persistently on `:1` and is accessible via noVNC on port 8740.

```detect bash
systemctl is-active --quiet rpcs3-xvfb.service && systemctl is-active --quiet rpcs3.service && systemctl is-active --quiet rpcs3-vnc.service && systemctl is-active --quiet rpcs3-novnc.service
```

```install bash
cat > /etc/systemd/system/rpcs3-xvfb.service <<'EOF'
[Unit]
Description=Virtual X display :1 for RPCS3
After=network.target

[Service]
ExecStart=/usr/bin/Xvfb :1 -screen 0 1600x900x24 -nolisten tcp
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/rpcs3.service <<EOF
[Unit]
Description=RPCS3 PlayStation 3 emulator (headless on Xvfb :1)
After=rpcs3-xvfb.service
Requires=rpcs3-xvfb.service

[Service]
User=rpcs3
Group=rpcs3
WorkingDirectory=/srv/rpcs3
Environment=DISPLAY=:1
Environment=HOME=/srv/rpcs3
Environment=XDG_CONFIG_HOME=/srv/rpcs3/config
Environment=QT_QPA_PLATFORM=xcb
Environment=LIBGL_ALWAYS_SOFTWARE=1
Environment=TZ=Europe/Berlin
ExecStartPre=/bin/sleep 2
ExecStart={{REPO_DIR}}/app/AppRun
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/rpcs3-vnc.service <<'EOF'
[Unit]
Description=x11vnc export of RPCS3 display :1
After=rpcs3-xvfb.service
Requires=rpcs3-xvfb.service

[Service]
ExecStartPre=/bin/sleep 2
ExecStart=/usr/bin/x11vnc -display :1 -rfbport 8741 -localhost -rfbauth /etc/rpcs3/vncpasswd -forever -shared -noxdamage
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/rpcs3-novnc.service <<'EOF'
[Unit]
Description=noVNC web client for RPCS3 on port 8740
After=rpcs3-vnc.service
Wants=rpcs3-vnc.service

[Service]
ExecStart=/usr/bin/websockify --web /usr/share/novnc 8740 localhost:8741
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now rpcs3-xvfb.service rpcs3.service rpcs3-vnc.service rpcs3-novnc.service
```

```verify bash
systemctl is-active --quiet rpcs3-xvfb.service && \
systemctl is-active --quiet rpcs3.service && \
systemctl is-active --quiet rpcs3-vnc.service && \
systemctl is-active --quiet rpcs3-novnc.service && \
ss -tln | grep -q ':8741 ' && \
ss -tln | grep -q ':8740 ' && \
curl -fsSI http://127.0.0.1:8740/vnc.html >/dev/null
```
