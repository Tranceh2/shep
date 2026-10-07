package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
)

// runDoctor executes `shep doctor` against a fresh App with cfg pre-set,
// returning captured stdout/stderr. The theme report reads no real
// environment: NO_COLOR and SHEP_THEME are unset and Herdr's configuration is
// a path that does not exist.
func runDoctor(t *testing.T, cfg *config.Config) (string, string, error) {
	t.Helper()
	return runDoctorWithEnv(t, cfg, map[string]string{"HERDR_CONFIG_PATH": filepath.Join(t.TempDir(), "herdr", "config.toml")})
}

// runDoctorWithEnv is runDoctor with the theme environment env (NO_COLOR,
// SHEP_THEME, HERDR_CONFIG_PATH...).
func runDoctorWithEnv(t *testing.T, cfg *config.Config, env map[string]string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.probes = config.Probes{}
	app.themeGetenv = func(k string) string { return env[k] }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"doctor"})
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestDoctor_ReportsTheThemeAndItsSource proves doctor names the theme the
// picker uses and where it came from: inherited from Herdr by default (with
// the Herdr file consulted and Herdr's own diagnostics as notes), a built-in
// selected by [tui].theme, the no-color theme forced by NO_COLOR, and an
// ignored SHEP_THEME as a note.
func TestDoctor_ReportsTheThemeAndItsSource(t *testing.T) {
	t.Parallel()
	herdr := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(herdr, []byte("[theme]\nname = \"nord\"\ndark_name = \"nope\"\n[theme.custom]\naccent = \"#12\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent.toml")
	dracula := config.Defaults()
	dracula.TUI.Theme = "dracula"
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		env  map[string]string
		want []string
		not  []string
	}{
		{
			name: "inherit with Herdr diagnostics",
			cfg:  config.Defaults(),
			env:  map[string]string{"HERDR_CONFIG_PATH": herdr},
			want: []string{
				"Theme: inherit\n",
				"  source: inherit:nord (default)\n",
				"  herdr config: " + herdr + "\n",
				`  note: unknown theme name theme.dark_name = "nope"`,
				`  note: theme.custom.accent: invalid color "#12"`,
				"Herdr uses cyan",
			},
		},
		{
			name: "inherit without a Herdr configuration",
			cfg:  config.Defaults(),
			env:  map[string]string{"HERDR_CONFIG_PATH": missing},
			want: []string{"  source: inherit:catppuccin (default)\n", "  note: Herdr configuration " + missing + " not found; using Herdr's default theme catppuccin\n"},
		},
		{
			name: "built-in from tui.theme",
			cfg:  dracula,
			env:  map[string]string{"HERDR_CONFIG_PATH": herdr},
			want: []string{"Theme: dracula\n", "  source: builtin (tui.theme)\n"},
			not:  []string{"herdr config:", "note:"},
		},
		{
			name: "NO_COLOR",
			cfg:  dracula,
			env:  map[string]string{"NO_COLOR": "1", "HERDR_CONFIG_PATH": herdr},
			want: []string{"Theme: plain\n", "  source: no-color (NO_COLOR)\n"},
		},
		{
			name: "ignored SHEP_THEME",
			cfg:  dracula,
			env:  map[string]string{"SHEP_THEME": "nope", "HERDR_CONFIG_PATH": herdr},
			want: []string{"Theme: dracula\n", `  note: SHEP_THEME="nope" ignored: unknown theme "nope"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, _, err := runDoctorWithEnv(t, tc.cfg, tc.env)
			if err != nil {
				t.Fatalf("doctor: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("doctor output missing %q:\n%s", want, out)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(out, not) {
					t.Errorf("doctor output must not contain %q:\n%s", not, out)
				}
			}
		})
	}
}

// TestDoctor_ReportsMissingWorkspacePath (requirement 11) confirms a
// configured workspace whose path does not exist on disk is reported as a
// warning, and doctor still exits 0 (a warning, not a hard failure).
func TestDoctor_ReportsMissingWorkspacePath(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ghost", Path: missing}}
	out, errOut, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	combined := out + errOut
	if !strings.Contains(combined, "ghost") || !strings.Contains(combined, "missing") {
		t.Errorf("expected a missing-path warning naming 'ghost', got stdout=%q stderr=%q", out, errOut)
	}
}

// TestDoctor_HealthyWorkspaceReportsOK confirms an existing workspace path
// is reported as healthy, not as a warning.
func TestDoctor_HealthyWorkspaceReportsOK(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	dir := t.TempDir()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ok", Path: dir}}
	out, _, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected healthy workspace 'ok' reported, got %q", out)
	}
}

// TestDoctor_NoWorkspacesConfigured confirms doctor degrades gracefully with
// no [[workspaces]] entries at all.
func TestDoctor_NoWorkspacesConfigured(t *testing.T) {
	t.Parallel()
	out, _, err := runDoctor(t, config.Defaults())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("expected some doctor output even with no workspaces configured")
	}
}

// TestDoctor_GroupWorkspacePathChecked confirms a type=group workspace's own
// path is also checked for existence.
func TestDoctor_GroupWorkspacePathChecked(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	missing := filepath.Join(t.TempDir(), "ghost-group")
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "group", Type: config.WorkspaceTypeGroup, Path: missing, SourceOrder: []string{config.SourceProjects}},
	}
	out, errOut, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	combined := out + errOut
	if !strings.Contains(combined, "group") || !strings.Contains(combined, "missing") {
		t.Errorf("expected missing-path warning for group workspace, got stdout=%q stderr=%q", out, errOut)
	}
}

// TestDoctor_ExpandsTildeInWorkspacePath confirms a "~/..." workspace path is
// tilde-expanded before the existence check (regression for the old local
// expandTildeDoctor, which used home+p[1:] instead of filepath.Join and could
// emit a malformed path). Cannot run t.Parallel because it mutates HOME.
func TestDoctor_ExpandsTildeInWorkspacePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	existing := filepath.Join(home, "real-project")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "real", Path: "~/real-project"},
		{Name: "ghost", Path: "~/no-such-project"},
	}
	out, _, err := runDoctor(t, cfg)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// The expanded path (home-prefixed, no stray "~/") must appear, proving
	// pathutil.ExpandTilde is wired in and joins separators correctly.
	if !strings.Contains(out, existing) {
		t.Errorf("expected expanded path %q in output, got %q", existing, out)
	}
	if !strings.Contains(out, "OK      real") {
		t.Errorf("expected real project reported OK, got %q", out)
	}
	if !strings.Contains(out, "MISSING ghost") {
		t.Errorf("expected ghost project reported MISSING, got %q", out)
	}
	// The raw "~/" shorthand must NOT leak into output: a stale un-expanded
	// form would mean expansion was skipped entirely.
	if strings.Contains(out, "~/") {
		t.Errorf("raw ~/ shorthand leaked into doctor output: %q", out)
	}
}
