package track

import (
	"sort"
	"strings"
)

// Change is one thing an install added or changed.
//
// Kinds: package, upgraded (a package that was there and changed version;
// reported, never rolled back), by-package (a system file package scripts
// created or changed; left to the package manager), file (a new file or
// folder), data (a new file or folder
// that holds data: under /var/lib, /var/log, a home folder's cache, a
// Windows app-data folder), modified (a file under /etc that existed and
// changed; Info is its hash after the install, Backup the archive holding
// the original), enabled and running (systemd units), user, group,
// container, image, network, volume (data), program (Windows; Info is
// "name|uninstall command"), path-machine, path-user, env-machine,
// env-user, service, task.
type Change struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Info   string `json:"info,omitempty"`
	Backup string `json:"backup,omitempty"`
}

// Data reports whether undoing the change deletes data the user may want.
func (c Change) Data() bool { return c.Kind == "data" || c.Kind == "volume" }

// Files under /etc whose contents are not restored from the backup: the
// account databases (users and groups are deleted by name instead, so
// later changes by others survive) and files the system rewrites itself.
var notRestored = map[string]bool{
	"/etc/passwd": true, "/etc/passwd-": true, "/etc/shadow": true, "/etc/shadow-": true,
	"/etc/group": true, "/etc/group-": true, "/etc/gshadow": true, "/etc/gshadow-": true,
	"/etc/subuid": true, "/etc/subuid-": true, "/etc/subgid": true, "/etc/subgid-": true,
	"/etc/ld.so.cache": true, "/etc/mtab": true, "/etc/resolv.conf": true, "/etc/hostname": true,
	"/etc/hosts": true, "/etc/machine-id": true, "/etc/adjtime": true,
}

// Paths that change on their own or belong to InstallZero.
var ignoredPrefixes = []string{
	"/var/lib/installzero", "/var/cache/apt", "/var/lib/apt", "/var/lib/dpkg", "/var/log/journal",
	"/var/lib/systemd", "/var/cache/ldconfig", "/var/cache/debconf", "/var/lib/ucf", "/var/log/apt",
	"/var/log/dpkg.log", "/var/log/alternatives.log", "/var/cache/man", "/var/lib/man-db",
}

var dataPrefixes = []string{"/var/lib/", "/var/log/", "/var/cache/", "/root/.cache/", "/root/.local/share/", "/root/.config/"}

func isData(p string, windowsDataRoots []string) bool {
	for _, d := range dataPrefixes {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	if strings.HasPrefix(p, "/home/") {
		parts := strings.Split(p, "/")
		return len(parts) > 3 && (parts[3] == ".cache" || parts[3] == ".config" || parts[3] == ".local")
	}
	lp := strings.ToLower(p)
	for _, d := range windowsDataRoots {
		if d != "" && strings.HasPrefix(lp, strings.ToLower(strings.TrimRight(d, `\`))+`\`) {
			return true
		}
	}
	return false
}

// systemPath is where packages keep their files (not /usr/local, /opt or
// /srv, which are the admin's).
func systemPath(p string) bool {
	for _, d := range []string{"/etc/", "/usr/", "/lib/", "/lib64/", "/bin/", "/sbin/", "/boot/"} {
		if strings.HasPrefix(p, d) && !strings.HasPrefix(p, "/usr/local/") {
			return true
		}
	}
	return false
}

func ignored(p string, windowsIgnored []string) bool {
	for _, x := range ignoredPrefixes {
		if p == x || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	lp := strings.ToLower(p)
	for _, x := range windowsIgnored {
		x = strings.ToLower(strings.TrimRight(x, `\`))
		if x != "" && (lp == x || strings.HasPrefix(lp, x+`\`)) {
			return true
		}
	}
	return false
}

// Context says how to read a pair of snapshots.
type Context struct {
	Dir  string // the install folder; what is new in it is never data
	Code string // the step's install code (with any fixes), to tell its own edits from package scripts'
	// Windows only: folders whose new entries are data (ProgramData,
	// LOCALAPPDATA, APPDATA) and folders to ignore (TEMP, InstallZero's
	// own state).
	WindowsData, WindowsIgnored []string
}

func newKeys(before, after map[string]string) []string {
	var out []string
	for k := range after {
		if _, ok := before[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sep(p string) string {
	if strings.Contains(p, `\`) {
		return `\`
	}
	return "/"
}

// topmost drops paths inside another path of the list.
func topmost(paths []string) []string {
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		if n := len(out); n > 0 {
			last := out[n-1]
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(strings.TrimRight(last, sep(last))+sep(last))) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func under(p, dir string) bool {
	if dir == "" {
		return false
	}
	s := sep(dir)
	lp, ld := strings.ToLower(p), strings.ToLower(strings.TrimRight(dir, s))
	return lp == ld || strings.HasPrefix(lp, ld+s)
}

// Diff is what changed from before to after.
func Diff(before, after Snapshot, c Context) []Change {
	var out []Change
	add := func(kind, key, info string) { out = append(out, Change{Kind: kind, Key: key, Info: info}) }
	pm := ""
	for k := range after["pm"] {
		pm = k
	}
	newPkgs := newKeys(before["package"], after["package"])
	for _, k := range newPkgs {
		add("package", k, pm)
	}
	upgraded := 0
	for _, k := range sortedKeys(after["package"]) {
		if old, ok := before["package"][k]; ok && old != after["package"][k] {
			add("upgraded", k, old+" -> "+after["package"][k])
			upgraded++
		}
	}
	// In a step that installed or upgraded packages, system files the
	// step's own code does not name were changed by package scripts
	// (triggers, postinst); the package manager undoes those when it
	// purges, and undoing them here would fight packages that stay.
	byPackage := func(p string) bool {
		return (len(newPkgs) > 0 || upgraded > 0) && systemPath(p) && !strings.Contains(c.Code, p)
	}
	var paths []string
	for _, cat := range []string{"path", "top"} {
		for _, k := range newKeys(before[cat], after[cat]) {
			// A path seen in one listing before (e.g. /var/lib/x in "top")
			// is not new because the other listing first saw it now.
			_, inPath := before["path"][k]
			_, inTop := before["top"][k]
			if !inPath && !inTop && (under(k, c.Dir) || !ignored(k, c.WindowsIgnored)) {
				paths = append(paths, k)
			}
		}
	}
	for _, p := range topmost(paths) {
		kind := "file"
		switch {
		case under(p, c.Dir):
		case isData(p, c.WindowsData):
			kind = "data"
		case byPackage(p):
			kind = "by-package"
		}
		add(kind, p, "")
	}
	for _, k := range sortedKeys(after["sum"]) {
		if old, ok := before["sum"][k]; ok && old != after["sum"][k] && !notRestored[k] {
			if byPackage(k) {
				add("by-package", k, "changed")
			} else {
				add("modified", k, after["sum"][k])
			}
		}
	}
	// A service that only started (apt wakes packagekit and polkit) is the
	// install's only when its unit file is new too.
	newUnit := map[string]bool{}
	for _, cat := range []string{"path", "top"} {
		for _, k := range newKeys(before[cat], after[cat]) {
			newUnit[k[strings.LastIndex(k, "/")+1:]] = true
		}
	}
	for _, cat := range []string{"enabled", "running", "user", "group", "container", "image", "network", "volume",
		"program", "path-machine", "path-user", "env-machine", "env-user", "service", "task"} {
		for _, k := range newKeys(before[cat], after[cat]) {
			if cat == "running" && !newUnit[k] {
				continue
			}
			add(cat, k, after[cat][k])
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
