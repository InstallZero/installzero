package runner

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
)

// LocalMachine profiles this machine for choosing install.md blocks.
// Free disk space is left unknown, which does not constrain.
func LocalMachine(ctx context.Context) installmd.Machine {
	m := installmd.Machine{OS: runtime.GOOS, Arch: runtime.GOARCH, GPU: "none"}
	switch m.OS {
	case "darwin":
		m.OS = "macos"
	}
	switch m.Arch {
	case "amd64":
		m.Arch = "x64"
	}
	m.RAMGB = float64(ramBytes()) / (1 << 30)
	m.RAMGB = float64(int(m.RAMGB + 0.5))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader").Output(); err == nil {
		var names []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			name, drv, _ := strings.Cut(l, ",")
			names = append(names, strings.TrimSpace(name))
			if m.Driver == "" {
				m.Driver = strings.TrimSpace(drv)
			}
		}
		m.GPU, m.GPUs = "nvidia", strings.Join(names, ",")
	} else if m.OS == "macos" && m.Arch == "arm64" {
		m.GPU = "apple"
	}
	return m
}

func ramBytes() uint64 {
	switch runtime.GOOS {
	case "windows":
		return windowsRAM()
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
			return n
		}
	default:
		b, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
					n, _ := strconv.ParseUint(f[1], 10, 64)
					return n * 1024
				}
			}
		}
	}
	return 0
}
