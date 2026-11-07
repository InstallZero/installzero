// Package runner carries out an install.md: for each step that applies to
// the machine it runs detect (skipping the step when it passes), install and
// verify, in code, without a model. Every block gets its safety tier first;
// machine-wide blocks are approved together before the run, and never-tier
// blocks are refused. A failed step goes to a Fixer (the catalog, then a
// model's fix episode) when there is one.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/output"
	"github.com/InstallZero/installzero/internal/policy"
	"github.com/InstallZero/installzero/internal/target"
)

// Status is where a step ended.
type Status string

const (
	Done          Status = "done"           // installed and verified
	AlreadyDone   Status = "already-done"   // detect passed
	NotApplicable Status = "not-applicable" // requirements not met on this machine
	Failed        Status = "failed"
	Refused       Status = "refused"      // a never-tier block
	NotApproved   Status = "not-approved" // the user declined its machine-wide blocks
	NotRun        Status = "not-run"      // an earlier step stopped the run
)

// Block is one block chosen for this machine, with its tier.
type Block struct {
	*installmd.Block
	Shell string // the block's shell, or the machine's default
	Tier  policy.Result
}

// Step is a step of the plan.
type Step struct {
	*installmd.Step
	Unmet                   []string
	Detect, Install, Verify *Block
}

// Blocks are the step's chosen blocks in run order.
func (s *Step) Blocks() []*Block {
	var out []*Block
	for _, b := range []*Block{s.Detect, s.Install, s.Verify} {
		if b != nil {
			out = append(out, b)
		}
	}
	return out
}

// Highest is the step's highest tier (never above system above project
// above read); "" for a step that does not apply.
func (s *Step) Highest() string {
	rank := map[string]int{policy.Read: 1, policy.Project: 2, policy.System: 3, policy.Never: 4}
	best := ""
	for _, b := range s.Blocks() {
		if rank[b.Tier.Tier] > rank[best] {
			best = b.Tier.Tier
		}
	}
	return best
}

// Plan is a document's steps for one machine.
type Plan struct {
	Name  string
	Doc   *installmd.Doc
	Steps []*Step
}

// System is the steps with a machine-wide block, which the user approves
// before the run.
func (p *Plan) System() []*Step {
	var out []*Step
	for _, s := range p.Steps {
		if s.Unmet == nil && s.Highest() == policy.System {
			out = append(out, s)
		}
	}
	return out
}

// Options say where and how a plan runs.
type Options struct {
	Exec    Exec
	Machine installmd.Machine
	// Dir is the install folder: where blocks run (when it exists) and what
	// project-tier blocks may change.
	Dir string
	// Values fill {{NAME}} placeholders; INSTALL_DIR defaults to Dir and
	// REPO_DIR to Dir too (installzero install runs at the project root).
	Values map[string]string
	// Env is set in every block (secrets the install.md names, settings).
	Env map[string]string
	// Timeouts per block kind; zero values are 5 minutes for detect and
	// verify and 30 for install.
	DetectTimeout, InstallTimeout, VerifyTimeout time.Duration
	// Approve is asked once, before the run, for the plan's machine-wide
	// steps; nil approves nothing. Confirm is asked for a machine-wide block
	// a fixer proposes during the run; nil declines.
	Approve func(steps []*Step) bool
	Confirm func(step *Step, b *Block) bool
	Fixer   Fixer
	// Attempts is how many fixes a step may get (default 3).
	Attempts int
	// Lock, when set, is the file where progress is written after each step.
	Lock   string
	Events func(Event)
}

// Event is progress for the console.
type Event struct {
	Step   *Step
	Kind   string // step, block, result, fix
	Block  string // detect, install, verify
	Status Status
	Code   int
	Text   string
}

// Failure is a failed install or verify, handed to the Fixer.
type Failure struct {
	Step    *Step
	Block   string // install, verify or detect (a detect-only step)
	Code    int
	Output  string // summarised
	Attempt int    // 1 for the first fix
	History []string
}

// Fix is what a Fixer did about a failure.
type Fix struct {
	// Action: "retry" (the fix is applied; run the step again), "replace"
	// (run Block instead of the step's install block from now on) or
	// "give-up".
	Action string
	Block  *installmd.Block
	Note   string // for the report: what was done, or what the user must do
}

// Fixer tries to repair a failed step.
type Fixer interface {
	Fix(ctx context.Context, f Failure) Fix
}

// StepResult is a step's outcome.
type StepResult struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Status   Status    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Output   string    `json:"output,omitempty"` // the failing block's summarised output
	Fixes    []string  `json:"fixes,omitempty"`
	Proposed string    `json:"proposed,omitempty"` // a replacement install block to put in the install.md
	Started  time.Time `json:"started"`
	Seconds  float64   `json:"seconds"`
}

