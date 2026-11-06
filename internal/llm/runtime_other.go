//go:build !windows

package llm

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// TotalRAMMB is the machine's physical memory in MB.
func TotalRAMMB() int {
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.Atoi(f[1])
				return kb / 1024
			}
		}
	}
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		b, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return int(b >> 20)
	}
	return 0
}

func excludedPorts() [][2]int { return nil }

func hideWindow(*exec.Cmd) {}
