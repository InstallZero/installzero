package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/psrx"
	"github.com/InstallZero/installzero/internal/readonly"
	"github.com/InstallZero/installzero/internal/secrets"
	"github.com/InstallZero/installzero/prompts"
)

const refusedHidden = "refused: that path is hidden: the probe works from what runs, not from install files or docs."

var gitContent = psrx.MustCompile(`(?i)\bgit\b[^|;\r\n]*\s(show|cat-file|diff|grep|archive|blame|whatchanged|format-patch|bundle|log\b[^|;\r\n]*\s(-p|--patch|-L|--follow)\b)`)

// Tools are the tool definitions offered to the model on a Linux target.
func Tools() ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, key := range []string{"linux/run_command", "linux/read_file", "linux/list_dir", "linux/grep", "linux/inventory", "probe/test_check", "probe/write_install_md", "probe/finish"} {
		t, err := prompts.Tool(key, prompts.Values{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// dispatch runs one tool call.
func (p *probe) dispatch(ctx context.Context, name string, a *agentloop.Args) (string, error) {
	if name == "write_install_md" {
		return p.draftCheck(ctx, a.String("content")), nil
	}
	code := ""
	switch name {
	case "run_command":
		code = a.String("command")
	case "test_check":
		code = a.String("code")
	}
	if gitContent.Match(code) {
		p.blind.attempts++
		return "refused: git commands that print file contents, diffs or patches are not available, since they would show the hidden install files. git remote, branch, status, rev-parse and log without -p work.", nil
	}
	out, err := p.linuxTool(ctx, name, a, code)
	if err != nil {
		return "", err
	}
	return p.blind.removeLeaked(out), nil
}

// linuxTool is Invoke-LinuxProbeTool.
func (p *probe) linuxTool(ctx context.Context, name string, a *agentloop.Args, code string) (string, error) {
	cwd := p.linuxDir
	if cwd == "" {
		cwd = "/"
	}
	path := a.String("path")
	if path != "" && !strings.HasPrefix(path, "/") {
		path = cwd + "/" + path
	}
	switch name {
	case "read_file", "list_dir", "grep":
		if path != "" && p.blind.isHidden(path) {
			p.blind.attempts++
			return refusedHidden, nil
		}
	case "run_command", "test_check":
		if p.blind.isHidden(code) {
			p.blind.attempts++
			return refusedHidden, nil
		}
	}
	orHere := func(path string) string {
		if path == "" {
			return cwd
		}
		return path
	}
	var out string
	switch name {
	case "inventory":
		out = p.target.Inventory(ctx, a.String("area"), path)
		if f := a.String("filter"); f != "" {
			re, err := compileFilter(f)
			if err != nil {
				return "", err
			}
			var kept []string
			for _, l := range strings.Split(out, "\n") {
				if re.Match(l) || strings.HasSuffix(l, ":") {
					kept = append(kept, l)
				}
			}
			out = strings.Join(kept, "\n")
		}
	case "read_file":
		if secrets.IsSecretPath(path) {
			out = "refused: that file holds secrets."
			break
		}
		start, err := psInt("0" + a.String("start_line"))
		if err != nil {
			return "", err
		}
		start = max(1, start)
		n := 200
		if truthy(a.Values["max_lines"]) {
			if n, err = psInt(a.String("max_lines")); err != nil {
				return "", err
			}
		}
		out = p.target.ReadFile(ctx, path, start, min(400, n))
	case "list_dir":
		out = p.target.ListDir(ctx, orHere(path))
	case "grep":
		var kept []string
		for _, l := range strings.Split(p.target.Grep(ctx, a.String("pattern"), a.String("include"), orHere(path)), "\n") {
			if l != "" && !p.blind.isHidden(grepFile.Replace(l, "")) {
				kept = append(kept, l)
			}
		}
		out = "no matches"
		if len(kept) > 0 {
			out = strings.Join(kept, "\n")
		}
	case "run_command", "test_check":
		if why := readonly.Bash(code); why != "" {
			out = "refused: " + why + ". Only read-only commands run here."
			break
		}
		timeout := 60
		if name == "run_command" && truthy(a.Values["timeout_seconds"]) {
			t, err := psInt(a.String("timeout_seconds"))
			if err != nil {
				return "", err
			}
			timeout = min(180, t)
		}
		out = p.target.Bash(ctx, code, time.Duration(timeout)*time.Second, cwd).Format(0)
	default:
		out = "error: unknown tool '" + name + "'"
	}
	return secrets.RedactLinux(secrets.Redact(p.blind.removeOwnLines(out), p.known)), nil
}

var grepFile = psrx.MustCompile(`:\d+:.*$`)

func compileFilter(f string) (re *psrx.Regexp, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid filter pattern '%s': %v", f, r)
		}
	}()
	return psrx.MustCompile("(?i)" + f), nil
}

// psInt is PowerShell's [int] cast of a string: a number, rounded half to
// even.
func psInt(s string) (int, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) > math.MaxInt32 {
		return 0, fmt.Errorf(`Cannot convert value "%s" to type "System.Int32". Error: "Input string was not in a correct format."`, s)
	}
	return int(math.RoundToEven(f)), nil
}

// truthy is a JSON value as a PowerShell condition sees it.
func truthy(raw json.RawMessage) bool {
	switch t := strings.TrimSpace(string(raw)); t {
	case "", "null", "false", `""`, "[]":
		return false
	default:
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f != 0
		}
		return true
	}
}
