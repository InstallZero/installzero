package track

import (
	"strings"
	"testing"
)

const before = "pm\tapt\npackage\tbash\t5.2\npackage\tcurl\t8.5\n" +
	"path\t/etc\npath\t/etc/nginx\npath\t/etc/nginx/nginx.conf\npath\t/opt\npath\t/home/u/app\npath\t/home/u/app/README.md\n" +
	"sum\t/etc/nginx/nginx.conf\taaa\nsum\t/etc/passwd\tp1\nsum\t/etc/ca-certificates.conf\tz1\nsum\t/etc/ld.so.cache\tc1\n" +
	"top\t/var/lib/dpkg\ntop\t/var/log/apt\ntop\t/root/.cache\n" +
	"enabled\tssh.service\nuser\troot\ngroup\troot\n" +
	"volume\told\n"

const after = "pm\tapt\npackage\tbash\t5.2\npackage\tcurl\t8.6\npackage\tnginx\t1.24\npackage\tlibnginx\t1.24\npath\t/etc/ssl/certs/new.pem\nsum\t/etc/ca-certificates.conf\tz2\n" +
	"path\t/etc\npath\t/etc/nginx\npath\t/etc/nginx/nginx.conf\npath\t/etc/nginx/sites-enabled\npath\t/etc/nginx/sites-enabled/app\n" +
	"path\t/opt\npath\t/opt/app\npath\t/opt/app/bin\npath\t/opt/app/bin/app\n" +
	"path\t/home/u/app\npath\t/home/u/app/README.md\npath\t/home/u/app/.venv\npath\t/home/u/app/data.db\n" +
	"sum\t/etc/nginx/nginx.conf\tbbb\nsum\t/etc/passwd\tp2\nsum\t/etc/ld.so.cache\tc2\nsum\t/etc/nginx/sites-enabled/app\tnew\n" +
	"top\t/var/lib/dpkg\ntop\t/var/log/apt\ntop\t/root/.cache\ntop\t/var/lib/app\ntop\t/usr/lib/systemd/system/nginx.service\ntop\t/usr/lib/systemd/system/packagekit.service\ntop\t/var/log/nginx\ntop\t/var/lib/installzero\ntop\t/root/.cache/pip\n" +
	"enabled\tssh.service\nenabled\tnginx.service\nrunning\tnginx.service\nrunning\tpolkit.service\nuser\troot\nuser\tapp\ngroup\troot\ngroup\tapp\n" +
	"container\tc1\tweb (nginx:1)\nvolume\told\nvolume\tappdata\n"

const code = "apt-get install -y nginx\nsed -i s/80/8080/ /etc/nginx/nginx.conf\nln -s ../sites-available/app /etc/nginx/sites-enabled/app"

func changes(t *testing.T) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, c := range Diff(parse(before), parse(after), Context{Dir: "/home/u/app", Code: code}) {
		got[c.Kind] = append(got[c.Kind], c.Key)
	}
	return got
}

func TestDiff(t *testing.T) {
	got := changes(t)
	want := map[string]string{
		"package":    "libnginx nginx",
		"upgraded":   "curl",
		"by-package": "/etc/ssl/certs/new.pem /usr/lib/systemd/system/nginx.service /usr/lib/systemd/system/packagekit.service /etc/ca-certificates.conf",
		"file":       "/etc/nginx/sites-enabled /home/u/app/.venv /home/u/app/data.db /opt/app",
		"data":       "/root/.cache/pip /var/lib/app /var/log/nginx",
		"modified":   "/etc/nginx/nginx.conf",
		"enabled":    "nginx.service",
		"running":    "nginx.service",
		"user":       "app",
		"group":      "app",
		"container":  "c1",
		"volume":     "appdata",
	}
	for k, v := range want {
		if g := strings.Join(got[k], " "); g != v {
			t.Errorf("%s: got %q, want %q", k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s: %v", k, got[k])
		}
	}
}

func TestProtected(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/etc": true, "/etc/": true, "/opt": true, "/home/u": true, "/var/lib": true,
		"/opt/app": false, "/var/lib/app": false, "/home/u/app": false, "/etc/nginx/sites-enabled": false,
		`C:\`: true, `C:\Windows\System32`: true, `C:\Program Files`: true, `C:\Users\ben`: true, `C:\Users\ben\AppData\Local`: true,
		`C:\Program Files\App`: false, `C:\Users\ben\AppData\Local\App`: false, `C:\Users\ben\proj\.venv`: false,
	} {
		if got := Protected(p); got != want {
			t.Errorf("Protected(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestUndoUnixOrderAndData(t *testing.T) {
	r := &Record{OS: "linux", Steps: []*StepRecord{
		{ID: "nginx", Changes: Diff(parse(before), parse(after), Context{Dir: "/home/u/app", Code: code}), Uninstall: &Block{Shell: "bash", Code: "rm -f /etc/app.conf"}},
		{ID: "bad", Changes: []Change{{Kind: "file", Key: "/etc"}}},
	}}
	r.Steps[0].Changes = append(r.Steps[0].Changes, Change{}) // ignored kind
	for i := range r.Steps[0].Changes {
		if r.Steps[0].Changes[i].Kind == "modified" {
			r.Steps[0].Changes[i].Backup = "/var/lib/installzero/backups/x.tgz"
		}
	}
	acts := Undo(r, UndoOptions{})
	var titles []string
	all := ""
	for _, a := range acts {
		titles = append(titles, a.Title)
		all += a.Code
		if strings.Contains(a.Title, "data") || strings.Contains(a.Title, "volumes") {
			if !a.Data {
				t.Errorf("%q is not marked as data", a.Title)
			}
		}
	}
	order := []string{"uninstall block", "containers", "services", "purge packages", "remove files", "restore files", "users and groups", "reload", "delete data", "volumes"}
	j := 0
	for _, ti := range titles {
		if j < len(order) && strings.Contains(ti, order[j]) {
			j++
		}
	}
	if j != len(order) {
		t.Errorf("order: %q", titles)
	}
	for _, want := range []string{"rm -rf -- '/opt/app'", "apt-get purge -y $present", "userdel 'app'", "systemctl disable --now 'nginx.service'", "tar xzf '/var/lib/installzero/backups/x.tgz' -C / 'etc/nginx/nginx.conf'", "docker volume rm 'appdata'"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in the uninstall", want)
		}
	}
	if strings.Contains(all, "rm -rf -- '/etc'\n") {
		t.Error("removes /etc")
	}
	keep := Undo(r, UndoOptions{KeepPackages: true})
	for _, a := range keep {
		if strings.Contains(a.Title, "purge packages") {
			t.Error("--keep-packages still purges")
		}
	}
}
