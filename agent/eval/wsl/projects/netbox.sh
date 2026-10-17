# NetBox from the release archive, as the docs describe: PostgreSQL, Redis,
# venv via upgrade.sh, gunicorn and netbox-rq under systemd, nginx on 8091.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
NETBOX_VERSION=4.7.3
apt-get update -q
# Port 80 is taken on this host (WSL distros share one network): packages
# must not start their services before they are moved to their own port.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d; chmod +x /usr/sbin/policy-rc.d
apt-get install -y -q postgresql redis-server python3 python3-pip python3-venv python3-dev build-essential \
  libxml2-dev libxslt1-dev libffi-dev libpq-dev libssl-dev zlib1g-dev nginx curl openssl
rm -f /usr/sbin/policy-rc.d
systemctl enable --now postgresql redis-server
systemctl stop nginx 2>/dev/null || true

DB_PASS=$(openssl rand -hex 16)
runuser -u postgres -- psql -v ON_ERROR_STOP=1 <<SQL
CREATE DATABASE netbox;
CREATE USER netbox WITH PASSWORD '$DB_PASS';
ALTER DATABASE netbox OWNER TO netbox;
SQL

cd /opt
curl -fsSL -o netbox.tar.gz "https://github.com/netbox-community/netbox/archive/refs/tags/v$NETBOX_VERSION.tar.gz"
tar xzf netbox.tar.gz
ln -sfn "/opt/netbox-$NETBOX_VERSION" /opt/netbox
adduser --system --group netbox
chown --recursive netbox /opt/netbox/netbox/media/ /opt/netbox/netbox/reports/ /opt/netbox/netbox/scripts/

cd /opt/netbox/netbox/netbox
cp configuration_example.py configuration.py
SECRET_KEY=$(python3 ../generate_secret_key.py)
DB_PASS="$DB_PASS" SECRET_KEY="$SECRET_KEY" python3 - <<'PY'
import os, re
p = 'configuration.py'
s = open(p).read()
s = s.replace("ALLOWED_HOSTS = []", "ALLOWED_HOSTS = ['netbox.bench.lan', 'localhost', '127.0.0.1']", 1)
s = re.sub(r"('USER': )''", r"\1'netbox'", s, count=1)
s = re.sub(r"('PASSWORD': )''", lambda m: m.group(1) + repr(os.environ['DB_PASS']), s, count=1)
s = s.replace("SECRET_KEY = ''", "SECRET_KEY = " + repr(os.environ['SECRET_KEY']), 1)
s = re.sub(r"TIME_ZONE = '[^']*'", "TIME_ZONE = 'America/Los_Angeles'", s, count=1)
s = re.sub(r"LOGIN_REQUIRED = \w+", "LOGIN_REQUIRED = True", s, count=1)
open(p, 'w').write(s)
PY

/opt/netbox/upgrade.sh
cd /opt/netbox/netbox
DJANGO_SUPERUSER_PASSWORD=$(openssl rand -hex 12) /opt/netbox/venv/bin/python3 manage.py createsuperuser --no-input --username admin --email admin@bench.lan
ln -sf /opt/netbox/contrib/netbox-housekeeping.sh /etc/cron.daily/netbox-housekeeping

cp /opt/netbox/contrib/gunicorn.py /opt/netbox/gunicorn.py
cp /opt/netbox/contrib/*.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now netbox netbox-rq

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
rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/netbox /etc/nginx/sites-enabled/netbox
systemctl restart nginx
grep -q ' netbox.bench.lan' /etc/hosts || echo '127.0.0.1 netbox.bench.lan' >> /etc/hosts
