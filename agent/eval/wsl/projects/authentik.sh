# authentik with Docker Compose: generated secrets in .env, ports 9010/9453,
# pinned tag, bootstrap admin from the environment.
AUTHENTIK_TAG=2026.8.4
mkdir -p /opt/authentik
cd /opt/authentik
curl -fsSL -o docker-compose.yml https://docs.goauthentik.io/compose.yml
cat > .env <<EOF
PG_PASS=$(openssl rand -base64 36 | tr -d '\n/+=')
AUTHENTIK_SECRET_KEY=$(openssl rand -base64 60 | tr -d '\n/+=')
AUTHENTIK_TAG=$AUTHENTIK_TAG
COMPOSE_PORT_HTTP=9010
COMPOSE_PORT_HTTPS=9453
AUTHENTIK_ERROR_REPORTING__ENABLED=false
AUTHENTIK_BOOTSTRAP_EMAIL=admin@bench.lan
AUTHENTIK_BOOTSTRAP_PASSWORD=$(openssl rand -hex 12)
EOF
chmod 600 .env
docker compose pull -q
docker compose up -d
