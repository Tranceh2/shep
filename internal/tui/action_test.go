package tui

import (
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestRowAction_SynthesizedTabPaneRowsAreFocusTab proves the typed RowAction
// model: a synthesized RowTab/RowPane carries RowActionFocusTab (Enter focuses
// its containing tab), while every RowCandidate carries the zero-value
// RowActionOpen (the normal launch path). The launch decision no longer
// depends on the candidate's Source string.
func TestRowAction_SynthesizedTabPaneRowsAreFocusTab(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := treeFromFake(driver)
	m := NewModelWithTree(cands, nil, tree, Layout{})
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()

	var sawCandidate, sawTab, sawPane bool
	for _, r := range m.rows {
		switch r.Kind {
		case RowCandidate:
			sawCandidate = true
			if r.Action != RowActionOpen {
				t.Errorf("candidate row Action = %v, want RowActionOpen", r.Action)
			}
		case RowTab:
			sawTab = true
			if r.Action != RowActionFocusTab {
				t.Errorf("tab row Action = %v, want RowActionFocusTab", r.Action)
			}
		case RowPane:
			sawPane = true
			if r.Action != RowActionFocusTab {
				t.Errorf("pane row Action = %v, want RowActionFocusTab", r.Action)
			}
		}
	}
	if !sawCandidate || !sawTab || !sawPane {
		t.Fatalf("expected candidate+tab+pane rows present; got candidate=%v tab=%v pane=%v (rows=%d)",
			sawCandidate, sawTab, sawPane, len(m.rows))
	}
}

// TestRowAction_FinalizeRunCarriesAction proves finalizeRun (the unit-testable
// seam under Run/RunWithTree) threads the selected row's Action into the run's
// return value, and collapses a cancellation to RowActionOpen.
func TestRowAction_FinalizeRunCarriesAction(t *testing.T) {
	t.Parallel()
	sel := mWithSelectedAction(RowActionFocusTab)
	cand, action, _, ok, err := finalizeRun(sel)
	if !ok || err != nil {
		t.Fatalf("finalizeRun selected: ok=%v err=%v", ok, err)
	}
	if action != RowActionFocusTab {
		t.Errorf("finalizeRun action = %v, want RowActionFocusTab", action)
	}
	if cand.Path != "/svc/api" {
		t.Errorf("finalizeRun candidate Path = %q, want /svc/api", cand.Path)
	}

	cancelled := Model{cancelled: true}
	_, action2, _, ok2, err2 := finalizeRun(cancelled)
	if ok2 || err2 == nil {
		t.Fatalf("finalizeRun cancelled: ok=%v want false, err=%v want non-nil", ok2, err2)
	}
	if action2 != RowActionOpen {
		t.Errorf("finalizeRun cancelled action = %v, want RowActionOpen (zero)", action2)
	}
}

// mWithSelectedAction builds a Model whose Selected()/SelectedAction() report a
// successful pick with the given action, ready for finalizeRun.
func mWithSelectedAction(action RowAction) Model {
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.selected = source.Candidate{Path: "/svc/api", Label: "api", Source: "herdr"}
	m.hasSelected = true
	m.selectedAction = action
	return m
}

// reflects the Action of whatever row Enter was pressed on: RowActionFocusTab
// for a pane row, RowActionOpen for a top-level candidate row.
func TestRowAction_SelectedActionTracksCurrentRow(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()

	// Walk the cursor down onto the synthesized pane row, then Enter.
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatalf("setup: cursor never reached a RowPane")
	}
	m, _ = update(t, m, key("enter"))
	if got := m.SelectedAction(); got != RowActionFocusTab {
		t.Errorf("SelectedAction after Enter on pane = %v, want RowActionFocusTab", got)
	}

	// Triangulate: Enter on the top-level candidate row yields RowActionOpen.
	m2 := NewModelWithTree(base, nil, tree, Layout{})
	m2, _ = update(t, m2, sizeMsg(120, 36))
	m2.cursor = 0
	m2, _ = update(t, m2, key("enter"))
	if got := m2.SelectedAction(); got != RowActionOpen {
		t.Errorf("SelectedAction after Enter on candidate = %v, want RowActionOpen", got)
	}
}
