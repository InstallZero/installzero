package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/agentinstall"
	"github.com/InstallZero/installzero/internal/agentloop"
	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/keystore"
	"github.com/InstallZero/installzero/internal/llm"
	"github.com/InstallZero/installzero/internal/policy"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/state"
	"github.com/InstallZero/installzero/internal/ui"
)

// installReport is one install run, written the same way by the step
// runner and by --agent so the two can be compared.
type installReport struct {
	// Mode: deterministic (no model), fixer (a model fixes what the catalog
	// cannot) or agent (a model carries out the whole install).
	Mode    string              `json:"mode"`
	Model   string              `json:"model,omitempty"`
	Name    string              `json:"name"`
	Target  string              `json:"target"`
	Dir     string              `json:"dir"`
	Machine installmd.Machine   `json:"machine"`
	Started time.Time           `json:"started"`
	Seconds float64             `json:"seconds"`
	OK      bool                `json:"ok"`
	Steps   []runner.StepResult `json:"steps"`
	// Blocks counts the blocks and agent commands run (detect, install,
	// verify, run_change); Fixes the fixes the step runner applied.
	Blocks          int                  `json:"blocks"`
	ApprovalPrompts int                  `json:"approval_prompts"`
	Fixes           int                  `json:"fixes"`
	Agent           *agentinstall.Result `json:"agent,omitempty"`
	// ModelFixes are the fixer model's episodes and FixCommands what they ran.
	ModelFixes  []agentinstall.Episode `json:"model_fixes,omitempty"`
	FixCommands []agentinstall.Command `json:"fix_commands,omitempty"`
	Transcript  string                 `json:"transcript,omitempty"`
}

// modelUse sums the model's turns and tokens over a run.
func (r installReport) modelUse() (turns, prompt, completion int) {
	var runs []*agentloop.Run
	if r.Agent != nil {
		runs = append(runs, r.Agent.Run)
	}
	for _, e := range r.ModelFixes {
		runs = append(runs, e.Run)
	}
	for _, run := range runs {
		if run != nil {
			turns, prompt, completion = turns+run.Steps, prompt+run.PromptTokens, completion+run.CompletionTokens
		}
	}
	return
}

