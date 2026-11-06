package probe

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/InstallZero/installzero/internal/psrx"
	"github.com/InstallZero/installzero/internal/secrets"
	"github.com/dlclark/regexp2"
)

// The probe recovers the procedure from what runs, blind to anything that
// describes the install: docs, install and uninstall scripts, installer state
// and logs, shell history, InstallZero's own files (including the expected
// answers), and the test harness's hidden paths.
const (
	izDirs     = `(\\|/)(agent|doctor|console|catalog|schemas|docs|prompts|fixtures|internal|cmd|\.env)((\\|/)|$)`
	izByName   = `|installzero(\\|/)(agent|doctor|console|catalog|engine|eval|prompts|fixtures)((\\|/)|$)`
	doctorHide = `|scripts(\\|/)install(\\|/)doctor((\\|/)|\b)|scripts(\\|/)install(\\|/)agent(\\|/)eval|doctor-assets|rl-roboracer(\\|/)(doctor|eval-stash)\b|eval-results\.jsonl|Scenarios\.ps1|Invoke-DoctorEval`
	probeHide  = `|scripts(\\|/)install(\\|/)agent|installzero(\\|/)(probe|agent|eval)` +
		`|(?i)(^|\\|/)(README|INSTALL|SETUP|GETTING[-_]?STARTED|QUICKSTART|CONTRIBUTING|CHANGELOG|HISTORY)[^\\/]*$|(?i)(\\|/)docs?(\\|/)|(?i)\.(md|markdown|rst|adoc)$` +
		`|(?i)(^|\\|/)(un)?install[^\\/]*\.(ps1|psm1|sh|bash|bat|cmd|py)$|(?i)(^|\\|/)(setup|bootstrap)\.(ps1|sh|bash|bat|cmd)$|(?i)(\\|/)scripts(\\|/)install(\\|/)|(?i)(\\|/)installers?(\\|/)` +
		`|(?i)PSReadLine(\\|/)ConsoleHost_history|(?i)(^|\\|/)\.(bash|zsh|sh|python|node_repl|psql|mysql)_history$|(?i)(\\|/)Microsoft(\\|/)Windows(\\|/)Recent(\\|/)`
	// The WSL harness's own settings, and package-manager logs.
	linuxHide = `|/etc/wsl\.conf|/var/log/(apt|dpkg|installer)\b|\.(wget-hsts|lesshst)$`
	ownFiles  = `|scripts(\\|/)install(\\|/)(agent|doctor)|installzero(\\|/)probe|doctor-assets|eval-results\.jsonl`
)

var escapedSeparator = regexp.MustCompile(`\\\\|/`)

// rootPattern matches a folder under InstallZero's root, with either
// separator.
func rootPattern(root string) string {
	return escapedSeparator.ReplaceAllLiteralString(regexp2.Escape(root), `(\\|/)`) + izDirs
}

