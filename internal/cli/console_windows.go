package cli

import (
	"syscall"
	"unsafe"
)

var getConsoleProcessList = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// ownConsole reports whether this process is alone on its console, as when
// it is opened from Explorer: the window closes as soon as it exits.
func ownConsole() bool {
	var ids [2]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), 2)
	return n == 1
}
