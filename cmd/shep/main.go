// Package main is the shep entry point. It only constructs the command tree
// and forwards the exit status to the OS; all behaviour lives in
// internal/command so the binary stays a thin shell.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/tranceh2/shep/internal/command"
)

// version and commit are injected via -ldflags by the Makefile, GoReleaser
// and the Herdr plugin build. `go install module@version` injects nothing;
// buildVersion then reads them from the build information Go records.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	app := command.New(command.WithVersion(buildVersion(version, commit, debug.ReadBuildInfo)))
	if err := app.Execute(); err != nil {
		os.Exit(handleAppError(err))
	}
}

// buildVersion fills a version or commit the linker left at its default from
// the build information: the module version of `go install module@vX.Y.Z`
// (without the "v", as release builds print it), and the VCS revision of a
// build inside a checkout.
func buildVersion(version, commit string, readBuildInfo func() (*debug.BuildInfo, bool)) (string, string) {
	info, ok := readBuildInfo()
	if !ok {
		return version, commit
	}
	if v := info.Main.Version; version == "dev" && v != "" && v != "(devel)" {
		version = strings.TrimPrefix(v, "v")
	}
	for _, setting := range info.Settings {
		if commit == "none" && setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
			commit = setting.Value[:7]
		}
	}
	return version, commit
}

func handleAppError(err error) int {
	return command.ExitCode(err)
}
