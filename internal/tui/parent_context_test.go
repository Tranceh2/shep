package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestRowDisplayText_TabRow_OmitsParentWorkspaceContext proves a RowTab does
// not render a duplicated trailing "in <workspace>" secondary.
func TestRowDisplayText_TabRow_OmitsParentWorkspaceContext(t *testing.T) {
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
	if secondary != "" {
		t.Errorf("tab row secondary = %q, want empty (no trailing parent workspace context)", secondary)
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

// TestRowDisplayText_PaneRow_ShowsParentWorkspaceContext previously proved a
// RowPane's SECONDARY carried both its pane id and the parent workspace
// context. Change 2 (unified "<label> · <path>" primary text) removes a
// RowPane's secondary entirely and folds its pane id into the primary
// instead — the parent workspace context is intentionally dropped for panes
// (see rowSecondaryText's doc comment), not moved elsewhere. This test now
// proves that removal directly: the pane id still appears, but in the
// PRIMARY text, and the secondary is always empty.
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
	primary, secondary := m.rowDisplayText(row)
	if !strings.Contains(primary, "p1") {
		t.Errorf("pane row primary = %q, want it to contain the pane id \"p1\"", primary)
	}
	if secondary != "" {
		t.Errorf("pane row secondary = %q, want empty (RowPane secondary removed entirely by Change 2)", secondary)
	}
}

// TestRowDisplayText_PaneRow_NoWorkspaceLabelKeepsBarePaneID triangulates the
// pane case: no workspace_label still shows the bare pane id, now in the
// PRIMARY text (RowPane's secondary is always empty since Change 2).
func TestRowDisplayText_PaneRow_NoWorkspaceLabelKeepsBarePaneID(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowPane, Candidate: source.Candidate{Label: "p1", Path: "/srv/api"}}
	primary, secondary := m.rowDisplayText(row)
	if !strings.Contains(primary, "p1") {
		t.Errorf("pane row primary = %q, want it to contain the bare pane id \"p1\"", primary)
	}
	if secondary != "" {
		t.Errorf("pane row secondary with no workspace_label = %q, want empty", secondary)
	}
}
