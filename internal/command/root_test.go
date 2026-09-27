package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
)

// TestApp_HerdrEnvEnablesProbeAndDriver verifies plugin-style startup with a
// minimal PATH: the valid absolute env binary gates Herdr and is used by the
// lazily-created driver.
func TestApp_HerdrEnvEnablesProbeAndDriver(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(binary, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", binary)
	app := New()
	app.cfg = config.Defaults()
	app.probes = config.ProbesFor(app.cfg)
	if !app.probes.Herdr {
		t.Fatal("valid HERDR_BIN_PATH should enable Herdr")
	}
	if app.Driver() == nil {
		t.Fatal("Driver should be available with valid HERDR_BIN_PATH")
	}
}

// TestApp_HelpOutput verifies the skeleton root renders its Long description
// and exits cleanly with --help. Each case rebuilds the tree via New so flag
// state does not leak between assertions.
func TestApp_HelpOutput(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"--help"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("expected nil error for --help, got %v", e)
	}

	help := out.String()
	const long = "shep enumerates project workspaces from Herdr, predefined workspaces,"
	if !strings.Contains(help, long) {
		t.Errorf("help output missing long description\ngot:\n%s", help)
	}
}

// TestApp_PersistentPreRunE_PrintsConfigLoadError (regression): a config that
// fails to Load (e.g. a strict-validation rejection) must print the failure
// to stderr before exiting 1. root has SilenceErrors:true so cobra itself
// never prints PersistentPreRunE's returned error, and main.go's
// `if err := app.Execute(); err != nil { os.Exit(1) }` never prints anything
// either — every subcommand body is expected to print its own message before
// returning errExitOne, but PersistentPreRunE runs before any subcommand
// body even starts. Before this fix, an invalid --config silently exited 1
// with zero output on either stream, which is indistinguishable from the
// process never having started at all.
func TestApp_PersistentPreRunE_PrintsConfigLoadError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badConfig := filepath.Join(dir, "config.toml")
	// close_on_exit on a leaf node with an empty command can never trigger,
	// so config.Load rejects it at parse time (any other Load-time rejection
	// would exercise the same PersistentPreRunE path just as well).
	const contents = `version = 2
[templates.dev]
description = "test"
[[templates.dev.tabs]]
name = "code"
root = "main"
  [[templates.dev.tabs.nodes]]
  id = "main"
  command = ""
  close_on_exit = true
`
	if err := os.WriteFile(badConfig, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"--config", badConfig, "list"})

	if e := cmd.Execute(); e == nil {
		t.Fatal("expected a non-nil error from a rejected config, got nil")
	}
	if errOut.Len() == 0 {
		t.Fatal("expected the config load failure to be printed to stderr, got nothing")
	}
	if !strings.Contains(errOut.String(), "close_on_exit") {
		t.Errorf("stderr should name the actual validation failure, got: %q", errOut.String())
	}
}

// TestApp_ConfigFlagRegistered confirms the --config persistent flag is wired
// on the root, independent of help rendering (a non-runnable root skips the
// Flags section of the help template).
func TestApp_ConfigFlagRegistered(t *testing.T) {
	t.Parallel()

	app := New()
	cmd := app.rootCmd()
	if f := cmd.PersistentFlags().Lookup("config"); f == nil {
		t.Fatal("expected persistent --config flag on root command")
	}
}

// TestApp_ConfigFlagHelpMatchesDiscoveryOrder (requirement: public docs/help
// must match the actual config discovery order). DiscoverPath prefers
// $XDG_CONFIG_HOME (or ~/.config when that is unset) on every platform,
// including macOS; os.UserConfigDir is only a last-resort fallback when no
// home directory can be resolved. The --config default description must reflect
// that order so macOS users are not misled into ~/Library/Application Support.
func TestApp_ConfigFlagHelpMatchesDiscoveryOrder(t *testing.T) {
	t.Parallel()

	app := New()
	cmd := app.rootCmd()
	f := cmd.PersistentFlags().Lookup("config")
	if f == nil {
		t.Fatal("expected persistent --config flag on root command")
	}
	usage := f.Usage
	if !strings.Contains(usage, "$XDG_CONFIG_HOME") {
		t.Errorf("--config usage %q must document the $XDG_CONFIG_HOME discovery order", usage)
	}
	if strings.Contains(usage, "os.UserConfigDir") {
		t.Errorf("--config usage %q must not name os.UserConfigDir (XDG ~/.config is primary on every platform)", usage)
	}
}

// TestApp_VersionFlag confirms the version flag is wired when metadata is
// injected, guarding the WithVersion option against future regressions.
func TestApp_VersionFlag(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	app := New(WithVersion("test", "abc123"), WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"--version"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("expected nil error for --version, got %v", e)
	}
	if !strings.Contains(out.String(), "test (commit: abc123)") {
		t.Errorf("expected version output, got:\n%s", out.String())
	}
}

// TestApp_ExecuteStreamsNotPanic ensures rootCmd() is safe to call repeatedly
// and produces an *App with non-nil cobra.Command expectations.
func TestApp_ExecuteRootStable(t *testing.T) {
	t.Parallel()

	app := New()
	c := app.rootCmd()
	if c == nil {
		t.Fatal("rootCmd returned nil")
	}
	if c.Use != "shep" {
		t.Errorf("expected root Use=%q, got %q", "shep", c.Use)
	}
}

// TestApp_HelpListsPreviewCommand (WP-4, task 4.4) confirms `shep preview` is
// registered on the root command tree and shows up in --help.
func TestApp_HelpListsPreviewCommand(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"--help"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("expected nil error for --help, got %v", e)
	}
	if !strings.Contains(out.String(), "preview") {
		t.Errorf("help output missing 'preview' subcommand\ngot:\n%s", out.String())
	}
}
