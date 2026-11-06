// Package cli is the installzero command line.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/InstallZero/installzero/catalog"
	"github.com/InstallZero/installzero/internal/source"
	"github.com/InstallZero/installzero/internal/state"
)

// Name is how this process was invoked, without a directory or .exe.
// iz and installzero are the same command.
var Name = "installzero"

// SetName records the binary name from argv[0].
func SetName(argv0 string) {
	base := filepath.Base(argv0)
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".exe") {
		base = strings.TrimSuffix(base, ext)
	}
	if base != "" && base != "." {
		Name = base
	}
}

func printUsage(w io.Writer, rest string) {
	fmt.Fprintf(w, "usage: %s %s\n", Name, rest)
}

func usageText() string {
	return fmt.Sprintf(`InstallZero installs a project from its install.json, looks up known failures,
and records what happened. iz is the short name for the same command.

Usage:
  %s <command> [arguments]

Commands:`, Name)
}

const commandList = `
  add <owner/repo | git URL | ./folder>   install a project from its install.json
  check <source>                          report what is present or missing; change nothing
  list                                    installed projects from lock.json
  update [project]                        re-run against the newer manifest
  remove <project>                        uninstall from the lock file
  doctor [--prompt | <agent>]             diagnose a failure
  find "<error text>"                     look an error up in the catalog
  failures                                local failure records, newest first
  share <id>                              send one local record
  capture -- <command>                    run a command and record its failure
  init                                    write a starter install.json
  probe --wsl <distro> [--target <path>]  write an install.md from a running install

add takes --list to show phases and install nothing. remove takes --data to
include data folders. -y skips prompts, --phase <id> limits the run to one
phase, --doctor <model> chooses the model, and -g is machine scope.
`

// Run executes one invocation. args does not include the program name.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usageText()+commandList)
		return 0
	}
	switch args[0] {
	case "add":
		return cmdAdd(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "list":
		return cmdList(args[1:], stdout, stderr)
	case "update":
		return cmdUpdate(args[1:], stdout, stderr)
	case "remove":
		return cmdRemove(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "find":
		return cmdFind(args[1:], stdout, stderr)
	case "failures":
		return cmdFailures(args[1:], stdout, stderr)
	case "share":
		return cmdShare(args[1:], stdout, stderr)
	case "capture":
		return cmdCapture(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "probe":
		return cmdProbe(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usageText()+commandList)
		return 2
	}
}

type common struct {
	yes    bool
	phase  string
	doctor string
	global bool
}

func addCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.BoolVar(&c.yes, "y", false, "do not prompt")
	fs.StringVar(&c.phase, "phase", "", "run only this phase")
	fs.StringVar(&c.doctor, "doctor", "", "model used when the catalog does not match")
	fs.BoolVar(&c.global, "g", false, "machine scope")
	return c
}

func newFlag(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s %s [flags]", Name, name)
	}
	return fs
}

func cmdFind(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("find", stderr)
	fs.Usage = func() { printUsage(stderr, "find [--phase <id>] <error text or entry id>") }
	phase := fs.String("phase", "", "only entries for this phase")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	text := strings.Join(fs.Args(), " ")
	cat, err := catalog.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	e := cat.Lookup(text, *phase)
	if e == nil {
		fmt.Fprintln(stderr, "no catalog entry matches")
		return 1
	}
	fmt.Fprintf(stdout, "%s (v%d)\nphases: %s\n%s\n\n%s\n\nfix: %s\n", e.ID, e.Version, strings.Join(e.Phases, ", "), e.Diagnosis, e.Advice, e.Action)
	return 0
}

