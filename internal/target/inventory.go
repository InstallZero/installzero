package target

import (
	"context"
	"strings"
	"time"
)

// InventoryAreas are the areas Inventory knows, in the order the model is
// told them.
var InventoryAreas = []string{"software", "containers", "processes", "ports", "services", "system", "files", "python", "node", "git", "gpu", "env"}

// Inventory is Get-LinuxInventory: one area of what is installed and
// running. path roots the python and git searches (/ when "").
func (u *Unix) Inventory(ctx context.Context, area, path string) string {
	p := path
	if p == "" {
		p = "/"
	}
	var sh string
	switch strings.ToLower(area) {
	case "software":
		sh = `echo "Packages installed by hand (apt-mark showmanual):"; dpkg-query -W -f='${Package} ${Version}\n' $(apt-mark showmanual) 2>/dev/null
echo; echo "apt sources:"; ls -la /etc/apt/sources.list.d/ 2>/dev/null; for f in /etc/apt/sources.list.d/*; do [ -f "$f" ] && { echo "--- $f"; grep -v '^#' "$f" | grep -v '^\s*$'; }; done
echo; echo "keyrings:"; ls -la /etc/apt/keyrings /usr/share/keyrings 2>/dev/null
echo; echo "snaps:"; snap list 2>/dev/null || echo "(no snapd)"
echo; echo "/opt:"; ls -la /opt; echo; echo "/usr/local/bin:"; ls -la /usr/local/bin`
	case "containers":
		sh = `command -v docker >/dev/null || { echo "docker is not installed"; exit 0; }
echo "docker: $(docker --version)"; docker info --format 'server {{.ServerVersion}}, storage {{.Driver}}, root {{.DockerRootDir}}, cgroup {{.CgroupDriver}}' 2>&1
echo; echo "Containers:"; docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}\tproject={{.Label "com.docker.compose.project"}} service={{.Label "com.docker.compose.service"}}'
echo; echo "Compose projects:"; docker compose ls -a 2>&1
echo; echo "Compose working dirs and files:"; for c in $(docker ps -aq); do docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}: {{index .Config.Labels "com.docker.compose.project.working_dir"}} [{{index .Config.Labels "com.docker.compose.project.config_files"}}]' $c; done | sort -u
echo; echo "Images:"; docker images --format '{{.Repository}}:{{.Tag}}  {{.Size}}  {{.CreatedSince}}' | head -80
echo; echo "Volumes:"; docker volume ls --format '{{.Name}} {{.Mountpoint}}'
echo; echo "Networks:"; docker network ls --format '{{.Name}} {{.Driver}}'`
	case "processes":
		sh = `ps -eo pid,user,etime,args --sort=pid | grep -v ' \[[^]]*\]$' | grep -v 'bash -ls$' | head -150`
	case "ports":
		sh = `ss -ltnup 2>/dev/null | head -80`
	case "services":
		sh = `echo "Running services:"; systemctl list-units --type=service --state=running --no-pager --no-legend 2>&1
echo; echo "Enabled unit files:"; systemctl list-unit-files --state=enabled --no-pager --no-legend 2>&1
echo; echo "Units in /etc/systemd/system:"; find /etc/systemd/system -maxdepth 2 \( -name '*.service' -o -name '*.timer' -o -name '*.conf' \) 2>/dev/null
echo; echo "Timers:"; systemctl list-timers --no-pager --no-legend 2>&1 | head -30
echo; echo "Cron:"; for u in $(cut -d: -f1 /etc/passwd); do crontab -l -u $u 2>/dev/null | sed "s/^/$u: /"; done; ls -la /etc/cron.d 2>/dev/null`
	case "system":
		sh = `. /etc/os-release; echo "OS: $PRETTY_NAME; kernel $(uname -r); arch $(uname -m)"; echo "hostname: $(hostname)"
echo; echo "/etc/hosts:"; grep -v '^#' /etc/hosts | grep -v '^\s*$'
echo; echo "addresses:"; ip -br addr 2>/dev/null
echo; echo "users with a shell:"; getent passwd | awk -F: '$7 !~ /(nologin|false|sync)$/ {print $1" uid="$3" home="$6" shell="$7}'
echo; echo "groups with members:"; getent group | awk -F: '$4 != "" {print $1": "$4}'
echo; echo "sysctl settings in /etc:"; grep -hv '^\s*[#;]' /etc/sysctl.conf /etc/sysctl.d/*.conf 2>/dev/null | grep -v '^\s*$'
echo; echo "mounts:"; findmnt -rn -o TARGET,SOURCE,FSTYPE -t ext4,xfs,btrfs,nfs,nfs4,cifs,overlay 2>/dev/null | grep -v '/var/lib/docker' | head -30
echo; df -h / ; free -h`
	case "files":
		sh = `for d in /etc /opt /srv /data /usr/local /var/lib /home /root; do [ -d $d ] && find $d -xdev -maxdepth 3 -newer /usr/lib/os-release -type d 2>/dev/null; done | grep -Ev '^/var/lib/(docker|containerd|apt|dpkg|systemd|ucf|polkit|private)/|^/etc/(alternatives|ssl/certs)' | sort | head -250`
	case "python":
		sh = `echo "python3: $(python3 --version 2>&1) at $(command -v python3)"; command -v conda >/dev/null && conda env list; find '` + p + `' -maxdepth 5 -name pyvenv.cfg 2>/dev/null | head -8 | while read c; do d=$(dirname $c); echo "venv $d: $(tr '\n' ';' < $c)"; $d/bin/pip list --format freeze 2>/dev/null | head -80 | paste -sd, - | sed 's/^/  packages: /'; done`
	case "node":
		sh = `echo "node $(node -v 2>&1); npm $(npm -v 2>&1)"; npm ls -g --depth 0 2>&1`
	case "git":
		sh = `find '` + p + `' -maxdepth 4 -name .git -type d 2>/dev/null | head -15 | while read g; do d=$(dirname $g); echo "$d"; echo "  remote: $(git -C $d remote get-url origin 2>&1)"; echo "  branch: $(git -C $d rev-parse --abbrev-ref HEAD 2>&1) at $(git -C $d log -1 --format='%h %cs %s' 2>&1)"; echo "  local changes: $(git -C $d status --porcelain 2>/dev/null | wc -l) files"; done`
	case "gpu":
		sh = `export PATH=$PATH:/usr/lib/wsl/lib; nvidia-smi 2>&1 | head -25; echo "nvcc: $(nvcc --version 2>&1 | tail -2 | tr "\n" " ")"; ls -d /usr/local/cuda* 2>/dev/null`
	case "env":
		sh = `echo "Login shell environment:"; env | sort | grep -v "^_="; echo; echo "/etc/environment:"; cat /etc/environment; echo; echo "/etc/profile.d:"; ls /etc/profile.d`
	default:
		return "error: unknown area '" + area + "'; use " + strings.Join(InventoryAreas, ", ")
	}
	return u.Bash(ctx, sh, 90*time.Second, "").Out
}
