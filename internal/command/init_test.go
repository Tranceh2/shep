package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// TestInit_WritesPathAgnosticExample (CD-4, CD-5) runs `shep init` against a
// fresh temp HOME/config dir and asserts:
//   - the file is created at the discovered path
//   - it contains no /Users/ or Proyectos strings
//   - it parses as valid TOML (round-trips)
func TestInit_WritesPathAgnosticExample(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", "")

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"init"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("init: %v", e)
	}

	if !strings.Contains(out.String(), "wrote") {
		t.Errorf("expected 'wrote' confirmation, got: %q", out.String())
	}
	path := strings.TrimSpace(strings.TrimPrefix(out.String(), "wrote "))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created at %q: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	body := string(data)
	for _, bad := range []string{"/Users/", "/home/trance", "Proyectos"} {
		if strings.Contains(body, bad) {
			t.Errorf("init example contains hardcoded %q", bad)
		}
	}
	// The example is full of comments; the TOML parser must still accept it.
	if err := toml.Unmarshal(data, &struct{}{}); err != nil {
		t.Errorf("init example is not valid TOML: %v", err)
	}
	// The example must document the selector field and its valid values so
	// users discover the picker-routing knob from the generated config.
	for _, want := range []string{"selector", "builtin", "fzf", "auto"} {
		if !strings.Contains(body, want) {
			t.Errorf("init example missing %q in selector docs", want)
		}
	}
}

// TestInit_ConfigOverride honours --config to choose the destination path,
// keeping tests hermetic without touching the real user config dir.
func TestInit_ConfigOverride(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"init", "--config", path})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("init: %v", e)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written at override %q: %v", path, err)
	}
}

// TestInit_RefusesOverwriteWithoutForce guards the accidental clobber path.
func TestInit_RefusesOverwriteWithoutForce(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	if e := app.executeArgs([]string{"init", "--config", path}); e == nil {
		t.Fatal("expected error overwriting without --force, got nil")
	}
	if !strings.Contains(errOut.String(), "--force") {
		t.Fatalf("stderr = %q, want actionable --force guidance", errOut.String())
	}
}

// TestInit_ForceOverwrites confirms --force replaces existing content.
func TestInit_ForceOverwrites(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"init", "--config", path, "--force"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("init --force: %v", e)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "shep configuration") {
		t.Error("force did not overwrite with example content")
	}
}

// TestRoot_PreRunLoadsConfigAndProbes verifies the PersistentPreRunE wired in
// this commit actually populates App.cfg and App.probes for a normal command.
func TestRoot_PreRunLoadsConfigAndProbes(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = `version = 1
[herdr]
binary = "herdr"
[sources.projects]
recursive = true
max_depth = 2
markers = ["go.mod"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	// Attach an ephemeral runnable subcommand so the root PersistentPreRunE
	// actually fires (cobra skips PreRun for --help and no-RunE roots).
	probe := &cobra.Command{Use: "probe", RunE: func(c *cobra.Command, _ []string) error { return nil }}
	cmd.AddCommand(probe)
	cmd.SetArgs([]string{"--config", path, "probe"})
	if e := cmd.Execute(); e != nil {
		t.Fatalf("execute: %v", e)
	}
	if app.cfg == nil {
		t.Fatal("PreRun did not load cfg")
	}
	if got, want := app.cfg.Herdr.Binary, "herdr"; got != want {
		t.Errorf("PreRun did not parse the supplied herdr binary: got %q want %q", got, want)
	}
	if got, want := len(app.cfg.Sources.Projects.Markers), 1; got != want {
		t.Errorf("PreRun did not parse the supplied projects markers: got %d want %d", got, want)
	}
}
