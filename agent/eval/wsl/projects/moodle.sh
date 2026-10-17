# Moodle 4.5 LTS: nginx, PHP 8.3 FPM, MariaDB, CLI install, cron every minute.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
# Port 80 is taken on this host (WSL distros share one network): packages
# must not start their services before they are moved to their own port.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d; chmod +x /usr/sbin/policy-rc.d
apt-get install -y -q nginx mariadb-server cron curl openssl \
  php8.3-fpm php8.3-cli php8.3-mysql php8.3-curl php8.3-gd php8.3-intl php8.3-mbstring php8.3-xml php8.3-zip php8.3-soap php8.3-bcmath php8.3-opcache
rm -f /usr/sbin/policy-rc.d; rm -f /etc/nginx/sites-enabled/default
systemctl enable --now mariadb php8.3-fpm nginx cron

for d in fpm cli; do printf 'max_input_vars = 5000\nmemory_limit = 512M\nupload_max_filesize = 64M\npost_max_size = 64M\n' > /etc/php/8.3/$d/conf.d/99-moodle.ini; done
systemctl restart php8.3-fpm

DB_PASS=$(openssl rand -hex 16)
mysql <<SQL
CREATE DATABASE moodle DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'moodleuser'@'localhost' IDENTIFIED BY '$DB_PASS';
GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,CREATE TEMPORARY TABLES,DROP,INDEX,ALTER ON moodle.* TO 'moodleuser'@'localhost';
SQL

cd /var/www
curl -fsSL -o moodle.tgz https://download.moodle.org/download.php/direct/stable405/moodle-latest-405.tgz
tar xzf moodle.tgz
mkdir -p /var/moodledata
chown -R www-data:www-data /var/www/moodle /var/moodledata
chmod 0770 /var/moodledata

runuser -u www-data -- php /var/www/moodle/admin/cli/install.php --non-interactive --agree-license --lang=en \
  --wwwroot=http://localhost:8092 --dataroot=/var/moodledata \
  --dbtype=mariadb --dbhost=localhost --dbname=moodle --dbuser=moodleuser --dbpass="$DB_PASS" --prefix=mdl_ \
  --fullname="Bench Academy" --shortname=bench --summary="Install bench" \
  --adminuser=admin --adminpass="$(openssl rand -hex 10)Aa1!" --adminemail=admin@bench.lan --supportemail=admin@bench.lan
chown -R root:root /var/www/moodle
chmod 0644 /var/www/moodle/config.php

cat > /etc/nginx/sites-available/moodle <<'EOF'
server {
    listen 8092;
    server_name localhost;
    root /var/www/moodle;
    index index.php;
    client_max_body_size 64m;
    location ~ [^/]\.php(/|$) {
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_index index.php;
        fastcgi_pass unix:/run/php/php8.3-fpm.sock;
        include fastcgi_params;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    }
    location ~ (/vendor/|/node_modules/|composer\.json|/readme|/README|readme\.txt|/upgrade\.txt|/UPGRADING\.md|db/install\.xml|/fixtures/|/behat/|phpunit\.xml|\.lock|environment\.xml) { deny all; return 404; }
}
EOF
rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/moodle /etc/nginx/sites-enabled/moodle
systemctl restart nginx
echo '* * * * * www-data /usr/bin/php /var/www/moodle/admin/cli/cron.php >/dev/null 2>&1' > /etc/cron.d/moodle
