package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// treeTestBaseCandidates builds a small flat candidate set: one zoxide entry
// (never expands) and one SourceHerdr workspace parent (workspace_id "w1")
// whose tabs/panes are supplied by the fakeTreeDriver a given test wires in.
func treeTestBaseCandidates() []source.Candidate {
	return []source.Candidate{
		{Path: "/zx", NormalizedPath: "/zx", Label: "zxproj", Source: config.SourceZoxide},
		{Path: "/svc", NormalizedPath: "/svc", Label: "backend", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}},
	}
}

// TestModelTree_EmptyQueryKeepsFlatRowsAndDoesNotFetch (5.1/R1): with an
// empty query — both at construction and after an explicit re-filter — the
// tree-wired model shows exactly the flat baseCandidates rows and never
// calls the driver.
func TestModelTree_EmptyQueryKeepsFlatRowsAndDoesNotFetch(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	base := treeTestBaseCandidates()
	m := NewModelWithTree(base, nil, tree, Layout{})

	if len(m.filtered) != 2 || len(m.candidates) != 2 {
		t.Fatalf("expected 2 flat rows on construction, got filtered=%d candidates=%d", len(m.filtered), len(m.candidates))
	}
	if driver.listTabsN != 0 || driver.listPanesN != 0 {
		t.Errorf("construction with empty query must not fetch; listTabsN=%d listPanesN=%d", driver.listTabsN, driver.listPanesN)
	}

	// Re-apply the filter with the query still empty (mirrors a no-op
	// keypress like backspace on an already-empty query): must stay flat.
	m.applyFilter()
	if len(m.filtered) != 2 || len(m.candidates) != 2 {
		t.Fatalf("expected 2 flat rows after re-applying an empty filter, got filtered=%d candidates=%d", len(m.filtered), len(m.candidates))
	}
	if driver.listTabsN != 0 || driver.listPanesN != 0 {
		t.Errorf("empty-query re-filter must not fetch; listTabsN=%d listPanesN=%d", driver.listTabsN, driver.listPanesN)
	}
}

// TestModelTree_PrintableNoopDoesNotFetch (5.1/R6): a second printable
// keystroke that still matches the same herdr parent must be a no-op with
// respect to the driver — the cached Fetch from the first keystroke is
// reused, not re-issued as a fresh ListTabs/ListPanes round-trip.
func TestModelTree_PrintableNoopDoesNotFetch(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	base := treeTestBaseCandidates()
	m := NewModelWithTree(base, nil, tree, Layout{})

	m.query = "a"
	m.applyFilter()
	firstListTabsN := driver.listTabsN
	if firstListTabsN == 0 {
		t.Fatal("expected the first non-empty-query filter pass to fetch the herdr parent's tree at least once")
	}

	m.query = "ap"
	m.applyFilter()
	if driver.listTabsN != firstListTabsN {
		t.Errorf("expected a second keystroke matching the same parent to reuse the cached fetch, got %d ListTabs calls (was %d)", driver.listTabsN, firstListTabsN)
	}
}

// TestModelTree_ParentKeptWhenOnlyChildMatches (5.1/R2 triangulation): a
// query that matches ONLY a tab's label — not the parent workspace's own
// label/path at all — still keeps the parent row in the filtered result,
// directly above its one matching child. This is the exact scenario R2's
// "single tab match expands only that tab" scenario describes.
func TestModelTree_ParentKeptWhenOnlyChildMatches(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "api"},
			{ID: "t2", WorkspaceID: "w1", Label: "db"},
		},
		panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t2", CWD: "/svc/db"},
		},
	}
	tree := NewTreeExpander(driver, time.Minute)
	base := []source.Candidate{
		{Path: "/svc", NormalizedPath: "/svc", Label: "backend", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}},
	}
	m := NewModelWithTree(base, nil, tree, Layout{})

	m.query = "api"
	m.applyFilter()

	if len(m.filtered) != 2 {
		t.Fatalf("expected [parent, matching child] rows, got %d: %+v", len(m.filtered), m.candidates)
	}
	if got := m.candidates[m.filtered[0]].Label; got != "backend" {
		t.Errorf("row 0 = %q, want the parent workspace row \"backend\"", got)
	}
	if got := m.candidates[m.filtered[1]].Label; got != "api" {
		t.Errorf("row 1 = %q, want the matching child \"api\" (sibling \"db\" must be excluded)", got)
	}
}

// TestModelTree_ChildSkipsAsyncPreview (5.1/R3): once cursor lands on a
// synthesized SourceHerdrTab child row, syncPreviewAfterSelectionChange must
// not dispatch an async preview Cmd and must leave previewLoading false —
// even though a real Renderer is wired.
func TestModelTree_ChildSkipsAsyncPreview(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	base := []source.Candidate{
		{Path: "/svc", NormalizedPath: "/svc", Label: "backend", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}},
	}
	m := NewModelWithTree(base, stubRenderer{}, tree, Layout{})

	m.query = "api"
	m.applyFilter()
	if len(m.filtered) != 2 {
		t.Fatalf("expected parent+child rows after query \"api\", got %d: %+v", len(m.filtered), m.candidates)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	cand, ok := mm.currentCandidate()
	if !ok || cand.Source != config.SourceHerdrTab {
		t.Fatalf("expected cursor to land on the synthesized child row, got %+v (ok=%v)", cand, ok)
	}
	if cmd != nil {
		t.Error("expected no async preview Cmd for a child row")
	}
	if mm.previewLoading {
		t.Error("expected previewLoading=false for a child row")
	}
}

// TestModelTree_TargetBindingsNoopOnChild (5.1/R5): ctrl+t on a synthesized
// child row is a no-op through the tree-wired constructor — the same
// source.SupportsCurrentWorkspaceTarget contract PR1 already covers, now
// proven end-to-end through NewModelWithTree/applyFilter's tree branch.
func TestModelTree_TargetBindingsNoopOnChild(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	base := []source.Candidate{
		{Path: "/svc", NormalizedPath: "/svc", Label: "backend", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}},
	}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.query = "api"
	m.applyFilter()
	if len(m.filtered) != 2 {
		t.Fatalf("expected parent+child rows, got %d: %+v", len(m.filtered), m.candidates)
	}
	m.cursor = 1 // the synthesized child row
	pane := source.Pane{ID: "p1"}
	m = m.WithCurrentPane(&pane)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if cmd != nil {
		t.Error("expected ctrl+t to no-op (no quit Cmd) on a child row")
	}
	if mm.ChosenTarget() != "" {
		t.Errorf("ChosenTarget = %q, want empty for a child row", mm.ChosenTarget())
	}
	if _, ok := mm.Selected(); ok {
		t.Error("expected no selection from ctrl+t on a child row")
	}
}