type manifest struct {
	Name   string `json:"name"`
	Phases []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"phases"`
}

func readManifest(dir string) (manifest, error) {
	body, err := os.ReadFile(filepath.Join(dir, "install.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return manifest{}, fmt.Errorf("%s has no install.json", dir)
		}
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return manifest{}, fmt.Errorf("install.json: %w", err)
	}
	if m.Name == "" {
		return manifest{}, fmt.Errorf("install.json has no name")
	}
	return m, nil
}

func printPhases(stdout io.Writer, m manifest) {
	fmt.Fprintln(stdout, m.Name)
	if len(m.Phases) == 0 {
		fmt.Fprintln(stdout, "  (no phases)")
		return
	}
	for _, p := range m.Phases {
		fmt.Fprintf(stdout, "  %s\t%s\n", p.ID, p.Type)
	}
}

func cmdAdd(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("add", stderr)
	fs.Usage = func() {
		printUsage(stderr, "add [--list] [-y] [--phase <id>] [--doctor <model>] [-g] <source>")
	}
	list := fs.Bool("list", false, "show phases and install nothing")
	addCommon(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	src, err := source.Resolve(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if src.Kind != source.KindDir {
		fmt.Fprintf(stdout, "resolved %s\n", src.URL)
		fmt.Fprintln(stderr, "installing from a remote source is not in this build yet")
		return 2
	}
	m, err := readManifest(src.Path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	printPhases(stdout, m)
	if *list {
		return 0
	}
	fmt.Fprintln(stderr, "installing phases is not in this build yet")
	return 2
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("check", stderr)
	fs.Usage = func() { printUsage(stderr, "check [--phase <id>] <source>") }
	addCommon(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	src, err := source.Resolve(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if src.Kind != source.KindDir {
		fmt.Fprintf(stdout, "resolved %s\n", src.URL)
		fmt.Fprintln(stderr, "checking a remote source is not in this build yet")
		return 2
	}
	m, err := readManifest(src.Path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	printPhases(stdout, m)
	fmt.Fprintln(stderr, "detect steps are not in this build yet")
	return 2
}

func cmdList(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("list", stderr)
	fs.Usage = func() { printUsage(stderr, "list") }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	dir, err := state.Dir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	projects, err := state.Projects(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(projects) == 0 {
		fmt.Fprintln(stdout, "No projects installed.")
		return 0
	}
	for _, p := range projects {
		if p.Source != "" {
			fmt.Fprintf(stdout, "%s\t%s\n", p.Name, p.Source)
		} else {
			fmt.Fprintln(stdout, p.Name)
		}
	}
	return 0
}

func projectInLock(name string) (bool, error) {
	dir, err := state.Dir()
	if err != nil {
		return false, err
	}
	projects, err := state.Projects(dir)
	if err != nil {
		return false, err
	}
	if name == "" {
		return len(projects) > 0, nil
	}
	for _, p := range projects {
		if p.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func cmdUpdate(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("update", stderr)
	fs.Usage = func() { printUsage(stderr, "update [project]") }
	addCommon(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	ok, err := projectInLock(name)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !ok {
		if name == "" {
			fmt.Fprintln(stderr, "no projects installed")
		} else {
			fmt.Fprintf(stderr, "no installed project %q\n", name)
		}
		return 1
	}
	fmt.Fprintln(stderr, "updating an install is not in this build yet")
	return 2
}

func cmdRemove(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("remove", stderr)
	fs.Usage = func() { printUsage(stderr, "remove [--data] [-g] <project>") }
	data := fs.Bool("data", false, "also remove data folders")
	addCommon(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	_ = data
	ok, err := projectInLock(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "no installed project %q\n", fs.Arg(0))
		return 1
	}
	fmt.Fprintln(stderr, "removing an install is not in this build yet")
	return 2
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("doctor", stderr)
	fs.Usage = func() { printUsage(stderr, "doctor [--prompt | <agent>] [project or record]") }
	prompt := fs.Bool("prompt", false, "print evidence and instructions for an external agent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 2 {
		fs.Usage()
		return 2
	}
	if *prompt && fs.NArg() > 1 {
		fmt.Fprintln(stderr, "doctor takes --prompt or an agent name, not both")
		return 2
	}
	fmt.Fprintln(stderr, "the doctor is not in this build yet")
	return 2
}

func cmdFailures(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "show" {
		return cmdFailuresShow(args[1:], stdout, stderr)
	}
	fs := newFlag("failures", stderr)
	fs.Usage = func() { printUsage(stderr, "failures [show <id>]") }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	dir, err := state.Dir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	recs, err := state.Records(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Fprintln(stdout, "No failure records.")
		return 0
	}
	for _, r := range recs {
		match := r.Match.Entry
		if match == "" {
			match = "unmatched"
		}
		line := ""
		if len(r.Signature.Lines) > 0 {
			line = r.Signature.Lines[0]
		}
		id := r.ID
		if id == "" {
			id = r.Dir
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", id, r.Phase.ID, match, line)
	}
	return 0
}

func cmdFailuresShow(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("failures show", stderr)
	fs.Usage = func() { printUsage(stderr, "failures show <id>") }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	dir, err := state.Dir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	recs, err := state.Records(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	want := fs.Arg(0)
	for _, r := range recs {
		if r.ID != want && r.Dir != want {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, "records", r.Dir, "record.json"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		stdout.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return 0
	}
	fmt.Fprintf(stderr, "no failure record %q\n", want)
	return 1
}

func cmdShare(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("share", stderr)
	fs.Usage = func() { printUsage(stderr, "share <id> | --all") }
	all := fs.Bool("all", false, "share every local record")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 0 && !*all) || (fs.NArg() == 1 && *all) {
		fs.Usage()
		return 2
	}
	fmt.Fprintln(stderr, "sharing records is not in this build yet")
	return 2
}

func cmdCapture(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("capture", stderr)
	fs.Usage = func() { printUsage(stderr, "capture -- <command>") }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	fmt.Fprintln(stderr, "recording a captured command is not in this build yet")
	return 2
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("init", stderr)
	fs.Usage = func() { printUsage(stderr, "init [-o install.json] [-name <project>]") }
	out := fs.String("o", "install.json", "path to write")
	name := fs.String("name", "", "project name (default: this folder's name)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	project := *name
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		project = filepath.Base(cwd)
		if project == "." || project == string(filepath.Separator) {
			project = "project"
		}
	}
	if _, err := os.Stat(*out); err == nil {
		fmt.Fprintf(stderr, "%s already exists\n", *out)
		return 1
	}
	starter := struct {
		Schema   string `json:"schema"`
		Name     string `json:"name"`
		Requires struct {
			OS string `json:"os"`
		} `json:"requires"`
		Phases []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"phases"`
	}{
		Schema: "https://raw.githubusercontent.com/InstallZero/installzero/main/schemas/manifest-1.json",
		Name:   project,
	}
	starter.Requires.OS = "windows>=10.0.19045"
	starter.Phases = append(starter.Phases, struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{ID: "preflight", Type: "preflight"})
	body, err := json.MarshalIndent(starter, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	body = append(body, '\n')
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	return 0
}
