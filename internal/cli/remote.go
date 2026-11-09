package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/InstallZero/installzero/internal/fetch"
	"github.com/InstallZero/installzero/internal/tag"
	"github.com/InstallZero/installzero/internal/ui"
)

// resumeExe is the executable the sign-in entry starts, when not this one.
var resumeExe string

// defaultCheckout is where a remote project goes: a folder named after it in
// the home folder, holding the checkout, so what the project keeps beside
// its checkout (../data) stays in that folder.
func defaultCheckout(r fetch.Repo) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, r.Name(), r.Name()), nil
}

// checkout returns dest's install.md, fetching r into dest first when dest
// does not exist; a dest that holds an install.md is used as it is.
func checkout(ctx context.Context, r fetch.Repo, dest string, stdout io.Writer) (string, error) {
	md := filepath.Join(dest, "install.md")
	if _, err := os.Stat(md); err == nil {
		fmt.Fprintf(stdout, "Using %s, already in %s.\n", r.Name(), dest)
		return md, nil
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s exists and has no install.md; move it aside or pass --into <folder>", dest)
	}
	fmt.Fprintf(stdout, "Getting %s into %s ...\n", r, dest)
	var m *ui.Meter
	how, err := r.Get(ctx, dest, func(total int64) io.Writer {
		m = ui.NewMeter(stdout, r.Name(), total)
		return m
	})
	if m != nil {
		m.Done()
	}
	if err != nil {
		return "", err
	}
	fmt.Fprintf(stdout, "Got %s (%s).\n", r.Name(), how)
	if _, err := os.Stat(md); err != nil {
		return "", fmt.Errorf("%s has no install.md at %s", r, firstNonEmpty(r.Ref, "its default branch"))
	}
	return md, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// opened is installzero started without arguments from Explorer, or a copy
// tagged with a project: install the install.md here, else the tagged
// project, else the one the user names.
func opened(stdout, stderr io.Writer) int {
	if stdin != io.Reader(os.Stdin) || !ui.IsConsoleInput(os.Stdin) {
		fmt.Fprintf(stderr, "This copy installs %s when opened; without a console, run %s install %s.\n", firstNonEmpty(tag.Project(), "a project"), Name, firstNonEmpty(tag.Project(), "<owner/repo>"))
		return 2
	}
	if _, err := os.Stat("install.md"); err == nil {
		return cmdInstall(nil, stdout, stderr)
	}
	term := ui.New(stdout)
	in := bufio.NewReader(stdin)
	fmt.Fprintf(stdout, "%s %s\n", term.Paint("InstallZero", ui.Bold), Version)
	project := tag.Project()
	if project != "" {
		fmt.Fprintf(stdout, "This copy was published with %s.\n", project)
	} else {
		fmt.Fprint(stdout, "Project to install (owner/repo or a git URL; Enter to quit): ")
		line, _ := in.ReadString('\n')
		if project = strings.TrimSpace(line); project == "" {
			return 0
		}
	}
	r, ok := fetch.Parse(project)
	if !ok {
		fmt.Fprintf(stderr, "%s is not an owner/repo or a git URL\n", project)
		return 2
	}
	dest, err := defaultCheckout(r)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	q := fmt.Sprintf("Install %s into %s? [Y/n] ", r.Name(), dest)
	if _, err := os.Stat(filepath.Join(dest, "install.md")); err == nil {
		q = fmt.Sprintf("Continue installing %s in %s? [Y/n] ", r.Name(), dest)
	}
	fmt.Fprint(stdout, q)
	line, _ := in.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "" && a != "y" && a != "yes" {
		return 0
	}
	if exe, err := installSelf(); err != nil {
		fmt.Fprintf(stdout, "warning: could not keep a copy of installzero (%v); continuing an install after a restart then needs this file where it is.\n", err)
	} else if exe != "" {
		resumeExe = exe
		fmt.Fprintf(stdout, "installzero is kept in %s.\n", exe)
	}
	return cmdInstall([]string{project}, stdout, stderr)
}

// installSelf copies this executable to the per-user bin folder on Windows
// (%LOCALAPPDATA%\installzero\bin), so the sign-in entry that continues an
// install does not depend on the download; it returns the copy's path.
func installSelf() (string, error) {
	if runtime.GOOS != "windows" || os.Getenv("LOCALAPPDATA") == "" {
		return "", nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(os.Getenv("LOCALAPPDATA"), "installzero", "bin", "installzero.exe")
	if strings.EqualFold(filepath.Clean(exe), dst) {
		return "", nil
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func pauseBeforeClose(stdout io.Writer) {
	fmt.Fprint(stdout, "\nPress Enter to close this window.")
	bufio.NewReader(stdin).ReadString('\n')
}

func cmdTag(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("tag", stderr)
	fs.Usage = func() {
		printUsage(stderr, "tag <owner/repo[@ref] | git URL[@ref]> -o <out.exe> [-i <installzero executable>]\n       "+Name+" tag --show <executable>   |   "+Name+" tag --clear -o <out> [-i <in>]")
		fmt.Fprintln(stderr, "A tagged copy installs that project when it is opened without arguments (double-clicked).")
	}
	inFile := fs.String("i", "", "the installzero executable to copy (default: this one)")
	out := fs.String("o", "", "where to write the tagged copy")
	show := fs.String("show", "", "print the project an executable is tagged with")
	clearTag := fs.Bool("clear", false, "write a copy with no project")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	if *show != "" {
		p, err := tag.Read(*show)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if p == "" {
			fmt.Fprintf(stdout, "%s has no project tag\n", *show)
		} else {
			fmt.Fprintln(stdout, p)
		}
		return 0
	}
	project := ""
	if !*clearTag {
		if len(pos) != 1 {
			fs.Usage()
			return 2
		}
		project = pos[0]
		if _, ok := fetch.Parse(project); !ok {
			fmt.Fprintf(stderr, "%s is not an owner/repo or a git URL\n", project)
			return 2
		}
	}
	if *out == "" {
		fs.Usage()
		return 2
	}
	src := *inFile
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		src = exe
	}
	if err := tag.Write(src, *out, project); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if project == "" {
		fmt.Fprintf(stdout, "wrote %s with no project tag\n", *out)
	} else {
		fmt.Fprintf(stdout, "wrote %s: opened without arguments, it installs %s\n", *out, project)
	}
	return 0
}
