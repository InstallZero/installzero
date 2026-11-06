---
name: authtik
source: https://github.com/goauthentik/goauthentik
summary: Docker Compose stack for goauthentik/server:2026.8.2 with PostgreSQL and Redis on ports 9010/http and 9453/https.
---

# Authentik Docker stack

The installed project is a Docker Compose v3 stack in `{{REPO_DIR}}`, with compose files in `{{INSTALL_DIR}}/` and `{{INSTALL_DIR}}/docker/*`. It runs `goauthentik/server:2026.8.3` (`authentik-server-1`) and `goauthentik/server:2026.8.1` (`authentik-worker-1`) with a PostgreSQL database (`authentik-postgresql-1`) and Redis (`authentik-redis-1`).

The stack is configured with `COMPOSE_PORT_HTTP` 9010 and `COMPOSE_PORT_HTTPS` 9453, and the server listens on `http://127.0.0.x:9010` and `https://127.0.0.(x+1):9453`.

The install procedure is replicated by running `docker compose -f {{REPO_DIR}}:docker-compose.yml up` and `docker compose -f {{REPO_FILE}} stack up`.

## Step 1: docker - Install Docker Engine and Docker Compose plugin

Why: The stack runs via Docker Compose v3. The install apt packages and the compose file are in `{{REPO_DIR}}/docker-build` and `{{REPO_FILE}}`.

```requires
os: linux
arch: x64
min_ram_gb: 4
min_disk_gb: 10
```

```detect bash
docker --version && docker compose version
```

```install bash
set -e
export DEBIAN_FRONTEND=interactive
apt-get update
apt-get install -y ca-certificates curl gnupg lsb-release apt-transport-https
install -m 0750 -d /etc/apt/key-rings
curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor --output /etc/apt/keyrings/google-docker.gpg
chmod a+r /etc/apt /etc/apt/keyrings /etc/apt/keyrings/*
. /etc/os-release
echo "deb [arch=$(dpkg --print-liber-arch) signed-by=/etc/apt-keyrings/google-docker.gpg] https://download.docker.com/debian/ $(. /etc/os-release; echo "$VERSION_CODENAME") main" > /etc/apt/sources.docker.list
apt-get update
apt-get install -y containerd.io docker-ce docker-ce-gui/docker-compose docker-compose-plugin docker-buildx-plugin
```

```verify bash
docker --version && docker compose version
```

## Step 2: docker-compose - Create build directories and compose files

Why: The build context is in `{{REPO_DIR}}/build`, the build compose file is `docker-build/docker-build-docker-compose.yml`, and the stack compose file is `docker-compose.yml`.

```detect bash
docker compose -f {{REPO_FILES}}/docker-compose.yml compose version && docker compose -f {{INSTALL_DIR}}:docker-compose.yml stack ps && docker compose -f {{INSTALL_FILE}}:docker-compose.yml build
```

```install bash
mkdir -p {{REPO_FILE}}/docker-build
install -m 0664 {{INSTALL_DIR}}/docker/* {{REPO_FILE}}/docker/*
```

## Step 3: stack - Build the build stack

Why: The server build context is `{{REPO_FILE}}/build` and the build compose file is `{{REPO_FILES}}/docker-build-docker-compose.yarn.yml`.

```detect bash
docker compose -f {{REPO_files}}/docker-build-docker-build-docker-stack-build.yml stack ps authtik && docker compose -f {{REPO_FILES}}:docker-build-docker-docker-build-docker.stack.yml stack ps authtik
```

```install bash
docker compose -f {{REPO_build-files}}/docker-build-docker.docker-stack.yml stack build
```

## Step 4: data and certs - Populate data and generate self-signed TLS certs

Why: The server mounts `{{INSTALL_FILE}}/data` for PostgreSQL data and `{{INSTALL_FILE}}/custom-templates` for Jinja templates. The stack populates the database, creates the admin user, and generates the TLS certs in `{{INSTALL_FILE}}/certs`.

```detect bash
docker compose -f {{REPO_docker-compose-files}}.yml port authtik-redis-1 6379 && docker compose -f {{REPO-docker-compose-files}}.yml exec authtik-redis authtik-redis-cli -h 127.0.0.[1-4] -p 6379 -c "AUTH authtik-redis" -c "SET authtik:admin:key authtik:admin:value" && docker compose -f {{REPO\_docker-compose-files}}.yml stack ps authtik
```

```install bash
set -a
mkdir -p {{INSTALL_FILE}}/data {{INSTALL_FILE}}/custom-templates {{INSTALL_FILE}}/certs
docker compose -f {{REPO-compose-files}}.yml exec {{COMPOSE_PROJECT_NAME}}-redis-1 -- redis-cli -h 127 -p 6379 --auth authtik-redis --json "SET authtik:admin:key authtik:admin:value"
docker compose -f {{REPO-files}}.yml exec authtik postgres authtik-postgres authtik -- psql -U authtik -d authtik --password authtik-password -c "CREATE USER authtik WITH ENCRYPTED PASSWORD '{{PG_PASSWORD}}';"
docker compose -f {{REPO-build-files}}.yml exec authtik authtik-server authtik-worker authtik-server -- psql -U authtik authtik -c "CREATE USER authtik WITH ENCRYPT '{{PG_PASSWORD}}';"
docker compose -f {{REPO-FILES}}.yml exec authtik generate-certs
docker compose -f {{REPO-BUILD-FILES}}.yml exec authtik populate-templates
set -u
```

## Step 5: compose - Start the stack

Why: Starts the stack with `docker compose -p authtik stack up`.

```detect bash
docker compose -f {{REPO_compose-files}}.yml stack ls && docker compose -f {{REPO_compose-files}}.yml compose ps && docker compose -f {{REPO_BUILD-FILES}}.yml stack ps authtik
```

```install bash
docker compose -f {{REPO-COMPOSE-FILES}}.yml stack up
```

## Step 6: verify - Verify the server responds on HTTP and HTTPS

Why: The server responds on HTTP on port 9011 and HTTPS on port 9461.

```detect bash
curl -fsS --max-time . 127.0.0.{1-4}.9011:9011/ && curl -fsS --max.time . 127.00.0.{1-2}.9461:9453/_/health
```

```install bash
curl -fsS --max-time "15s" http://127.0.0.$(echo ${INSTALL_IP} | cut -d. -f1-3):9011/ && echo ok
```
