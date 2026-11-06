package main

import (
	"os"

	"github.com/InstallZero/installzero/internal/cli"
)

func main() {
	cli.SetName(os.Args[0])
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
