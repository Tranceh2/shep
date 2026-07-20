package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/source"
)

// TestApplyFilter_ResetsCursorWhenQueryChanges proves filtering begins at the
// first visible row whenever the query differs from the last applied query.
func TestApplyFilter_ResetsCursorWhenQueryChanges(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("alphabet", "/alphabet"),
		zoxideCandidate("beta", "/beta"),
	}
	m := NewModel(cands, nil)
	// Highlight "alphabet" (rows: alpha, alphabet, beta -> index 1).
	m.cursor = 1
	if got, _ := m.currentCandidate(); got.Label != "alphabet" {
		t.Fatalf("setup: cursor not on alphabet, got %+v", got)
	}

	m.query = "alpha"
	m.applyFilter()

	got, ok := m.currentCandidate()
	if !ok || got.Label != "alpha" {
		t.Errorf("after filtering to \"alpha\": currentCandidate = %+v (ok=%v), want first visible alpha", got, ok)
	}

	m.cursor = 1
	m.query = ""
	m.applyFilter()
	got, ok = m.currentCandidate()
	if !ok || got.Label != "alpha" {
		t.Errorf("after clearing the query: currentCandidate = %+v (ok=%v), want first visible alpha", got, ok)
	}
}

// TestApplyFilter_RetainsSelectionWhenQueryUnchanged proves rebuilds caused
// by non-query state retain the current row identity.
func TestApplyFilter_RetainsSelectionWhenQueryUnchanged(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("alphabet", "/alphabet"),
		zoxideCandidate("beta", "/beta"),
	}, nil)
	m.cursor = 1
	m.applyFilter()

	got, ok := m.currentCandidate()
	if !ok || got.Label != "alphabet" {
		t.Errorf("after non-query rebuild: currentCandidate = %+v (ok=%v), want alphabet retained", got, ok)
	}
}

// TestUpdate_PrintableQueryChangeResetsCursor proves the key handling path
// applies the reset when a printable rune mutates m.query.
func TestUpdate_PrintableQueryChangeResetsCursor(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("alphabet", "/alphabet"),
		zoxideCandidate("beta", "/beta"),
	}, nil)
	m.cursor = 2 // beta remains visible for query "a" but must not be retained.

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if got, want := m.query, "a"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got := m.cursor; got != 0 {
		t.Errorf("cursor after printable query change = %d, want 0", got)
	}
}

// TestApplyFilter_FallsBackToNearestSelectableWhenLost proves that when the
// previously highlighted row disappears entirely, the cursor lands on the
// nearest sensible selectable row instead of an invalid/header position.
func TestApplyFilter_FallsBackToNearestSelectableWhenLost(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("beta", "/beta"),
	}
	m := NewModel(cands, nil)
	m.cursor = 0 // "alpha" (no header anymore: alpha=0, beta=1)
	m.query = "beta"
	m.applyFilter()

	row, ok := m.currentRow()
	if !ok || !row.Selectable() {
		t.Fatalf("expected a selectable row after losing the previous selection, got %+v (ok=%v)", row, ok)
	}
	if row.Candidate.Label != "beta" {
		t.Errorf("currentRow = %+v, want beta (the only surviving candidate)", row)
	}
}

// TestFetchAllChildren_SkipsUnexpandedWorkspaceAtEmptyQuery proves the
// bounded-fetch contract: a collapsed workspace at an empty query is never
// fetched at all (not just hidden) — "never perform [tree fetching] from
// View" and "do not dump all workspace descendants at empty query".
func TestFetchAllChildren_SkipsUnexpandedWorkspaceAtEmptyQuery(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1"}}}
	tree := NewTreeExpander(driver, time.Minute)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})

	if driver.listTabsN != 0 {
		t.Errorf("construction at empty query must not fetch; listTabsN=%d", driver.listTabsN)
	}

	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	if driver.listTabsN != 1 {
		t.Errorf("manual expand must fetch exactly once; listTabsN=%d", driver.listTabsN)
	}
}

// TestFetchAllChildren_NonHerdrCandidateNeverFetched proves only
// config.SourceHerdr candidates are ever considered for fetching.
func TestFetchAllChildren_NonHerdrCandidateNeverFetched(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{}
	tree := NewTreeExpander(driver, time.Minute)
	base := []source.Candidate{zoxideCandidate("dir", "/d")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.query = "dir"
	m.applyFilter()
	if driver.listTabsN != 0 {
		t.Errorf("expected zero fetches for a non-Herdr candidate, got listTabsN=%d", driver.listTabsN)
	}
}

// TestApplyFilter_NilTreeDegradesToFlatGroups proves a plain NewModel (no
// tree) never synthesizes children even for a SourceHerdr candidate.
func TestApplyFilter_NilTreeDegradesToFlatGroups(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{herdrCandidate("backend", "/svc", "w1")}, nil)
	m.query = "backend"
	m.applyFilter()
	for _, r := range m.rows {
		if r.Kind == RowTab || r.Kind == RowPane {
			t.Fatalf("expected no synthesized children without a tree, got %+v", r)
		}
	}
}
