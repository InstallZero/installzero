package llm

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/ui"
)

// The local runtime: finds the llama.cpp builds and models, picks a backend
// and model for this machine, and runs llama-server on loopback.
//
// Assets folder layout (archives are unpacked into llama/<backend> on first
// use, and downloaded first when allowed):
//
//	llama-<build>-bin-win-cuda-12.4-x64.zip, cudart-llama-bin-win-cuda-12.4-x64.zip,
//	llama-<build>-bin-ubuntu-vulkan-x64.tar.gz, llama-<build>-bin-macos-arm64.tar.gz, ...
//	*.gguf, and <model>.gguf.iz.json with request settings

// LlamaBuild is the llama.cpp release the runtime runs.
const LlamaBuild = "b9771"

// Asset is one release archive and its sha256.
type Asset struct{ Name, SHA256 string }

// builds are the archives each backend needs, per GOOS/GOARCH. Linux has no
// CUDA build: NVIDIA GPUs run there on Vulkan.
var builds = map[string]map[string][]Asset{
	"windows/amd64": {
		"cuda": {{"llama-b9771-bin-win-cuda-12.4-x64.zip", "a8106946ddd37a79d8f0e4a79a17fab60b60579cbe0e19bb58f5c83d7c8918af"},
			{"cudart-llama-bin-win-cuda-12.4-x64.zip", "8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6"}},
		"vulkan": {{"llama-b9771-bin-win-vulkan-x64.zip", "8183a26e70260e5b51b82647c5b05116fa368285cb6510227e4fac039e1033c3"}},
		"cpu":    {{"llama-b9771-bin-win-cpu-x64.zip", "1b3f373b3618a7cebe79b0fb33d8c26176d19374b560f9490565d6fdb6a11536"}},
	},
	"windows/arm64": {
		"cpu": {{"llama-b9771-bin-win-cpu-arm64.zip", "ceacc9c72d1caa6da7ad20d73bff96a34a553f8881d0fcf000677710ad8e6cb5"}},
	},
	"linux/amd64": {
		"vulkan": {{"llama-b9771-bin-ubuntu-vulkan-x64.tar.gz", "b06642bcda879bf2fd7bde9ee067e097c9925d46c43c40e6d3da65a319cb90cb"}},
		"cpu":    {{"llama-b9771-bin-ubuntu-x64.tar.gz", "9ea63c3e901ed05f7e5dba391c9858e5f0ad13428af7c8decaabaf5681862b75"}},
	},
	"linux/arm64": {
		"vulkan": {{"llama-b9771-bin-ubuntu-vulkan-arm64.tar.gz", "07877202ef931b2eed0b78c3c1075ac55680b8683aad1b9b5c4311db5fdcb8f8"}},
		"cpu":    {{"llama-b9771-bin-ubuntu-arm64.tar.gz", "5aee1dbbac6cfc653f9f922a5e46f286df0c7146a59241f93da6e26946b232cb"}},
	},
	"darwin/arm64": {
		"metal": {{"llama-b9771-bin-macos-arm64.tar.gz", "27c65b7b5d1e74774b14a19345b9a41498a215ee032191c5946c7ecc6c0bfa17"}},
	},
	"darwin/amd64": {
		"cpu": {{"llama-b9771-bin-macos-x64.tar.gz", "053770942d8ceec7d56b8aa3c684c49df3d9fad4674afd7f2f2543ab4519f2ef"}},
	},
}

var releaseURL = "https://github.com/ggml-org/llama.cpp/releases/download/" + LlamaBuild + "/"

// DoctorModel is a model the runtime knows, in preference order.
// MinVramMB: free GPU memory needed to run fully on the GPU at 32K context.
// MinRamMB: system RAM needed to run on the CPU.
type DoctorModel struct {
	File      string
	MinVramMB int
	MinRamMB  int
}

var DoctorModels = []DoctorModel{
	{"gpt-oss-20b-MXFP4.gguf", 13500, 24000},
	{"gemma-4-E4B-it-Q4_K_M.gguf", 4800, 8000},
	{"Qwen3.5-9B-Q4_K_M.gguf", 7000, 12000},
	{"granite-4.1-8b-Q4_K_M.gguf", 11000, 14000},
}

