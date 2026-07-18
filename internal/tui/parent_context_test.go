package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestRowDisplayText_TabRow_ShowsParentWorkspaceContext proves a RowTab whose
// candidate carries Meta["workspace_label"] shows a concise "in <workspace>"
// secondary — so a path/label match on a tab row explains WHERE it is open,
// instead of leaving the user to guess which workspace it belongs to.
func TestRowDisplayText_TabRow_ShowsParentWorkspaceContext(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind: RowTab,
		Candidate: source.Candidate{
			Label: "api", Path: "/srv/api",
			Meta: map[string]string{"workspace_label": "backend"},
		},
	}
	_, secondary := m.rowDisplayText(row)
	if !strings.Contains(secondary, "backend") {
		t.Errorf("tab row secondary = %q, want it to mention the parent workspace \"backend\"", secondary)
	}
}

// TestRowDisplayText_TabRow_NoWorkspaceLabelKeepsEmptySecondary triangulates:
// a RowTab candidate with no workspace_label (should not happen in practice
// for a synthesized row, but rowDisplayText must degrade gracefully) keeps
// the pre-existing empty secondary rather than showing "in ".
func TestRowDisplayText_TabRow_NoWorkspaceLabelKeepsEmptySecondary(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowTab, Candidate: source.Candidate{Label: "api", Path: "/srv/api"}}
	_, secondary := m.rowDisplayText(row)
	if secondary != "" {
		t.Errorf("tab row secondary with no workspace_label = %q, want empty", secondary)
	}
}

// TestRowDisplayText_PaneRow_ShowsParentWorkspaceContext proves a RowPane
// still shows its own pane id AND the parent workspace context together,
// concisely, when Meta["workspace_label"] is set.
func TestRowDisplayText_PaneRow_ShowsParentWorkspaceContext(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api",
			Meta: map[string]string{"workspace_label": "backend"},
		},
	}
	_, secondary := m.rowDisplayText(row)
	if !strings.Contains(secondary, "p1") {
		t.Errorf("pane row secondary = %q, want it to still contain the pane id \"p1\"", secondary)
	}
	if !strings.Contains(secondary, "backend") {
		t.Errorf("pane row secondary = %q, want it to mention the parent workspace \"backend\"", secondary)
	}
}

// TestRowDisplayText_PaneRow_NoWorkspaceLabelKeepsBarePaneID triangulates the
// pane case: no workspace_label keeps the exact pre-existing "<pane id>"
// secondary (see TestRowDisplayText_PaneRow_PathPrimarySecondaryPaneID in
// corrective_test.go, which this must not regress).
func TestRowDisplayText_PaneRow_NoWorkspaceLabelKeepsBarePaneID(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowPane, Candidate: source.Candidate{Label: "p1", Path: "/srv/api"}}
	_, secondary := m.rowDisplayText(row)
	if secondary != "p1" {
		t.Errorf("pane row secondary with no workspace_label = %q, want the bare pane id \"p1\"", secondary)
	}
}
