---
name: trinitycore
source: https://github.com/TrinityCore/TrinityCore
summary: TrinityCore 3.3.5a MMORPG server framework (authserver on TCP port 8470, worldserver on TCP port 8471)
---

# TrinityCore (3.3.5a) Server Installation

This guide installs and configures **TrinityCore** (`TDB335.24081`, commit `8938f383572a`) on Ubuntu 24.04 LTS under `{{INSTALL_DIR}}` (`/opt/trinitycore`).

What gets installed and configured:
- **Build toolchain & runtime libraries**: Clang 18, CMake 3.28, Make, GCC/G++, Boost 1.83 (`libboost-all-dev`), OpenSSL 3 (`libssl-dev`), MySQL 8.0 Server and client libraries (`mysql-server`, `libmysqlclient-dev`), Readline, Ncurses, BZip2, and `p7zip-full`.
- **Source & binaries**: TrinityCore source tree in `{{INSTALL_DIR}}/src`, compiled in `Release` mode (`-DTOOLS=0`) to produce `{{INSTALL_DIR}}/bin/authserver` and `{{INSTALL_DIR}}/bin/worldserver`.
- **Database (`mysql.service`)**: MySQL user `tcbench` and databases `tc_auth`, `tc_world`, and `tc_characters`. `tc_auth` is initialized from `sql/base/auth_database.sql` and auto-updated by `authserver`, with realm `InstallZero Bench` (`trinitycore.bench.lan:8471`) and administrator account `BENCHADMIN` (GM security level 3).
- **Systemd services**:
  - `trinity-authserver.service`: active and listening on `0.0.0.0:8470`.
  - `trinity-worldserver.service`: enabled and gated by `ConditionPathExists=/srv/trinitycore/data/dbc` (starts automatically once 3.3.5a client data files `dbc`, `maps`, `vmaps`, `mmaps` are placed in `/srv/trinitycore/data` and `TDB_full_world_335.24081_2024_08_17.sql` is available).

### Environment Variables / Secrets

- `TC_DB_PASSWORD`: Password for the MySQL user `tcbench` (if unset during install, a random 32-character hex secret is generated with `openssl rand -hex 16` and written to `/root/trinitycore-credentials.txt`).
- `TC_ADMIN_PASSWORD`: Password for the initial TrinityCore GM account `BENCHADMIN` (if unset during install, a random secret is generated and written to `/root/trinitycore-credentials.txt`).

---

## Step 1: build-deps - Install Build Toolchain, Libraries, and MySQL Server

Why: TrinityCore requires Clang/GCC, CMake, Boost, OpenSSL, Readline, Ncurses, BZip2, p7zip, and MySQL 8.0 Server & development headers.

```requires
os: linux
arch: x64
min_ram_gb: 4
min_disk_gb: 15
```

```detect bash
dpkg -s clang cmake make gcc g++ git p7zip-full \
  libboost-all-dev libbz2-dev libmysqlclient-dev \
  libncurses-dev libreadline-dev libssl-dev mysql-server >/dev/null 2>&1
```

```install bash
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y \
  clang=1:18.0-59~exp2 \
  cmake=3.28.3-1build7 \
  make=4.3-4.1build2 \
  gcc=4:13.2.0-7ubuntu1 \
  g++=4:13.2.0-7ubuntu1 \
  git \
  p7zip-full=16.02+transitional.1 \
  libboost-all-dev=1.83.0.1ubuntu2 \
  libbz2-dev=1.0.8-5.1ubuntu0.1 \
  libmysqlclient-dev=8.0.46-0ubuntu0.24.04.4 \
  libncurses-dev=6.4+20240113-1ubuntu2.2 \
  libreadline-dev=8.2-4build1 \
  libssl-dev=3.0.13-0ubuntu3.16 \
  mysql-server=8.0.46-0ubuntu0.24.04.4 || \
apt-get install -y \
  clang cmake make gcc g++ git p7zip-full \
  libboost-all-dev libbz2-dev libmysqlclient-dev \
  libncurses-dev libreadline-dev libssl-dev mysql-server
```

