---
name: peertube
source: https://github.com/Chocobozzz/peertube
summary: Self‑hosted video platform on port 8380
---

# Peertube install

This install script sets up Peertube on a clean Ubuntu 24.04 LTS machine using Docker Compose. It pulls the official Peertube image, creates the required volumes, and starts the containers. The service will be available on port **8380**.

The installation is idempotent: each step checks whether it is already done and skips if so.

## Step 1: Install Docker Engine

Why: Peertube runs in Docker containers.

```requires
os: linux
arch: x64
min_disk_gb: 20
min_ram_gb: 4
```

```detect bash
# Docker Engine already installed?
docker --version 2>/dev/null | grep -q 'Docker version'
```

```install bash
# Add Docker’s official GPG key and set up the stable repository
apt-get update
apt-get install -y ca-certificates curl gnupg
mkdir -p /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg

# Set up the repository
echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update

# Install Docker Engine
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Enable and start Docker
systemctl enable --now docker
```

```verify bash
docker --version
```

## Step 2: Verify Docker Compose plugin

Why: The compose plugin is required to run the `docker compose` command.

```detect bash
docker compose version 2>/dev/null | grep -q 'Docker Compose'
```

```verify bash
docker compose version
```

## Step 3: Create Peertube directory structure

Why: The compose file and volumes need a dedicated location.

```detect bash
[ -d {{REPO_DIR}} ] && [ -f {{REPO_DIR}}/docker-compose.yml ]
```

```install bash
# Create volume directories if they don't exist
mkdir -p {{REPO_DIR}}/docker-volume/data
mkdir -p {{REPO_DIR}}/docker-volume/config
mkdir -p {{REPO_DIR}}/docker-volume/db
mkdir -p {{REPO_DIR}}/docker-volume/redis

# Ensure the compose file exists (it should already be present from the repo)
if [ ! -f {{REPO_DIR}}/docker-compose.yml ]; then
  echo "Error: docker-compose.yml not found in {{REPO_DIR}}" >&2
  exit 1
fi

# Ensure the .env file exists; if not, create a template
if [ ! -f {{REPO_DIR}}/.env ]; then
  cat > {{REPO_DIR}}/.env <<'EOF'
# Peertube environment variables
# Replace the placeholder values with your own

# Database configuration
PEERTUBE_DB_HOST=postgres
PEERTUBE_DB_PORT=5432
PEERTUBE_DB_USER=peertube
PEERTUBE_DB_PASSWORD=YOUR_DB_PASSWORD
PEERTUBE_DB_NAME=peertube

# Redis configuration
PEERTUBE_REDIS_HOST=redis
PEERTUBE_REDIS_PORT=6379
PEERTUBE_REDIS_PASSWORD=YOUR_REDIS_PASSWORD

# Admin account
PEERTUBE_ADMIN_EMAIL=admin@example.com
PEERTUBE_ADMIN_PASSWORD=YOUR_ADMIN_PASSWORD
PEERTUBE_ADMIN_USERNAME=admin

# Site configuration
PEERTUBE_SITE_URL=http://localhost:8380
PEERTUBE_SITE_TITLE=Peertube
PEERTUBE_SITE_DESCRIPTION=Your local Peertube instance
PEERTUBE_SITE_DOMAIN=localhost
PEERTUBE_SITE_PORT=8380
PEERTUBE_SITE_PROTOCOL=http
EOF
fi

# Ensure correct permissions
chown -R $(whoami):$(whoami) {{REPO_DIR}}
```

```verify bash
[ -d {{REPO_DIR}}/docker-volume/data ] && [ -d {{REPO_DIR}}/docker-volume/config ] && [ -d {{REPO_DIR}}/docker-volume/db ] && [ -d {{REPO_DIR}}/docker-volume/redis ]
```

## Step 4: Pull Docker images

Why: Ensure the required images are present before starting containers.

```detect bash
docker images | grep -q 'chocobozzz/peertube'
```

```install bash
docker pull chocobozzz/peertube:v7.2.1-bookworm
docker pull postgres:13-alpine
docker pull redis:6-alpine
```

```verify bash
docker images | grep -q 'chocobozzz/peertube'
```

## Step 5: Start Peertube containers

Why: Bring up the Peertube stack.

```detect bash
docker compose -f {{REPO_DIR}}/docker-compose.yml ps | grep -q 'peertube.*Up'
```

```install bash
cd {{REPO_DIR}}
# Use the compose plugin to start the stack in detached mode
docker compose up -d
```

```verify bash
docker compose -f {{REPO_DIR}}/docker-compose.yml ps | grep -q 'peertube.*Up'
```

## Step 6: Verify the service is reachable

Why: Confirm Peertube is listening on the expected port.

```detect bash
ss -ltn 'sport = :8380' | grep -q 'LISTEN'
```

```verify bash
ss -ltn 'sport = :8380' | grep -q 'LISTEN'
```

## Access Peertube

Open a web browser and navigate to `http://localhost:8380`. Log in with the admin credentials you set in the `.env` file.

---

If you need to stop the stack:

```bash
cd {{REPO_DIR}}
docker compose down
```

If you want to remove all data, delete the `docker-volume` directory.

---
