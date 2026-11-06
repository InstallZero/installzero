// Package readonly decides whether a command the model wants to run only
// reads: bash scripts (Bash) for Linux targets and PowerShell commands
// (PowerShell) for Windows ones. Ported from agent/LinuxTarget.ps1 and
// doctor/ReadOnly.ps1; fixtures/readonly holds the shared cases.
package readonly

import (
	"strings"

	"github.com/InstallZero/installzero/internal/psrx"
)

var (
	bashAny     = psrx.MustCompile(`^(cd|cat|ls|ll|head|tail|grep|egrep|fgrep|zgrep|zcat|ps|ss|netstat|lsof|journalctl|which|whereis|type|uname|lsb_release|hostname|id|whoami|groups|getent|env|printenv|df|du|free|nproc|lscpu|lsblk|findmnt|nvidia-smi|nvcc|test|\[|\[\[|echo|printf|true|false|stat|file|readlink|realpath|wc|sort|uniq|cut|tr|jq|yq|tree|basename|dirname|date|uptime|column|namei|getfacl|ldd|md5sum|sha256sum|exit|sleep|seq|diff|cmp|strings|nl|less|more|pwd|ulimit|timedatectl|hostnamectl|lsmod|blkid|awk|sed|xxd|od|python3?|node|npm|pip3?|git|docker|systemctl|dpkg|dpkg-query|apt|apt-cache|apt-mark|snap|curl|wget|ip|sysctl|crontab|openssl|find|command|mount|java|go|rustc|cargo|conda|ruby|php|psql|mysql|redis-cli|helm|kubectl|kind|k3s)$`)
	bashKeyword = psrx.MustCompile(`^(if|then|else|elif|fi|for|while|until|do|done|in|case|esac|function|time|!|\{|\}|local|export|declare|readonly)$`)

	lineContinuation = psrx.MustCompile(`\\\r?\n`)
	toDevNull        = psrx.MustCompile(`\d?>>?\s*/dev/null|&>\s*/dev/null|\d?>&\d`)
	redirection      = psrx.MustCompile(`(^|[^<>=-])>{1,2}\s*[^\s>&]`)
	sudo             = psrx.MustCompile(`(?i)\b(sudo|su)\b`)
	dockerExec       = psrx.MustCompile(`(?i)\bdocker\s+(compose\s+)?exec\b`)
	notReadOnly      = psrx.MustCompile(`(?i)\b(tee|xargs|eval|exec|source|nohup|setsid|dd|truncate|shred)\b`)
	substitution     = psrx.MustCompile(`(\$|<)\(([^()]*)\)`)
	assignments      = psrx.MustCompile(`^(\w+=("[^"]*"|'[^']*'|\S*)\s+)+`)
	whitespace       = psrx.MustCompile(`\s+`)
	assignment       = psrx.MustCompile(`^\w+=`)
	caseLabel        = psrx.MustCompile(`^[\w-]+\)$`)
	dirPrefix        = psrx.MustCompile(`^.*/`)
)

// SplitBash splits a bash script into simple commands at ; | || && & ( )
// and newlines outside quotes, so a quoted pattern such as grep -E 'a|b'
// stays one command. The & of the redirections 2>&1 and &> does not split.
func SplitBash(code string) []string {
	r := []rune(code)
	var parts []string
	var cur strings.Builder
	var q rune
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if q != 0 {
			cur.WriteRune(ch)
			if ch == '\\' && q == '"' && i+1 < len(r) {
				i++
				cur.WriteRune(r[i])
			} else if ch == q {
				q = 0
			}
			continue
		}
		if ch == '\\' && i+1 < len(r) {
			cur.WriteRune(ch)
			i++
			cur.WriteRune(r[i])
			continue
		}
		if ch == '\'' || ch == '"' {
			q = ch
			cur.WriteRune(ch)
			continue
		}
		redirect := ch == '&' && ((i > 0 && r[i-1] == '>') || (i+1 < len(r) && r[i+1] == '>'))
		if !redirect && strings.ContainsRune(";|&()\r\n", ch) {
			parts = append(parts, cur.String())
			cur.Reset()
			if i+1 < len(r) && ((ch == '|' && r[i+1] == '|') || (ch == '&' && r[i+1] == '&')) {
				i++
			}
			continue
		}
		cur.WriteRune(ch)
	}
	return append(parts, cur.String())
}

