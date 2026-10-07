package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
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

// === SPEC-NAV-1: Row-kind explicit effective Enter hint (descriptor) ===

// TestRowActionDescriptor_TruthfulPerRowKind proves rowActionDescriptor
// derives a truthful, row-kind-explicit Enter label and matching Action for
// every row kind (SPEC-NAV-1.1–1.6). The label must never promise "create" or
// "focus existing" for a top-level candidate, must read "focus tab" for a
// synthesized tab row (matching driver.FocusTab), and must describe focusing
// the CONTAINING tab for a pane row — never per-pane focus.
func TestRowActionDescriptor_TruthfulPerRowKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		row        Row
		wantAction RowAction
		// wantFooterExact asserts the footer label verbatim when non-empty.
		wantFooterExact string
		// mustNotContain lists substrings the label MUST NOT contain.
		mustNotContain []string
	}{
		{
			name:            "SPEC-NAV-1.1 herdr workspace row",
			row:             Row{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w1")},
			wantAction:      RowActionOpen,
			wantFooterExact: "open",
			mustNotContain:  []string{"create", "focus existing"},
		},
		{
			name:           "SPEC-NAV-1.2 session row",
			row:            Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "alpha", Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}}},
			wantAction:     RowActionOpen,
			mustNotContain: []string{"create", "focus tab"},
		},
		{
			name:            "SPEC-NAV-1.3 zoxide path row",
			row:             Row{Kind: RowCandidate, Candidate: zoxideCandidate("Downloads", "/home/dev/Downloads")},
			wantAction:      RowActionOpen,
			wantFooterExact: "open",
			mustNotContain:  []string{"create", "focus existing"},
		},
		{
			name:            "SPEC-NAV-1.5 synthesized tab row reads focus tab",
			row:             Row{Kind: RowTab, Action: RowActionFocusTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}},
			wantAction:      RowActionFocusTab,
			wantFooterExact: "focus tab",
		},
		{
			name:       "SPEC-NAV-1.6 synthesized pane row focuses containing tab",
			row:        Row{Kind: RowPane, Action: RowActionFocusTab, Candidate: source.Candidate{Label: "p1", Meta: map[string]string{"pane_id": "p1", "tab_id": "t1"}}},
			wantAction: RowActionFocusTab,
			// Must not promise per-pane focus; "focus pane" is the false claim
			// SPEC-NAV-1.6 forbids (the word "pane" alone is fine — a pane row
			// truthfully mentions the pane it belongs to).
			mustNotContain: []string{"focus pane"},
		},
		{
			name:            "agents candidate row focuses containing tab",
			row:             Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "pi", Source: config.SourceAgents, Meta: map[string]string{"pane_id": "p1", "tab_id": "t1"}}},
			wantAction:      RowActionFocusTab,
			wantFooterExact: "focus",
			mustNotContain:  []string{"focus pane", "create"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := rowActionDescriptor(tc.row)
			if d.Action != tc.wantAction {
				t.Errorf("descriptor Action = %v, want %v", d.Action, tc.wantAction)
			}
			if tc.wantFooterExact != "" && d.FooterLabel != tc.wantFooterExact {
				t.Errorf("descriptor FooterLabel = %q, want %q", d.FooterLabel, tc.wantFooterExact)
			}
			if d.FooterLabel == "" {
				t.Errorf("descriptor FooterLabel must be non-empty for a highlighted row (%s)", tc.name)
			}
			if d.HelpText == "" {
				t.Errorf("descriptor HelpText must be non-empty for a highlighted row (%s)", tc.name)
			}
			for _, banned := range tc.mustNotContain {
				if strings.Contains(strings.ToLower(d.FooterLabel), strings.ToLower(banned)) {
					t.Errorf("descriptor FooterLabel %q must not contain %q", d.FooterLabel, banned)
				}
				if strings.Contains(strings.ToLower(d.HelpText), strings.ToLower(banned)) {
					t.Errorf("descriptor HelpText %q must not contain %q", d.HelpText, banned)
				}
			}
		})
	}
}