// Report is a run's outcome.
type Report struct {
	Name     string       `json:"name"`
	Dir      string       `json:"dir"`
	Started  time.Time    `json:"started"`
	Finished time.Time    `json:"finished"`
	OK       bool         `json:"ok"`
	Steps    []StepResult `json:"steps"`
}

// NewPlan chooses each step's blocks for the machine and classifies them.
func NewPlan(ctx context.Context, doc *installmd.Doc, o Options) (*Plan, error) {
	name, _ := doc.Meta.Get("name")
	p := &Plan{Name: name, Doc: doc}
	def := "bash"
	if strings.EqualFold(o.Machine.OS, "windows") {
		def = "powershell"
	}
	values := o.values()
	for _, s := range doc.Steps {
		st := &Step{Step: s, Unmet: s.Unmet(o.Machine)}
		p.Steps = append(p.Steps, st)
		if st.Unmet != nil {
			continue
		}
		for kind, dst := range map[string]**Block{"detect": &st.Detect, "install": &st.Install, "verify": &st.Verify} {
			b := s.Select(kind, o.Machine)
			if b == nil {
				continue
			}
			shell := b.Shell
			if shell == "" {
				shell = def
			}
			cp := *b
			cp.Code = installmd.Expand(b.Code, values)
			*dst = &Block{Block: &cp, Shell: shell}
		}
	}
	// One Classify per shell: PowerShell's rules run in a PowerShell process.
	byShell := map[string][]*Block{}
	for _, s := range p.Steps {
		for _, b := range s.Blocks() {
			byShell[b.Shell] = append(byShell[b.Shell], b)
		}
	}
	for shell, blocks := range byShell {
		codes := make([]string, len(blocks))
		for i, b := range blocks {
			codes[i] = b.Code
		}
		tiers, err := policy.Classify(ctx, shell, []string{o.Dir}, codes...)
		if err != nil {
			return nil, err
		}
		for i, b := range blocks {
			b.Tier = tiers[i]
		}
	}
	return p, nil
}

func (o Options) values() map[string]string {
	v := map[string]string{"INSTALL_DIR": o.Dir, "REPO_DIR": o.Dir}
	for k, x := range o.Values {
		v[k] = x
	}
	return v
}

func (o Options) event(e Event) {
	if o.Events != nil {
		o.Events(e)
	}
}

// Run carries out a plan. It stops at the first step that fails, is
// refused or is not approved; the steps after it are not run.
func Run(ctx context.Context, p *Plan, o Options) Report {
	if o.Attempts == 0 {
		o.Attempts = 3
	}
	rep := Report{Name: p.Name, Dir: o.Dir, Started: time.Now()}
	approved := false
	if sys := p.System(); len(sys) > 0 && o.Approve != nil {
		approved = o.Approve(sys)
	}
	stopped := false
	for _, s := range p.Steps {
		r := StepResult{ID: s.ID, Title: s.Title, Started: time.Now()}
		switch {
		case stopped:
			r.Status = NotRun
		case s.Unmet != nil:
			r.Status, r.Reason = NotApplicable, strings.Join(s.Unmet, "; ")
		case s.Highest() == policy.Never:
			r.Status = Refused
			for _, b := range s.Blocks() {
				if b.Tier.Tier == policy.Never {
					r.Reason = fmt.Sprintf("%s block: %s", b.Kind, b.Tier.Reason)
				}
			}
		case s.Highest() == policy.System && !approved:
			r.Status, r.Reason = NotApproved, "has machine-wide commands the user did not approve"
		default:
			o.event(Event{Step: s, Kind: "step"})
			r = runStep(ctx, s, o, r)
		}
		r.Seconds = time.Since(r.Started).Seconds()
		if r.Status == Failed || r.Status == Refused || r.Status == NotApproved {
			stopped = true
		}
		if r.Status != NotRun {
			o.event(Event{Step: s, Kind: "result", Status: r.Status, Text: r.Reason})
		}
		rep.Steps = append(rep.Steps, r)
		if o.Lock != "" {
			writeLock(o.Lock, rep)
		}
	}
	rep.Finished = time.Now()
	rep.OK = !stopped
	if o.Lock != "" {
		writeLock(o.Lock, rep)
	}
	return rep
}

func (o Options) run(ctx context.Context, s *Step, b *Block) target.Result {
	timeout := map[string]time.Duration{"detect": o.DetectTimeout, "install": o.InstallTimeout, "verify": o.VerifyTimeout}[b.Kind]
	if timeout == 0 {
		timeout = 5 * time.Minute
		if b.Kind == "install" {
			timeout = 30 * time.Minute
		}
	}
	o.event(Event{Step: s, Kind: "block", Block: b.Kind})
	res := o.Exec.Run(ctx, b.Shell, withEnv(b.Shell, b.Code, o.Env), o.Dir, timeout)
	for k, v := range o.Env {
		if len(v) >= 4 {
			res.Out = strings.ReplaceAll(res.Out, v, "["+k+"]")
		}
	}
	o.event(Event{Step: s, Kind: "block-done", Block: b.Kind, Code: res.Code})
	return res
}

