//go:build !windows

package wsl

import "os/exec"

func hideWindow(*exec.Cmd) {}
