package command

import (
	"bytes"
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
