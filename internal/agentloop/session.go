package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/InstallZero/installzero/internal/llm"
)

// SessionOptions choose the model a run talks to: a hosted model
// ("provider:model-id"), a given llama-server URL, or a local llama-server
// (Model and Backend chosen for this machine when empty).
type SessionOptions struct {
	Model, Backend, Remote, URL string
	Context                     int // tokens, 32768 when 0
	// Root is the InstallZero folder; EnvFiles hold API keys.
	Root     string
	EnvFiles []string
	// Download allows fetching a missing llama.cpp build.
	Download bool
	Trail    io.Writer
	// Seed is sent with every request to a llama-server (not to hosted
	// models), so runs given the same seed sample the same way; 0 for none.
	Seed int
}

func withSeed(request json.RawMessage, seed int) (json.RawMessage, error) {
	if seed == 0 {
		return request, nil
	}
	m := map[string]json.RawMessage{}
	if len(request) > 0 && string(request) != "null" {
		if err := json.Unmarshal(request, &m); err != nil {
			return nil, fmt.Errorf("model request settings: %v", err)
		}
	}
	m["seed"] = json.RawMessage(strconv.Itoa(seed))
	return json.Marshal(m)
}

// StartSession connects to the model, starting a local llama-server when
// none is given. stop ends that server; call it when the run is over.
func StartSession(ctx context.Context, o SessionOptions) (s *Session, stop func(), err error) {
	line := func(format string, args ...any) {
		if o.Trail != nil {
			fmt.Fprintf(o.Trail, format+"\n", args...)
		}
	}
	if o.Context == 0 {
		o.Context = 32768
	}
	stop = func() {}
	if o.Remote != "" {
		r, err := llm.NewRemote(o.Remote, llm.RemoteConfig{Root: o.Root, EnvFiles: o.EnvFiles, Trail: o.Trail})
		if err != nil {
			return nil, stop, err
		}
		s = &Session{Model: &llm.Client{Llm: r, URL: r.URL}, Label: o.Remote, Context: 200000, Hosted: true}
		// An hf: model is the local GGUF on a remote llama-server: keep the
		// local context and its request settings.
		if r.Provider == "hf" {
			s.Context, s.LlamaServer = o.Context, true
			assets, err := llm.FindAssets(o.Root)
			if err != nil {
				return nil, stop, err
			}
			if s.Request, err = llm.ModelSettings((&llm.Runtime{Assets: assets}).ModelPath(r.Model)); err != nil {
				return nil, stop, err
			}
			if s.Request, err = withSeed(s.Request, o.Seed); err != nil {
				return nil, stop, err
			}
		}
		return s, stop, nil
	}
	if o.URL != "" {
		request, err := withSeed(nil, o.Seed)
		if err != nil {
			return nil, stop, err
		}
		return &Session{Model: &llm.Client{URL: o.URL}, Label: o.URL, Context: o.Context, Request: request, LlamaServer: true}, stop, nil
	}
	assets, err := llm.FindAssets(o.Root)
	if err != nil {
		return nil, stop, err
	}
	rt := &llm.Runtime{Assets: assets, Download: o.Download, Trail: o.Trail}
	if o.Backend == "" {
		o.Backend = rt.SelectBackend()
	}
	if o.Model == "" {
		if o.Model, err = rt.SelectModel(o.Backend); err != nil {
			return nil, stop, err
		}
	}
	modelPath := rt.ModelPath(o.Model)
	// A trainer sharing the GPU can leave too little memory for the model
	// plus its context; shrink the context first, fall back to the CPU last.
	if gpus := llm.NvidiaGPUs(); o.Backend != "cpu" && len(gpus) > 0 {
		if st, err := os.Stat(modelPath); err == nil {
			freeMB := 0
			for _, g := range gpus {
				freeMB = max(freeMB, g.FreeMB)
			}
			modelMB := int(st.Size() >> 20)
			fit := 0
			for _, c := range []int{32768, 16384, 8192} {
				if c <= o.Context && freeMB >= modelMB+512+roundInt(float64(c)/24) {
					fit = c
					break
				}
			}
			if fit > 0 && fit < o.Context {
				line("  Only %d MB of GPU memory free (the model needs %d MB): using a %d-token context instead of %d.", freeMB, modelMB, fit, o.Context)
				o.Context = fit
			} else if fit == 0 {
				line("  Only %d MB of GPU memory free and the model needs %d MB: running on the CPU (much slower). Pause GPU jobs, or use a remote model, for a faster run.", freeMB, modelMB)
				o.Backend = "cpu"
			}
		}
	}
	line("Starting the local model ...")
	srv, err := rt.Start(ctx, llm.ServerOptions{Model: o.Model, Backend: o.Backend, Context: o.Context})
	if err != nil {
		return nil, stop, err
	}
	line("  %s on %s, ready in %vs", srv.Model, srv.Backend, srv.LoadSeconds)
	request, err := llm.ModelSettings(modelPath)
	if err == nil {
		request, err = withSeed(request, o.Seed)
	}
	if err != nil {
		srv.Stop()
		return nil, stop, err
	}
	s = &Session{
		Model:        &llm.Client{URL: srv.URL},
		Label:        fmt.Sprintf("%s (%s)", srv.Model, srv.Backend),
		Context:      o.Context,
		Request:      request,
		LlamaServer:  true,
		ServerExited: srv.Exited,
	}
	return s, srv.Stop, nil
}