```verify bash
clang --version
cmake --version
mysqld --version
dpkg -s libboost-all-dev libmysqlclient-dev libssl-dev libreadline-dev libncurses-dev libbz2-dev >/dev/null
```

---

## Step 2: system-user-and-dirs - Create Trinity System User, Directories, and Host Entry

Why: The TrinityCore services run as the unprivileged `trinity` system user, log to `/var/log/trinitycore`, read client data from `/srv/trinitycore/data`, and resolve the realm hostname `trinitycore.bench.lan`.

```detect bash
id -u trinity >/dev/null 2>&1 && \
test -d "{{INSTALL_DIR}}" && \
test -d /srv/trinitycore/data && \
test -d /var/log/trinitycore && \
grep -q "trinitycore.bench.lan" /etc/hosts
```

```install bash
if ! getent group trinity >/dev/null 2>&1; then
  groupadd --system trinity
fi
if ! id -u trinity >/dev/null 2>&1; then
  useradd --system --gid trinity --home-dir "{{INSTALL_DIR}}" --no-create-home --shell /usr/sbin/nologin trinity
fi

mkdir -p "{{INSTALL_DIR}}" /srv/trinitycore/data /var/log/trinitycore
chown -R trinity:trinity /srv/trinitycore /var/log/trinitycore
chmod 755 /srv/trinitycore /srv/trinitycore/data /var/log/trinitycore

if ! grep -q "trinitycore.bench.lan" /etc/hosts; then
  echo "127.0.0.1 trinitycore.bench.lan" >> /etc/hosts
fi
```

```verify bash
getent passwd trinity
test -d "{{INSTALL_DIR}}"
test -d /srv/trinitycore/data
test -d /var/log/trinitycore
grep "trinitycore.bench.lan" /etc/hosts
```

---

## Step 3: checkout - Clone TrinityCore 3.3.5a Source Repository

Why: TrinityCore is built from source at release tag `TDB335.24081` (commit `8938f383572a`), and `authserver`/`worldserver` also reference `{{INSTALL_DIR}}/src/sql` at runtime for automatic database schema updates.

```detect bash
test -d "{{INSTALL_DIR}}/src/.git" && \
test "$(git -C "{{INSTALL_DIR}}/src" rev-parse --short=7 HEAD)" = "8938f38"
```

```install bash
rm -rf "{{INSTALL_DIR}}/src"
git clone --branch TDB335.24081 --depth 1 https://github.com/TrinityCore/TrinityCore.git "{{INSTALL_DIR}}/src"
git -C "{{INSTALL_DIR}}/src" checkout 8938f383572a
```

```verify bash
git -C "{{INSTALL_DIR}}/src" status
test "$(git -C "{{INSTALL_DIR}}/src" rev-parse --short=7 HEAD)" = "8938f38"
test -f "{{INSTALL_DIR}}/src/sql/base/auth_database.sql"
```

---

## Step 4: build - Compile and Install TrinityCore Server Binaries

Why: Compiles `authserver` and `worldserver` in `Release` mode using Clang and installs the binaries and default `.conf.dist` files into `{{INSTALL_DIR}}`.

```detect bash
test -x "{{INSTALL_DIR}}/bin/authserver" && \
test -x "{{INSTALL_DIR}}/bin/worldserver" && \
test -f "{{INSTALL_DIR}}/etc/authserver.conf.dist" && \
test -f "{{INSTALL_DIR}}/etc/worldserver.conf.dist"
```

```install bash
cmake -S "{{INSTALL_DIR}}/src" -B "{{INSTALL_DIR}}/src/build" \
  -DCMAKE_INSTALL_PREFIX="{{INSTALL_DIR}}" \
  -DCMAKE_C_COMPILER=clang \
  -DCMAKE_CXX_COMPILER=clang++ \
  -DCMAKE_BUILD_TYPE=Release \
  -DTOOLS=0 \
  -DWITH_WARNINGS=0

cmake --build "{{INSTALL_DIR}}/src/build" --parallel "$(nproc)"
cmake --install "{{INSTALL_DIR}}/src/build"
rm -rf "{{INSTALL_DIR}}/src/build"
```

