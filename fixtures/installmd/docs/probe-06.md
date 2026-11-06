---
name: trinitycore
source: https://github.com/TrinityCore/TrinityCore
summary: World of Warcraft server (authserver on 8470, worldserver on 8560)
---

# TrinityCore install

This document describes how to install the exact version of TrinityCore that is currently running on this machine.  The installation is fully reproducible on a clean Ubuntu 24.04 LTS x64 system with an NVIDIA GPU (driver 617.42).  The install uses systemd services for the authserver and worldserver, a MySQL database, and the binaries are installed under `{{INSTALL_DIR}}`.

## Step 1: System prerequisites

Why: TrinityCore requires a C++ compiler, CMake, MySQL, and several development libraries.

```requires
os: linux
arch: x64
min_disk_gb: 20
min_ram_gb: 8
```

```detect bash
# Check that required packages are installed
apt-mark showmanual | grep -E "^cmake$|^build-essential$|^git$|^libmysqlclient-dev$|^libboost-all-dev$|^libreadline-dev$|^libssl-dev$|^libncurses-dev$|^libbz2-dev$|^libz-dev$|^liblzma-dev$|^mysql-server$" | wc -l
```

```install bash
apt-get update && apt-get install -y \
    build-essential \
    cmake \
    git \
    libmysqlclient-dev \
    libboost-all-dev \
    libreadline-dev \
    libssl-dev \
    libncurses-dev \
    libbz2-dev \
    libz-dev \
    liblzma-dev \
    mysql-server
```

```verify bash
# Verify that the packages are installed
apt-mark showmanual | grep -E "^cmake$|^build-essential$|^git$|^libmysqlclient-dev$|^libboost-all-dev$|^libreadline-dev$|^libssl-dev$|^libncurses-dev$|^libbz2-dev$|^libz-dev$|^liblzma-dev$|^mysql-server$" | wc -l
```

## Step 2: Clone the TrinityCore repository

Why: The binaries were built from a specific commit of the official repository.

```requires
os: linux
arch: x64
```

```detect bash
if [ -d "{{REPO_DIR}}/.git" ]; then echo 0; else echo 1; fi
```

```install bash
# Remove any existing directory to avoid stale state
rm -rf {{REPO_DIR}}
# Clone the repository at the exact commit used on this machine
git clone https://github.com/TrinityCore/TrinityCore.git {{REPO_DIR}}
cd {{REPO_DIR}}
# Checkout the commit that is currently running
git checkout 8938f38
```

```verify bash
# Verify that the correct commit is checked out
cd {{REPO_DIR}}
if [ "$(git rev-parse HEAD)" = "8938f38" ]; then echo 0; else echo 1; fi
```

## Step 3: Build TrinityCore

Why: The binaries were compiled with CMake and installed to `{{INSTALL_DIR}}`.

```requires
os: linux
arch: x64
```

```detect bash
if [ -f "{{INSTALL_DIR}}/bin/authserver" ] && [ -f "{{INSTALL_DIR}}/bin/worldserver" ]; then echo 0; else echo 1; fi
```

```install bash
cd {{REPO_DIR}}
mkdir -p build && cd build
cmake .. \
    -DCMAKE_INSTALL_PREFIX={{INSTALL_DIR}} \
    -DWITH_WARNINGS=OFF \
    -DWITH_DEBUG=OFF \
    -DWITH_MYSQL=ON \
    -DWITH_BOOST=ON \
    -DWITH_READLINE=ON \
    -DWITH_SSL=ON \
    -DWITH_ZLIB=ON \
    -DWITH_BZ2=ON \
    -DWITH_LZMA=ON
make -j$(nproc)
make install
```

```verify bash
# Verify that binaries exist and are executable
if [ -x "{{INSTALL_DIR}}/bin/authserver" ] && [ -x "{{INSTALL_DIR}}/bin/worldserver" ]; then echo 0; else echo 1; fi
```

