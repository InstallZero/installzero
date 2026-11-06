---
name: netbox
source: https://github.com/netbox-community/netbox-docker (or equivalent stable source)
summary: NetBox 4.7.3 deployed with PostgreSQL 16, Redis, Nginx reverse proxy, and Gunicorn WSGI service.
---

# NetBox 4.7.3 install

This document records the procedure used to install NetBox 4.7.3 on this Ubuntu 24.04 LTS machine. NetBox is installed in `/opt/netbox` with Python 3.12, a virtual environment (`/opt/netbox/venv`), and the application directory (`/opt/netbox/netbox`). It uses PostgreSQL 16 as its database and Redis 7 as its cache backend. Nginx proxies HTTP requests to the Gunicorn WSGI service on port 8001, and a systemd request queue worker (`netbox-rq.service`) consumes Redis queue jobs.

**Secrets**: The database password (`NETBOX_DB_PASSWORD`) and the Redis password (`REDIS_PASSWORD`) are secrets and must be provided by the user.

## Step 1: apt-cache-prereq-packages - apt packages

Why: Install build tools, Python 3.12, PostgreSQL 16 client and contrib packages, Redis server, and Nginx.

```requires
os: linux
arch: x64
min_ram_gb: 2
min_disk_gb: 10
```

```detect bash
test -x /usr/lib/postgresql/16/bin/postgres && test -x /usr/lib/postgresql/16/bin/redis-server && test -x /usr/sbin/nginx
```

```install bash
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y \
  build-essential \
  libpq-dev \
  python3-dev \
  python3-venv \
  python3-pip \
  python3-pip-stdlib \
  python3-pip-venv \
  python3-pip-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-venv-stdlib-dev \
  python3-pip-stdlib-dev
```

```verify bash
test -x /usr/lib/postgresql/16/bin/postgres && test -x /usr/lib/postgresql/16/bin/redis-server && test -x /usr/sbin/nginx
```

## Step 2: postgresql-16-database - PostgreSQL 16 database and user

Why: Create the PostgreSQL 16 database `netbox_db` with owner `netbox`, and the PostgreSQL role `netbox` with password authentication.

```detect bash
sudo -u postgres psql -tAc "select 1 from pg_database where datname='netbox_db'" | grep -q 1
sudo -u postgres psql -tAc "select 1 from pg_roles where rolname='netbox'" | grep -q 1
```

```install bash
export PGPASSWORD="${NETBOX_DB_PASSWORD}"
sudo -u postgres psql -c "CREATE USER netbox WITH ENCRYPTED PASSWORD '${NETBOX_DB_PASSWORD}' NOT NULL;"
sudo -u postgres psql -c "CREATE DATABASE netbox_db OWNER netbox;"
```

```verify bash
sudo -u postgres psql -tAc "select 1 from pg_database where datname='netbox_db'" | grep -q 1
sudo -u postgres psql -tAc "select 1 from pg_roles where rolname='netbox'" | grep -q 1
```

## Step 3: redis-server-config - Redis configuration

Why: Configure Redis to listen on 127.0.0.1:6379, reject passwords, and use the redis-cli password authentication model.

```detect bash
test -f /etc/redis/redis.conf && grep -qE '^(requirepass|bind|port|supervised) ' /etc/redis/redis.conf
```

```install bash
cat > /etc/redis/redis.conf <<EOF
requirepass ${REDIS_PASSWORD}
bind 127.0.0.1
port 6379
supervised systemd
maxmemory-policy allkeys-lru
maxmemory 256mb
EOF
```

```verify redis-cli -a "${REDIS_PASSWORD}" -p 6379 ping
```

## Step 4: redis-service - Redis systemd service

Why: Ensure Redis restarts automatically on failure.

```detect bash
systemctl is-active redis-server
```

```install bash
systemctl enable --now redis-server
```

```verify bash
systemctl is-active redis-server
```

## Step 5: nginx-site - Nginx reverse proxy site

Why: Configure Nginx to listen on port 8091, serve NetBox static files from `alias /opt/netbox/netbox/static/`, and proxy HTTP requests to Gunicorn on port 8001.

```detect bash
curl -fsS "http://netbox.bench.lan:8091/api/v1/version/" | grep -q "NetBox"
```

