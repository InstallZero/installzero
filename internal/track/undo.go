package track

import (
	"fmt"
	"sort"
	"strings"

	"github.com/InstallZero/installzero/internal/target"
)

// Action is one part of an uninstall.
type Action struct {
	Title string   // what it does, for the user
	Items []string // what it touches, for the user
	Shell string
	Code  string
	Data  bool // deletes data; runs only when the user agrees to that separately
}

// Options shape an uninstall.
type UndoOptions struct {
	KeepPackages bool // leave packages and programs installed
}

// Folders never removed, whatever a record says: removing one would take
// the system or a user's files with it.
var protected = map[string]bool{
	"/": true, "/etc": true, "/usr": true, "/usr/local": true, "/usr/bin": true, "/usr/sbin": true, "/usr/lib": true,
	"/opt": true, "/srv": true, "/var": true, "/var/lib": true, "/var/log": true, "/var/cache": true, "/home": true,
	"/root": true, "/bin": true, "/sbin": true, "/lib": true, "/lib64": true, "/boot": true, "/tmp": true,
	"/usr/local/bin": true, "/usr/local/lib": true, "/usr/local/share": true, "/etc/systemd": true, "/etc/systemd/system": true,
	"/usr/lib/systemd": true, "/usr/lib/systemd/system": true, "/var/spool": true, "/var/spool/cron": true,
}

