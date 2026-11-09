package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/InstallZero/installzero/catalog"
	"github.com/InstallZero/installzero/internal/agentinstall"
	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/catalogfix"
	"github.com/InstallZero/installzero/internal/fetch"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/output"
	"github.com/InstallZero/installzero/internal/ports"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
	"github.com/InstallZero/installzero/internal/target/wsl"
	"github.com/InstallZero/installzero/internal/track"
	"github.com/InstallZero/installzero/internal/ui"
)

// stdin is where approvals are read; tests replace it.
var stdin io.Reader = os.Stdin

func cmdInstall(args []string, stdout, stderr io.Writer) int {
	term := ui.New(stdout)
	defer term.Clear()
	stdout = term
	fs := newFlag("install", stderr)
	fs.Usage = func() {
		printUsage(stderr, "install [install.md] [--dir <folder>] [--wsl <distro>] [--skip <step>] [--env NAME=VALUE] [--model <choice>] [--agent <provider:model|model.gguf|local|url>] [--report <file>] [--plan] [-y]")
		fs.PrintDefaults()
		fmt.Fprintln(stderr, "exit codes: 0 installed, 1 stopped, 2 usage, 3 sign out and in to continue, 3010 restart Windows to continue")
	}
	dir := fs.String("dir", "", "install folder (default: the folder holding install.md; in WSL /root/<name>)")
	distro := fs.String("wsl", "", "install into this WSL distro (blocks run inside it as root)")
	planOnly := fs.Bool("plan", false, "show the steps, their blocks and tiers for this machine, and run nothing")
	yes := fs.Bool("y", false, "approve machine-wide steps and catalog fixes without asking; continue after a restart at the next sign-in")
	noTrack := fs.Bool("no-track", false, "do not record what the install changes (installzero uninstall then cannot undo it)")
	var skips listFlag
	fs.Var(&skips, "skip", "step id not to run (repeatable or comma separated); the steps after it still run")
	var envs listFlag
	fs.Var(&envs, "env", "NAME=VALUE set in every block (repeatable); variables the install.md requires are otherwise taken from this environment or asked for")
	modelFlag := fs.String("model", "", "the model that fixes failures the catalog cannot: none, local, anthropic, openai, xai, google, provider:model-id, a .gguf or a server URL (asked once and remembered when not given)")
	agentSpec := fs.String("agent", "", "let a model carry out the install instead of the step runner: provider:model-id (e.g. google:gemini-4-argon; the key from the environment or InstallZero's .env), a local .gguf, local for the default model, or an OpenAI-compatible server URL")
	maxTurns := fs.Int("max-turns", 120, "with --agent: model turns")
	agentTimeout := fs.Int("agent-timeout", 240, "with --agent: minutes")
	promptOut := fs.String("prompt-out", "", "with --agent: write the system prompt, first user message and tools as JSON and stop")
	reportPath := fs.String("report", "", "JSON report of the run, the same for both modes, for comparing them (default: under the state folder's reports)")
	into := fs.String("into", "", "with owner/repo or a git URL: the folder for its checkout (default ~/<repo>/<repo>)")
	resumeArgs := append([]string(nil), args...)
	// Flags may come after the path too.
	var paths []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		paths, args = append(paths, fs.Arg(0)), fs.Args()[1:]
	}
	if len(paths) > 1 {
		fs.Usage()
		return 2
	}
	path := ""
	if len(paths) == 1 {
		path = paths[0]
	}
	if path == "" {
		path = "install.md"
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, "install.md")
	} else if r, ok := fetch.Parse(path); ok && len(paths) == 1 {
		dest := *into
		if dest == "" {
			if dest, err = defaultCheckout(r); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		md, err := checkout(context.Background(), r, dest, stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for i, a := range resumeArgs {
			if a == paths[0] {
				resumeArgs[i] = md
			}
		}
		path = md
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && len(paths) == 0 {
			fmt.Fprintln(stderr, "no install.md here. Turning a README into an install.md is not in this build yet; run installzero install <path to install.md>.")
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	doc := installmd.Parse(string(body))
	if p := doc.Problems(); len(p) > 0 {
		fmt.Fprintf(stderr, "%s is not a usable install.md:\n  %s\n", path, strings.Join(p, "\n  "))
		return 1
	}
	name, _ := doc.Meta.Get("name")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var o runner.Options
	live := &linePrefixer{w: stdout, term: term, prefix: "      | "}
	var lock string
	if *distro != "" {
		t := wsl.New(*distro)
		o.Exec, o.Machine = runner.Unix{Runner: t.Runner}, t.Profile(ctx)
		o.Dir = *dir
		if o.Dir == "" {
			o.Dir = "/root/" + name
		}
		lock = filepath.Join(".installzero", "wsl-"+*distro+"-"+name+".lock.json")
	} else {
		o.Exec, o.Machine = runner.Local{Live: live}, runner.LocalMachine(ctx)
		o.Dir = *dir
		if o.Dir == "" {
			o.Dir = filepath.Dir(path)
		}
		if o.Dir, err = filepath.Abs(o.Dir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		lock = filepath.Join(o.Dir, ".installzero", "lock.json")
	}
	tgt := "local"
	if *distro != "" {
		tgt = "wsl:" + *distro
	}
	windows := strings.EqualFold(o.Machine.OS, "windows")

	// Ports: declared ones are checked now and moved when something else
	// holds them; the chosen ports fill {{PORT_<NAME>}}.
	var host runner.Exec
	if *distro != "" && runtime.GOOS == "windows" && ports.MirroredWSL() {
		host = runner.Local{}
	}
	quiet := o.Exec
	if *distro == "" {
		quiet = runner.Local{}
	}
	o.Read = quiet
	listening := func(ctx context.Context) map[int]ports.Listener { return ports.Listeners(ctx, quiet, windows, host) }
	var prev *track.Record
	if sd, err := state.Dir(); err == nil {
		prev, _ = track.Store{Dir: sd}.Load(track.ID(name, tgt, o.Dir))
	}
	declaredText, _ := doc.Meta.Get("ports")
	declared, err := ports.Declared(declaredText)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var now map[int]ports.Listener
	chosen := map[string]int{}
	o.Values = map[string]string{}
	if len(declared) > 0 {
		want := map[string]int{}
		for k, v := range declared {
			want[k] = v
			if prev != nil && prev.Ports[k] != 0 {
				want[k] = prev.Ports[k]
			}
		}
		now = listening(ctx)
		for _, c := range ports.Resolve(want, now, prev != nil && prev.OK) {
			chosen[c.Name] = c.Port
			o.Values[ports.Placeholder(c.Name)] = strconv.Itoa(c.Port)
			switch {
			case c.Problem != "":
				fmt.Fprintf(stdout, "warning: port %s (%d) is held by %s, and there is %s\n", c.Name, c.Want, c.Owner, c.Problem)
			case c.Owner != nil:
				fmt.Fprintf(stdout, "Port %s (%d) is held by %s; using %d.\n", c.Name, c.Want, c.Owner, c.Port)
			}
		}
	}

	o.Skip = map[string]bool{}
	for _, v := range skips {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				o.Skip[id] = true
			}
		}
	}
	for id := range o.Skip {
		known := false
		for _, s := range doc.Steps {
			known = known || s.ID == id
		}
		if !known {
			fmt.Fprintf(stderr, "--skip %s: the install.md has no step with that id\n", id)
			return 2
		}
	}
	plan, err := runner.NewPlan(ctx, doc, o)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	planPorts := map[int]bool{}
	for _, p := range chosen {
		planPorts[p] = true
	}
	var codes []string
	for _, s := range plan.Steps {
		for _, b := range s.Blocks() {
			codes = append(codes, b.Code)
		}
	}
	for _, p := range ports.Used(codes...) {
		if planPorts[p] {
			continue
		}
		planPorts[p] = true
		// On a rerun of a finished install the listener is the install.
		if prev != nil {
			continue
		}
		if now == nil {
			now = listening(ctx)
		}
		if l, busy := now[p]; busy {
			fmt.Fprintf(stdout, "warning: the install.md uses port %d, which %s already listens on. Declaring it in the front matter (ports: name=%d, used as {{PORT_NAME}}) would let installzero move it.\n", p, l, p)
		}
	}
	fmt.Fprintf(stdout, "%s: %d steps, into %s (%s %s", name, len(plan.Steps), o.Dir, o.Machine.OS, o.Machine.Arch)
	if o.Machine.GPU != "none" && o.Machine.GPU != "" {
		fmt.Fprintf(stdout, ", %s GPU", o.Machine.GPU)
	}
	fmt.Fprintln(stdout, ")")
	if *planOnly {
		printPlan(stdout, plan)
		return 0
	}
	if *promptOut != "" {
		if *agentSpec == "" {
			fmt.Fprintln(stderr, "--prompt-out needs --agent")
			return 2
		}
		system, user, tools, err := agentinstall.Prompts(plan, agentinstall.Options{Runner: o, Remote: *distro != ""})
		if err == nil {
			err = writeJSON(*promptOut, map[string]any{"system": system, "user": user, "tools": tools})
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	in := bufio.NewReader(stdin)
	asked, blocks, fixes := 0, 0, 0
	o.Env = map[string]string{}
	for _, e := range envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			fmt.Fprintf(stderr, "--env %q: want NAME=VALUE\n", e)
			return 2
		}
		o.Env[k] = v
	}
	for _, k := range plan.RequiredEnv(o.Env) {
		if v, ok := os.LookupEnv(k); ok {
			o.Env[k] = v
			continue
		}
		fmt.Fprintf(stdout, "The install.md needs %s (not stored; shown as you type, or pass --env or set it beforehand): ", k)
		line, _ := in.ReadString('\n')
		if v := strings.TrimSpace(line); v != "" {
			o.Env[k] = v
		}
	}
	interactive := stdin == io.Reader(os.Stdin) && ui.IsConsoleInput(os.Stdin)
	var fixer *agentinstall.Fixer
	fixSpec := ""
	if *agentSpec != "" {
		if err := ensureKey(ctx, *agentSpec, interactive, in, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		if fixSpec, err = chooseModel(*modelFlag, interactive, in, stdout, term); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if fixSpec != "" {
			if err := ensureKey(ctx, fixSpec, interactive, in, stdout); err != nil {
				fmt.Fprintf(stdout, "warning: %v; continuing without a model.\n", err)
				fixSpec = ""
			}
		}
		if fixSpec != "" {
			fmt.Fprintf(stdout, "Failures the catalog cannot fix go to %s.\n", fixSpec)
			var stopModel func()
			defer func() {
				if stopModel != nil {
					stopModel()
				}
			}()
			fixer = &agentinstall.Fixer{Connect: func(ctx context.Context) (*agentloop.Session, error) {
				s, stop, err := startAgentModel(ctx, fixSpec, stdout)
				stopModel = stop
				return s, err
			}}
		}
	}
	o.Approve = func(steps []*runner.Step) bool {
		fmt.Fprintf(stdout, "\n%s\n", term.Paint("These steps change the machine outside the install folder:", ui.Yellow, ui.Bold))
		for _, s := range steps {
			fmt.Fprintf(stdout, "  %s\n", term.Paint(fmt.Sprintf("Step %d: %s - %s", s.Number, s.ID, s.Title), ui.Bold))
			for _, b := range s.Blocks() {
				if b.Tier.Tier == "system" {
					fmt.Fprintf(stdout, "    %s (%s):\n", b.Kind, term.Paint(b.Tier.Reason, ui.Yellow))
					printCode(stdout, b.Code, "      ")
				}
			}
		}
		if *yes {
			fmt.Fprintln(stdout, "Approved (-y).")
			return true
		}
		asked++
		return ask(stdout, in, "Run them? [y/N] ")
	}
	o.Confirm = func(s *runner.Step, b *runner.Block) bool {
		fmt.Fprintf(stdout, "\nA fix for step %s is machine-wide (%s):\n", s.ID, b.Tier.Reason)
		printCode(stdout, b.Code, "  ")
		if *yes {
			return true
		}
		asked++
		return ask(stdout, in, "Run it? [y/N] ")
	}
	o.Lock = lock
	cat, err := loadCatalog(doc, filepath.Dir(path))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cf := catalogfix.Fixer{
		Catalog: cat, Exec: quiet, Windows: windows && *distro == "",
		Confirm: func(q string) bool {
			if *yes {
				return true
			}
			asked++
			return ask(stdout, in, "\n"+q+" [y/N] ")
		},
	}
	if fixer != nil {
		cf.Next = fixer
	}
	o.Fixer = ports.Fixer{
		Exec: quiet, Windows: windows, Listening: listening, PlanPorts: planPorts,
		Confirm: func(q string) bool {
			if *yes {
				return false
			}
			asked++
			return ask(stdout, in, "\n"+q+" [y/N] ")
		},
		Next: cf,
	}
	var rec *track.Recorder
	if !*noTrack {
		sd, err := state.Dir()
		if err == nil {
			rec, err = track.NewRecorder(quiet, track.Store{Dir: sd}, name, tgt, o.Machine.OS, o.Dir, string(body))
			if err == nil {
				rec.Warn = func(s string) { fmt.Fprintln(stdout, "  warning: "+s) }
				if rec.Record.Lock, err = filepath.Abs(lock); err != nil {
					rec.Record.Lock = lock
				}
				rec.Record.Ports = chosen
				o.Tracker = stepTracker{rec}
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "cannot record this install for uninstall: %v\n", err)
			return 1
		}
	}
	done, started := 0, time.Now()
	var shown *runner.Step
	heading := func(s *runner.Step) {
		shown = s
		if s.Number == 0 {
			fmt.Fprintf(stdout, "\n%s\n", term.Paint(s.Title, ui.Bold, ui.Cyan))
			return
		}
		fmt.Fprintf(stdout, "\n%s %s\n", term.Paint(fmt.Sprintf("Step %d/%d:", s.Number, len(plan.Steps)), ui.Bold, ui.Cyan), term.Paint(s.ID+" - "+s.Title, ui.Bold))
	}
	o.Events = func(e runner.Event) {
		switch e.Kind {
		case "step":
			heading(e.Step)
			term.SetStatus(ui.Status{Done: done, Total: len(plan.Steps), Step: e.Step.ID, Started: started})
		case "block":
			blocks++
			if e.Step != shown {
				heading(e.Step)
			}
			fmt.Fprintf(stdout, "  %s\n", term.Paint(e.Block, ui.Cyan))
			term.SetStatus(ui.Status{Done: done, Total: len(plan.Steps), Step: e.Step.ID, Block: e.Block, Started: started})
		case "block-done":
			live.Done()
			term.SetSub(-1, "")
			switch {
			case e.Code != 0 && e.Block == "detect":
				fmt.Fprintf(stdout, "  %s\n", term.Paint("not done yet", ui.Dim))
			case e.Code != 0:
				fmt.Fprintf(stdout, "  %s\n", term.Paint(fmt.Sprintf("%s exited %d", e.Block, e.Code), ui.Yellow))
			}
		case "fix":
			if !strings.HasPrefix(e.Text, "give-up:") {
				fixes++
			}
			if strings.TrimSpace(strings.TrimPrefix(e.Text, "give-up:")) == "" {
				break
			}
			fmt.Fprintf(stdout, "  %s %s\n", term.Paint("fix:", ui.Yellow, ui.Bold), e.Text)
		case "result":
			done++
			if e.Step != nil && (e.Status == runner.NotApplicable || e.Status == runner.Refused || e.Status == runner.NotApproved || e.Status == runner.Skipped) {
				heading(e.Step)
			}
			fmt.Fprintf(stdout, "  => %s%s\n", paintStatus(term, e.Status), map[bool]string{true: ": " + e.Text, false: ""}[e.Text != ""])
			if e.Step != nil {
				term.SetStatus(ui.Status{Done: done, Total: len(plan.Steps), Step: e.Step.ID, Started: started})
			}
		}
	}
	mode := "deterministic"
	var rep runner.Report
	var agentRes *agentinstall.Result
	transcript := ""
	if *agentSpec != "" || fixer != nil {
		if sd, err := state.Dir(); err == nil {
			transcript = filepath.Join(sd, "agent", fmt.Sprintf("%s-%s.jsonl", started.Format("20060102-150405"), name))
			os.MkdirAll(filepath.Dir(transcript), 0o755)
		}
	}
	model := agentinstall.Options{
		Runner: o, Remote: *distro != "", Yes: *yes,
		Ask: func(q, code string) bool {
			fmt.Fprintf(stdout, "\n%s\n", term.Paint(q, ui.Yellow, ui.Bold))
			printCode(stdout, code, "  ")
			return ask(stdout, in, "Run it? [y/N] ")
		},
		Transcript: transcript, Trail: stdout,
	}
	switch {
	case *agentSpec != "":
		mode = "agent"
		session, stopModel, err := startAgentModel(ctx, *agentSpec, stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer stopModel()
		fmt.Fprintf(stdout, "\n%s %s\n", term.Paint("Agent:", ui.Bold, ui.Cyan), session.Label)
		model.Session, model.MaxTurns, model.Timeout = session, *maxTurns, time.Duration(*agentTimeout)*time.Minute
		agentRes, err = agentinstall.Run(ctx, plan, model)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		asked += agentRes.Asked
		rep = runner.Report{Name: name, Dir: o.Dir, Started: started, Finished: time.Now(), OK: agentRes.OK, Steps: agentRes.Steps}
	case fixer != nil:
		mode = "fixer"
		fixer.Options, fixer.Plan, fixer.Attempts = model, plan, 3
		rep = runner.Run(ctx, plan, o)
		asked += fixer.Asked()
	default:
		rep = runner.Run(ctx, plan, o)
	}
	term.Clear()
	ir := installReport{
		Mode: mode, Name: name, Target: tgt, Dir: o.Dir, Machine: o.Machine, Started: started,
		Seconds: time.Since(started).Seconds(), OK: rep.OK, Steps: rep.Steps,
		Blocks: blocks, ApprovalPrompts: asked, Fixes: fixes, Agent: agentRes, Transcript: transcript,
	}
	switch {
	case agentRes != nil:
		ir.Model = *agentSpec
	case fixer != nil:
		ir.Model, ir.ModelFixes, ir.FixCommands = fixSpec, fixer.Episodes, fixer.Commands()
	}
	if p, err := writeInstallReport(*reportPath, ir); err != nil {
		fmt.Fprintf(stdout, "warning: could not write the report: %v\n", err)
	} else {
		defer fmt.Fprintf(stdout, "Report: %s\n", p)
	}
	if rec != nil && (prev != nil || len(rec.Record.Steps) > 0) {
		rec.Record.OK = rep.OK
		if err := rec.Store.Save(rec.Record); err != nil {
			fmt.Fprintf(stdout, "warning: could not save the install record: %v\n", err)
		}
	}

	fmt.Fprintf(stdout, "\n%s\n", term.Paint(fmt.Sprintf("Summary (%s)", ui.Elapsed(time.Since(started))), ui.Bold))
	for _, s := range rep.Steps {
		fmt.Fprintf(stdout, "  %s %s\n", paintStatus(term, runner.Status(fmt.Sprintf("%-15s", s.Status))), s.ID)
	}
	if agentRes != nil {
		printAgentSummary(stdout, term, agentRes)
		if rep.OK {
			fmt.Fprintf(stdout, "\n%s Every step's verify passed after the run.\n", term.Paint(name+" is installed.", ui.Green, ui.Bold))
			if o.Tracker != nil {
				fmt.Fprintf(stdout, "installzero uninstall %s undoes it.\n", name)
			}
			return 0
		}
		for _, s := range rep.Steps {
			if s.Status == runner.Failed && s.Output != "" {
				fmt.Fprintf(stdout, "\n%s Its output:\n", term.Paint(fmt.Sprintf("Step %s failed (%s).", s.ID, s.Reason), ui.Red, ui.Bold))
				printCode(stdout, s.Output, "  ")
			}
		}
		fmt.Fprintf(stdout, "\n%s\n", term.Paint("The agent's install is not complete: the steps above did not pass their verify.", ui.Red, ui.Bold))
		return 1
	}
	if rep.OK {
		fmt.Fprintf(stdout, "\n%s Progress is in %s.\n", term.Paint(name+" is installed.", ui.Green, ui.Bold), lock)
		if o.Tracker != nil {
			fmt.Fprintf(stdout, "installzero uninstall %s undoes it.\n", name)
		}
		return 0
	}
	if rep.Resume != "" {
		confirm := func(q string) bool {
			fmt.Fprint(stdout, "\n"+q)
			line, _ := in.ReadString('\n')
			a := strings.ToLower(strings.TrimSpace(line))
			return a == "" || a == "y" || a == "yes"
		}
		return reportResume(ctx, rep, name, lock, resumeArgs, runtime.GOOS == "windows", *yes, confirm,
			func(f string, a ...any) { fmt.Fprintf(stdout, f, a...) })
	}
	for _, s := range rep.Steps {
		if s.Status == runner.Failed && s.Output != "" {
			fmt.Fprintf(stdout, "\n%s Its output:\n", term.Paint(fmt.Sprintf("Step %s failed (%s).", s.ID, s.Reason), ui.Red, ui.Bold))
			printCode(stdout, s.Output, "  ")
		}
	}
	fmt.Fprintf(stdout, "\n%s Fix the problem and run installzero install again: steps that are done are detected and skipped. Progress is in %s.\n", term.Paint("The install stopped.", ui.Red, ui.Bold), lock)
	return 1
}

// loadCatalog is the bundled failure catalog, with the project's own entries
// first when the front matter names their folder (catalog: <path>, relative
// to the install.md).
func loadCatalog(doc *installmd.Doc, base string) (*catalog.Catalog, error) {
	bundled, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	dir, _ := doc.Meta.Get("catalog")
	if dir == "" {
		return bundled, nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	project, err := catalog.LoadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("the install.md's catalog %s: %w", dir, err)
	}
	return catalog.Merge(project, bundled), nil
}

// paintStatus colors a step status (padding kept): green for done, red for
// failures, yellow for what waits on the user, dim for what did not run.
func paintStatus(t *ui.Term, s runner.Status) string {
	style := ui.Dim
	switch runner.Status(strings.TrimSpace(string(s))) {
	case runner.Done, runner.AlreadyDone:
		style = ui.Green
	case runner.Failed, runner.Refused:
		style = ui.Red
	case runner.RestartNeeded, runner.SignOutNeeded, runner.NotApproved:
		style = ui.Yellow
	}
	return t.Paint(string(s), style)
}

// stepTracker records each step's changes for installzero uninstall.
type stepTracker struct{ rec *track.Recorder }

func (t stepTracker) Before(ctx context.Context, s *runner.Step) {
	t.rec.Before(ctx, s.ID, s.Highest() == "system")
}

func (t stepTracker) After(ctx context.Context, s *runner.Step, status runner.Status) {
	var u *track.Block
	if s.Uninstall != nil {
		u = &track.Block{Shell: s.Uninstall.Shell, Code: s.Uninstall.Code}
	}
	code := ""
	if s.Install != nil {
		code = s.Install.Code
	}
	t.rec.After(ctx, s.ID, s.Title, string(status), code, u)
}

func printPlan(w io.Writer, p *runner.Plan) {
	for _, s := range p.Steps {
		fmt.Fprintf(w, "\nStep %d: %s - %s\n", s.Number, s.ID, s.Title)
		if s.Why != "" {
			fmt.Fprintf(w, "  Why: %s\n", s.Why)
		}
		if s.Unmet != nil {
			fmt.Fprintf(w, "  Skipped on this machine: %s\n", strings.Join(s.Unmet, "; "))
			continue
		}
		for _, b := range s.Blocks() {
			fmt.Fprintf(w, "  %s [%s, %s]: %s\n", b.Kind, b.Shell, b.Tier.Tier, b.Tier.Reason)
			printCode(w, b.Code, "      ")
		}
	}
}

func printCode(w io.Writer, code, indent string) {
	for _, l := range strings.Split(code, "\n") {
		fmt.Fprintln(w, indent+l)
	}
}

func ask(w io.Writer, in *bufio.Reader, q string) bool {
	fmt.Fprint(w, q)
	line, _ := in.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// linePrefixer shows a command's live output indented, with a run of lines
// that differ only in their numbers (progress) shown as its first line, a
// count and its last line. Progress goes to the status line instead of the
// output: "##progress <percent>[%] [label]" or "##progress <done>/<total>
// [label]" lines, text a tool redraws in place with a carriage return
// (curl, pip), and percentages in ordinary lines.
type linePrefixer struct {
	mu      sync.Mutex
	w       io.Writer
	term    *ui.Term
	prefix  string
	partial []byte
	key     string
	last    string
	hidden  int
}

var (
	digitRun     = regexp.MustCompile(`\d+`)
	percentRe    = regexp.MustCompile(`(?:^|[\s\[(|])(\d{1,3}(?:\.\d+)?)\s?%(?:$|[\s\])|,])`)
	progressLine = regexp.MustCompile(`^##progress\s+(?:(\d+(?:\.\d+)?)%?|(\d+)/(\d+))(?:\s+(.*))?$`)
)

func (l *linePrefixer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexAny(l.partial, "\r\n")
		if i < 0 {
			break
		}
		if l.partial[i] == '\r' {
			if i+1 == len(l.partial) {
				break // a \n may follow
			}
			if l.partial[i+1] != '\n' {
				l.progress(output.Clean(string(l.partial[:i])), "")
				l.partial = l.partial[i+1:]
				continue
			}
			l.line(output.Clean(string(l.partial[:i])))
			l.partial = l.partial[i+2:]
			continue
		}
		l.line(output.Clean(string(l.partial[:i])))
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

// progress shows the last percentage in s on the status line.
func (l *linePrefixer) progress(s, label string) bool {
	m := percentRe.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return false
	}
	pct, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil || pct > 100 {
		return false
	}
	if l.term != nil {
		l.term.SetSub(pct, label)
	}
	return true
}

func (l *linePrefixer) line(s string) {
	if m := progressLine.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		pct, _ := strconv.ParseFloat(m[1], 64)
		if m[2] != "" {
			done, _ := strconv.ParseFloat(m[2], 64)
			if total, _ := strconv.ParseFloat(m[3], 64); total > 0 {
				pct = 100 * done / total
			}
		}
		if l.term != nil {
			l.term.SetSub(min(pct, 100), m[4])
		}
		return
	}
	l.progress(s, "")
	key := digitRun.ReplaceAllString(s, "#")
	if key == l.key {
		l.hidden++
		l.last = s
		return
	}
	l.flush()
	l.key = key
	l.print(s)
}

func (l *linePrefixer) print(s string) {
	p := l.prefix
	if l.term != nil {
		p = l.term.Paint(p, ui.Dim)
	}
	fmt.Fprintln(l.w, p+s)
}

// flush ends a run of similar lines.
func (l *linePrefixer) flush() {
	if l.hidden > 1 {
		l.print(fmt.Sprintf("[... %d similar lines]", l.hidden-1))
	}
	if l.hidden > 0 {
		l.print(l.last)
	}
	l.hidden, l.key = 0, ""
}

// Done prints what is still held back, at the end of a block.
func (l *linePrefixer) Done() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.line(output.Clean(string(l.partial)))
		l.partial = nil
	}
	l.flush()
}
