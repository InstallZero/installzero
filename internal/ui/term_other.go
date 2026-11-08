//go:build !windows

package ui

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// IsConsoleInput reports whether f is a keyboard someone can answer from.
func IsConsoleInput(f *os.File) bool { return isTerminal(f) }

// WithoutEcho runs read with the terminal not showing what is typed.
func WithoutEcho(f *os.File, read func()) {
	stty := func(arg string) error {
		c := exec.Command("stty", arg)
		c.Stdin = f
		return c.Run()
	}
	if stty("-echo") == nil {
		defer stty("echo")
	}
	read()
}

func termWidth(f *os.File) int {
	var ws struct{ rows, cols, x, y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); e == 0 && ws.cols > 0 {
		return int(ws.cols)
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 80
}
