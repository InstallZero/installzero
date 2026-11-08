package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/secrets"
)

// Hosted models, for evaluation only (the shipped engine runs offline). All
// serve OpenAI-compatible chat completions under their URL.
var providers = map[string]struct {
	URL     string
	KeyVars []string
}{
	"anthropic": {"https://api.anthropic.com/v1", []string{"ANTHROPIC_API_KEY"}},
	"openai":    {"https://api.openai.com/v1", []string{"OPENAI_API_KEY"}},
	"xai":       {"https://api.x.ai/v1", []string{"XAI_API_KEY"}},
	"google":    {"https://generativelanguage.googleapis.com/v1beta/openai", []string{"GOOGLE_GEMINI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	"hf":        {"", []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN"}},
}

// Remote is a hosted model, or ("hf:<file>.gguf") a local GGUF served by
// llama-server on Hugging Face Jobs.
type Remote struct {
	Provider string
	Model    string
	URL      string
	APIKey   string
}

// IsRemoteSpec reports whether spec is "provider:model-id" with a known
// provider.
func IsRemoteSpec(spec string) bool {
	p, m, ok := strings.Cut(spec, ":")
	_, known := providers[strings.ToLower(p)]
	return ok && known && m != ""
}

// RemoteConfig is what resolving a remote model needs.
type RemoteConfig struct {
	// Root is the InstallZero folder (for hf: models, agent/eval/sft and
	// the doctor assets).
	Root string
	// EnvFiles are .env files searched for keys after the environment (the
	// project's, then InstallZero's).
	EnvFiles []string
	// Trail receives progress lines; nil for none.
	Trail io.Writer
}

// NewRemote resolves "provider:model-id" and its API key.
func NewRemote(spec string, c RemoteConfig) (*Remote, error) {
	if !IsRemoteSpec(spec) {
		return nil, fmt.Errorf("Remote model spec must be provider:model-id with provider anthropic, openai, xai, google or hf; got '%s'.", spec)
	}
	p, model, _ := strings.Cut(spec, ":")
	p = strings.ToLower(p)
	key, err := APIKey(p, c.EnvFiles...)
	if err != nil {
		return nil, err
	}
	if p == "hf" {
		return hfServed(model, key, c)
	}
	return &Remote{Provider: p, Model: model, URL: providers[p].URL, APIKey: key}, nil
}

// hfServed is "hf:<file>.gguf": a local GGUF served by llama-server on
// Hugging Face Jobs (agent/eval/sft/hf_serve.py starts one or reuses the
// running one), for evaluation runs that should not wait for this
// machine's GPU. Requests go as to a local server; the HF token opens the
// Jobs proxy.
func hfServed(file, key string, c RemoteConfig) (*Remote, error) {
	serve := filepath.Join(c.Root, "agent", "eval", "sft", "hf_serve.py")
	var py string
	if runtime.GOOS == "windows" {
		py = filepath.Join(os.Getenv("LOCALAPPDATA"), "installzero", "sft", "venv", "Scripts", "python.exe")
	} else {
		home, _ := os.UserHomeDir()
		py = filepath.Join(home, ".local", "share", "installzero", "sft", "venv", "bin", "python")
	}
	if _, err := os.Stat(py); err != nil {
		return nil, fmt.Errorf("hf: models need the training venv (%s); run a training job once to create it.", py)
	}
	assets, err := FindAssets(c.Root)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(py, serve, "ensure", "--model", file, "--assets", assets)
	hideWindow(cmd)
	out, runErr := cmd.CombinedOutput()
	var lines []string
	url := ""
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if l == "" {
			continue
		}
		lines = append(lines, l)
		if strings.HasPrefix(l, "https://") {
			url = strings.TrimSpace(l)
		}
	}
	if runErr != nil || url == "" {
		last := lines
		if len(last) > 3 {
			last = last[len(last)-3:]
		}
		return nil, fmt.Errorf("No Hugging Face server for %s: %s", file, strings.Join(last, " "))
	}
	if c.Trail != nil {
		for _, l := range lines {
			if !strings.HasPrefix(l, "https://") {
				fmt.Fprintf(c.Trail, "  %s\n", l)
			}
		}
	}
	return &Remote{Provider: "hf", Model: file, URL: url, APIKey: key}, nil
}

// APIKey finds a provider's key: the environment first, then the .env
// files in order.
func APIKey(provider string, envFiles ...string) (string, error) {
	vars := providers[provider].KeyVars
	for _, v := range vars {
		if k := os.Getenv(v); k != "" {
			return k, nil
		}
	}
	for _, f := range envFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, e := range secrets.EnvEntries(string(data)) {
			for _, v := range vars {
				if e.Name == v && e.Value != "" {
					return e.Value, nil
				}
			}
		}
	}
	return "", fmt.Errorf("Set %s (environment, the project's .env or InstallZero's .env) to use %s models.", vars[0], provider)
}

// KeyVars are the environment variables a provider's key is read from, the
// usual one first.
func KeyVars(provider string) []string { return providers[provider].KeyVars }

// CheckKey asks the provider whether it accepts a key, by listing its models.
func CheckKey(ctx context.Context, provider, key string) error {
	p, ok := providers[provider]
	if !ok || p.URL == "" {
		return fmt.Errorf("no key check for %s", provider)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/models", nil)
	if err != nil {
		return err
	}
	for k, v := range Headers(&Remote{Provider: provider, APIKey: key}) {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", provider, res.Status)
	}
	return nil
}

// Headers are the authentication headers for a model; none for a local one.
func Headers(r *Remote) map[string]string {
	if r == nil || r.APIKey == "" {
		return nil
	}
	h := map[string]string{"Authorization": "Bearer " + r.APIKey}
	if r.Provider == "anthropic" {
		h["x-api-key"] = r.APIKey
		h["anthropic-version"] = "2023-06-01"
	}
	return h
}
