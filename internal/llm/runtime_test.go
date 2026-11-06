package llm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		v, min string
		want   bool
	}{{"551.61", "551.61", true}, {"560.94", "551.61", true}, {"551.6", "551.61", false}, {"537.13", "551.61", false}, {"1000", "551.61", true}} {
		if got := versionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("%s >= %s: got %v", c.v, c.min, got)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}

func tgzOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	w := tar.NewWriter(gz)
	for name, body := range files {
		w.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		w.Write([]byte(body))
	}
	w.Close()
	gz.Close()
	return b.Bytes()
}

func TestUnpack(t *testing.T) {
	dir := t.TempDir()
	z := filepath.Join(dir, "a.zip")
	os.WriteFile(z, zipOf(t, map[string]string{"bin/llama-server.exe": "x", "bin/ggml.dll": "y"}), 0o644)
	out := filepath.Join(dir, "out")
	if err := unpack(z, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bin", "ggml.dll")); string(b) != "y" {
		t.Error("zip not unpacked")
	}
	tg := filepath.Join(dir, "a.tar.gz")
	os.WriteFile(tg, tgzOf(t, map[string]string{"build/bin/llama-server": "z"}), 0o644)
	if err := unpack(tg, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "build", "bin", "llama-server")); string(b) != "z" {
		t.Error("tar.gz not unpacked")
	}
	bad := filepath.Join(dir, "bad.zip")
	os.WriteFile(bad, zipOf(t, map[string]string{"../evil.txt": "x"}), 0o644)
	if err := unpack(bad, out); err == nil {
		t.Error("a path leaving the folder was unpacked")
	}
}

// fakeBuild puts this platform's cpu build in builds, served from body.
func fakeBuild(t *testing.T, body []byte) Asset {
	t.Helper()
	sum := sha256.Sum256(body)
	name := "llama-test-cpu.zip"
	if !strings.HasSuffix(serverExe(), ".exe") {
		name = "llama-test-cpu.tar.gz"
	}
	a := Asset{name, hex.EncodeToString(sum[:])}
	old, had := builds[platform()]
	builds[platform()] = map[string][]Asset{"cpu": {a}}
	t.Cleanup(func() {
		if had {
			builds[platform()] = old
		} else {
			delete(builds, platform())
		}
	})
	return a
}

func buildArchive(t *testing.T) []byte {
	files := map[string]string{"llama-test/" + serverExe(): "server", "llama-test/lib.so": "lib"}
	if strings.HasSuffix(serverExe(), ".exe") {
		return zipOf(t, files)
	}
	return tgzOf(t, files)
}

func TestInstall(t *testing.T) {
	body := buildArchive(t)
	a := fakeBuild(t, body)
	assets := t.TempDir()
	rt := &Runtime{Assets: assets}
	if _, err := rt.Install(context.Background(), "cpu"); err == nil || !strings.Contains(err.Error(), "downloads are off") {
		t.Fatalf("no archive and no downloads: %v", err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+a.Name) {
			w.Write(body)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	old := releaseURL
	releaseURL = s.URL + "/"
	defer func() { releaseURL = old }()
	rt.Download = true
	dir, err := rt.Install(context.Background(), "cpu")
	if err != nil {
		t.Fatal(err)
	}
	// The server comes up to the folder's top, with its libraries.
	for _, f := range []string{serverExe(), "lib.so"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not at the top of %s", f, dir)
		}
	}
	if _, err := os.Stat(filepath.Join(assets, a.Name)); err != nil {
		t.Error("the downloaded archive was not kept")
	}
}

func TestDownloadChecksSha256(t *testing.T) {
	fakeBuild(t, []byte("the real build"))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("something else")) }))
	defer s.Close()
	old := releaseURL
	releaseURL = s.URL + "/"
	defer func() { releaseURL = old }()
	assets := t.TempDir()
	_, err := (&Runtime{Assets: assets, Download: true}).Install(context.Background(), "cpu")
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("got %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(assets, "llama-test-cpu*")); len(m) > 0 {
		t.Errorf("a bad download was kept: %v", m)
	}
}

