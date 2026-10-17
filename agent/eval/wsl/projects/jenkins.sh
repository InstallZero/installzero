# Jenkins LTS from pkg.jenkins.io on Java 21, port 8095 via a systemd override,
# setup wizard off, plugins preinstalled, admin user from Configuration as Code.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q fontconfig openjdk-21-jre-headless curl openssl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL -o /etc/apt/keyrings/jenkins-keyring.asc https://pkg.jenkins.io/debian-stable/jenkins.io-2026.key
echo "deb [signed-by=/etc/apt/keyrings/jenkins-keyring.asc] https://pkg.jenkins.io/debian-stable binary/" > /etc/apt/sources.list.d/jenkins.list
apt-get update -q
apt-get install -y -q jenkins
systemctl stop jenkins

curl -fsSL -o /tmp/jenkins-plugin-manager.jar https://github.com/jenkinsci/plugin-installation-manager-tool/releases/download/2.15.0/jenkins-plugin-manager-2.15.0.jar
java -jar /tmp/jenkins-plugin-manager.jar --war /usr/share/java/jenkins.war --plugin-download-directory /var/lib/jenkins/plugins \
  --plugins git workflow-aggregator configuration-as-code credentials-binding timestamper
chown -R jenkins:jenkins /var/lib/jenkins/plugins

cat > /var/lib/jenkins/casc.yaml <<'EOF'
jenkins:
  systemMessage: "Install bench Jenkins"
  numExecutors: 2
  securityRealm:
    local:
      allowsSignup: false
      users:
        - id: admin
          password: ${JENKINS_ADMIN_PASSWORD}
  authorizationStrategy:
    loggedInUsersCanDoAnything:
      allowAnonymousRead: false
unclassified:
  location:
    url: http://localhost:8095/
EOF
chown jenkins:jenkins /var/lib/jenkins/casc.yaml

mkdir -p /etc/systemd/system/jenkins.service.d
cat > /etc/systemd/system/jenkins.service.d/override.conf <<EOF
[Service]
Environment="JENKINS_PORT=8095"
Environment="JAVA_OPTS=-Djava.awt.headless=true -Djenkins.install.runSetupWizard=false -Xmx1g"
Environment="CASC_JENKINS_CONFIG=/var/lib/jenkins/casc.yaml"
Environment="JENKINS_ADMIN_PASSWORD=$(openssl rand -hex 12)"
EOF
chmod 600 /etc/systemd/system/jenkins.service.d/override.conf
systemctl daemon-reload
systemctl enable --now jenkins