func runStep(ctx context.Context, s *Step, o Options, r StepResult) StepResult {
	// A passing detect skips the step only when verify agrees: detect checks
	// are often weaker than verify (a config file exists, the service is
	// active) and would leave a broken step marked done.
	if s.Detect != nil {
		if res := o.run(ctx, s, s.Detect); res.Code == 0 {
			if s.Verify == nil || o.run(ctx, s, s.Verify).Code == 0 {
				r.Status = AlreadyDone
				return r
			}
		}
	}
	install := s.Install
	for attempt := 1; ; attempt++ {
		var f Failure
		if install == nil {
			// A detect-only step whose detect failed: something the user or
			// an earlier step must provide.
			f = Failure{Step: s, Block: "detect", Code: 1, Output: "detect failed and the step has no install block"}
		} else if res := o.run(ctx, s, install); res.Code != 0 {
			f = Failure{Step: s, Block: "install", Code: res.Code, Output: describe(res)}
		} else if s.Verify == nil {
			r.Status = Done
			return r
		} else if v := o.run(ctx, s, s.Verify); v.Code != 0 {
			f = Failure{Step: s, Block: "verify", Code: v.Code, Output: describe(v)}
		} else {
			r.Status = Done
			return r
		}
		r.Output = f.Output
		r.Reason = fmt.Sprintf("%s exited %d", f.Block, f.Code)
		if o.Fixer == nil || attempt > o.Attempts {
			r.Status = Failed
			return r
		}
		f.Attempt, f.History = attempt, r.Fixes
		fix := o.Fixer.Fix(ctx, f)
		o.event(Event{Step: s, Kind: "fix", Text: fix.Action + ": " + fix.Note})
		r.Fixes = append(r.Fixes, fmt.Sprintf("%s: %s", fix.Action, fix.Note))
		switch fix.Action {
		case "retry":
		case "replace":
			if fix.Block == nil {
				r.Status = Failed
				return r
			}
			nb := &Block{Block: fix.Block, Shell: fix.Block.Shell}
			if nb.Shell == "" && install != nil {
				nb.Shell = install.Shell
			}
			nb.Kind = "install"
			res, err := policy.Classify(ctx, nb.Shell, []string{o.Dir}, nb.Code)
			if err != nil {
				r.Status, r.Reason = Failed, err.Error()
				return r
			}
			nb.Tier = res[0]
			switch {
			case nb.Tier.Tier == policy.Never:
				r.Status, r.Reason = Refused, "the proposed fix: "+nb.Tier.Reason
				return r
			case nb.Tier.Tier == policy.System && (o.Confirm == nil || !o.Confirm(s, nb)):
				r.Status, r.Reason = NotApproved, "the proposed fix is machine-wide and was not approved"
				return r
			}
			install = nb
			r.Proposed = nb.Code
		default:
			r.Status = Failed
			if fix.Note != "" {
				r.Reason += "; " + fix.Note
			}
			return r
		}
	}
}

var requiredVar = regexp.MustCompile(`\$\{(\w+):\?`)

// RequiredEnv lists the variables the plan's blocks insist on (bash
// ${NAME:?message}) that are not in env, in order of first use.
func (p *Plan) RequiredEnv(env map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Steps {
		for _, b := range s.Blocks() {
			for _, m := range requiredVar.FindAllStringSubmatch(b.Code, -1) {
				if n := m[1]; n != "" && !seen[n] {
					seen[n] = true
					if _, ok := env[n]; !ok {
						out = append(out, n)
					}
				}
			}
		}
	}
	return out
}

// withEnv sets variables at the top of a block, so they reach a target
// that does not share this process's environment (WSL).
func withEnv(shell, code string, env map[string]string) string {
	if len(env) == 0 {
		return code
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if isPowerShell(shell) {
			fmt.Fprintf(&b, "$env:%s = '%s'\n", k, strings.ReplaceAll(env[k], "'", "''"))
		} else {
			fmt.Fprintf(&b, "export %s=%s\n", k, target.Quote(env[k]))
		}
	}
	return b.String() + code
}

// describe is a block's output as the report and a fixer read it.
func describe(res target.Result) string {
	s := output.Summarize(res.Out, 6000)
	if res.Code == 124 {
		if q := output.WaitingForInput(res.Out); q != "" {
			s += "\n[the command seems to wait for an answer: " + q + "]"
		}
	}
	return s
}

func writeLock(path string, rep Report) {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o644) == nil {
		os.Rename(tmp, path)
	}
}
