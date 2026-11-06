---
name: invoiceninja
source: https://github.com/invoiceninja/invoiceninja
summary: Invoice Ninja (Laravel 11/Livewire/Tailwind) on port 8910 with Redis queue worker
---

# Invoice Ninja install

Replay the procedure that set up the Invoice Ninja application at `{{INSTALL_DIR}}` from a fresh Ubuntu 24.04 WSL machine.

## Step 1: packages - System packages and apt repository

Why: MariaDB, Nginx, PHP 8.3 FPM, Redis, and git are installed from the Ubuntu package repositories.

```requires
os: linux
min_disk_gb: 10
min_ram_gb: 2
```

```detect bash
dpkg-query -W -f='${Package} ${Version}\n' mariadb-server nginx php8.3-fpm php8.3-mysql php8.3-cli php8.3-curl php8.3-gd php8.3-imagick php8.3-intl php8.3-mbstring php8.3-redis php8.3-soap php8.3-zip php8.3-bcmath php8.3-xml php8.3-zip php8.3-gd php8.3-imagick -W 'php8.3-' | grep -vc 9
```

```install bash
apt-get update
apt-get install -y \
    nginx \
    nginx-full \
    mariadb-server \
    php8.3-mysql \
    php8.3-cli \
    php8.3-curl \
    php8.3-gd \
    php8.3-imagick \
    php8.3-intl \
    php8.3-mbstring \
    php8.3-redis \
    php8.3-soap \
    php8.3-zip \
    php8.3-bcmath \
    php8.3-xml \
    php8.3-cli \
    php8.3-gd \
    php8.3-imagick \
    php8.3-intl \
    php8.3-mbstring \
    php8.3-redis \
    php8.3-soap \
    php8.3-zip \
    php8.3-bcmath \
    php8.3-xml \
    php8.3-gd \
    php8.3-imagick \
    php8.3-intl \
    php8.3-mbstring \
    php8.3-redis \
    php8.3-soap \
    php8.3-zip \
    php8.3-bcmath \
    php8.3-xml \
    python3-venv \
    python3-pip
```

```verify bash
dpkg-query -W -f='${Package} ${Version}\n' \
    mariadb-server \
    nginx \
    php8.3-fpm \
    php8.3-cli \
    php8.3-mysql \
    php8.3-curl \
    php8.3-gd \
    php8.3-imagick \
    php8.3-intl \
    php8.3-mbstring \
    php8.3-redis \
    php8.3-soap \
    php8.3-zip \
    php8.3-bcmath \
    php8.3-xml \
    python3-venv \
    python3-pip | \
    grep -c '^mariadb.*' | grep -qx 1 && \
    grep -c '^nginx.*' | grep -qx 1 && \
    grep -c '^php8\.3' | grep -qx 1 && \
    grep -c '^python3' | grep -qx 1
```

## Step 2: apt-extras - Docker and WSL optional packages

Why: Docker Compose CLI and Python virtualenv packages are installed if the WSL host has a Docker container engine and WSLg.

```detect bash
docker info > /dev/null && test -d /etc/docker/daemon.json && test -f /etc/docker/daemon.json
```

```detect bash
readlink /usr/bin/docker-compose > /dev/null
```

```install bash
apt-get install -y \
    docker-compose \
    python3-pip \
    python3-venv \
    python3-dev \
    build-essential \
    unzip \
    gnupg
```

```verify bash
docker info > /dev/null && test -d /etc/docker/daemon.json && test -f /etc/docker/daemon.json
```

```verify bash
command -v docker-compose > /dev/null && docker-compose --version
```

## Step 3: mariadb - Database and Redis

Why: MariaDB 10.11 database with the `invoice_ninja` database and a Redis instance used by the queue worker.

```detect bash
docker exec -d ${CONTAINER_NAME:-invoices_redis} /usr/local/bin/docker-entrypoint.sh-redis-cli --redis-cli -p 6379 -c "SELECT COUNT(*) FROM invoice_ninja.redis_key_names" | grep -qx 1
```

```detect bash
/opt/docker-compose/docker-compose.yml