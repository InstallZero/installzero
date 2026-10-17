# Apache Airflow 3 from PyPI with the constraints file, PostgreSQL metadata DB,
# LocalExecutor, api-server on 8093, components as systemd units.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
AIRFLOW_VERSION=3.3.2
PY=3.12
apt-get update -q
apt-get install -y -q python3-venv python3-dev build-essential libpq-dev postgresql curl openssl
systemctl enable --now postgresql

DB_PASS=$(openssl rand -hex 16)
runuser -u postgres -- psql -v ON_ERROR_STOP=1 <<SQL
CREATE DATABASE airflow_db;
CREATE USER airflow_user WITH PASSWORD '$DB_PASS';
ALTER DATABASE airflow_db OWNER TO airflow_user;
SQL

adduser --system --group --home /opt/airflow --shell /bin/bash airflow
runuser -u airflow -- python3 -m venv /opt/airflow/venv
runuser -u airflow -- /opt/airflow/venv/bin/pip install -q --upgrade pip
runuser -u airflow -- /opt/airflow/venv/bin/pip install -q "apache-airflow[postgres]==$AIRFLOW_VERSION" \
  --constraint "https://raw.githubusercontent.com/apache/airflow/constraints-$AIRFLOW_VERSION/constraints-$PY.txt"

cat > /etc/default/airflow <<EOF
AIRFLOW_HOME=/opt/airflow
AIRFLOW__DATABASE__SQL_ALCHEMY_CONN=postgresql+psycopg2://airflow_user:$DB_PASS@localhost/airflow_db
AIRFLOW__CORE__EXECUTOR=LocalExecutor
AIRFLOW__CORE__LOAD_EXAMPLES=False
AIRFLOW__CORE__DEFAULT_TIMEZONE=America/Los_Angeles
AIRFLOW__API__PORT=8093
AIRFLOW__CORE__EXECUTION_API_SERVER_URL=http://localhost:8093/execution/
AIRFLOW__CORE__SIMPLE_AUTH_MANAGER_USERS=admin:admin
EOF
chmod 640 /etc/default/airflow; chgrp airflow /etc/default/airflow

mkdir -p /opt/airflow/dags
cat > /opt/airflow/dags/bench_hello.py <<'EOF'
from datetime import datetime
from airflow.sdk import dag, task

@dag(schedule="@daily", start_date=datetime(2026, 1, 1), catchup=False, tags=["bench"])
def bench_hello():
    @task
    def hello():
        print("hello from the install bench")
    hello()

bench_hello()
EOF
chown -R airflow:airflow /opt/airflow
runuser -u airflow -- bash -c 'set -a; . /etc/default/airflow; /opt/airflow/venv/bin/airflow db migrate'

for c in api-server scheduler dag-processor triggerer; do
cat > /etc/systemd/system/airflow-$c.service <<EOF
[Unit]
Description=Airflow $c
After=network.target postgresql.service
Wants=postgresql.service

[Service]
EnvironmentFile=/etc/default/airflow
User=airflow
Group=airflow
ExecStart=/opt/airflow/venv/bin/airflow $c
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
done
systemctl daemon-reload
systemctl enable --now airflow-api-server airflow-scheduler airflow-dag-processor airflow-triggerer