// FindAssets finds the folder with the llama.cpp builds and models: the
// RL_DOCTOR_ASSETS variable, then doctor/assets in the InstallZero folder
// root, then doctor-assets next to it.
func FindAssets(root string) (string, error) {
	candidates := []string{os.Getenv("RL_DOCTOR_ASSETS"), filepath.Join(root, "doctor", "assets"), filepath.Join(root, "..", "doctor-assets")}
	var tried []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		tried = append(tried, c)
		if m, _ := filepath.Glob(filepath.Join(c, "*.gguf")); len(m) > 0 {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("No doctor assets (llama.cpp builds and .gguf models) found. Looked in: %s. Set RL_DOCTOR_ASSETS.", strings.Join(tried, "; "))
}

// GPU is an NVIDIA GPU as nvidia-smi reports it.
type GPU struct {
	Name            string
	Driver          string
	TotalMB, FreeMB int
}

// NvidiaGPUs lists the NVIDIA GPUs; none when nvidia-smi is missing or fails.
func NvidiaGPUs() []GPU {
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	out, err := exec.Command(smi, "--query-gpu=name,driver_version,memory.total,memory.free", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.Split(line, ",")
		if len(p) < 4 {
			continue
		}
		for i := range p {
			p[i] = strings.TrimSpace(p[i])
		}
		total, err1 := strconv.Atoi(p[2])
		free, err2 := strconv.Atoi(p[3])
		if err1 != nil || err2 != nil {
			continue
		}
		gpus = append(gpus, GPU{Name: p[0], Driver: p[1], TotalMB: total, FreeMB: free})
	}
	return gpus
}

// versionAtLeast compares dotted version numbers.
func versionAtLeast(v, min string) bool {
	a, b := strings.Split(v, "."), strings.Split(min, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			y, _ = strconv.Atoi(b[i])
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

func serverExe() string {
	if runtime.GOOS == "windows" {
		return "llama-server.exe"
	}
	return "llama-server"
}

// Runtime is the local model runtime over one assets folder.
type Runtime struct {
	Assets string
	// Download allows fetching a missing build from the llama.cpp release.
	Download bool
	// Trail receives progress lines; nil for none.
	Trail io.Writer
}

func (rt *Runtime) line(format string, args ...any) {
	if rt.Trail != nil {
		fmt.Fprintf(rt.Trail, format+"\n", args...)
	}
}

// available reports whether a backend's build is unpacked, its archives
// are in the assets folder, or it may be downloaded.
func (rt *Runtime) available(backend string) bool {
	assets, ok := builds[platform()][backend]
	if !ok {
		return false
	}
	if findFile(filepath.Join(rt.Assets, "llama", backend), serverExe()) != "" || rt.Download {
		return true
	}
	for _, a := range assets {
		if _, err := os.Stat(filepath.Join(rt.Assets, a.Name)); err != nil {
			return false
		}
	}
	return true
}

// SelectBackend picks CUDA for an NVIDIA GPU with a driver new enough for
// CUDA 12.4 (551.61 on Windows), Metal on Apple silicon, Vulkan where its
// loader is installed, else the CPU.
func (rt *Runtime) SelectBackend() string {
	gpus := NvidiaGPUs()
	if len(gpus) > 0 && versionAtLeast(gpus[0].Driver, "551.61") && rt.available("cuda") {
		return "cuda"
	}
	if rt.available("metal") {
		return "metal"
	}
	if hasVulkan() && rt.available("vulkan") {
		return "vulkan"
	}
	return "cpu"
}

func hasVulkan() bool {
	var paths []string
	switch runtime.GOOS {
	case "windows":
		dir := os.Getenv("SystemRoot")
		if dir == "" {
			dir = `C:\Windows`
		}
		paths = []string{filepath.Join(dir, "System32", "vulkan-1.dll")}
	case "linux":
		for _, d := range []string{"/usr/lib/x86_64-linux-gnu", "/usr/lib/aarch64-linux-gnu", "/usr/lib64", "/usr/lib", "/lib/x86_64-linux-gnu", "/lib/aarch64-linux-gnu"} {
			paths = append(paths, filepath.Join(d, "libvulkan.so.1"))
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// SelectModel picks the first known model present that fits: in free GPU
// memory on an NVIDIA GPU, in RAM on the CPU. A GPU that reports no free
// memory (Vulkan on another vendor) gets the smallest; Metal shares RAM
// with the GPU and may use about three quarters of it. When nothing fits
// comfortably the smallest file still runs, slowly.
func (rt *Runtime) SelectModel(backend string) (string, error) {
	var present []DoctorModel
	for _, m := range DoctorModels {
		if _, err := os.Stat(filepath.Join(rt.Assets, m.File)); err == nil {
			present = append(present, m)
		}
	}
	if len(present) == 0 {
		var names []string
		for _, m := range DoctorModels {
			names = append(names, m.File)
		}
		return "", fmt.Errorf("No known model file in %s (expected one of: %s).", rt.Assets, strings.Join(names, ", "))
	}
	ramMB := TotalRAMMB()
	gpuFree := -1
	if backend == "metal" {
		gpuFree = ramMB * 3 / 4
	} else if backend != "cpu" {
		if gpus := NvidiaGPUs(); len(gpus) > 0 {
			gpuFree = gpus[0].FreeMB
		}
	}
	if backend != "cpu" && gpuFree < 0 {
		smallest := present[0]
		for _, m := range present[1:] {
			if m.MinVramMB < smallest.MinVramMB {
				smallest = m
			}
		}
		return smallest.File, nil
	}
	for _, m := range present {
		if gpuFree >= 0 && gpuFree >= m.MinVramMB {
			return m.File, nil
		}
		if gpuFree < 0 && ramMB >= m.MinRamMB {
			return m.File, nil
		}
	}
	sort.SliceStable(present, func(i, j int) bool {
		return fileSize(filepath.Join(rt.Assets, present[i].File)) < fileSize(filepath.Join(rt.Assets, present[j].File))
	})
	return present[0].File, nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// findFile finds name under dir; "" when absent.
func findFile(dir, name string) string {
	found := ""
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Install unpacks a backend's build into llama/<backend> (downloading its
// archives first when they are missing and downloads are allowed) and
// returns the folder holding llama-server.
func (rt *Runtime) Install(ctx context.Context, backend string) (string, error) {
	dir := filepath.Join(rt.Assets, "llama", backend)
	if _, err := os.Stat(filepath.Join(dir, serverExe())); err == nil {
		return dir, nil
	}
	assets, ok := builds[platform()][backend]
	if !ok {
		return "", fmt.Errorf("llama.cpp %s has no %s build for %s", LlamaBuild, backend, platform())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, a := range assets {
		archive := filepath.Join(rt.Assets, a.Name)
		if _, err := os.Stat(archive); err != nil {
			if !rt.Download {
				return "", fmt.Errorf("%s is not in %s (downloads are off)", a.Name, rt.Assets)
			}
			if err := rt.download(ctx, a, archive); err != nil {
				return "", err
			}
		}
		if err := unpack(archive, dir); err != nil {
			return "", fmt.Errorf("unpacking %s: %v", a.Name, err)
		}
	}
	exe := findFile(dir, serverExe())
	if exe == "" {
		return "", fmt.Errorf("%s not found after unpacking %s", serverExe(), assetNames(assets))
	}
	// The server and its libraries go to the folder's top, next to the
	// CUDA runtime unpacked there.
	if from := filepath.Dir(exe); from != dir {
		entries, err := os.ReadDir(from)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			to := filepath.Join(dir, e.Name())
			os.RemoveAll(to)
			if err := os.Rename(filepath.Join(from, e.Name()), to); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

func assetNames(as []Asset) string {
	var n []string
	for _, a := range as {
		n = append(n, a.Name)
	}
	return strings.Join(n, ", ")
}

// download fetches a release archive and keeps it only when its sha256
// matches the pinned one.
func (rt *Runtime) download(ctx context.Context, a Asset, to string) error {
	rt.line("  Downloading %s ...", a.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL+a.Name, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %v", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", a.Name, resp.Status)
	}
	tmp := to + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	dst := io.MultiWriter(f, h)
	if rt.Trail != nil {
		m := ui.NewMeter(rt.Trail, a.Name, resp.ContentLength)
		defer m.Done()
		dst = io.MultiWriter(f, h, m)
	}
	_, err = io.Copy(dst, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("downloading %s: %v", a.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		os.Remove(tmp)
		return fmt.Errorf("%s has sha256 %s, expected %s; not used", a.Name, got, a.SHA256)
	}
	return os.Rename(tmp, to)
}

// unpack extracts a .zip or .tar.gz into dir, refusing paths that leave it.
func unpack(archive, dir string) error {
	target := func(name string) (string, error) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if rel, err := filepath.Rel(dir, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s leaves the folder", name)
		}
		return p, nil
	}
	write := func(p string, mode os.FileMode, r io.Reader) error {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		os.Remove(p)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			p, err := target(f.Name)
			if err != nil {
				return err
			}
			if f.FileInfo().IsDir() {
				os.MkdirAll(p, 0o755)
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			err = write(p, f.Mode().Perm(), rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	fh, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := target(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			if err := write(p, os.FileMode(h.Mode).Perm(), tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || strings.Contains(filepath.ToSlash(h.Linkname), "../") {
				return fmt.Errorf("%s links outside the folder", h.Name)
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

// FreeLoopbackPort finds a loopback port in [from, to] outside the ranges
// Windows reserves (Hyper-V, WSL).
func FreeLoopbackPort(from, to int) (int, error) {
	excluded := excludedPorts()
	for p := from; p <= to; p++ {
		skip := false
		for _, r := range excluded {
			if p >= r[0] && p <= r[1] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("No free loopback port in %d-%d", from, to)
}

// Server is a running llama-server.
type Server struct {
	Cmd         *exec.Cmd
	URL         string
	Model       string
	Backend     string
	Log         string
	LoadSeconds float64
	done        chan struct{}
}

// Exited reports whether the server has stopped, and its log.
func (s *Server) Exited() (bool, string) {
	select {
	case <-s.done:
		return true, s.Log
	default:
		return false, s.Log
	}
}

// Stop ends the server.
func (s *Server) Stop() {
	if s == nil || s.Cmd == nil || s.Cmd.Process == nil {
		return
	}
	if exited, _ := s.Exited(); !exited {
		s.Cmd.Process.Kill()
		<-s.done
	}
}

// ServerOptions start a llama-server; empty fields are chosen for this
// machine.
type ServerOptions struct {
	Model   string // a file in the assets folder, or a path
	Backend string
	Context int           // tokens, 32768 when 0
	Timeout time.Duration // to load, 600 s when 0
}

// Start runs llama-server on loopback with a model and waits until it
// answers /health.
func (rt *Runtime) Start(ctx context.Context, o ServerOptions) (*Server, error) {
	if o.Context == 0 {
		o.Context = 32768
	}
	if o.Timeout == 0 {
		o.Timeout = 600 * time.Second
	}
	if o.Backend == "" {
		o.Backend = rt.SelectBackend()
	}
	if o.Model == "" {
		m, err := rt.SelectModel(o.Backend)
		if err != nil {
			return nil, err
		}
		o.Model = m
	}
	modelPath := rt.ModelPath(o.Model)
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("Model not found: %s", modelPath)
	}
	dir, err := rt.Install(ctx, o.Backend)
	if err != nil {
		return nil, err
	}
	port, err := FreeLoopbackPort(18080, 18180)
	if err != nil {
		return nil, err
	}
	log := filepath.Join(rt.Assets, "llama-server-"+o.Backend+".log")
	args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "-c", strconv.Itoa(o.Context), "--jinja", "--no-webui"}
	if o.Backend != "cpu" {
		args = append(args, "-ngl", "999", "-fa", "on")
	}
	cmd := exec.Command(filepath.Join(dir, serverExe()), args...)
	cmd.Dir = dir
	if runtime.GOOS == "linux" {
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+dir+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	}
	hideWindow(cmd)
	stderr, err := os.Create(log)
	if err != nil {
		return nil, err
	}
	stdout, err := os.Create(log + ".out")
	if err != nil {
		stderr.Close()
		return nil, err
	}
	cmd.Stderr, cmd.Stdout = stderr, stdout
	if err := cmd.Start(); err != nil {
		stderr.Close()
		stdout.Close()
		return nil, err
	}
	s := &Server{Cmd: cmd, URL: fmt.Sprintf("http://127.0.0.1:%d", port), Model: filepath.Base(modelPath), Backend: o.Backend, Log: log, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		stderr.Close()
		stdout.Close()
		close(s.done)
	}()
	started := time.Now()
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Since(started) < o.Timeout {
		if exited, _ := s.Exited(); exited {
			return nil, fmt.Errorf("llama-server exited (code %d); see %s", cmd.ProcessState.ExitCode(), log)
		}
		if resp, err := client.Get(s.URL + "/health"); err == nil {
			var h struct{ Status string }
			json.NewDecoder(resp.Body).Decode(&h)
			resp.Body.Close()
			if h.Status == "ok" {
				s.LoadSeconds = float64(time.Since(started).Milliseconds()/100) / 10
				return s, nil
			}
		}
		select {
		case <-ctx.Done():
			s.Stop()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	s.Stop()
	return nil, fmt.Errorf("llama-server not ready after %.0f s; see %s", o.Timeout.Seconds(), log)
}

// ModelPath is a model given as a path, or a file in the assets folder.
func (rt *Runtime) ModelPath(model string) string {
	if st, err := os.Stat(model); err == nil && !st.IsDir() {
		p, _ := filepath.Abs(model)
		return p
	}
	return filepath.Join(rt.Assets, model)
}

// ModelSettings are the request settings a GGUF needs, from <file>.iz.json
// next to it ({"request": {...}}, merged into every chat request); nil if
// none. A fine-tune trained without reasoning carries
// {"request": {"chat_template_kwargs": {"enable_thinking": false}}}.
func ModelSettings(modelPath string) (json.RawMessage, error) {
	data, err := os.ReadFile(modelPath + ".iz.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct{ Request json.RawMessage }
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s.iz.json: %v", modelPath, err)
	}
	if string(f.Request) == "null" {
		return nil, nil
	}
	return f.Request, nil
}
