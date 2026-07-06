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
// returning captured stdout/stderr.
func runDoctor(t *testing.T, cfg *config.Config) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.probes = config.Probes{}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"doctor"})
	err := cmd.Execute()
	return out.String(), errOut.String(), err
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
		{Name: "group", Type: config.WorkspaceTypeGroup, Path: missing, Sources: []string{config.SourceProjects}},
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
