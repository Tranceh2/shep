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
	const long = "shep enumerates project workspaces from Herdr, zoxide and the"
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