// TestRowActionDescriptor_PaneRowDescribesContainingTab proves SPEC-NAV-1.6
// precisely: a pane row's label describes focusing the tab that contains the
// pane, so the copy is truthful about Herdr having no per-pane focus command.
func TestRowActionDescriptor_PaneRowDescribesContainingTab(t *testing.T) {
	t.Parallel()
	pane := Row{Kind: RowPane, Action: RowActionFocusTab, Candidate: source.Candidate{Label: "p1", Meta: map[string]string{"pane_id": "p1", "tab_id": "t1"}}}
	d := rowActionDescriptor(pane)
	if !strings.Contains(strings.ToLower(d.FooterLabel), "tab") && !strings.Contains(strings.ToLower(d.HelpText), "tab") {
		t.Errorf("pane row copy must mention focusing the containing tab; got footer=%q help=%q", d.FooterLabel, d.HelpText)
	}
	// It must NOT claim to focus the pane itself.
	if strings.Contains(strings.ToLower(d.HelpText), "focus pane") || strings.Contains(strings.ToLower(d.FooterLabel), "focus pane") {
		t.Errorf("pane row copy must not promise per-pane focus; got footer=%q help=%q", d.FooterLabel, d.HelpText)
	}
}

// TestHandleEnter_SourcesActionFromDescriptorNotRowField proves SPEC-NAV-1.8's
// core guarantee: handleEnter derives selectedAction from the SHARED
// rowActionDescriptor — the same source footer/help read — and NOT from the
// row's raw Action field. On every row buildRows produces the two agree, so
// this uses a SYNTHETIC row with deliberate drift (row.Action set to a value
// that disagrees with the descriptor's Action for that kind) to prove which
// source handleEnter actually consumes. With the descriptor as source of
// truth, the drifted row.Action is ignored; a regression that reads row.Action
// directly would leak the drifted value and fail this test.
func TestHandleEnter_SourcesActionFromDescriptorNotRowField(t *testing.T) {
	t.Parallel()

	// A synthesized tab row: the descriptor unconditionally resolves RowTab ->
	// RowActionFocusTab. We plant RowActionOpen in the raw field as drift.
	tabRow := Row{Kind: RowTab, Action: RowActionOpen, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}
	if want := rowActionDescriptor(tabRow).Action; want == tabRow.Action {
		t.Fatalf("setup: drift row must disagree with descriptor; row.Action=%v descriptor=%v", tabRow.Action, want)
	}
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.rows = []Row{tabRow}
	m.cursor = 0
	after, _ := m.handleEnter()
	got := after.selectedAction
	if want := rowActionDescriptor(tabRow).Action; got != want {
		t.Errorf("handleEnter selectedAction = %v, want descriptor Action %v (must source from descriptor, not row.Action=%v)", got, want, tabRow.Action)
	}
	if got == tabRow.Action {
		t.Errorf("handleEnter leaked the drifted row.Action %v — it must ignore row.Action and use the descriptor", tabRow.Action)
	}

	// Triangulate the opposite drift direction: a candidate row (descriptor ->
	// RowActionOpen) with RowActionFocusTab planted as raw drift.
	candRow := Row{Kind: RowCandidate, Action: RowActionFocusTab, Candidate: zoxideCandidate("proj", "/proj")}
	if want := rowActionDescriptor(candRow).Action; want == candRow.Action {
		t.Fatalf("setup: candidate drift row must disagree with descriptor; row.Action=%v descriptor=%v", candRow.Action, want)
	}
	m2 := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m2.rows = []Row{candRow}
	m2.cursor = 0
	after2, _ := m2.handleEnter()
	got2 := after2.selectedAction
	if want := rowActionDescriptor(candRow).Action; got2 != want {
		t.Errorf("handleEnter selectedAction = %v, want descriptor Action %v (candidate drift)", got2, want)
	}
	if got2 == candRow.Action {
		t.Errorf("handleEnter leaked the drifted candidate row.Action %v", candRow.Action)
	}
}

// TestRowActionDescriptor_ParityWithHandleEnter proves SPEC-NAV-1.8: the
// Action the descriptor reports for a row equals the Action handleEnter sets
// as selectedAction for that same highlighted row. Footer, help, and Enter
// therefore all resolve from one source.
func TestRowActionDescriptor_ParityWithHandleEnter(t *testing.T) {
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

	for i := range m.rows {
		m.cursor = i
		row, ok := m.currentRow()
		if !ok {
			t.Fatalf("row %d: currentRow ok=false", i)
		}
		d := rowActionDescriptor(row)
		after, _ := m.handleEnter()
		got := after.selectedAction
		if got != d.Action {
			t.Errorf("row %d (kind=%v): handleEnter action = %v, descriptor Action = %v (must match)", i, row.Kind, got, d.Action)
		}
	}
}
