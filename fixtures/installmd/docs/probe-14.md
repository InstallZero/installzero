---
name: jellyfin
source: https://github.com/jellyfin/jellyfin
summary: Jellyfin Media Server 10.10.7 via Docker Compose on port 9940
---

# Jellyfin Media Server Install

This procedure installs Jellyfin Media Server (`jellyfin/jellyfin:10.10.7`) using Docker Compose on Ubuntu 24.04 LTS, serving HTTP on port `9940` (`http://jellyfin.bench.lan:9940`).

Configuration and cache directories are stored under `/srv/jellyfin` (`/srv/jellyfin/config` and `/srv/jellyfin/cache`), media directories are stored under `/srv/media` (`/srv/media/movies` and `/srv/media/music`), and the Docker Compose project is placed in `{{INSTALL_DIR}}` (`/opt/jellyfin`).

## Step 1: docker - Install Docker Engine and Compose Plugin

Why: Jellyfin runs as a Docker container managed by Docker Compose.

```requires
os: linux
arch: x64
min_disk_gb: 5
```

```detect bash
docker --version && docker compose version && systemctl is-active --quiet docker
```

```install bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce=5:29.9.2-1~ubuntu.24.04~noble docker-ce-cli=5:29.9.2-1~ubuntu.24.04~noble containerd.io=2.4.2-3~ubuntu.24.04~noble docker-buildx-plugin=0.38.1-1~ubuntu.24.04~noble docker-compose-plugin=5.6.1-1~ubuntu.24.04~noble
sudo systemctl enable --now docker
```

```verify bash
docker --version && docker compose version && systemctl is-active --quiet docker
```

## Step 2: hosts - Configure Local Hostname Resolution

Why: Jellyfin is configured with `JELLYFIN_PublishedServerUrl=http://jellyfin.bench.lan:9940`, so `jellyfin.bench.lan` must resolve to `127.0.0.1` locally.

```detect bash
grep -qE '^127\.0\.0\.1[[:space:]]+jellyfin\.bench\.lan([[:space:]]|$)' /etc/hosts
```

```install bash
if ! grep -qE '^127\.0\.0\.1[[:space:]]+jellyfin\.bench\.lan([[:space:]]|$)' /etc/hosts; then
  echo "127.0.0.1 jellyfin.bench.lan" | sudo tee -a /etc/hosts > /dev/null
fi
```

```verify bash
grep -qE '^127\.0\.0\.1[[:space:]]+jellyfin\.bench\.lan([[:space:]]|$)' /etc/hosts && getent hosts jellyfin.bench.lan | grep -q '127.0.0.1'
```

## Step 3: directories - Create Storage and Media Directories

Why: The Jellyfin container runs as UID/GID `1000:1000` and mounts `/srv/jellyfin/config`, `/srv/jellyfin/cache`, and `/srv/media` (`/srv/media/movies`, `/srv/media/music`).

```detect bash
test -d /srv/jellyfin/config && test -d /srv/jellyfin/cache && test -d /srv/media/movies && test -d /srv/media/music && [ "$(stat -c '%u:%g' /srv/jellyfin)" = "1000:1000" ]
```

```install bash
sudo mkdir -p /srv/jellyfin/config /srv/jellyfin/cache /srv/media/movies /srv/media/music
sudo chown -R 1000:1000 /srv/jellyfin /srv/media
```

```verify bash
test -d /srv/jellyfin/config && test -d /srv/jellyfin/cache && test -d /srv/media/movies && test -d /srv/media/music && [ "$(stat -c '%u:%g' /srv/jellyfin)" = "1000:1000" ] && [ "$(stat -c '%u:%g' /srv/media)" = "1000:1000" ]
```

## Step 4: compose-file - Write Jellyfin Docker Compose Configuration

Why: Defines the `jellyfin/jellyfin:10.10.7` service, port `9940:8096`, timezone `Europe/Berlin`, published URL `http://jellyfin.bench.lan:9940`, and volume mounts in `{{INSTALL_DIR}}/docker-compose.yml`.

```detect bash
test -f "{{INSTALL_DIR}}/docker-compose.yml" && grep -q "jellyfin/jellyfin:10.10.7" "{{INSTALL_DIR}}/docker-compose.yml" && grep -q "9940:8096" "{{INSTALL_DIR}}/docker-compose.yml"
```

