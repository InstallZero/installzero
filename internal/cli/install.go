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

	"github.com/InstallZero/installzero/catalog"
	"github.com/InstallZero/installzero/internal/catalogfix"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/output"
	"github.com/InstallZero/installzero/internal/ports"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
	"github.com/InstallZero/installzero/internal/target/wsl"
	"github.com/InstallZero/installzero/internal/track"
)

// stdin is where approvals are read; tests replace it.
var stdin io.Reader = os.Stdin

func cmdInstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("install", stderr)
	fs.Usage = func() {
		printUsage(stderr, "install [install.md] [--dir <folder>] [--wsl <distro>] [--plan] [-y]")
		fs.PrintDefaults()
		fmt.Fprintln(stderr, "exit codes: 0 installed, 1 stopped, 2 usage, 3 sign out and in to continue, 3010 restart Windows to continue")
	}
	dir := fs.String("dir", "", "install folder (default: the folder holding install.md; in WSL /root/<name>)")
	distro := fs.String("wsl", "", "install into this WSL distro (blocks run inside it as root)")
	planOnly := fs.Bool("plan", false, "show the steps, their blocks and tiers for this machine, and run nothing")
	yes := fs.Bool("y", false, "approve machine-wide steps and catalog fixes without asking; continue after a restart at the next sign-in")
	noTrack := fs.Bool("no-track", false, "do not record what the install changes (installzero uninstall then cannot undo it)")
	var envs listFlag
	fs.Var(&envs, "env", "NAME=VALUE set in every block (repeatable); variables the install.md requires are otherwise taken from this environment or asked for")
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
	live := &linePrefixer{w: stdout, prefix: "      | "}
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

	in := bufio.NewReader(stdin)
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
	o.Approve = func(steps []*runner.Step) bool {
		fmt.Fprintln(stdout, "\nThese steps change the machine outside the install folder:")
		for _, s := range steps {
			fmt.Fprintf(stdout, "  Step %d: %s - %s\n", s.Number, s.ID, s.Title)
			for _, b := range s.Blocks() {
				if b.Tier.Tier == "system" {
					fmt.Fprintf(stdout, "    %s (%s):\n", b.Kind, b.Tier.Reason)
					printCode(stdout, b.Code, "      ")
				}
			}
		}
		if *yes {
			fmt.Fprintln(stdout, "Approved (-y).")
			return true
		}
		return ask(stdout, in, "Run them? [y/N] ")
	}
	o.Confirm = func(s *runner.Step, b *runner.Block) bool {
		fmt.Fprintf(stdout, "\nA fix for step %s is machine-wide (%s):\n", s.ID, b.Tier.Reason)
		printCode(stdout, b.Code, "  ")
		return *yes || ask(stdout, in, "Run it? [y/N] ")
	}
	o.Lock = lock
	cat, err := loadCatalog(doc, filepath.Dir(path))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	o.Fixer = ports.Fixer{
		Exec: quiet, Windows: windows, Listening: listening, PlanPorts: planPorts,
		Confirm: func(q string) bool { return !*yes && ask(stdout, in, "\n"+q+" [y/N] ") },
		Next: catalogfix.Fixer{
			Catalog: cat, Exec: quiet, Windows: windows && *distro == "",
			Confirm: func(q string) bool { return *yes || ask(stdout, in, "\n"+q+" [y/N] ") },
		},
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
	o.Events = func(e runner.Event) {
		switch e.Kind {
		case "step":
			fmt.Fprintf(stdout, "\nStep %d: %s - %s\n", e.Step.Number, e.Step.ID, e.Step.Title)
		case "block":
			fmt.Fprintf(stdout, "  %s\n", e.Block)
		case "block-done":
			live.Done()
			if e.Code != 0 {
				fmt.Fprintf(stdout, "  %s exited %d\n", e.Block, e.Code)
			}
		case "fix":
			fmt.Fprintf(stdout, "  fix: %s\n", e.Text)
		case "result":
			if e.Step != nil && (e.Status == runner.NotApplicable || e.Status == runner.Refused || e.Status == runner.NotApproved) {
				fmt.Fprintf(stdout, "\nStep %d: %s - %s\n", e.Step.Number, e.Step.ID, e.Step.Title)
			}
			fmt.Fprintf(stdout, "  => %s%s\n", e.Status, map[bool]string{true: ": " + e.Text, false: ""}[e.Text != ""])
		}
	}
	rep := runner.Run(ctx, plan, o)
	if rec != nil && (prev != nil || len(rec.Record.Steps) > 0) {
		rec.Record.OK = rep.OK
		if err := rec.Store.Save(rec.Record); err != nil {
			fmt.Fprintf(stdout, "warning: could not save the install record: %v\n", err)
		}
	}

	fmt.Fprintln(stdout)
	for _, s := range rep.Steps {
		fmt.Fprintf(stdout, "  %-15s %s\n", s.Status, s.ID)
	}
	if rep.OK {
		fmt.Fprintf(stdout, "\n%s is installed. Progress is in %s.\n", name, lock)
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
		return reportResume(ctx, rep, name, lock, args, runtime.GOOS == "windows", *yes, confirm,
			func(f string, a ...any) { fmt.Fprintf(stdout, f, a...) })
	}
	for _, s := range rep.Steps {
		if s.Status == runner.Failed && s.Output != "" {
			fmt.Fprintf(stdout, "\nStep %s failed (%s). Its output:\n", s.ID, s.Reason)
			printCode(stdout, s.Output, "  ")
		}
	}
	fmt.Fprintf(stdout, "\nThe install stopped. Fix the problem and run installzero install again: steps that are done are detected and skipped. Progress is in %s.\n", lock)
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
// count and its last line.
type linePrefixer struct {
	mu      sync.Mutex
	w       io.Writer
	prefix  string
	partial []byte
	key     string
	last    string
	hidden  int
}

var digitRun = regexp.MustCompile(`\d+`)

func (l *linePrefixer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		l.line(output.Clean(string(l.partial[:i])))
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

func (l *linePrefixer) line(s string) {
	key := digitRun.ReplaceAllString(s, "#")
	if key == l.key {
		l.hidden++
		l.last = s
		return
	}
	l.flush()
	l.key = key
	fmt.Fprintln(l.w, l.prefix+s)
}

// flush ends a run of similar lines.
func (l *linePrefixer) flush() {
	if l.hidden > 1 {
		fmt.Fprintf(l.w, "%s[... %d similar lines]\n", l.prefix, l.hidden-1)
	}
	if l.hidden > 0 {
		fmt.Fprintln(l.w, l.prefix+l.last)
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
