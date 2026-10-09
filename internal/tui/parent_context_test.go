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
// an empty secondary rather than showing "in ".
func TestRowDisplayText_TabRow_NoWorkspaceLabelKeepsEmptySecondary(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowTab, Candidate: source.Candidate{Label: "api", Path: "/srv/api"}}
	_, secondary := m.rowDisplayText(row)
	if secondary != "" {
		t.Errorf("tab row secondary with no workspace_label = %q, want empty", secondary)
	}
}

// TestRowDisplayText_PaneRow_OmitsParentWorkspaceContext proves a RowPane
// names itself by its pane id in the PRIMARY text and never shows the parent
// workspace context anywhere: a RowPane has no secondary text, even when its
// candidate carries a workspace_label.
func TestRowDisplayText_PaneRow_OmitsParentWorkspaceContext(t *testing.T) {
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
		t.Errorf("pane row secondary = %q, want empty (a RowPane has no secondary text)", secondary)
	}
}

// TestRowDisplayText_PaneRow_NoWorkspaceLabelKeepsBarePaneID triangulates the
// pane case: with no workspace_label the bare pane id appears in the
// PRIMARY text, and RowPane's secondary stays empty.
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