// Protected reports whether a path must never be removed.
func Protected(p string) bool {
	if strings.HasPrefix(p, "/") {
		c := "/" + strings.Trim(p, "/")
		if protected[c] {
			return true
		}
		parts := strings.Split(strings.Trim(c, "/"), "/")
		// A home folder itself (/home/<user>).
		return len(parts) < 2 || (parts[0] == "home" && len(parts) == 2)
	}
	// Windows: a drive, a top folder of it (C:\Windows, C:\Users), and a
	// user profile (C:\Users\<user>) or its AppData folders.
	q := strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`))
	parts := strings.Split(q, `\`)
	if len(parts) <= 2 {
		return true
	}
	if parts[1] == "users" && (len(parts) <= 3 || (len(parts) <= 5 && parts[3] == "appdata")) {
		return true
	}
	return parts[1] == "windows" || (parts[1] == "programdata" && len(parts) <= 2)
}

// Undo plans the uninstall of a record: the install.md's own uninstall
// blocks first (last step first), then the tracked changes in reverse
// order of how installs make them.
func Undo(r *Record, o UndoOptions) []Action {
	var acts []Action
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if s := r.Steps[i]; s.Uninstall != nil {
			acts = append(acts, Action{Title: "step " + s.ID + ": the install.md's uninstall block", Items: firstLines(s.Uninstall.Code, 6), Shell: s.Uninstall.Shell, Code: s.Uninstall.Code})
		}
	}
	by := map[string][]Change{}
	for _, c := range r.Changes() {
		by[c.Kind] = append(by[c.Kind], c)
	}
	if r.OS == "windows" {
		acts = append(acts, undoWindows(by, o)...)
	} else {
		acts = append(acts, undoUnix(by, o)...)
	}
	// What stays, said rather than done (Code "").
	if cs := by["upgraded"]; len(cs) > 0 {
		var items []string
		for _, c := range cs {
			items = append(items, c.Key+" "+c.Info)
		}
		acts = append(acts, Action{Title: "packages that were there and got upgraded stay upgraded (rolling back could undo security fixes)", Items: items})
	}
	if n := len(by["by-package"]); n > 0 {
		acts = append(acts, Action{Title: fmt.Sprintf("%d system file%s that package scripts created or changed are left to the package manager", n, map[bool]string{true: "s", false: ""}[n != 1])})
	}
	return acts
}

func firstLines(s string, n int) []string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = append(l[:n], fmt.Sprintf("... (%d more lines)", len(l)-n))
	}
	return l
}

func keys(cs []Change) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Key
	}
	return out
}

func labels(cs []Change) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Key
		if c.Info != "" {
			out[i] = c.Info
		}
	}
	return out
}

// removable splits paths into those that may be removed and protected ones.
func removable(cs []Change) (ok []string, refused []string) {
	// Steps each record their topmost new paths; across steps one can be
	// inside another's.
	for _, p := range topmost(keys(cs)) {
		if Protected(p) {
			refused = append(refused, p)
		} else {
			ok = append(ok, p)
		}
	}
	// Deepest first, so nothing is removed from under a later entry.
	sort.Slice(ok, func(i, j int) bool { return len(ok[i]) > len(ok[j]) })
	return ok, refused
}

func undoUnix(by map[string][]Change, o UndoOptions) []Action {
	var acts []Action
	q := target.Quote
	if cs := by["container"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if docker inspect %s >/dev/null 2>&1; then docker rm -f %s >/dev/null; fi\n", q(c.Key), q(c.Key))
		}
		acts = append(acts, Action{Title: "remove Docker containers", Items: labels(cs), Shell: "bash", Code: b.String()})
	}
	units := append(append([]Change{}, by["enabled"]...), by["running"]...)
	if len(units) > 0 {
		var b strings.Builder
		seen := map[string]bool{}
		for _, c := range units {
			if !seen[c.Key] {
				seen[c.Key] = true
				fmt.Fprintf(&b, "systemctl disable --now %s 2>/dev/null || systemctl stop %s 2>/dev/null || true\n", q(c.Key), q(c.Key))
			}
		}
		acts = append(acts, Action{Title: "stop and disable services", Items: keys(units), Shell: "bash", Code: b.String()})
	}
	if cs := by["package"]; len(cs) > 0 && !o.KeepPackages {
		pkgs := strings.Join(keys(cs), " ")
		code := ""
		if cs[0].Info == "rpm" {
			code = fmt.Sprintf("present=$(rpm -q --qf '%%{NAME}\\n' %s 2>/dev/null | grep -v 'is not installed')\n[ -z \"$present\" ] && exit 0\ndnf remove -y $present || yum remove -y $present\n", pkgs)
		} else {
			// apt removes what depends on a purged package too; stop if that
			// would take a package that was there before the install.
			code = fmt.Sprintf(`ours="%s"
present=$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' $ours 2>/dev/null | awk '$1 ~ /^ii/ {print $2}')
[ -z "$present" ] && { echo "already removed"; exit 0; }
extra=$(apt-get -s purge $present 2>/dev/null | awk '/^Purg /{sub(/:.*/, "", $2); print $2}' | grep -vxF -f <(printf '%%s\n' $ours) || true)
if [ -n "$extra" ]; then echo "not purged: this would also remove packages that were installed before:"; echo "$extra"; exit 1; fi
apt-get purge -y $present
`, pkgs)
		}
		acts = append(acts, Action{Title: "purge packages the install added", Items: keys(cs), Shell: "bash", Code: code})
	}
	if cs := by["image"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if docker image inspect %s >/dev/null 2>&1; then docker rmi %s >/dev/null; fi\n", q(c.Key), q(c.Key))
		}
		acts = append(acts, Action{Title: "remove Docker images", Items: labels(cs), Shell: "bash", Code: b.String()})
	}
	if cs := by["network"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if docker network inspect %s >/dev/null 2>&1; then docker network rm %s >/dev/null; fi\n", q(c.Key), q(c.Key))
		}
		acts = append(acts, Action{Title: "remove Docker networks", Items: labels(cs), Shell: "bash", Code: b.String()})
	}
	if ok, refused := removable(by["file"]); len(ok)+len(refused) > 0 {
		acts = append(acts, removeUnix("remove files and folders the install created", ok, refused, false))
	}
	if cs := by["modified"]; len(cs) > 0 {
		var b strings.Builder
		var items []string
		for _, c := range cs {
			if c.Backup == "" {
				items = append(items, c.Key+" (no backup; left as is)")
				continue
			}
			items = append(items, c.Key)
			fmt.Fprintf(&b, `f=%s
if [ ! -e "$f" ]; then echo "gone: $f"
elif [ "$(md5sum < "$f" | cut -c1-32)" != %s ]; then echo "changed since the install, left as is: $f"
else tar xzf %s -C / %s && echo "restored: $f"; fi
`, q(c.Key), q(c.Info), q(c.Backup), q(strings.TrimPrefix(c.Key, "/")))
		}
		acts = append(acts, Action{Title: "restore files under /etc the install changed", Items: items, Shell: "bash", Code: b.String()})
	}
	if users, groups := by["user"], by["group"]; len(users)+len(groups) > 0 {
		var b strings.Builder
		for _, c := range users {
			fmt.Fprintf(&b, "if id -u %s >/dev/null 2>&1; then userdel %s; fi\n", q(c.Key), q(c.Key))
		}
		for _, c := range groups {
			fmt.Fprintf(&b, "if getent group %s >/dev/null; then groupdel %s; fi\n", q(c.Key), q(c.Key))
		}
		var items []string
		for _, c := range users {
			items = append(items, "user "+c.Key)
		}
		for _, c := range groups {
			items = append(items, "group "+c.Key)
		}
		acts = append(acts, Action{Title: "delete users and groups the install created", Items: items, Shell: "bash", Code: b.String()})
	}
	if len(units) > 0 || len(by["file"]) > 0 {
		acts = append(acts, Action{Title: "reload systemd and the library cache", Shell: "bash", Code: "command -v systemctl >/dev/null && systemctl daemon-reload || true\nldconfig 2>/dev/null || true\n"})
	}
	if ok, refused := removable(by["data"]); len(ok)+len(refused) > 0 {
		acts = append(acts, removeUnix("delete data the install created", ok, refused, true))
	}
	if cs := by["volume"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if docker volume inspect %s >/dev/null 2>&1; then docker volume rm %s >/dev/null; fi\n", q(c.Key), q(c.Key))
		}
		acts = append(acts, Action{Title: "delete Docker volumes", Items: keys(cs), Shell: "bash", Code: b.String(), Data: true})
	}
	return acts
}

func removeUnix(title string, ok, refused []string, data bool) Action {
	var b strings.Builder
	items := append([]string{}, ok...)
	for _, p := range ok {
		fmt.Fprintf(&b, "rm -rf -- %s\n", target.Quote(p))
	}
	for _, p := range refused {
		items = append(items, p+" (protected; kept)")
	}
	if b.Len() == 0 {
		b.WriteString("true\n")
	}
	return Action{Title: title, Items: items, Shell: "bash", Code: b.String(), Data: data}
}

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func undoWindows(by map[string][]Change, o UndoOptions) []Action {
	var acts []Action
	if cs := by["container"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "cmd /c \"docker rm -f %s >nul 2>&1\"\n", c.Key)
		}
		acts = append(acts, Action{Title: "remove Docker containers", Items: labels(cs), Shell: "powershell", Code: b.String()})
	}
	if cs := by["task"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			i := strings.LastIndex(c.Key, `\`)
			fmt.Fprintf(&b, "Get-ScheduledTask -TaskPath %s -TaskName %s -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false\n", psq(c.Key[:i+1]), psq(c.Key[i+1:]))
		}
		acts = append(acts, Action{Title: "remove scheduled tasks", Items: keys(cs), Shell: "powershell", Code: b.String()})
	}
	if cs := by["service"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if (Get-Service -Name %s -ErrorAction SilentlyContinue) { Stop-Service -Name %s -Force -ErrorAction SilentlyContinue }\n", psq(c.Key), psq(c.Key))
		}
		acts = append(acts, Action{Title: "stop services", Items: labels(cs), Shell: "powershell", Code: b.String()})
	}
	if cs := by["program"]; len(cs) > 0 && !o.KeepPackages {
		var b strings.Builder
		var items []string
		for _, c := range cs {
			name, cmd, _ := strings.Cut(c.Info, "|")
			items = append(items, name)
			if cmd == "" {
				fmt.Fprintf(&b, "Write-Output %s\n", psq("no uninstall command for "+name+"; remove it in Settings > Apps"))
				continue
			}
			// msiexec /I{guid} is the repair form many entries list; /X with
			// /qn uninstalls without a window.
			if strings.Contains(strings.ToLower(cmd), "msiexec") {
				cmd = strings.NewReplacer("/I{", "/X{", "/i{", "/X{").Replace(cmd) + " /qn /norestart"
			}
			fmt.Fprintf(&b, "if (Test-Path %s) { Write-Output %s; cmd /c %s; if ($LASTEXITCODE -and $LASTEXITCODE -ne 3010) { Write-Output \"exit $LASTEXITCODE\"; $failed = $true } }\n",
				psq("Registry::"+c.Key), psq("uninstalling "+name), psq(cmd))
		}
		b.WriteString("if ($failed) { exit 1 }\nexit 0\n")
		acts = append(acts, Action{Title: "uninstall programs the install added (an uninstaller may open a window)", Items: items, Shell: "powershell", Code: b.String()})
	}
	if cs := by["service"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "if (Get-Service -Name %s -ErrorAction SilentlyContinue) { sc.exe delete %s | Out-Null }\n", psq(c.Key), psq(c.Key))
		}
		acts = append(acts, Action{Title: "delete services still left", Items: labels(cs), Shell: "powershell", Code: b.String()})
	}
	for _, kind := range []string{"image", "network"} {
		if cs := by[kind]; len(cs) > 0 {
			var b strings.Builder
			for _, c := range cs {
				cmd := "rmi"
				if kind == "network" {
					cmd = "network rm"
				}
				fmt.Fprintf(&b, "cmd /c \"docker %s %s >nul 2>&1\"\n", cmd, c.Key)
			}
			acts = append(acts, Action{Title: "remove Docker " + kind + "s", Items: labels(cs), Shell: "powershell", Code: b.String()})
		}
	}
	for _, scope := range []string{"machine", "user"} {
		paths, envs := by["path-"+scope], by["env-"+scope]
		if len(paths)+len(envs) == 0 {
			continue
		}
		s := map[string]string{"machine": "Machine", "user": "User"}[scope]
		var b strings.Builder
		if len(paths) > 0 {
			var list []string
			for _, c := range paths {
				list = append(list, psq(c.Key))
			}
			fmt.Fprintf(&b, "$drop = @(%s)\n$p = @([Environment]::GetEnvironmentVariable('Path', '%s') -split ';' | Where-Object { $_ -and $_ -notin $drop })\n[Environment]::SetEnvironmentVariable('Path', ($p -join ';'), '%s')\n", strings.Join(list, ", "), s, s)
		}
		for _, c := range envs {
			fmt.Fprintf(&b, "[Environment]::SetEnvironmentVariable(%s, $null, '%s')\n", psq(c.Key), s)
		}
		acts = append(acts, Action{Title: fmt.Sprintf("remove %s PATH entries and environment variables the install added", scope), Items: append(keys(paths), keys(envs)...), Shell: "powershell", Code: b.String()})
	}
	if ok, refused := removable(by["file"]); len(ok)+len(refused) > 0 {
		acts = append(acts, removeWindows("remove files and folders the install created", ok, refused, false))
	}
	if ok, refused := removable(by["data"]); len(ok)+len(refused) > 0 {
		acts = append(acts, removeWindows("delete data the install created", ok, refused, true))
	}
	if cs := by["volume"]; len(cs) > 0 {
		var b strings.Builder
		for _, c := range cs {
			fmt.Fprintf(&b, "cmd /c \"docker volume rm %s >nul 2>&1\"\n", c.Key)
		}
		acts = append(acts, Action{Title: "delete Docker volumes", Items: keys(cs), Shell: "powershell", Code: b.String(), Data: true})
	}
	return acts
}

func removeWindows(title string, ok, refused []string, data bool) Action {
	var b strings.Builder
	items := append([]string{}, ok...)
	for _, p := range ok {
		fmt.Fprintf(&b, "if (Test-Path -LiteralPath %s) { Remove-Item -LiteralPath %s -Recurse -Force }\n", psq(p), psq(p))
	}
	for _, p := range refused {
		items = append(items, p+" (protected; kept)")
	}
	if b.Len() == 0 {
		b.WriteString("$null\n")
	}
	return Action{Title: title, Items: items, Shell: "powershell", Code: b.String(), Data: data}
}
