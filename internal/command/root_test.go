package command

import (
	"bytes"
	"strings"
	"testing"
)

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
