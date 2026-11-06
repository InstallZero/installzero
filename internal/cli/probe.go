package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/probe"
	"github.com/InstallZero/installzero/internal/target/wsl"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// findRoot is the InstallZero folder: IZ_ROOT, or the first folder at or
// above the working directory or this binary that holds the probe's spec.
func findRoot() (string, error) {
	if r := os.Getenv("IZ_ROOT"); r != "" {
		return filepath.Abs(r)
	}
	var starts []string
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, s := range starts {
		for d := s; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "agent", "INSTALL-MD.md")); err == nil {
				return d, nil
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return "", errors.New("InstallZero's folder (with agent/INSTALL-MD.md) was not found; run from inside it or set IZ_ROOT")
}

func writeJSON(path string, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

func cmdProbe(args []string, stdout, stderr io.Writer) int {
	fs := newFlag("probe", stderr)
	fs.Usage = func() {
		printUsage(stderr, "probe --wsl <distro> [--target <path|url|name>] [--out <install.md>] [--model <gguf> | --remote <provider:model> | --llm-url <url>] [flags]")
		fs.PrintDefaults()
	}
	var o probe.Options
	var hide listFlag
	distro := fs.String("wsl", "", "probe a project installed in this WSL distro (tools run inside it as root)")
	fs.StringVar(&o.Where, "target", "", "where to start: the project's folder, a URL or port it serves on, or a name")
	fs.StringVar(&o.Out, "out", "", "install.md to write (default probe-<time>.install.md here)")
	fs.StringVar(&o.Draft, "draft", "", "install.md to refine (with --agent-report)")
	fs.StringVar(&o.AgentReport, "agent-report", "", "installer report from replaying --draft on a clean machine")
	fs.BoolVar(&o.NoDocs, "no-docs", false, "kept for compatibility: docs are always hidden")
	fs.StringVar(&o.Model, "model", "", "local GGUF model")
	fs.StringVar(&o.Backend, "backend", "", "llama-server backend: cuda, vulkan or cpu")
	fs.StringVar(&o.URL, "llm-url", "", "an OpenAI-compatible server already running")
	fs.StringVar(&o.Remote, "remote", "", "hosted model, provider:model-id")
	fs.Var(&hide, "hide", "test-harness folder or file to hide as well (repeatable, comma-separated)")
	fs.IntVar(&o.Context, "context", 32768, "largest context for a local model; less is used when GPU memory is short")
	fs.IntVar(&o.MaxTurns, "max-turns", 40, "model turns")
	timeout := fs.Int("timeout", 30, "minutes")
	fs.StringVar(&o.ResultFile, "result", "", "result JSON to write (default next to the transcript)")
	fs.StringVar(&o.Guidance, "guidance", "", "lesson set JSON {id, text} added to the system prompt")
	fs.BoolVar(&o.Download, "download", false, "download a missing llama.cpp build")
	toolsOut := fs.String("tools-out", "", "write the tool definitions as JSON and stop")
	promptOut := fs.String("prompt-out", "", "write the system prompt, first user message and tools as JSON and stop")
	toolCalls := fs.String("tool-calls", "", "run the tool calls in this JSON file ([{name, arguments}]), write their outputs to <file>.out.json and stop")
	quiet := fs.Bool("quiet", false, "no progress output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && o.Where != "") {
		fs.Usage()
		return 2
	}
	if fs.NArg() == 1 {
		o.Where = fs.Arg(0)
	}
	o.Hide = hide
	o.Timeout = time.Duration(*timeout) * time.Minute
	if !*quiet {
		o.Log = stdout
	}
	if *toolsOut != "" {
		tools, err := probe.Tools()
		if err == nil {
			err = writeJSON(*toolsOut, tools)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *distro == "" {
		fmt.Fprintln(stderr, "probe needs --wsl <distro>: this build probes projects installed in WSL; use agent/Probe.ps1 for Windows")
		return 2
	}
	root, err := findRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	o.Root = root
	o.Target = wsl.New(*distro)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *promptOut != "" {
		pr, err := probe.Prompts(ctx, o)
		if err == nil {
			err = writeJSON(*promptOut, pr)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *toolCalls != "" {
		res, err := probe.CallTools(ctx, o, *toolCalls)
		if err == nil {
			err = writeJSON(*toolCalls+".out.json", res)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if _, err := probe.Run(ctx, o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