```install bash
sudo mkdir -p "{{INSTALL_DIR}}"
sudo tee "{{INSTALL_DIR}}/docker-compose.yml" > /dev/null <<'EOF'
services:
  jellyfin:
    image: jellyfin/jellyfin:10.10.7
    container_name: jellyfin
    user: "1000:1000"
    ports:
      - "9940:8096"
    environment:
      - TZ=Europe/Berlin
      - JELLYFIN_PublishedServerUrl=http://jellyfin.bench.lan:9940
    volumes:
      - /srv/jellyfin/config:/config
      - /srv/jellyfin/cache:/cache
      - /srv/media:/media:ro
    restart: unless-stopped
EOF
```

```verify bash
test -f "{{INSTALL_DIR}}/docker-compose.yml" && docker compose -f "{{INSTALL_DIR}}/docker-compose.yml" config --quiet
```

## Step 5: start - Pull Image and Start Jellyfin Container

Why: Starts the Jellyfin container in detached mode and waits until its healthcheck reports healthy.

```detect bash
[ "$(docker inspect -f '{{.State.Running}}' jellyfin 2>/dev/null)" = "true" ]
```

```install bash
cd "{{INSTALL_DIR}}"
sudo docker compose up -d
for i in $(seq 1 60); do
  if [ "$(curl -fsS http://localhost:9940/health 2>/dev/null)" = "Healthy" ]; then
    break
  fi
  sleep 2
done
```

```verify bash
[ "$(docker inspect -f '{{.State.Running}}' jellyfin 2>/dev/null)" = "true" ] && [ "$(curl -fsS http://localhost:9940/health)" = "Healthy" ]
```

## Step 6: wizard - Complete Initial Startup Wizard and Configure Library

Why: Initializes the Jellyfin server metadata preferences (`de` / `DE`), creates the administrator account (`benchadmin`), adds the `Movies` virtual folder pointing to `/media/movies`, and marks the startup wizard as completed.

> **Secrets**: Set `JELLYFIN_ADMIN_PASSWORD` in your environment to the desired password for the `benchadmin` user before running this step.

```detect bash
curl -fsS http://jellyfin.bench.lan:9940/System/Info/Public | grep -q '"StartupWizardCompleted":true'
```

```install bash
: "${JELLYFIN_ADMIN_PASSWORD:?Set JELLYFIN_ADMIN_PASSWORD to the desired admin password for benchadmin}"

curl -fsS -X POST "http://jellyfin.bench.lan:9940/Startup/Configuration" \
  -H "Content-Type: application/json" \
  -d '{"UICulture":"de","MetadataCountryCode":"DE","PreferredMetadataLanguage":"de"}'

curl -fsS -X GET "http://jellyfin.bench.lan:9940/Startup/User" > /dev/null

curl -fsS -X POST "http://jellyfin.bench.lan:9940/Startup/User" \
  -H "Content-Type: application/json" \
  -d "{\"Name\":\"benchadmin\",\"Password\":\"${JELLYFIN_ADMIN_PASSWORD}\"}"

curl -fsS -X POST "http://jellyfin.bench.lan:9940/Startup/RemoteAccess" \
  -H "Content-Type: application/json" \
  -d '{"EnableRemoteAccess":true,"EnableAutomaticPortMapping":false}'

curl -fsS -X POST "http://jellyfin.bench.lan:9940/Library/VirtualFolders?name=Movies&collectionType=movies&paths=%2Fmedia%2Fmovies&refreshLibrary=true" \
  -H "Content-Type: application/json" \
  -d '{"LibraryOptions":{"EnableRealtimeMonitor":false,"EnableChapterImageExtraction":false,"ExtractChapterImagesDuringLibraryScan":false,"EnableTrickplayImageExtraction":false,"ExtractTrickplayImagesDuringLibraryScan":false,"SaveLocalMetadata":false,"EnableAutomaticSeriesGrouping":true}}'

curl -fsS -X POST "http://jellyfin.bench.lan:9940/Startup/Complete"
```

```verify bash
curl -fsS http://jellyfin.bench.lan:9940/System/Info/Public | grep -q '"StartupWizardCompleted":true' && \
curl -fsS http://jellyfin.bench.lan:9940/System/Info/Public | grep -q '"Version":"10.10.7"' && \
test -f /srv/jellyfin/config/root/default/Movies/movies.mblink
```
