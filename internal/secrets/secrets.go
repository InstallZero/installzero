// Package secrets keeps secret values from reaching the model: which files
// and variable names hold secrets, and redaction of tool output against
// known values and key formats. Ported from doctor/Tools.ps1,
// doctor/ReadOnly.ps1 and agent/LinuxTarget.ps1; fixtures/secrets holds the
// shared cases.
package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/InstallZero/installzero/internal/psrx"
)

var (
	secretPath = psrx.MustCompile(`(?i)((^|[\\/])\.env(\.(?!(example|sample|template)$)[^\\/]+)?$|(^|[\\/])secrets[\\/].|\.(pem|key|pfx|p12)$|(^|[\\/])id_(rsa|dsa|ecdsa|ed25519)|(^|[\\/])\.ssh[\\/]|\.git-credentials$|(^|[\\/])\.(netrc|npmrc|pypirc)$|\.aws[\\/]credentials$|\.docker[\\/]config\.json$)`)
	envFile    = psrx.MustCompile(`(?i)(^|[\\/])\.env(\.(?!(example|sample|template)$)[^\\/]+)?$`)
	// Names whose values are secrets (API_KEY, ..._TOKEN_SECRET,
	// SMTP_PASSWORD); not ..._TOKEN_TTL_SECONDS.
	secretName = psrx.MustCompile(`(?i)(^|_)(API_?KEY|KEY|SECRET|TOKEN|PASSWORD|PASSWD|PWD|PASS|CREDENTIALS?|COOKIE|AUTH)$`)
	linuxValue = psrx.MustCompile(`(?im)^(\s*[\w.-]*(password|passwd|secret|token|api_?key|private_?key|access_?key)[\w.-]*\s*[:=]\s*)(?!\s*$)(?!<secret>)[^\r\n#]+`)
	envLine    = psrx.MustCompile(`^\s*(export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
)

// Key formats and key=value pairs, applied in order.
var patterns = []struct {
	re   *psrx.Regexp
	repl string
}{
	{psrx.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`), "<secret>"},
	{psrx.MustCompile(`\bsk-(proj-|svcacct-)?[A-Za-z0-9_\-]{20,}`), "<secret>"},
	{psrx.MustCompile(`\bxai-[A-Za-z0-9_\-]{20,}`), "<secret>"},
	{psrx.MustCompile(`\bAIza[0-9A-Za-z_\-]{30,}`), "<secret>"},
	{psrx.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), "<secret>"},
	{psrx.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`), "<secret>"},
	{psrx.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`), "<secret>"},
	{psrx.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "<secret>"},
	{psrx.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), "<secret>"},
	{psrx.MustCompile(`(?i)(\bBearer\s+)[A-Za-z0-9._~+/\-]{16,}=*`), "$1<secret>"},
	{psrx.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`), "$1<secret>@"},
	{psrx.MustCompile(`(?i)((?<!<)(password|passwd|pwd|secret|api[_-]?key|access[_-]?token)["']?\s*[:=]\s*["']?)(?!<secret)[^\s"',;}\\<>]{4,}`), "$1<secret>"},
}

// IsSecretPath reports whether a file's contents are secret (.env files
// other than templates, keys, credentials).
func IsSecretPath(path string) bool { return secretPath.Match(path) }

// IsEnvFile reports whether a path is a .env file other than a template.
func IsEnvFile(path string) bool { return envFile.Match(path) }

// IsSecretName reports whether a variable's value is secret.
func IsSecretName(name string) bool { return secretName.Match(name) }

// Value is a known secret value and the variable it came from.
type Value struct {
	Value string
	Name  string
}

// Redact replaces the known values (longest first) by <secret:NAME>, then
// key formats and key=value pairs by <secret>.
func Redact(text string, known []Value) string {
	if text == "" {
		return text
	}
	known = append([]Value(nil), known...)
	sort.SliceStable(known, func(i, j int) bool { return length(known[i].Value) > length(known[j].Value) })
	for _, s := range known {
		if s.Value != "" {
			text = strings.ReplaceAll(text, s.Value, "<secret:"+s.Name+">")
		}
	}
	for _, p := range patterns {
		text = p.re.Replace(text, p.repl)
	}
	return text
}

// RedactLinux replaces the values of password, secret, token and key
// settings in config files and environment listings.
func RedactLinux(text string) string {
	if text == "" {
		return text
	}
	return linuxValue.Replace(text, "$1<secret>")
}

// EnvEntry is one line of a .env file; Name is "" for lines that set nothing.
type EnvEntry struct {
	Name  string
	Value string
	Line  string
}

// EnvEntries parses a .env file's content.
func EnvEntries(content string) []EnvEntry {
	var out []EnvEntry
	for _, line := range Lines(content) {
		if g := envLine.Groups(line); g != nil {
			v := strings.Trim(strings.Trim(strings.TrimSpace(g[3]), `"`), "'")
			out = append(out, EnvEntry{Name: g[2], Value: v, Line: line})
		} else {
			out = append(out, EnvEntry{Line: line})
		}
	}
	return out
}

// MaskEnvFile shows a .env file as the model sees it: every name, values of
// secret-named variables replaced by their length, the rest as written
// (callers redact it like any other output).
func MaskEnvFile(path, content string) string {
	var lines []string
	for i, e := range EnvEntries(content) {
		text := e.Line
		if e.Name != "" && IsSecretName(e.Name) {
			text = e.Name + "="
			if e.Value != "" {
				text += fmt.Sprintf("<secret, %d characters>", length(e.Value))
			}
		}
		lines = append(lines, fmt.Sprintf("%5d| %s", i+1, text))
	}
	return path + " (secret values masked; empty means the variable is set but blank)\n" + strings.Join(lines, "\n")
}

// Lines splits text into lines the way Get-Content does: at \r\n, \n or
// \r, without an empty last line after a final line break.
func Lines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' || text[i] == '\r' {
			out = append(out, text[start:i])
			if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

var urlPassword = psrx.MustCompile(`://[^/\s:@]+:([^@\s/]+)@`)

// Known is Get-SecretValues: values of secret-named variables in dir's .env
// files and in environ (NAME=value entries), and passwords embedded in URLs
// there. A value seen twice keeps the last name.
func Known(dir string, environ []string) []Value {
	found := map[string]string{}
	var order []string
	add := func(name, value string) {
		set := func(v string) {
			if _, ok := found[v]; !ok {
				order = append(order, v)
			}
			found[v] = name
		}
		if g := urlPassword.Groups(value); g != nil && length(g[1]) >= 4 {
			set(g[1])
		}
		if IsSecretName(name) && length(value) >= 6 {
			set(value)
		}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(strings.ToLower(e.Name()), ".env") || !IsEnvFile(e.Name()) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for _, x := range EnvEntries(string(data)) {
				if x.Name != "" {
					add(x.Name, x.Value)
				}
			}
		}
	}
	for _, kv := range environ {
		if i := strings.Index(kv, "="); i > 0 {
			add(kv[:i], kv[i+1:])
		}
	}
	out := make([]Value, 0, len(order))
	for _, v := range order {
		out = append(out, Value{Value: v, Name: found[v]})
	}
	return out
}

// length counts UTF-16 code units, as .NET string lengths do.
func length(s string) int { return len(utf16.Encode([]rune(s))) }
