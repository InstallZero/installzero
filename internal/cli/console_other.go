//go:build !windows

package cli

func ownConsole() bool { return false }
