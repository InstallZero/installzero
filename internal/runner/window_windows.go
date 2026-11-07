package runner

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func windowsRAM() uint64 {
	var m struct {
		Length, MemoryLoad                        uint32
		TotalPhys, AvailPhys, TotalPageFile       uint64
		AvailPageFile, TotalVirtual, AvailVirtual uint64
		AvailExtendedVirtual                      uint64
	}
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return m.TotalPhys
}
