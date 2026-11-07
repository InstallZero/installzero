//go:build !windows

package runner

import "os/exec"

func hideWindow(*exec.Cmd) {}

func windowsRAM() uint64 { return 0 }
