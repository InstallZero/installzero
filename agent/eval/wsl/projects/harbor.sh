# Harbor online installer on Ubuntu 24.04 with Docker Engine, as the docs
# describe; HTTP on 8880 without TLS, its own hostname, Trivy scanner.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
HARBOR_VERSION=v2.15.4
HARBOR_HOST=harbor.bench.lan
HARBOR_PORT=8880

# Docker Engine from Docker's apt repository
apt-get update -q
apt-get install -y -q ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
apt-get update -q
apt-get install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker

# Name Harbor serves under
grep -q " $HARBOR_HOST" /etc/hosts || echo "127.0.0.1 $HARBOR_HOST" >> /etc/hosts

# Harbor
cd /opt
curl -fsSL -o harbor-online-installer.tgz "https://github.com/goharbor/harbor/releases/download/$HARBOR_VERSION/harbor-online-installer-$HARBOR_VERSION.tgz"
tar xzf harbor-online-installer.tgz
cd /opt/harbor
cp harbor.yml.tmpl harbor.yml
sed -i "s/^hostname: .*/hostname: $HARBOR_HOST/" harbor.yml
sed -i "0,/^  port: 80$/s//  port: $HARBOR_PORT/" harbor.yml
awk 'BEGIN{h=0} /^https:/{h=1; print "# " $0; next} h && (/^  / || /^$/){ if ($0 ~ /^$/) {h=0; print; next} print "# " $0; next} {h=0; print}' harbor.yml > harbor.yml.new && mv harbor.yml.new harbor.yml
sed -i "s/^harbor_admin_password: .*/harbor_admin_password: $(openssl rand -hex 12)/" harbor.yml
sed -i "0,/^  password: root123$/s//  password: $(openssl rand -hex 12)/" harbor.yml
./install.sh --with-trivy

# Docker's restart policy does not bring Harbor back after a reboot (its
# containers log to harbor-log over syslog), so a unit starts it.
cat > /etc/systemd/system/harbor.service <<'EOF'
[Unit]
Description=Harbor container registry
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/harbor
ExecStart=/usr/bin/docker compose -f /opt/harbor/docker-compose.yml up -d
ExecStop=/usr/bin/docker compose -f /opt/harbor/docker-compose.yml down

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable harbor.service

for i in $(seq 1 60); do
  curl -fsS "http://localhost:$HARBOR_PORT/api/v2.0/health" | grep -q '"status":"healthy"' && break
  sleep 5
done
curl -fsS "http://localhost:$HARBOR_PORT/api/v2.0/health"
