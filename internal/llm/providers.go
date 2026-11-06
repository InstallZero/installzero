package llm

import (
	"fmt"
	"os"
	"strings"

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

// NewRemote resolves "provider:model-id" and its API key. envFiles are .env
// files searched after the environment (the project's, then InstallZero's).
func NewRemote(spec string, envFiles ...string) (*Remote, error) {
	if !IsRemoteSpec(spec) {
		return nil, fmt.Errorf("Remote model spec must be provider:model-id with provider anthropic, openai, xai, google or hf; got '%s'.", spec)
	}
	p, model, _ := strings.Cut(spec, ":")
	p = strings.ToLower(p)
	if p == "hf" {
		return nil, fmt.Errorf("hf: models are not supported by the Go engine yet")
	}
	key, err := APIKey(p, envFiles...)
	if err != nil {
		return nil, err
	}
	return &Remote{Provider: p, Model: model, URL: providers[p].URL, APIKey: key}, nil
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