```install bash
cat > /etc/nginx/sites-available/netbox <<'EOF'
server {
    listen 8091;
    server_name netbox.bench.lan localhost;
    client_max_body_size 25m;
    location /static/ { alias /opt/netbox/netbox/static/; }
    location / {
        proxy_pass http://127.0.0.1:8001;
        proxy_set_header X-Forwarded-Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
EOF
ln -sf /etc/nginx/sites-available/netbox /etc/nginx/sites-enabled/netbox
systemctl reload nginx
```

```verify bash
curl -fsS "http://netbox.bench.lan:8091/api/v1/version/" | grep -q "NetBox"
```

## Step 6: netbox-venv - Python virtual environment and pip packages

Why: Create the Python 3.12 virtual environment at `{{REPO_DIR}}/venv`, install `python-venv`, `python-pip`, `pip`, `setuptools`, `wheel`, and build Python packages `psycopg2-binary`, `pillow`, and `python-dotenv`.

```detect bash
test -d "{{REPO_DIR}}/venv/lib/python3.12/site-packages/pip-28.2.dev.dist-info/REQUESTEDTOINSTALL" && \
test -d "{{REPO_DIR}}/venv/lib/python3.12/site-packages/pip-28.2.dev.dist-info/direct_url.json"
```

```install bash
python3 -m venv "{{REPO_DIR}}/venv"
source "{{REPO_DIR}}/venv/bin/activate"
pip install --upgrade pip setuptools wheel
pip install \
  pip-venv \
  python-venv \
  python-venv-stdlib \
  python-pip \
  python-pip-stdlib \
  python-pip-venv \
  python-pip-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-venv-stdlib-dev \
  python-pip-stdlib-dev \
  python-pip-venv-stdlib-dev
pip install \
  psycopg2-binary \
  pillow \
  python-dotenv
```

```verify bash
{{REPO_DIR}}/venv/bin/pip show pip setuptools wheel psycopg2-binary pillow
```

## Step 7: netbox-pip-packages - virtualenv Python package extras

Why: Install the Python package extras `netbox` and `netbox-redis` into `{{REPO_DIR}}/venv`.

```detect bash
{{REPO_DIR}}/venv/bin/pip show netbox netbox-redis
```

```install bash
source "{{REPO_DIR}}/venv/bin/activate"
pip install netbox netbox-redis
```

```verify bash
{{REPO_DIR}}/venv/bin/pip show netbox netbox-redis
```

## Step 8: netbox-db-migrate - PostgreSQL database migration and NetBox database creation

Why: Create the PostgreSQL database schema for NetBox and populate the `{{REPO_DIR}}/venv/netbox/settings.py` database credentials, secret key, and `VERSION` table.

```detect bash
{{REPO_DIR}}/venv/bin/python3 -c 'import psycopg2; conn=psycopg2.connect(dbname="netbox_db", user="netbox", password=None); cursor=conn.cursor(); cursor.execute("select 1 from pg_database where datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles where rolname=\'netbox\'\'"); cursor.execute("select 1 from pg_user WHERE usename=\'netbox\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_database WHERE datname=\'postgres\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'postgres\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'netbox\'\'")' | grep -q 1
```

```install bash
{{REPO_DIR}}/venv/bin/python3 -c 'import psycopg2; conn=psycopg2.connect(dbname="netbox_db", user="netbox", password=None); cursor=conn.cursor(); cursor.execute("select 1 from pg_database where datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles where rolname=\'netbox\'\'"); cursor.execute("select 1 from pg_user WHERE usename=\'netbox\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_database WHERE datname=\'postgres\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'postgres\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'netbox\'\'")'
{{REPO_DIR}}/venv/bin/python3 manage.py migrate --noinput
{{REPO_DIR}}/venv/bin/python3 manage.py collectstatic --noinput
redis-cli -a "redis" -p 6379 set netbox:cache_status "ok"
```

```verify bash
{{REPO_DIR}}/venv/bin/python3 manage.py migrate --noinput
redis-cli -a "redis" -p 6379 get netbox:cache_status
```

## Step 9: netbox-redis - NetBox Redis settings

Why: Add the Redis cache backend configuration to `{{REPO_DIR}}/venv/netbox/settings.py`.

```detect bash
grep -q "REDIS_HOST = '127.0.0.1'" "{{REPO_DIR}}/venv/netbox/settings.py" && \
grep -q "REDIS_PORT = 6379" "{{REPO_DIR}}/venv/netbox/settings.py" && \
grep -q "REDIS_PASSWORD = 'redis'" "{{REPO_DIR}}/venv/netbox/settings.py"
```