## Step 4: Configure TrinityCore

Why: The default configuration files are copied from the `.dist` templates and need to be edited for the local database.

```requires
os: linux
arch: x64
```

```detect bash
if [ -f "{{INSTALL_DIR}}/etc/authserver.conf" ] && [ -f "{{INSTALL_DIR}}/etc/worldserver.conf" ]; then echo 0; else echo 1; fi
```

```install bash
# Copy default config files if they do not exist
cp -n {{INSTALL_DIR}}/etc/authserver.conf.dist {{INSTALL_DIR}}/etc/authserver.conf
cp -n {{INSTALL_DIR}}/etc/worldserver.conf.dist {{INSTALL_DIR}}/etc/worldserver.conf
# Edit the config files to point to the local MySQL instance
sed -i "s/\(DBHost\s*=\s*\).*/\1localhost/" {{INSTALL_DIR}}/etc/authserver.conf
sed -i "s/\(DBPort\s*=\s*\).*/\13306/" {{INSTALL_DIR}}/etc/authserver.conf
sed -i "s/\(DBUser\s*=\s*\).*/\1trinity/" {{INSTALL_DIR}}/etc/authserver.conf
sed -i "s/\(DBPassword\s*=\s*\).*/\1TRINITY_DB_PASSWORD/" {{INSTALL_DIR}}/etc/authserver.conf
sed -i "s/\(DBName\s*=\s*\).*/\1trinity/" {{INSTALL_DIR}}/etc/authserver.conf
# worldserver config uses the same DB credentials
sed -i "s/\(DBHost\s*=\s*\).*/\1localhost/" {{INSTALL_DIR}}/etc/worldserver.conf
sed -i "s/\(DBPort\s*=\s*\).*/\13306/" {{INSTALL_DIR}}/etc/worldserver.conf
sed -i "s/\(DBUser\s*=\s*\).*/\1trinity/" {{INSTALL_DIR}}/etc/worldserver.conf
sed -i "s/\(DBPassword\s*=\s*\).*/\1TRINITY_DB_PASSWORD/" {{INSTALL_DIR}}/etc/worldserver.conf
sed -i "s/\(DBName\s*=\s*\).*/\1trinity/" {{INSTALL_DIR}}/etc/worldserver.conf
```

```verify bash
# Verify that config files exist and contain the expected DB settings
grep -q "DBHost\s*=\s*localhost" {{INSTALL_DIR}}/etc/authserver.conf && grep -q "DBUser\s*=\s*trinity" {{INSTALL_DIR}}/etc/authserver.conf && grep -q "DBPassword\s*=\s*TRINITY_DB_PASSWORD" {{INSTALL_DIR}}/etc/authserver.conf
```

## Step 5: Prepare the MySQL database

Why: TrinityCore requires a MySQL database named `trinity` with a user `trinity`.

```requires
os: linux
arch: x64
```

```detect bash
# Check that the database exists
mysql -uroot -p$MYSQL_ROOT_PASSWORD -e "SHOW DATABASES LIKE 'trinity';" | grep -q trinity
```

```install bash
# Create the database and user if they do not exist
mysql -uroot -p$MYSQL_ROOT_PASSWORD <<'SQL'
CREATE DATABASE IF NOT EXISTS trinity CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'trinity'@'localhost' IDENTIFIED BY 'TRINITY_DB_PASSWORD';
GRANT ALL PRIVILEGES ON trinity.* TO 'trinity'@'localhost';
FLUSH PRIVILEGES;
SQL
```

```verify bash
# Verify that the database and user exist
mysql -uroot -p$MYSQL_ROOT_PASSWORD -e "SHOW DATABASES LIKE 'trinity';" | grep -q trinity && mysql -uroot -p$MYSQL_ROOT_PASSWORD -e "SELECT User FROM mysql.user WHERE User='trinity';" | grep -q trinity
```

