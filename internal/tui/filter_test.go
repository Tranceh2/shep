package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/source"
)

// TestApplyFilter_ResetsCursorWhenQueryChanges proves filtering begins at the
// first visible row whenever the query differs from the last applied query.
func TestApplyFilter_PutsOpenHerdrBeforeUnopenedMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		open  string
		other string
	}{
		{name: "exact", query: "shep", open: "shep", other: "shep"},
		{name: "prefix", query: "fso", open: "FSOCIETY/arcade", other: "fsociety-repo"},
		{name: "fuzzy label", query: "dpy", open: "deploy-open", other: "directory-py"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel([]source.Candidate{
				projectCandidate(tc.other, "/other"),
				herdrCandidate(tc.open, "/open", "open"),
			}, nil)
			m.query = tc.query
			m.applyFilter()
			if len(m.rows) < 2 || m.rows[0].Candidate.Source != "herdr" {
				t.Fatalf("rows = %+v, want open Herdr workspace first", m.rows)
			}
		})
	}
}

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
// by non-query state retain the current row identity when user has navigated.
func TestApplyFilter_RetainsSelectionWhenQueryUnchanged(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("alphabet", "/alphabet"),
		zoxideCandidate("beta", "/beta"),
	}, nil)
	m.cursor = 1
	m.cursorTouched = true
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
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})

	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
}

// TestFetchAllChildren_NonHerdrCandidateNeverFetched proves only
// config.SourceHerdr candidates are ever considered for fetching.
func TestFetchAllChildren_NonHerdrCandidateNeverFetched(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{}
	tree := treeFromFake(driver)
	base := []source.Candidate{zoxideCandidate("dir", "/d")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.query = "dir"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Candidate.Source != "zoxide" {
		t.Errorf("non-Herdr rows changed unexpectedly: %+v", m.rows)
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

func TestRetainSelection_DirectCoverage(t *testing.T) {
	t.Parallel()

	t.Run("present ID is retained at correct index", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.rows = []Row{
			{ID: "row1"},
			{ID: "row2"},
			{ID: "row3"},
		}
		m.cursor = 0
		m.retainSelection("row2")
		if m.cursor != 1 {
			t.Errorf("cursor = %d, want 1", m.cursor)
		}
	})

	t.Run("absent ID falls through to clamp", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.rows = []Row{
			{ID: "row1"},
			{ID: "row2"},
		}
		m.cursor = 1
		m.retainSelection("missing_row")
		if m.cursor != 1 {
			t.Errorf("cursor = %d, want 1", m.cursor)
		}
	})

	t.Run("empty rows clamps to 0", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.rows = nil
		m.cursor = 5
		m.retainSelection("row1")
		if m.cursor != 0 {
			t.Errorf("cursor = %d, want 0", m.cursor)
		}
	})

	t.Run("cursor beyond len-1 clamps down", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.rows = []Row{{ID: "row1"}, {ID: "row2"}}
		m.cursor = 10
		m.retainSelection("missing")
		if m.cursor != 1 {
			t.Errorf("cursor = %d, want 1", m.cursor)
		}
	})

	t.Run("cursor below zero clamps up to 0", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.rows = []Row{{ID: "row1"}, {ID: "row2"}}
		m.cursor = -5
		m.retainSelection("missing")
		if m.cursor != 0 {
			t.Errorf("cursor = %d, want 0", m.cursor)
		}
	})
}

func TestMaybeRefreshSnapshot_DirectCoverageWithClockSeam(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	currentTime := baseTime

	t.Run("nil snapshotDriver produces nil without panic", func(t *testing.T) {
		t.Parallel()
		var m Model
		m.lastSnapshotAt = baseTime
		m.nowFn = func() time.Time { return baseTime.Add(10 * time.Second) }
		if cmd := m.maybeRefreshSnapshot(); cmd != nil {
			t.Fatal("expected nil cmd when snapshotDriver is nil")
		}
	})

	t.Run("zero lastSnapshotAt produces nil", func(t *testing.T) {
		t.Parallel()
		driver := &scriptedSnapshotDriver{}
		var m Model
		m.snapshotDriver = driver
		m.nowFn = func() time.Time { return baseTime }
		if cmd := m.maybeRefreshSnapshot(); cmd != nil {
			t.Fatal("expected nil cmd when lastSnapshotAt is zero")
		}
	})

	t.Run("TTL gate not elapsed produces nil", func(t *testing.T) {
		t.Parallel()
		driver := &scriptedSnapshotDriver{}
		var m Model
		m.snapshotDriver = driver
		m.lastSnapshotAt = baseTime
		m.nowFn = func() time.Time { return baseTime.Add(2 * time.Second) } // 2s < 5s TTL
		if cmd := m.maybeRefreshSnapshot(); cmd != nil {
			t.Fatal("expected nil cmd when TTL not elapsed")
		}
		if m.snapshotRefreshing {
			t.Fatal("snapshotRefreshing marked true unexpectedly")
		}
	})

	t.Run("TTL elapsed dispatches refresh and stamps snapshotRequestSeq", func(t *testing.T) {
		t.Parallel()
		driver := &scriptedSnapshotDriver{}
		var m Model
		m.snapshotDriver = driver
		m.lastSnapshotAt = baseTime
		m.liveSeq = 42
		m.nowFn = func() time.Time { return currentTime.Add(6 * time.Second) } // 6s > 5s TTL
		cmd := m.maybeRefreshSnapshot()
		if cmd == nil {
			t.Fatal("expected non-nil cmd when TTL elapsed")
		}
		if !m.snapshotRefreshing {
			t.Fatal("expected snapshotRefreshing=true")
		}
		if m.snapshotSeq != 1 {
			t.Errorf("snapshotSeq = %d, want 1", m.snapshotSeq)
		}
		if m.snapshotRequestSeq != 42 {
			t.Errorf("snapshotRequestSeq = %d, want 42 (matching m.liveSeq)", m.snapshotRequestSeq)
		}
	})

	t.Run("in-flight refresh suppresses duplicate dispatch", func(t *testing.T) {
		t.Parallel()
		driver := &scriptedSnapshotDriver{}
		var m Model
		m.snapshotDriver = driver
		m.lastSnapshotAt = baseTime
		m.snapshotRefreshing = true
		m.nowFn = func() time.Time { return baseTime.Add(10 * time.Second) }
		if cmd := m.maybeRefreshSnapshot(); cmd != nil {
			t.Fatal("expected nil cmd when snapshotRefreshing is already true")
		}
	})
}
