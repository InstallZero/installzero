package runner

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/policy"
)

// A block that runs a script file is only as safe as the script: the
// command alone (.\setup.ps1, bash scripts/setup.sh) says nothing about what
// it changes. Scripts a block runs are read from the target and classified
// like blocks, following the scripts they run in turn; a script that cannot
// be read, or whose path is not literal, makes the block machine-wide.
// Programs and other languages (python main.py, make, npm install) are not
// inspected.

const scriptDepth = 3

var (
	scriptPath = `(['"]?)([^\s'";|&()]+?\.(?:ps1|sh|bat|cmd))\b`
	scriptCall = []*regexp.Regexp{
		// At a command position: .\x.ps1, & '.\x.ps1', . .\x.ps1, ./x.sh,
		// bash x.sh, sh x.sh, source x.sh, . x.sh.
		regexp.MustCompile(`(?im)(?:^|[;|{(]|&&)\s*(?:&\s*|\.\s+|(?:bash|sh|source)\s+(?:-\w+\s+)*)?` + scriptPath),
		// Arguments that name a script to run.
		regexp.MustCompile(`(?i)(?:-File|-FilePath)\s+` + scriptPath),
		regexp.MustCompile(`(?i)\bcmd(?:\.exe)?\s+/c\s+` + scriptPath),
	}
	absPath = regexp.MustCompile(`^(?:[A-Za-z]:[\\/]|\\\\|/)`)
	// & (Join-Path $repo 'x.ps1'), & $script, . $env:x: what runs is
	// computed.
	computedCall = regexp.MustCompile(`(?m)(?:^|[;|{(]|&&)\s*[&.]\s*[($]`)
	blockComment = regexp.MustCompile(`(?s)<#.*?#>`)
	lineComment  = regexp.MustCompile(`(?m)^\s*#.*$`)
)

// computed stands for a script whose path is worked out when it runs.
const computed = "$(computed path)"

// scriptsIn lists the script paths code runs, as written, ignoring comments.
func scriptsIn(code string) []string {
	code = lineComment.ReplaceAllString(blockComment.ReplaceAllString(code, ""), "")
	var out []string
	seen := map[string]bool{}
	if computedCall.MatchString(code) {
		seen[computed], out = true, append(out, computed)
	}
	for _, re := range scriptCall {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			if p := m[2]; !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

var rank = map[string]int{policy.Read: 1, policy.Project: 2, policy.System: 3, policy.Never: 4}

// inspector reads and classifies scripts, once each.
type inspector struct {
	o     Options
	seen  map[string]*policy.Result
	shell string // the target's shell, for reading files
}

func (in *inspector) join(dir, p string) string {
	if absPath.MatchString(p) {
		return p
	}
	sep := "/"
	if in.shell == "powershell" || in.shell == "pwsh" {
		sep = `\`
		p = strings.TrimPrefix(strings.TrimPrefix(p, `.\`), "./")
	} else {
		p = strings.TrimPrefix(p, "./")
	}
	return strings.TrimRight(dir, `\/`) + sep + p
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return "."
}

func (in *inspector) read(ctx context.Context, path string) (string, bool) {
	var code string
	if in.shell == "powershell" || in.shell == "pwsh" {
		code = "Get-Content -Raw -LiteralPath '" + strings.ReplaceAll(path, "'", "''") + "'"
	} else {
		code = "cat -- '" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	}
	e := in.o.Read
	if e == nil {
		e = in.o.Exec
	}
	r := e.Run(ctx, in.shell, code, "", time.Minute)
	return r.Out, r.Code == 0
}

// classify is the tier of a script at path (resolved), with what it runs.
func (in *inspector) classify(ctx context.Context, path string, depth int) policy.Result {
	if r := in.seen[path]; r != nil {
		return *r
	}
	in.seen[path] = &policy.Result{Tier: policy.System, Reason: "runs " + path + " again"}
	res := func() policy.Result {
		if strings.HasSuffix(path, computed) {
			return policy.Result{Tier: policy.System, Reason: "runs a script or program whose path is computed when it runs, so it cannot be checked"}
		}
		if strings.ContainsAny(path, "$%`{") {
			return policy.Result{Tier: policy.System, Reason: "runs a script whose path is not literal (" + path + "), so it cannot be checked"}
		}
		lower := strings.ToLower(path)
		var shell string
		switch {
		case strings.HasSuffix(lower, ".ps1"):
			shell = "powershell"
		case strings.HasSuffix(lower, ".sh"):
			shell = "bash"
		default:
			return policy.Result{Tier: policy.System, Reason: "runs " + path + " (batch files are not checked)"}
		}
		body, ok := in.read(ctx, path)
		if !ok {
			return policy.Result{Tier: policy.System, Reason: "runs " + path + ", which cannot be read now (an earlier step may create it)"}
		}
		tiers, err := policy.Classify(ctx, shell, []string{in.o.Dir}, body)
		if err != nil || len(tiers) == 0 {
			return policy.Result{Tier: policy.System, Reason: "runs " + path + ", which could not be checked"}
		}
		r := tiers[0]
		if depth < scriptDepth {
			for _, p := range scriptsIn(body) {
				if n := in.classify(ctx, in.join(dirOf(path), p), depth+1); rank[n.Tier] > rank[r.Tier] {
					r = n
				}
			}
		} else if len(scriptsIn(body)) > 0 {
			r = policy.Result{Tier: policy.System, Reason: "runs scripts nested more than " + fmt.Sprint(scriptDepth) + " deep"}
		}
		if r.Tier != policy.Read && !strings.HasPrefix(r.Reason, "runs ") {
			r.Reason = "runs " + path + ": " + r.Reason
		}
		return r
	}()
	in.seen[path] = &res
	return res
}

// inspectScripts raises each block's tier to that of the scripts it runs.
func inspectScripts(ctx context.Context, o Options, blocks []*Block) {
	ins := map[string]*inspector{}
	for _, b := range blocks {
		calls := scriptsIn(b.Code)
		if len(calls) == 0 {
			continue
		}
		in := ins[b.Shell]
		if in == nil {
			in = &inspector{o: o, seen: map[string]*policy.Result{}, shell: b.Shell}
			ins[b.Shell] = in
		}
		for _, p := range calls {
			r := in.classify(ctx, in.join(o.Dir, p), 1)
			if rank[r.Tier] > rank[b.Tier.Tier] {
				b.Tier = r
			}
		}
	}
}
