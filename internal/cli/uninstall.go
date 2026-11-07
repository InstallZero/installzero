package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/output"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
	"github.com/InstallZero/installzero/internal/target/wsl"
	"github.com/InstallZero/installzero/internal/track"
)

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("uninstall", stderr)
	fs.Usage = func() {
		printUsage(stderr, "uninstall [project] [--wsl <distro>] [--dir <folder>] [--plan] [-y] [--data] [--keep-packages]")
		fs.PrintDefaults()
	}
	distro := fs.String("wsl", "", "the install in this WSL distro")
	dir := fs.String("dir", "", "the install in this folder")
	planOnly := fs.Bool("plan", false, "show what would be undone, and change nothing")
	yes := fs.Bool("y", false, "do not ask (data is still kept unless --data)")
	data := fs.Bool("data", false, "also delete the data the install created (databases, logs, caches, Docker volumes)")
	keepPkgs := fs.Bool("keep-packages", false, "leave the packages and programs the install added")
	var names []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		names, args = append(names, fs.Arg(0)), fs.Args()[1:]
	}
	if len(names) > 1 {
		fs.Usage()
		return 2
	}
	sd, err := state.Dir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store := track.Store{Dir: sd}
	all, err := store.List()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var picked []*track.Record
	for _, r := range all {
		if len(names) == 1 && !strings.EqualFold(r.Name, names[0]) && r.ID != names[0] {
			continue
		}
		if *distro != "" && !strings.EqualFold(r.Target, "wsl:"+*distro) {
			continue
		}
		if *dir != "" && !samePath(r.Dir, *dir) {
			continue
		}
		picked = append(picked, r)
	}
	// Run at a project's folder with nothing else said: that project.
	if len(picked) > 1 && len(names) == 0 && *distro == "" && *dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			var here []*track.Record
			for _, r := range picked {
				if r.Target == "local" && within(cwd, r.Dir) {
					here = append(here, r)
				}
			}
			if len(here) == 1 {
				picked = here
			}
		}
	}
	switch {
	case len(picked) == 0 && len(all) == 0:
		fmt.Fprintln(stderr, "no installs recorded. installzero records what installzero install changes; installs made before this build or with --no-track cannot be undone this way.")
		return 1
	case len(picked) == 0:
		fmt.Fprintln(stderr, "no recorded install matches. Recorded installs:")
		printRecords(stderr, all)
		return 1
	case len(picked) > 1:
		fmt.Fprintln(stderr, "more than one recorded install matches; name one (and --wsl or --dir when a name is installed twice):")
		printRecords(stderr, picked)
		return 1
	}
	rec := picked[0]
	acts := track.Undo(rec, track.UndoOptions{KeepPackages: *keepPkgs})
	fmt.Fprintf(stdout, "%s on %s, in %s (installed %s, %d run%s)\n", rec.Name, rec.Target, rec.Dir, rec.Created.Format("2006-01-02 15:04"), rec.Runs, map[bool]string{true: "s", false: ""}[rec.Runs != 1])
	if len(acts) == 0 {
		fmt.Fprintln(stdout, "The install changed nothing that installzero recorded; forgetting it.")
		if !*planOnly {
			store.Retire(rec)
		}
		return 0
	}
	var dataActs, kept []track.Action
	fmt.Fprintln(stdout, "\nUninstall:")
	for _, a := range acts {
		if a.Code == "" {
			kept = append(kept, a)
			continue
		}
		if a.Data {
			dataActs = append(dataActs, a)
			continue
		}
		printAction(stdout, a)
	}
	if len(dataActs) > 0 {
		fmt.Fprintf(stdout, "\nData (kept unless you agree%s):\n", map[bool]string{true: "; --data given", false: ""}[*data])
		for _, a := range dataActs {
			printAction(stdout, a)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintln(stdout, "\nLeft in place:")
		for _, a := range kept {
			printAction(stdout, a)
		}
	}
	if *planOnly {
		return 0
	}
	in := bufio.NewReader(stdin)
	if !*yes && !ask(stdout, in, fmt.Sprintf("\nUninstall %s? [y/N] ", rec.Name)) {
		fmt.Fprintln(stdout, "Nothing changed.")
		return 1
	}
	withData := *data || (len(dataActs) > 0 && !*yes && ask(stdout, in, "Also delete the data listed above? [y/N] "))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var e runner.Exec
	if d, ok := strings.CutPrefix(rec.Target, "wsl:"); ok {
		e = runner.Unix{Runner: wsl.New(d).Runner}
	} else {
		e = runner.Local{}
	}
	failed := 0
	for _, a := range acts {
		if a.Code == "" || (a.Data && !withData) {
			continue
		}
		fmt.Fprintf(stdout, "\n%s\n", a.Title)
		res := e.Run(ctx, a.Shell, a.Code, "", 30*time.Minute)
		if s := output.Summarize(res.Out, 3000); s != "" {
			printCode(stdout, s, "  | ")
		}
		if res.Code != 0 {
			failed++
			fmt.Fprintf(stdout, "  failed (exit %d)\n", res.Code)
		} else {
			fmt.Fprintln(stdout, "  done")
		}
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "\n%d part%s of the uninstall failed (above). Fix what blocks them and run installzero uninstall again; the parts that worked are not repeated in effect.\n", failed, map[bool]string{true: "s", false: ""}[failed != 1])
		return 1
	}
	if rec.Lock != "" {
		os.Remove(rec.Lock)
		os.Remove(filepath.Dir(rec.Lock)) // only when empty
	}
	if err := store.Retire(rec); err != nil {
		fmt.Fprintln(stderr, err)
	}
	fmt.Fprintf(stdout, "\n%s is uninstalled.", rec.Name)
	if len(dataActs) > 0 && !withData {
		fmt.Fprint(stdout, " Its data was kept (listed above).")
	}
	fmt.Fprintln(stdout)
	return 0
}

func printAction(w io.Writer, a track.Action) {
	fmt.Fprintf(w, "  %s\n", a.Title)
	for i, it := range a.Items {
		if i == 12 {
			fmt.Fprintf(w, "      ... and %d more\n", len(a.Items)-12)
			break
		}
		fmt.Fprintf(w, "      %s\n", it)
	}
}

func printRecords(w io.Writer, rs []*track.Record) {
	for _, r := range rs {
		n := len(r.Changes())
		fmt.Fprintf(w, "  %-20s %-22s %s  (%d change%s, %s)\n", r.Name, r.Target, r.Dir, n, map[bool]string{true: "s", false: ""}[n != 1], r.Updated.Format("2006-01-02"))
	}
}

func samePath(a, b string) bool {
	if strings.HasPrefix(a, "/") || strings.HasPrefix(b, "/") {
		return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
	}
	ab, err1 := filepath.Abs(b)
	if err1 != nil {
		ab = b
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(ab))
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