```install bash
sed -i.bak 's/REDIS_CONFIG_FILES = .*/REDIS_CONFIG_FILES = [{{REPO_DIR}}/venv/netbox/settings.py]/g' "{{REPO_DIR}}/venv/netbox/settings.py"
sed -i.bak 's/REDIS_HOST = .*/REDIS_HOST = '\''127.0.0.1'\''/g' "{{REPO_DIR}}/venv/netbox/settings.py"
sed -i.bak 's/REDIS_PORT = .*/REDIS_PORT = 6379/g' "{{REPO_DIR}}/venv/netbox/settings.py"
sed -i.bak 's/REDIS_PASSWORD = .*/REDIS_PASSWORD = '\''redis'\''/g' "{{REPO_DIR}}/venv/netbox/settings.py"
rm -f "{{REPO_DIR}}/venv/netbox/settings.py.bak"
```

```verify bash
grep -q "REDIS_HOST = '127.0.0.1'" "{{REPO_DIR}}/venv/netbox/settings.py" && \
grep -q "REDIS_PORT = 6379" "{{REPO_DIR}}/venv/netbox/settings.py" && \
grep -q "REDIS_PASSWORD = 'redis'" "{{REPO_DIR}}/venv/netbox/settings.py"
```

## Step 10: netbox-venv-setup - NetBox database migrations and Redis cache setup

Why: Apply NetBox database migrations and create the initial Redis cache keyspace.

```detect bash
{{REPO_DIR}}/venv/bin/python3 -c 'import psycopg2; conn=psycopg2.connect(dbname="netbox_db", user="netbox", password=None); cursor=conn.cursor(); cursor.execute("select 1 from pg_database where datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles where rolname=\'netbox\'\'"); cursor.execute("select 1 from pg_user WHERE usename=\'netbox\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_database WHERE datname=\'postgres\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'postgres\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'netbox\'\'")' | grep -q 1
```

```install bash
{{REPO_DIR}}/venv/bin/python3 -c 'import psycopg2; conn=psycopg2.connect(dbname="netbox_db", user="netbox", password=None); cursor=conn.cursor(); cursor.execute("select 1 from pg_database where datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles where rolname=\'netbox\'\'"); cursor.execute("select 1 from pg_user WHERE usename=\'netbox\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_database WHERE datname=\'postgres\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'postgres\'"); cursor.execute("select 1 from pg_database WHERE datname=\'netbox_db\'"); cursor.execute("select 1 from pg_roles WHERE rolname=\'netbox\'\'")'
{{REPO_DIR}}/venv/bin/python3 manage.py migrate --noinput
{{REPO_DIR}}/venv/bin/python3 manage.py collectstatic --noinput
redis-cli -a "redis" -p 6379 set netbox:cache_status "ok"
```

```verify bash
{{REPO_DIR}}/venv/bin/python3 manage.py migrate --noinput
redis-cli -a "redis" -p 6379 get netbox:cache_status
```

## Step 11: netbox-redis-cache - NetBox Redis cache setup

Why: Create the NetBox Redis cache keyspace and populate `{{REPO_DIR}}/venv/netbox/settings.py` with the Redis password `redis` and `REDIS_CACHE_BACKEND = redis`.

```detect bash
redis-cli -a "redis" -p 6379 keys *:netbox:cache_status
```

```install bash
redis-cli -a "redis" -p 6379 set netbox:cache_status "ok"
```

```verify redis-cli -a "redis" -p 6379 keys *:netbox:cache_status
```

## Step 12: netbox-rq-worker - NetBox request queue worker

Why: Create and start the NetBox Request Queue Worker systemd service.

```detect bash
systemctl is-active netbox-rq.service
```

```install bash
systemctl enable --now netbox-rq.service
```

```verify bash
systemctl is-active netbox-rq.service
```

## Step 13: netbox-gunicorn - NetBox Gunicorn WSGI service

Why: Create and start the NetBox Gunicorn WSGI service.

```detect bash
systemctl is-active netbox.service
```

```install bash
systemctl enable --now netbox.service
```

```verify bash
systemctl is-active netbox.service
```

## Step 14: nginx-reload - Reload Nginx

Why: Reload Nginx configuration to serve NetBox on port 8091.

```detect bash
curl -fsS "http://netbox.bench.lan:8091/api/v1/version/" | grep -q "NetBox"
```

```install bash
systemctl reload nginx
```

```verify bash
curl -fsS "http://netbox.bench.lan:8091/api/v1/version/" | grep -q "NetBox"
```
