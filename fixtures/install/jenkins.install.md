---
name: jenkins
source: https://github.com/jenkinsci/jenkins
summary: Jenkins LTS on Java 21, served on port 8095
---

# Jenkins install

Jenkins LTS from pkg.jenkins.io, run by systemd on port 8095 with the
setup wizard off. The apt package starts Jenkins on its default port 8080
as soon as it is installed; step 4 moves it to 8095. A replay fixture for
port conflicts: with 8080 taken, the package's first start fails and
systemd's start limit then refuses the restart in step 4.

## Step 1: java - Java 21

Why: Jenkins LTS needs Java 17 or 21.

```requires
os: linux
```

```detect bash
java -version 2>&1 | grep -q '"21'
```

```install bash
apt-get update -q
apt-get install -y -q fontconfig openjdk-21-jre-headless curl
```

```verify bash
java -version 2>&1 | grep '"21'
```

## Step 2: repo - Jenkins apt repository

Why: Jenkins LTS is not in Ubuntu's archive.

```detect bash
grep -qs pkg.jenkins.io /etc/apt/sources.list.d/jenkins.list
```

```install bash
install -m 0755 -d /etc/apt/keyrings
curl -fsSL -o /etc/apt/keyrings/jenkins-keyring.asc https://pkg.jenkins.io/debian-stable/jenkins.io-2026.key
echo "deb [signed-by=/etc/apt/keyrings/jenkins-keyring.asc] https://pkg.jenkins.io/debian-stable binary/" > /etc/apt/sources.list.d/jenkins.list
apt-get update -q
```

```verify bash
apt-cache policy jenkins | grep -q pkg.jenkins.io
```

## Step 3: package - Jenkins package

Why: installs Jenkins and its systemd unit.

```detect bash
dpkg -s jenkins >/dev/null 2>&1
```

```install bash
apt-get install -y -q jenkins
```

```verify bash
dpkg -s jenkins | grep -q 'Status: install ok installed'
```

## Step 4: service - Jenkins on port 8095

Why: serves Jenkins on 8095 without the setup wizard.

```detect bash
curl -fsS -o /dev/null http://localhost:8095/login
```

```install bash
mkdir -p /etc/systemd/system/jenkins.service.d
cat > /etc/systemd/system/jenkins.service.d/override.conf <<'EOF'
[Service]
Environment="JENKINS_PORT=8095"
Environment="JAVA_OPTS=-Djava.awt.headless=true -Djenkins.install.runSetupWizard=false -Xmx1g"
EOF
systemctl daemon-reload
systemctl enable jenkins
systemctl restart jenkins
```

```verify bash
for i in $(seq 1 60); do
  curl -fsS -o /dev/null http://localhost:8095/login && exit 0
  sleep 5
done
exit 1
```