```verify bash
test -x "{{INSTALL_DIR}}/bin/authserver"
test -x "{{INSTALL_DIR}}/bin/worldserver"
test -f "{{INSTALL_DIR}}/etc/authserver.conf.dist"
test -f "{{INSTALL_DIR}}/etc/worldserver.conf.dist"
ldd "{{INSTALL_DIR}}/bin/authserver" | grep -q libmysqlclient
```

---

## Step 5: database - Configure MySQL Databases, Realm List, and Admin Account

Why: Creates the `tc_auth`, `tc_world`, and `tc_characters` databases and `tcbench` MySQL user, imports the base authentication schema, sets up realm `InstallZero Bench` (`trinitycore.bench.lan:8471`), and registers the initial GM account `BENCHADMIN`.

```detect bash
systemctl is-active --quiet mysql.service && \
test -d /var/lib/mysql/tc_auth && \
test -d /var/lib/mysql/tc_world && \
test -d /var/lib/mysql/tc_characters && \
test -f /var/lib/mysql/tc_auth/realmlist.ibd && \
grep -a -q "InstallZero Bench" /var/lib/mysql/tc_auth/realmlist.ibd && \
grep -a -q "BENCHADMIN" /var/lib/mysql/tc_auth/account.ibd
```

```install bash
systemctl enable --now mysql.service

TC_DB_USER="tcbench"
TC_DB_PASSWORD="${TC_DB_PASSWORD:-$(openssl rand -hex 16)}"
TC_ADMIN_USER="BENCHADMIN"
TC_ADMIN_PASSWORD="${TC_ADMIN_PASSWORD:-$(openssl rand -hex 12)}"

cat > /root/trinitycore-credentials.txt <<EOF
DB user: ${TC_DB_USER}
DB password: ${TC_DB_PASSWORD}
Admin account: ${TC_ADMIN_USER}
Admin password: ${TC_ADMIN_PASSWORD}
EOF
chmod 600 /root/trinitycore-credentials.txt

mysql -u root <<SQL
CREATE USER IF NOT EXISTS '${TC_DB_USER}'@'localhost' IDENTIFIED BY '${TC_DB_PASSWORD}';
ALTER USER '${TC_DB_USER}'@'localhost' IDENTIFIED BY '${TC_DB_PASSWORD}';
CREATE USER IF NOT EXISTS '${TC_DB_USER}'@'127.0.0.1' IDENTIFIED BY '${TC_DB_PASSWORD}';
ALTER USER '${TC_DB_USER}'@'127.0.0.1' IDENTIFIED BY '${TC_DB_PASSWORD}';

CREATE DATABASE IF NOT EXISTS \`tc_world\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS \`tc_characters\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS \`tc_auth\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

GRANT ALL PRIVILEGES ON \`tc_world\`.* TO '${TC_DB_USER}'@'localhost' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON \`tc_characters\`.* TO '${TC_DB_USER}'@'localhost' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON \`tc_auth\`.* TO '${TC_DB_USER}'@'localhost' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON \`tc_world\`.* TO '${TC_DB_USER}'@'127.0.0.1' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON \`tc_characters\`.* TO '${TC_DB_USER}'@'127.0.0.1' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON \`tc_auth\`.* TO '${TC_DB_USER}'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL

mysql -u root tc_auth < "{{INSTALL_DIR}}/src/sql/base/auth_database.sql"

read -r SALT_HEX VERIFIER_HEX < <(python3 - "${TC_ADMIN_USER}" "${TC_ADMIN_PASSWORD}" <<'PY'
import hashlib, os, sys
user = sys.argv[1].upper()
password = sys.argv[2].upper()
N = int("894B645E89E1535BBDAD5B8B290650530801B18EBFBF5E8FAB3C82872A3E9BB7", 16)
g = 7
salt = os.urandom(32)
h1 = hashlib.sha1(f"{user}:{password}".encode("utf-8")).digest()
h2 = int.from_bytes(hashlib.sha1(salt + h1).digest(), "little")
verifier = pow(g, h2, N).to_bytes(32, "little")
print(salt.hex(), verifier.hex())
PY
)

mysql -u root tc_auth <<SQL
UPDATE realmlist
SET name='InstallZero Bench',
    address='trinitycore.bench.lan',
    localAddress='127.0.0.1',
    localSubnetMask='255.255.255.0',
    port=8471,
    gamebuild=12340
WHERE id=1;

DELETE FROM account_access WHERE AccountID=1;
DELETE FROM account WHERE id=1 OR username='${TC_ADMIN_USER}';
INSERT INTO account (id, username, salt, verifier, email, reg_mail, expansion)
VALUES (1, '${TC_ADMIN_USER}', UNHEX('${SALT_HEX}'), UNHEX('${VERIFIER_HEX}'), 'admin@trinitycore.bench.lan', 'admin@trinitycore.bench.lan', 2);

INSERT INTO account_access (AccountID, SecurityLevel, RealmID, Comment)
VALUES (1, 3, -1, 'bench admin');
SQL
```

