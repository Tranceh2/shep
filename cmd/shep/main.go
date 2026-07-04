// Package main is the shep entry point. It only constructs the command tree
// and forwards the exit status to the OS; all behaviour lives in
// internal/command so the binary stays a thin shell.
package main

import (
	"os"

	"github.com/tranceh2/shep/internal/command"
)

// version and commit are injected via -ldflags from the Makefile.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	app := command.New(command.WithVersion(version, commit))
	if err := app.Execute(); err != nil {
		os.Exit(1)
	}
}