// installZeroPattern is $script:InstallZeroPathRegex for a root folder (a
// Windows path, also matched as /mnt/<drive>/... from WSL).
func installZeroPattern(root string) string {
	root = strings.TrimRight(root, `\`)
	p := rootPattern(root)
	if len(root) > 2 && root[1] == ':' {
		p += "|" + rootPattern("/mnt/"+strings.ToLower(root[:1])+strings.ReplaceAll(root[2:], `\`, "/"))
	}
	return p + izByName
}

// blind hides install files from the model: paths it may not open, and
// lines that occur only in hidden files.
type blind struct {
	hidden   *psrx.Regexp
	own      *psrx.Regexp
	out      string
	paths    []string
	leak     map[string]bool
	withheld int
	attempts int
}

func newBlind(root, out string, hide []string, linux bool) *blind {
	pattern := installZeroPattern(root) + doctorHide + probeHide
	b := &blind{out: out, leak: map[string]bool{}}
	for _, h := range hide {
		for _, part := range strings.Split(h, ",") {
			part = strings.Trim(strings.Trim(strings.TrimSpace(part), "'"), `"`)
			if part == "" {
				continue
			}
			full, err := filepath.Abs(expandWindowsVars(part))
			if err != nil {
				continue
			}
			full = strings.TrimRight(full, `\`)
			b.paths = append(b.paths, full)
			pattern += `|(?i)` + regexp2.Escape(full) + `(\\|/|$)`
		}
	}
	if linux {
		pattern += linuxHide
	}
	b.hidden = psrx.MustCompile(pattern)
	b.own = psrx.MustCompile(`(?i)` + installZeroPattern(root) + ownFiles)
	return b
}

var windowsVar = regexp.MustCompile(`%([^%]+)%`)

// expandWindowsVars is [Environment]::ExpandEnvironmentVariables: %NAME%
// for a set variable, anything else left as is.
func expandWindowsVars(s string) string {
	return windowsVar.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}

// isHidden is the probe's Test-HiddenPath: the draft being written is the
// one hidden-looking file the model may read.
func (b *blind) isHidden(path string) bool { return path != b.out && b.hidden.Match(path) }

var lineBreak = regexp.MustCompile(`\r?\n`)

// removeOwnLines drops lines naming InstallZero's own files. A hidden path
// that shows up in what runs is evidence; only file contents are hidden,
// by the content guard.
func (b *blind) removeOwnLines(text string) string {
	var kept []string
	for _, l := range lineBreak.Split(text, -1) {
		if !b.own.Match(l) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

var (
	skipDirs  = psrx.MustCompile(`[\\/](\.git|node_modules|Library|Temp|Logs|obj|bin|__pycache__|\.venv|venv|site-packages|UnityBinary)[\\/]`)
	textExt   = psrx.MustCompile(`^\.(ps1|psm1|psd1|sh|bash|bat|cmd|py|md|markdown|rst|txt|json|ya?ml|toml|ini|cfg|conf|js|ts|cs|xml|log)$`)
	buildName = psrx.MustCompile(`^(Dockerfile|Makefile)`)
	numbered  = regexp.MustCompile(`^\s*\d+\|\s?`)
	exitLine  = psrx.MustCompile(`^\[exit \d+\]$`)
)

// textFiles is Get-ProbeTextFiles: the text files under root (root itself
// when it is a file), first max.
func textFiles(root string, max int) []string {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return []string{root}
	}
	var out []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if len(out) >= max {
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() >= 512*1024 || skipDirs.Match(path) || secrets.IsSecretPath(path) {
			return nil
		}
		if textExt.Match(filepath.Ext(path)) || buildName.Match(d.Name()) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func fileLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return secrets.Lines(strings.ToValidUTF8(string(data), "\uFFFD"))
}

// guardable is a line long enough to be told apart from common ones.
func guardable(line string) (string, bool) {
	t := strings.TrimSpace(line)
	return t, len(utf16.Encode([]rune(t))) >= 30
}

// guard collects the lines to withhold: those of hidden files (the hide
// paths and PowerShell's history) that no visible file of the project
// (under linuxDir on the target) has too.
func (b *blind) guard(ctx context.Context, run func(ctx context.Context, script string, timeout time.Duration) string, linuxDir string) {
	visible := map[string]bool{}
	if linuxDir != "" {
		out := run(ctx, "find '"+linuxDir+`' -maxdepth 4 -type f -size -512k \( -name '*.yml' -o -name '*.yaml' -o -name '*.json' -o -name '*.conf' -o -name '*.env' -o -name '*.toml' -o -name '*.ini' -o -name '*.cfg' -o -name '*.py' -o -name '*.sh' -o -name '*.txt' -o -name 'Dockerfile' -o -name 'env' \) 2>/dev/null | head -2000 | while read f; do cat "$f" 2>/dev/null; done`, 120*time.Second)
		for _, l := range strings.Split(out, "\n") {
			if t, ok := guardable(l); ok {
				visible[t] = true
			}
		}
	}
	var hidden []string
	for _, h := range b.paths {
		hidden = append(hidden, textFiles(h, 5000)...)
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		history := filepath.Join(appData, `Microsoft\Windows\PowerShell\PSReadLine\ConsoleHost_history.txt`)
		if _, err := os.Stat(history); err == nil {
			hidden = append(hidden, history)
		}
	}
	for _, f := range hidden {
		for _, l := range fileLines(f) {
			if t, ok := guardable(l); ok && !visible[t] {
				b.leak[t] = true
			}
		}
	}
}

// removeLeaked is Remove-LeakedLines: lines that occur only in hidden files
// are withheld from every tool output, however the model reached them (a
// wildcard, a copy, git history).
func (b *blind) removeLeaked(text string) string {
	if len(b.leak) == 0 || text == "" {
		return text
	}
	n := 0
	var kept []string
	for _, l := range lineBreak.Split(text, -1) {
		if t, ok := guardable(numbered.ReplaceAllString(l, "")); ok && b.leak[t] {
			n++
		} else {
			kept = append(kept, l)
		}
	}
	if n == 0 {
		return text
	}
	b.withheld += n
	// Mostly hidden content means a hidden file was dumped: its short lines go too.
	left := 0
	for _, l := range kept {
		if strings.TrimSpace(l) != "" && !exitLine.Match(l) {
			left++
		}
	}
	if n >= 5 && n >= left {
		return "[output withheld: it is the content of install files, which the probe does not see]"
	}
	return strings.Join(kept, "\n") + fmt.Sprintf("\n[%d line(s) withheld: they come from install files, which the probe does not see]", n)
}