```verify bash
systemctl is-active --quiet mysql.service
test -d /var/lib/mysql/tc_auth
test -d /var/lib/mysql/tc_world
test -d /var/lib/mysql/tc_characters
grep -a -q "InstallZero Bench" /var/lib/mysql/tc_auth/realmlist.ibd
grep -a -q "BENCHADMIN" /var/lib/mysql/tc_auth/account.ibd
test -f /root/trinitycore-credentials.txt
```

---

## Step 6: config - Configure authserver.conf and worldserver.conf

Why: Configures `authserver` (port `8470`, auto-update enabled) and `worldserver` (port `8471`, data directory `/srv/trinitycore/data`, headless mode `Console.Enable = 0`) with the MySQL connection strings and source directory.

```detect bash
test -f "{{INSTALL_DIR}}/etc/authserver.conf" && \
test -f "{{INSTALL_DIR}}/etc/worldserver.conf" && \
grep -q '^RealmServerPort = 8470' "{{INSTALL_DIR}}/etc/authserver.conf" && \
grep -q '^WorldServerPort = 8471' "{{INSTALL_DIR}}/etc/worldserver.conf" && \
grep -q '^DataDir = "/srv/trinitycore/data"' "{{INSTALL_DIR}}/etc/worldserver.conf"
```

```install bash
TC_DB_PASSWORD="${TC_DB_PASSWORD:-$(awk '/^DB password:/ {print $3}' /root/trinitycore-credentials.txt)}"

cp "{{INSTALL_DIR}}/etc/authserver.conf.dist" "{{INSTALL_DIR}}/etc/authserver.conf"
cp "{{INSTALL_DIR}}/etc/worldserver.conf.dist" "{{INSTALL_DIR}}/etc/worldserver.conf"

sed -i \
  -e 's|^LogsDir = .*|LogsDir = "/var/log/trinitycore"|' \
  -e 's|^RealmServerPort = .*|RealmServerPort = 8470|' \
  -e 's|^SourceDirectory *= .*|SourceDirectory = "{{INSTALL_DIR}}/src"|' \
  -e 's|^MySQLExecutable = .*|MySQLExecutable = "/usr/bin/mysql"|' \
  -e "s|^LoginDatabaseInfo *= .*|LoginDatabaseInfo = \"127.0.0.1;3306;tcbench;${TC_DB_PASSWORD};tc_auth\"|" \
  -e 's|^Updates.EnableDatabases = .*|Updates.EnableDatabases = 1|' \
  -e 's|^Updates.AutoSetup *= .*|Updates.AutoSetup = 1|' \
  "{{INSTALL_DIR}}/etc/authserver.conf"

sed -i \
  -e 's|^DataDir = .*|DataDir = "/srv/trinitycore/data"|' \
  -e 's|^LogsDir = .*|LogsDir = "/var/log/trinitycore"|' \
  -e "s|^LoginDatabaseInfo *= .*|LoginDatabaseInfo = \"127.0.0.1;3306;tcbench;${TC_DB_PASSWORD};tc_auth\"|" \
  -e "s|^WorldDatabaseInfo *= .*|WorldDatabaseInfo = \"127.0.0.1;3306;tcbench;${TC_DB_PASSWORD};tc_world\"|" \
  -e "s|^CharacterDatabaseInfo *= .*|CharacterDatabaseInfo = \"127.0.0.1;3306;tcbench;${TC_DB_PASSWORD};tc_characters\"|" \
  -e 's|^WorldServerPort = .*|WorldServerPort = 8471|' \
  -e 's|^SourceDirectory *= .*|SourceDirectory = "{{INSTALL_DIR}}/src"|' \
  -e 's|^MySQLExecutable = .*|MySQLExecutable = "/usr/bin/mysql"|' \
  -e 's|^Console.Enable = .*|Console.Enable = 0|' \
  "{{INSTALL_DIR}}/etc/worldserver.conf"

chown -R trinity:trinity "{{INSTALL_DIR}}/etc"
chmod 640 "{{INSTALL_DIR}}/etc/authserver.conf" "{{INSTALL_DIR}}/etc/worldserver.conf"
```