// Bash returns why a bash script is not read-only, or "" when it is: every
// command in it (including in pipelines, lists, subshells and command
// substitution) must be on the allowlist, with its read-only subcommands,
// and output may only be redirected to /dev/null.
func Bash(code string) string {
	c := lineContinuation.Replace(code, " ")
	if strings.Contains(c, "`") {
		return "uses backticks"
	}
	if redirection.Match(toDevNull.Replace(c, "")) {
		return "writes a file (output redirection)"
	}
	if g := sudo.Groups(c); g != nil {
		return "'" + g[1] + "' is not needed or run here: you are already root, so run the command itself"
	}
	if dockerExec.Match(c) {
		return "docker exec is not read-only: read a container's settings with docker inspect, docker compose config or docker logs"
	}
	if g := notReadOnly.Groups(c); g != nil {
		return "'" + g[1] + "' is not read-only"
	}
	// Command substitutions are checked on their own and stand for a word in
	// the command around them.
	var subs []string
	for g := substitution.Groups(c); g != nil; g = substitution.Groups(c) {
		subs = append(subs, g[2])
		c = strings.ReplaceAll(c, g[0], "SUBST")
	}
	for _, s := range subs {
		if strings.TrimSpace(s) != "" {
			if why := Bash(s); why != "" {
				return why
			}
		}
	}
	for _, seg := range SplitBash(c) {
		words := fields(assignments.Replace(strings.TrimSpace(seg), ""))
		if len(words) > 0 && (strings.EqualFold(words[0], "for") || strings.EqualFold(words[0], "select")) {
			continue
		}
		for len(words) > 0 && bashKeyword.Match(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 || assignment.Match(words[0]) || caseLabel.Match(words[0]) || words[0] == ";;" {
			continue
		}
		cmd := dirPrefix.Replace(words[0], "")
		rest := strings.Join(words[1:], " ")
		if !bashAny.Match(cmd) {
			return "'" + cmd + "' is not on the read-only list"
		}
		sub := strings.TrimSpace(word(words, 1) + " " + word(words, 2) + " " + word(words, 3))
		for _, rule := range bashRules {
			if rule.cmd.Match(cmd) {
				if why := rule.why(cmd, rest, sub); why != "" {
					return why
				}
			}
		}
	}
	return ""
}

func fields(s string) []string {
	var out []string
	for _, w := range whitespace.Split(s) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func word(words []string, i int) string {
	if i < len(words) {
		return words[i]
	}
	return ""
}

type bashRule struct {
	cmd *psrx.Regexp
	why func(cmd, rest, sub string) string
}

// unless returns msg when s does not match pattern.
func unless(pattern, msg string, onSub bool) func(cmd, rest, sub string) string {
	re := psrx.MustCompile(pattern)
	return func(cmd, rest, sub string) string {
		s := rest
		if onSub {
			s = sub
		}
		if re.Match(s) {
			return ""
		}
		return strings.ReplaceAll(msg, "{cmd}", cmd)
	}
}

// when returns msg when rest matches any of the patterns.
func when(msg string, patterns ...string) func(cmd, rest, sub string) string {
	res := make([]*psrx.Regexp, len(patterns))
	for i, p := range patterns {
		res[i] = psrx.MustCompile(p)
	}
	return func(cmd, rest, sub string) string {
		for _, re := range res {
			if re.Match(rest) {
				return msg
			}
		}
		return ""
	}
}

var (
	gitAllowed = psrx.MustCompile(`^(-C\s+\S+\s+)?(status|log|remote|branch|rev-parse|describe|tag|ls-files|show-ref|config (--get|-l|--list)|submodule status|--version)\b`)
	gitPatch   = psrx.MustCompile(`\s(-p|--patch)\b`)
	ipAllowed  = psrx.MustCompile(`^(-\w+\s+)*(a|addr|address|r|route|l|link|n|neigh)(\s+(show|list|ls))?(\s|$)`)
	ipChange   = psrx.MustCompile(`\b(add|del|delete|set|flush|change|replace)\b`)
)

var bashRules = []bashRule{
	{psrx.MustCompile(`^docker$`), unless(`^(ps|images|inspect|logs|version|info|top|port|stats\b.*--no-stream|system df|image (ls|inspect|history)|container (ls|inspect|logs|top|port)|network (ls|inspect)|volume (ls|inspect)|compose\b.*\b(ps|ls|config|images|version|logs|top)\b|context (ls|show)|buildx (ls|version)|--version|-v)\b`,
		"docker: only ps, images, inspect, logs, version, info and other listings (a container's settings: docker inspect, docker compose config, docker logs)", false)},
	{psrx.MustCompile(`^systemctl$`), unless(`(^|\s)(status|list-units|list-unit-files|list-timers|list-sockets|show|is-active|is-enabled|is-failed|cat|get-default|--version)\b`, "systemctl: only status, list-*, show, is-*, cat", false)},
	{psrx.MustCompile(`^(dpkg)$`), unless(`^(-l|-s|-L|-S|-p|--list|--status|--listfiles|--search|--print-architecture|--get-selections|--version)`, "dpkg: only queries", false)},
	{psrx.MustCompile(`^apt$`), unless(`^(list|policy|show|search|--version)`, "apt: only list, policy, show, search", true)},
	{psrx.MustCompile(`^apt-mark$`), unless(`^(showmanual|showauto|showhold)`, "apt-mark: only show*", true)},
	{psrx.MustCompile(`^snap$`), unless(`^(list|info|version|services)`, "snap: only list, info, services", true)},
	{psrx.MustCompile(`^pip3?$`), unless(`^(list|show|freeze|--version|-V|config list)`, "pip: only list, show, freeze", true)},
	{psrx.MustCompile(`^python3?$`), unless(`^(--version|-V|-m pip (list|show|freeze)|-c\s+["']?import \w+[\w., ]*;\s*print\([\w.]*(__version__|version)[\w.()]*\)["']?\s*$)`, "python: only --version, pip listings and printing a module version", false)},
	{psrx.MustCompile(`^node$`), unless(`^(-v|--version)`, "node: only --version", false)},
	{psrx.MustCompile(`^npm$`), unless(`^(ls|list|-v|--version|config (get|list)|view|root)`, "npm: only ls, view, config get", true)},
	{psrx.MustCompile(`^git$`), func(cmd, rest, sub string) string {
		if !gitAllowed.Match(rest) || gitPatch.Match(rest) {
			return "git: only status, log, remote, branch, rev-parse, describe, tag, ls-files"
		}
		return ""
	}},
	{psrx.MustCompile(`^curl$`), when("curl: only GET and HEAD to stdout", `(^|\s)(-o|-O|--output|--remote-name|-T|--upload-file|-d|--data\S*|-F|--form)\b`, `(-X|--request)\s+(?!GET|HEAD)`)},
	{psrx.MustCompile(`^wget$`), unless(`(-O\s*-|-qO-|--spider)`, "wget: only to stdout (-O -) or --spider", false)},
	{psrx.MustCompile(`^sed$`), when("sed: no in-place edits or writes", `(^|\s)-i|\bw\s|[;/]w\s*/|\be\b\s*$`)},
	{psrx.MustCompile(`^awk$`), when("awk: no system() or writes", `system\s*\(|\|\s*"|print[^;}]*>`)},
	{psrx.MustCompile(`^find$`), when("find: no -delete or -exec", `-(delete|exec|execdir|ok|okdir|fprint\w*|fls)\b`)},
	{psrx.MustCompile(`^sysctl$`), when("sysctl: read only", `-w\b|=`)},
	{psrx.MustCompile(`^crontab$`), unless(`^(-l|-u\s+\S+\s+-l)`, "crontab: only -l", false)},
	{psrx.MustCompile(`^ip$`), func(cmd, rest, sub string) string {
		if !ipAllowed.Match(rest) || ipChange.Match(rest) {
			return "ip: only show"
		}
		return ""
	}},
	{psrx.MustCompile(`^mount$`), func(cmd, rest, sub string) string {
		if rest != "" {
			return "mount: only without arguments"
		}
		return ""
	}},
	{psrx.MustCompile(`^openssl$`), unless(`^(version|x509|s_client)`, "openssl: only version and x509 inspection", true)},
	{psrx.MustCompile(`^(psql|mysql|redis-cli)$`), func(cmd, rest, sub string) string {
		return "database clients are not run: the database name, user and host are in the app's config files or .env (read_file masks secret values), docker compose config or docker inspect"
	}},
	{psrx.MustCompile(`^(helm|kubectl)$`), unless(`^(get|describe|version|list|ls|status|config (view|get-contexts|current-context)|api-resources|cluster-info|top|logs)`, "{cmd}: only get, describe, list, logs", true)},
	{psrx.MustCompile(`^(kind|k3s)$`), unless(`^(get|version|--version)`, "{cmd}: only get and version", true)},
	{psrx.MustCompile(`^(conda)$`), unless(`^(env list|list|info|--version)`, "conda: only listings", true)},
	{psrx.MustCompile(`^(cargo|go|java|rustc|ruby|php)$`), unless(`^(-?-?version|-v|-V|env|list)\b`, "{cmd}: only version queries", false)},
}
