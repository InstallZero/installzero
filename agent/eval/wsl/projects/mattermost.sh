# Mattermost from the mattermost/docker repo, without its nginx container:
# .env from env.example, app on 8099, volume folders owned by uid 2000.
mkdir -p /opt
git clone --depth 1 https://github.com/mattermost/docker /opt/mattermost-docker
cd /opt/mattermost-docker
cp env.example .env
sed -i 's/^DOMAIN=.*/DOMAIN=chat.bench.lan/' .env
sed -i 's#^TZ=.*#TZ=America/Los_Angeles#' .env
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 16)/" .env
sed -i 's/^APP_PORT=.*/APP_PORT=8099/' .env
sed -i 's#^MM_SERVICESETTINGS_SITEURL=.*#MM_SERVICESETTINGS_SITEURL=http://${DOMAIN}:8099#' .env
chmod 600 .env
mkdir -p ./volumes/app/mattermost/{config,data,logs,plugins,client/plugins,bleve-indexes}
chown -R 2000:2000 ./volumes/app/mattermost
grep -q ' chat.bench.lan' /etc/hosts || echo '127.0.0.1 chat.bench.lan' >> /etc/hosts
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml pull -q
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml up -d
