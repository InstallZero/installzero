package ui

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode             = kernel32.NewProc("GetConsoleMode")
	setConsoleMode             = kernel32.NewProc("SetConsoleMode")
	getConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const enableVirtualTerminalProcessing = 0x0004

// isTerminal reports whether f is a console that understands escape
// sequences, turning them on (Windows 10 and later) when needed.
func isTerminal(f *os.File) bool {
	var mode uint32
	if r, _, _ := getConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := setConsoleMode.Call(f.Fd(), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}

func termWidth(f *os.File) int {
	var info struct {
		size, cursor        [2]int16
		attributes          uint16
		left, top, right, _ int16
		maxSize             [2]int16
	}
	if r, _, _ := getConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 80
	}
	if w := int(info.right-info.left) + 1; w > 0 {
		return w
	}
	return 80
}