func TestSelectModelOnCPU(t *testing.T) {
	assets := t.TempDir()
	rt := &Runtime{Assets: assets}
	if _, err := rt.SelectModel("cpu"); err == nil {
		t.Error("no models, no error")
	}
	// Preference order decides among those that fit in RAM; the smallest
	// file when none fits.
	os.WriteFile(filepath.Join(assets, "granite-4.1-8b-Q4_K_M.gguf"), make([]byte, 30), 0o644)
	os.WriteFile(filepath.Join(assets, "Qwen3.5-9B-Q4_K_M.gguf"), make([]byte, 20), 0o644)
	got, err := rt.SelectModel("cpu")
	if err != nil {
		t.Fatal(err)
	}
	// Qwen comes before Granite and is the smaller file, so it wins whether
	// or not this machine has the RAM for it.
	if got != "Qwen3.5-9B-Q4_K_M.gguf" {
		t.Errorf("got %s with %d MB RAM", got, TotalRAMMB())
	}
}

func TestModelSettingsAndAssets(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.gguf")
	os.WriteFile(model, nil, 0o644)
	if r, err := ModelSettings(model); r != nil || err != nil {
		t.Errorf("no settings file: %s %v", r, err)
	}
	os.WriteFile(model+".iz.json", []byte(`{"request": {"chat_template_kwargs": {"enable_thinking": false}}}`), 0o644)
	r, err := ModelSettings(model)
	var v map[string]any
	if err != nil || json.Unmarshal(r, &v) != nil || v["chat_template_kwargs"] == nil {
		t.Errorf("settings: %s %v", r, err)
	}
	t.Setenv("RL_DOCTOR_ASSETS", dir)
	if got, err := FindAssets(t.TempDir()); err != nil || got != dir {
		t.Errorf("assets: %s %v", got, err)
	}
	t.Setenv("RL_DOCTOR_ASSETS", t.TempDir())
	if _, err := FindAssets(t.TempDir()); err == nil || !strings.Contains(err.Error(), "Set RL_DOCTOR_ASSETS") {
		t.Errorf("no assets: %v", err)
	}
}

func TestFreeLoopbackPort(t *testing.T) {
	p, err := FreeLoopbackPort(18080, 18180)
	if err != nil || p < 18080 || p > 18180 {
		t.Errorf("port %d, %v", p, err)
	}
}

// IZ_LLAMA_TEST=1 starts the real llama-server from the doctor assets and
// runs one streamed turn (IZ_LLAMA_MODEL and IZ_LLAMA_BACKEND choose).
func TestLlamaServer(t *testing.T) {
	if os.Getenv("IZ_LLAMA_TEST") == "" {
		t.Skip("set IZ_LLAMA_TEST=1 to start llama-server")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	assets, err := FindAssets(root)
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Assets: assets}
	backend := os.Getenv("IZ_LLAMA_BACKEND")
	if backend == "" {
		backend = rt.SelectBackend()
	}
	model := os.Getenv("IZ_LLAMA_MODEL")
	if model == "" {
		if model, err = rt.SelectModel(backend); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	srv, err := rt.Start(ctx, ServerOptions{Model: model, Backend: backend, Context: 8192})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	t.Logf("%s on %s at %s, ready in %vs", srv.Model, srv.Backend, srv.URL, srv.LoadSeconds)
	extra, _ := ModelSettings(rt.ModelPath(model))
	tools := []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"list_dir","description":"List a folder.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}`)}
	msgs := []Message{{Role: "system", Content: Text("You inspect machines with tools.")}, {Role: "user", Content: Text("List the folder C:\\Windows with the list_dir tool.")}}
	r, err := (&Client{URL: srv.URL}).Turn(ctx, msgs, tools, 0.2, 1024, extra, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reply: %q, reasoning %d chars, calls %+v, usage %+v", r.Message.ContentText(), len(r.Message.ReasoningContent), r.Message.ToolCalls, r.Usage)
	if len(r.Message.ToolCalls) == 0 && r.Message.ContentText() == "" {
		t.Error("an empty reply")
	}
	if r.Usage == nil || r.Usage.PromptTokens == 0 {
		t.Error("no usage")
	}
	if exited, _ := srv.Exited(); exited {
		t.Error("the server died")
	}
}