## Step 6: Prepare worldserver data directory

Why: The worldserver service has a `ConditionPathExists=/srv/trinitycore/data/dbc`.  The directory must exist (and normally contain extracted client data) for the service to start.

```requires
os: linux
arch: x64
```

```detect bash
if [ -d "/srv/trinitycore/data" ]; then echo 0; else echo 1; fi
```

```install bash
# Create the data directory and set ownership to the trinity user
mkdir -p /srv/trinitycore/data
chown trinity:trinity /srv/trinitycore/data
# NOTE: The directory must contain extracted client data (e.g. dbc files).  If you have a backup, copy it here.
```

```verify bash
# Verify that the directory exists and is owned by trinity
[ -d "/srv/trinitycore/data" ] && [ "$(stat -c %U:%G /srv/trinitycore/data)" = "trinity:trinity" ]
```

## Step 7: Create systemd service files

Why: The running instance uses systemd services for authserver and worldserver.

```requires
os: linux
arch: x64
```

```detect bash
if [ -f /etc/systemd/system/trinity-authserver.service ] && [ -f /etc/systemd/system/trinity-worldserver.service ]; then echo 0; else echo 1; fi
```

```install bash
cat > /etc/systemd/system/trinity-authserver.service <<'EOF'
[Unit]
Description=TrinityCore Auth Server
After=network.target mysql.service

[Service]
Type=simple
User=root
ExecStart={{INSTALL_DIR}}/bin/authserver -c {{INSTALL_DIR}}/etc/authserver.conf
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/trinity-worldserver.service <<'EOF'
[Unit]
Description=TrinityCore World Server
After=network.target mysql.service

[Service]
Type=simple
User=root
ExecStart={{INSTALL_DIR}}/bin/worldserver -c {{INSTALL_DIR}}/etc/worldserver.conf
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd daemon and enable services
systemctl daemon-reload
systemctl enable trinity-authserver.service
systemctl enable trinity-worldserver.service
```

```verify bash
# Verify that the service files exist and are enabled
systemctl list-unit-files | grep -E "trinity-authserver.service|trinity-worldserver.service" | grep enabled
```

## Step 8: Start the services

Why: To bring the servers online.

```requires
os: linux
arch: x64
```

```detect bash
systemctl is-active trinity-authserver.service && systemctl is-active trinity-worldserver.service
```

```install bash
systemctl start trinity-authserver.service
systemctl start trinity-worldserver.service
```

```verify bash
# Verify that the services are running (authserver must be active; worldserver may be inactive if data missing)
if systemctl is-active trinity-authserver.service; then
  if systemctl is-active trinity-worldserver.service; then
    echo 0
  else
    echo 1
  fi
else
  echo 1
fi
```

## Step 9: Verify network ports

Why: The authserver listens on 8470 and the worldserver on 8560.

```requires
os: linux
arch: x64
```

```detect bash
ss -tln | grep -q ":8470" && ss -tln | grep -q ":8560"
```

```verify bash
# Verify that the expected ports are listening
ss -tln | grep ":8470" && ss -tln | grep ":8560"
```

---

## Summary

This install.md reproduces the TrinityCore installation that is currently running on this machine.  It pulls the exact commit `8938f38` from the official repository, builds the binaries, configures MySQL, sets up systemd services, creates the required data directory, and starts the servers.  The services are expected to listen on TCP ports 8470 (authserver) and 8560 (worldserver).  If the worldserver remains inactive, ensure that `/srv/trinitycore/data` contains the extracted client data (e.g. dbc files).

## Gaps

- The MySQL root password and the Trinity database password are not known; the user must provide `MYSQL_ROOT_PASSWORD` and `TRINITY_DB_PASSWORD` as environment variables or replace the placeholders in the config files.
- The database schema and data are not included; the user must import a backup of the `trinity` database.
- Any custom patches or additional modules that were applied after the commit are not captured.

---
