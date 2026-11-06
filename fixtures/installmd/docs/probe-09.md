---
name: airflow
source: https://github.com/apache/airflow
summary: Apache Airflow 2.9.3 scheduler, API server, and local executor with PostgreSQL 16 and virtual environment at /opt/airflow
---

# Airflow 2.9.3 install

Any prose: Apache Airflow 2.9.3 (`apache-airflow==2.9.3`) with PostgreSQL 16 (`postgresql-contrib`) and local executor (`LocalExecutor`) running under a dedicated system user (`airflow`) and virtual environment (`/opt/airflow/venv`), orchestrated by four systemd services: `airflow-api-server.service` (API server on port 8093), `airflow-dag-processor.service`, `airflow-scheduler.service`, and `airflow-triggerer.service`. The virtual environment is located at `{{INSTALL_DIR}}/airflow/venv` (`/opt/airflow/venv`), and the configuration file is at `{{INSTALL_DIR}}/airflow/airflow.cfg`.

## Step 1: apt-cache-command - apt-get -y postgresql postgresql-contrib python3-venv build-essential python3-dev libpq-dev 
Why: PostgreSQL 16 database and PostgreSQL client libraries, Python virtual environment tools, and Python 3.12 development headers.

```requires
os: linux
arch: x64
```

```detect bash
dpkg-query -W -f='${db} ${status}\n' postgresql postgresql-contrib python3-venv build-essential python3-dev libpq-dev | grep -q 'ok installed'
```

```install bash
sudo apt-get update
sudo apt-get install -y postgresql postgresql-contrib python3-venv build-essential python3-dev libpq-dev
```

```verify bash
psql -c '\l' -c '\du'
```

<|tool_call>call:write_install_md{content: