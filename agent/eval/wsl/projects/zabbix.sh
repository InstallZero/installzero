# Zabbix 7.0 LTS server, frontend and agent from repo.zabbix.com, MySQL,
# Apache on 8096, frontend configured without the web wizard.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
# Port 80 is taken on this host (WSL distros share one network): packages
# must not start their services before they are moved to their own port.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d; chmod +x /usr/sbin/policy-rc.d
apt-get install -y -q curl openssl locales
locale-gen en_US.UTF-8
curl -fsSL -o /tmp/zabbix-release.deb https://repo.zabbix.com/zabbix/7.0/ubuntu/pool/main/z/zabbix-release/zabbix-release_latest_7.0+ubuntu24.04_all.deb
dpkg -i /tmp/zabbix-release.deb
apt-get update -q
# Port 80 is taken on this host (WSL distros share one network): packages
# must not start their services before they are moved to their own port.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d; chmod +x /usr/sbin/policy-rc.d
apt-get install -y -q mysql-server zabbix-server-mysql zabbix-frontend-php zabbix-apache-conf zabbix-sql-scripts zabbix-agent
systemctl start mysql; systemctl enable mysql

DB_PASS=$(openssl rand -hex 16)
mysql <<SQL
CREATE DATABASE zabbix CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'zabbix'@'localhost' IDENTIFIED BY '$DB_PASS';
GRANT ALL PRIVILEGES ON zabbix.* TO 'zabbix'@'localhost';
SET GLOBAL log_bin_trust_function_creators = 1;
SQL
zcat /usr/share/zabbix-sql-scripts/mysql/server.sql.gz | mysql --default-character-set=utf8mb4 -uzabbix -p"$DB_PASS" zabbix
mysql -e "SET GLOBAL log_bin_trust_function_creators = 0;"

sed -i "s/^# DBPassword=.*/DBPassword=$DB_PASS/" /etc/zabbix/zabbix_server.conf
sed -i 's/^# StartPollers=.*/StartPollers=10/' /etc/zabbix/zabbix_server.conf

example=$(find /usr/share/zabbix -name zabbix.conf.php.example | head -1)
cp "$example" /etc/zabbix/web/zabbix.conf.php
sed -i "s/^\(\$DB\['PASSWORD'\]\s*=\s*\)'';/\1'$DB_PASS';/" /etc/zabbix/web/zabbix.conf.php
sed -i "s/^\/\/ \$ZBX_SERVER_NAME\s*=.*/\$ZBX_SERVER_NAME = 'Bench Zabbix';/; s/^\$ZBX_SERVER_NAME\s*=.*/\$ZBX_SERVER_NAME = 'Bench Zabbix';/" /etc/zabbix/web/zabbix.conf.php
chown www-data:www-data /etc/zabbix/web/zabbix.conf.php
chmod 600 /etc/zabbix/web/zabbix.conf.php

rm -f /usr/sbin/policy-rc.d
sed -i 's/^Listen 80$/Listen 8096/' /etc/apache2/ports.conf
sed -i 's/<VirtualHost \*:80>/<VirtualHost *:8096>/' /etc/apache2/sites-available/000-default.conf
systemctl restart zabbix-server zabbix-agent apache2
systemctl enable zabbix-server zabbix-agent apache2
