# paperless-ngx with Docker Compose (PostgreSQL variant), port 8098,
# German OCR, own time zone, admin created from the environment.
PAPERLESS_VERSION=3.3.2
mkdir -p /opt/paperless/consume /opt/paperless/export
cd /opt/paperless
BASE="https://raw.githubusercontent.com/paperless-ngx/paperless-ngx/v$PAPERLESS_VERSION/docker/compose"
curl -fsSL -o docker-compose.yml "$BASE/docker-compose.postgres.yml"
curl -fsSL -o docker-compose.env "$BASE/docker-compose.env"
curl -fsSL -o .env "$BASE/.env"
DB_PASS=$(openssl rand -hex 16)
sed -i "s#ghcr.io/paperless-ngx/paperless-ngx:latest#ghcr.io/paperless-ngx/paperless-ngx:$PAPERLESS_VERSION#" docker-compose.yml
sed -i 's/"8000:8000"/"8098:8000"/' docker-compose.yml
sed -i "s/POSTGRES_PASSWORD: paperless/POSTGRES_PASSWORD: $DB_PASS/" docker-compose.yml
sed -i "s/^PAPERLESS_SECRET_KEY=.*/PAPERLESS_SECRET_KEY=$(openssl rand -hex 32)/" docker-compose.env
cat >> docker-compose.env <<EOF
PAPERLESS_URL=http://paperless.bench.lan:8098
PAPERLESS_TIME_ZONE=America/Los_Angeles
PAPERLESS_OCR_LANGUAGE=deu
PAPERLESS_DBPASS=$DB_PASS
PAPERLESS_ADMIN_USER=admin
PAPERLESS_ADMIN_PASSWORD=$(openssl rand -hex 12)
EOF
chmod 600 docker-compose.env
grep -q ' paperless.bench.lan' /etc/hosts || echo '127.0.0.1 paperless.bench.lan' >> /etc/hosts
docker compose pull -q
docker compose up -d