// writeInstallReport writes the report to path, or under the state folder
// when path is empty, and returns where it went.
func writeInstallReport(path string, r installReport) (string, error) {
	if path == "" {
		sd, err := state.Dir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(sd, "reports", fmt.Sprintf("%s-%s-%s.json", r.Started.Format("20060102-150405"), r.Name, r.Mode))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, writeJSON(path, r)
}

// modelChoices are the models installzero install offers to fix what the
// catalog cannot; any provider:model-id, .gguf or server URL works too.
var modelChoices = []struct{ Key, Spec, Label string }{
	{"none", "", "no model: only the known fixes from the failure catalog"},
	{"local", "local", "a local model picked for this PC (needs InstallZero's model files)"},
	{"anthropic", "anthropic:claude-opus-5-5", "Claude Opus 5.5 (hosted, Anthropic)"},
	{"openai", "openai:gpt-5.6-sol", "GPT 5.6 sol (hosted, OpenAI)"},
	{"xai", "xai:grok-4.7", "Grok 4.7 (hosted, xAI)"},
	{"google", "google:gemini-4-argon", "Gemini 4 Argon (hosted, Google)"},
}

// modelSpec turns a choice into a model spec ("" for none); ok is false
// for a choice that names no model.
func modelSpec(choice string) (spec string, ok bool) {
	c := strings.TrimSpace(choice)
	for _, m := range modelChoices {
		if strings.EqualFold(c, m.Key) {
			return m.Spec, true
		}
	}
	switch {
	case strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://"),
		strings.HasSuffix(strings.ToLower(c), ".gguf"), llm.IsRemoteSpec(c):
		return c, true
	}
	return "", false
}

// chooseModel is the model that fixes failures the catalog cannot: the
// --model flag, else the choice saved by an earlier run, else asked once
// when someone is at the keyboard (no model otherwise). The choice is kept
// for the next runs.
func chooseModel(flagValue string, interactive bool, in *bufio.Reader, w io.Writer, t *ui.Term) (string, error) {
	saved := ""
	if sd, err := state.Dir(); err == nil {
		saved = filepath.Join(sd, "model-choice.txt")
	}
	choice := flagValue
	if choice == "" && saved != "" {
		if b, err := os.ReadFile(saved); err == nil {
			choice = strings.TrimSpace(string(b))
		}
	}
	if choice == "" && interactive {
		fmt.Fprintf(w, "\n%s\n", t.Paint("When a step fails and the failure catalog has no fix, a model can investigate and fix it.", ui.Bold))
		fmt.Fprintln(w, "It reads logs, files and system state, and runs fixes under the same rules as the install: machine-wide changes need your yes.")
		for i, m := range modelChoices {
			fmt.Fprintf(w, "  %2d  %-10s %s\n", i+1, m.Key, m.Label)
		}
		for choice == "" {
			fmt.Fprint(w, "Model (a number, a name, provider:model-id or a .gguf) [1]: ")
			line, _ := in.ReadString('\n')
			a := strings.TrimSpace(line)
			if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(modelChoices) {
				a = modelChoices[n-1].Key
			}
			if a == "" {
				a = "none"
			}
			if _, ok := modelSpec(a); ok {
				choice = a
			} else {
				fmt.Fprintf(w, "  '%s' is not one of the choices.\n", a)
			}
		}
		fmt.Fprintf(w, "(Remembered for the next runs; --model changes it.)\n")
	}
	if choice == "" {
		return "", nil
	}
	spec, ok := modelSpec(choice)
	if !ok {
		return "", fmt.Errorf("--model %s: want none, local, anthropic, openai, xai, google, provider:model-id, a .gguf or a server URL", choice)
	}
	if saved != "" && (flagValue != "" || interactive) {
		os.MkdirAll(filepath.Dir(saved), 0o755)
		os.WriteFile(saved, []byte(choice+"\n"), 0o644)
	}
	return spec, nil
}

// envFiles are where API keys are looked for after the environment.
func envFiles() []string {
	if root, err := findRoot(); err == nil {
		return []string{filepath.Join(root, ".env")}
	}
	return nil
}

// ensureKey makes sure a hosted model has a key the provider accepts: from
// the environment, the OS credential store or InstallZero's .env, else
// typed (hidden) by someone at the keyboard and saved in the credential
// store. The key is used from memory; the install's commands never see it.
func ensureKey(ctx context.Context, spec string, interactive bool, in *bufio.Reader, w io.Writer) error {
	if !llm.IsRemoteSpec(spec) {
		return nil
	}
	provider := strings.ToLower(strings.SplitN(spec, ":", 2)[0])
	if provider == "hf" {
		return nil
	}
	key, _ := llm.APIKey(provider, envFiles()...)
	typed := false
	for tries := 0; ; tries++ {
		if key != "" {
			err := llm.CheckKey(ctx, provider, key)
			if err == nil {
				llm.UseKey(provider, key)
				if typed {
					storeKey(w, provider, key)
				}
				return nil
			}
			fmt.Fprintf(w, "  %s did not accept the API key (%v).\n", provider, err)
		}
		if !interactive || tries >= 2 {
			return fmt.Errorf("no %s API key that %s accepts; run %s key set %s", provider, provider, Name, provider)
		}
		if key = readKey(w, in, provider, "Enter for no model"); key == "" {
			return fmt.Errorf("no %s API key given", provider)
		}
		typed = true
	}
}

// readKey asks for a key without showing it.
func readKey(w io.Writer, in *bufio.Reader, provider, skip string) string {
	fmt.Fprintf(w, "  %s API key (hidden; saved in %s; %s): ", provider, keystore.Name(provider), skip)
	var line string
	ui.WithoutEcho(os.Stdin, func() { line, _ = in.ReadString('\n') })
	fmt.Fprintln(w)
	return strings.TrimSpace(line)
}

// storeKey saves an accepted key in the OS credential store; where there
// is none, the key lasts for this run only.
func storeKey(w io.Writer, provider, key string) {
	if err := keystore.Set(provider, key); err != nil {
		fmt.Fprintf(w, "  Could not save the key in %s (%v); it is used for this run only.\n", keystore.Name(provider), err)
		return
	}
	fmt.Fprintf(w, "  Saved in %s. %s key remove %s deletes it.\n", keystore.Name(provider), Name, provider)
}

// startAgentModel connects to a model: provider:model-id, a .gguf, "local"
// for the default model, or a server URL.
func startAgentModel(ctx context.Context, spec string, trail io.Writer) (*agentloop.Session, func(), error) {
	o := agentloop.SessionOptions{Trail: trail, Download: true}
	switch {
	case strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://"):
		o.URL = spec
	case spec == "local":
	case strings.HasSuffix(strings.ToLower(spec), ".gguf"):
		o.Model = spec
	case strings.Contains(spec, ":"):
		o.Remote = spec
	default:
		return nil, func() {}, fmt.Errorf("model %s: want provider:model-id (e.g. google:gemini-4-argon), a .gguf, local, or a server URL", spec)
	}
	if root, err := findRoot(); err == nil {
		o.Root, o.EnvFiles = root, []string{filepath.Join(root, ".env")}
	} else if o.Remote == "" && o.URL == "" {
		return nil, func() {}, err
	}
	return agentloop.StartSession(ctx, o)
}

// cmdCompare shows install reports side by side.
func cmdCompare(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr, "compare <report.json>... (written by install, --report or under the state folder's reports)")
		return 2
	}
	var reps []installReport
	for _, p := range args {
		var r installReport
		data, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(data, &r)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", p, err)
			return 1
		}
		reps = append(reps, r)
	}
	rows := [][]string{{""}}
	add := func(label string, f func(r installReport) string) {
		row := []string{label}
		for _, r := range reps {
			row = append(row, f(r))
		}
		rows = append(rows, row)
	}
	for i := range reps {
		rows[0] = append(rows[0], fmt.Sprintf("run %d", i+1))
	}
	add("mode", func(r installReport) string { return r.Mode })
	add("model", func(r installReport) string { return or(r.Model, "-") })
	add("installed", func(r installReport) string { return map[bool]string{true: "yes", false: "no"}[r.OK] })
	add("time", func(r installReport) string { return ui.Elapsed(time.Duration(r.Seconds * float64(time.Second))) })
	add("blocks and commands run", func(r installReport) string { return fmt.Sprint(r.Blocks) })
	add("approval prompts", func(r installReport) string { return fmt.Sprint(r.ApprovalPrompts) })
	add("runner fixes", func(r installReport) string { return fmt.Sprint(r.Fixes) })
	add("agent said", func(r installReport) string {
		if r.Agent == nil {
			return "-"
		}
		return r.Agent.Status
	})
	add("model fixes", func(r installReport) string {
		if len(r.ModelFixes) == 0 {
			return "-"
		}
		var acts []string
		for _, e := range r.ModelFixes {
			acts = append(acts, e.Step+":"+e.Action)
		}
		return strings.Join(acts, " ")
	})
	add("model turns", func(r installReport) string { t, _, _ := r.modelUse(); return fmt.Sprint(t) })
	add("tokens (prompt/completion)", func(r installReport) string {
		_, p, c := r.modelUse()
		return fmt.Sprintf("%d/%d", p, c)
	})
	var ids []string
	seen := map[string]bool{}
	for _, r := range reps {
		for _, s := range r.Steps {
			if !seen[s.ID] {
				seen[s.ID] = true
				ids = append(ids, s.ID)
			}
		}
	}
	for _, id := range ids {
		add("step "+id, func(r installReport) string {
			for _, s := range r.Steps {
				if s.ID == id {
					return string(s.Status)
				}
			}
			return "-"
		})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], len(c))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, c := range row {
			fmt.Fprintf(&b, "%-*s  ", widths[i], c)
		}
		fmt.Fprintln(stdout, strings.TrimRight(b.String(), " "))
	}
	return 0
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func printAgentSummary(w io.Writer, t *ui.Term, r *agentinstall.Result) {
	fmt.Fprintf(w, "\n%s %s\n", t.Paint("The agent says:", ui.Bold), r.Status)
	if r.Summary != "" {
		fmt.Fprintf(w, "  %s\n", r.Summary)
	}
	if r.BlockedOn != "" {
		fmt.Fprintf(w, "  %s %s\n", t.Paint("Needs you:", ui.Yellow), r.BlockedOn)
	}
	sys, refused := 0, 0
	for _, c := range r.Commands {
		switch c.Tier {
		case policy.System:
			sys++
		case policy.Never:
			refused++
		}
	}
	if run := r.Run; run != nil {
		fmt.Fprintf(w, "  %d model turns, %d tool calls, %d prompt and %d completion tokens", run.Steps, run.ToolCalls, run.PromptTokens, run.CompletionTokens)
		if run.StopReason != "" {
			fmt.Fprintf(w, ", stopped: %s", run.StopReason)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  %d commands (%d machine-wide, %d refused as destructive)\n", len(r.Commands), sys, refused)
}