```verify bash
grep -q '^LogsDir = "/var/log/trinitycore"' "{{INSTALL_DIR}}/etc/authserver.conf"
grep -q '^RealmServerPort = 8470' "{{INSTALL_DIR}}/etc/authserver.conf"
grep -q '^Updates.EnableDatabases = 1' "{{INSTALL_DIR}}/etc/authserver.conf"
grep -q '^DataDir = "/srv/trinitycore/data"' "{{INSTALL_DIR}}/etc/worldserver.conf"
grep -q '^WorldServerPort = 8471' "{{INSTALL_DIR}}/etc/worldserver.conf"
grep -q '^Console.Enable = 0' "{{INSTALL_DIR}}/etc/worldserver.conf"
```

---

## Step 7: services - Register and Start TrinityCore Systemd Services

Why: Configures and enables `trinity-authserver.service` and `trinity-worldserver.service`, starts `trinity-authserver.service`, and verifies that the realm server is listening on TCP port `8470`.

```detect bash
test -f /etc/systemd/system/trinity-authserver.service && \
test -f /etc/systemd/system/trinity-worldserver.service && \
systemctl is-enabled --quiet trinity-authserver.service && \
systemctl is-enabled --quiet trinity-worldserver.service && \
systemctl is-active --quiet trinity-authserver.service && \
ss -tln | grep -q ':8470 '
```

```install bash
cat > /etc/systemd/system/trinity-authserver.service <<'EOF'
[Unit]
Description=TrinityCore authserver (3.3.5a login / realm list)
After=network-online.target mysql.service
Wants=network-online.target
Requires=mysql.service

[Service]
User=trinity
Group=trinity
WorkingDirectory={{INSTALL_DIR}}/bin
ExecStart={{INSTALL_DIR}}/bin/authserver -c {{INSTALL_DIR}}/etc/authserver.conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/trinity-worldserver.service <<'EOF'
[Unit]
Description=TrinityCore worldserver (needs extracted client data in /srv/trinitycore/data)
After=network-online.target mysql.service trinity-authserver.service
Requires=mysql.service
ConditionPathExists=/srv/trinitycore/data/dbc

[Service]
User=trinity
Group=trinity
WorkingDirectory={{INSTALL_DIR}}/bin
ExecStart={{INSTALL_DIR}}/bin/worldserver -c {{INSTALL_DIR}}/etc/worldserver.conf
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable trinity-authserver.service trinity-worldserver.service
systemctl restart trinity-authserver.service
systemctl start trinity-worldserver.service || true

for i in $(seq 1 15); do
  if ss -tln | grep -q ':8470 '; then
    break
  fi
  sleep 1
done
```

```verify bash
systemctl is-enabled --quiet trinity-authserver.service
systemctl is-enabled --quiet trinity-worldserver.service
systemctl is-active --quiet trinity-authserver.service
ss -tlnp | grep -q ':8470.*authserver'
grep -q 'Added realm "InstallZero Bench" at trinitycore.bench.lan:8471.' /var/log/trinitycore/Auth.log
```
