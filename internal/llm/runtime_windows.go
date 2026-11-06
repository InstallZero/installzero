package llm

import (
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"unsafe"
)

// TotalRAMMB is the machine's physical memory in MB.
func TotalRAMMB() int {
	var m struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys, AvailPhys uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	m.Length = uint32(unsafe.Sizeof(m))
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0
	}
	return int(m.TotalPhys / (1 << 20))
}

var portRange = regexp.MustCompile(`(?m)^\s*(\d+)\s+(\d+)`)

// excludedPorts are the TCP port ranges Windows reserves (Hyper-V, WSL).
func excludedPorts() [][2]int {
	cmd := exec.Command("netsh", "interface", "ipv4", "show", "excludedportrange", "protocol=tcp")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var r [][2]int
	for _, m := range portRange.FindAllStringSubmatch(string(out), -1) {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		r = append(r, [2]int{a, b})
	}
	return r
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
