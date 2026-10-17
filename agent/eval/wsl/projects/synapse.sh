# Synapse from PyPI in a venv, PostgreSQL (C collation), systemd unit, port 8090.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
SYNAPSE_VERSION=1.163.0
apt-get update -q
apt-get install -y -q build-essential python3-dev libffi-dev python3-pip python3-setuptools python3-venv sqlite3 \
  libssl-dev libpq-dev libjpeg-dev libxslt1-dev libicu-dev pkg-config postgresql curl openssl
systemctl enable --now postgresql

DB_PASS=$(openssl rand -hex 16)
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -c "CREATE USER synapse_user WITH PASSWORD '$DB_PASS';"
runuser -u postgres -- createdb --encoding=UTF8 --locale=C --template=template0 --owner=synapse_user synapse

adduser --system --group --home /opt/synapse synapse
python3 -m venv /opt/synapse/env
/opt/synapse/env/bin/pip install -q --upgrade pip setuptools wheel
/opt/synapse/env/bin/pip install -q "matrix-synapse[postgres]==$SYNAPSE_VERSION"
cd /opt/synapse
env/bin/python -m synapse.app.homeserver --server-name chat.bench.lan --config-path homeserver.yaml --generate-config --report-stats=no
DB_PASS="$DB_PASS" env/bin/python - <<'PY'
import os, yaml
p = 'homeserver.yaml'
c = yaml.safe_load(open(p))
c['listeners'][0]['port'] = 8090
c['listeners'][0]['bind_addresses'] = ['127.0.0.1']
c['database'] = {'name': 'psycopg2', 'args': {'user': 'synapse_user', 'password': os.environ['DB_PASS'], 'dbname': 'synapse', 'host': 'localhost', 'cp_min': 5, 'cp_max': 10}}
c['public_baseurl'] = 'http://chat.bench.lan:8090/'
c['enable_registration'] = False
c['max_upload_size'] = '100M'
yaml.safe_dump(c, open(p, 'w'), sort_keys=False)
PY
chown -R synapse:synapse /opt/synapse

cat > /etc/systemd/system/matrix-synapse.service <<'EOF'
[Unit]
Description=Synapse Matrix homeserver
After=network.target postgresql.service

[Service]
Type=notify
NotifyAccess=main
User=synapse
Group=synapse
WorkingDirectory=/opt/synapse
ExecStart=/opt/synapse/env/bin/python -m synapse.app.homeserver --config-path=/opt/synapse/homeserver.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now matrix-synapse
grep -q ' chat.bench.lan' /etc/hosts || echo '127.0.0.1 chat.bench.lan' >> /etc/hosts

for i in $(seq 1 60); do curl -fsS http://localhost:8090/health >/dev/null 2>&1 && break; sleep 2; done
/opt/synapse/env/bin/register_new_matrix_user -c /opt/synapse/homeserver.yaml -u admin -p "$(openssl rand -hex 12)" -a http://localhost:8090
