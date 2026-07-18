package tui

import (
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// TestBuildRows_StableGroupAndParentOrder proves the DEFAULT source order
// (Herdr, Workspaces, Zoxide, Projects — mirroring config.defaultSourceOrder)
// applies when rowBuildInput.sourceOrder is empty, and that within a source,
// candidates keep their ORIGINAL PROVIDER ORDER regardless of query — a
// query only decides visibility, never re-ranks. Typing a query that keeps
// every workspace visible must not reorder them.
func TestBuildRows_StableGroupAndParentOrder(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("zzz-last", "/z", "w1"),
		herdrCandidate("aaa-first", "/a", "w2"),
		projectCandidate("proj", "/p"),
		zoxideCandidate("dir", "/d"),
	}
	for _, query := range []string{"", "aaa"} {
		rows := buildRows(rowBuildInput{candidates: cands, query: query})
		var order []string
		for _, r := range rows {
			if r.Kind == RowCandidate {
				order = append(order, r.Candidate.Label)
			}
		}
		if query == "" {
			want := []string{"zzz-last", "aaa-first", "dir", "proj"}
			if !equalStrings(order, want) {
				t.Errorf("query %q: order = %v, want %v", query, order, want)
			}
		} else {
			// "a" matches "aaa-first" (self) directly; "zzz-last" has no
			// self/descendant match and no children, so it drops out —
			// but the SURVIVING workspace ("aaa-first") must still appear
			// before dir/proj per source order, never reshuffled by score.
			want := []string{"aaa-first"}
			if !equalStrings(order, want) {
				t.Errorf("query %q: order = %v, want %v", query, order, want)
			}
		}
	}
}

// TestBuildRows_CustomSourceOrderOverridesDefault proves a non-default,
// explicitly-configured source order (rowBuildInput.sourceOrder — sourced
// from cfg.General.Sources at the Model layer) is honored verbatim instead
// of the hardcoded default, and that a source with zero visible members
// simply contributes no rows (there is no header to omit anymore).
func TestBuildRows_CustomSourceOrderOverridesDefault(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
		projectCandidate("shep", "/home/dev/shep"),
		zoxideCandidate("tmp", "/tmp"),
	}
	rows := buildRows(rowBuildInput{
		candidates:  cands,
		sourceOrder: []string{config.SourceProjects, config.SourceZoxide, config.SourceHerdr},
	})
	var order []string
	for _, r := range rows {
		order = append(order, r.Candidate.Label)
	}
	want := []string{"shep", "tmp", "backend"}
	if !equalStrings(order, want) {
		t.Errorf("custom sourceOrder: order = %v, want %v", order, want)
	}
}

// TestBuildRows_DescendantOnlyTabMatchRetainsParent proves a query that
// matches ONLY a tab's label (not the parent workspace's own label/path at
// all) still keeps the parent row, directly above its one matching child —
// and that the parent is marked MatchDescendant (not MatchDirect), while
// the matching tab is marked MatchDirect.
func TestBuildRows_DescendantOnlyTabMatchRetainsParent(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}},
			{Tab: source.Candidate{Label: "db", Path: "/svc/db", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2"}}},
		}},
	}
	rows := buildRows(rowBuildInput{
		candidates: []source.Candidate{ws},
		query:      "api",
		children:   children,
	})
	if len(rows) != 2 { // parent + matching tab
		t.Fatalf("expected [parent, matching tab], got %d rows: %+v", len(rows), rows)
	}
	parent, tab := rows[0], rows[1]
	if parent.Kind != RowCandidate || parent.Candidate.Label != "backend" {
		t.Fatalf("row 0 = %+v, want the parent workspace", parent)
	}
	if parent.Match != MatchDescendant {
		t.Errorf("parent.Match = %v, want MatchDescendant (parent's own text did not match)", parent.Match)
	}
	if tab.Kind != RowTab || tab.Candidate.Label != "api" {
		t.Fatalf("row 2 = %+v, want the matching tab \"api\" (sibling \"db\" must be excluded)", tab)
	}
	if tab.Match != MatchDirect {
		t.Errorf("tab.Match = %v, want MatchDirect", tab.Match)
	}
}

// TestBuildRows_DescendantOnlyPaneMatchRetainsWorkspaceAndTab proves a query
// matching ONLY a pane's label keeps both its parent tab (retained as
// structural context, MatchDescendant) and grandparent workspace
// (MatchDescendant) visible, while a sibling pane and sibling tab with no
// match are excluded — "only matching descendants are shown".
func TestBuildRows_DescendantOnlyPaneMatchRetainsWorkspaceAndTab(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{
				Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
				Panes: []source.Candidate{
					{Label: "p1-worker", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}},
					{Label: "p2-shell", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p2"}},
				},
			},
			{Tab: source.Candidate{Label: "db", Path: "/svc/db", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2"}}},
		}},
	}
	rows := buildRows(rowBuildInput{
		candidates: []source.Candidate{ws},
		query:      "worker",
		children:   children,
	})
	// workspace (descendant), tab "api" (descendant), pane "p1-worker" (direct).
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (workspace, tab, matching pane), got %d: %+v", len(rows), rows)
	}
	if rows[0].Match != MatchDescendant || rows[0].Candidate.Label != "backend" {
		t.Errorf("row 0 = %+v, want workspace backend marked MatchDescendant", rows[0])
	}
	if rows[1].Kind != RowTab || rows[1].Candidate.Label != "api" || rows[1].Match != MatchDescendant {
		t.Errorf("row 1 = %+v, want tab api marked MatchDescendant", rows[1])
	}
	if rows[2].Kind != RowPane || rows[2].Candidate.Label != "p1-worker" || rows[2].Match != MatchDirect {
		t.Errorf("row 2 = %+v, want pane p1-worker marked MatchDirect", rows[2])
	}
}

// TestBuildRows_WorkspaceOnlyMatchStaysFlat proves a query matching only the
// workspace's own label/path never auto-expands its children.
func TestBuildRows_WorkspaceOnlyMatchStaysFlat(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}},
		}},
	}
	rows := buildRows(rowBuildInput{
		candidates: []source.Candidate{ws},
		query:      "backend",
		children:   children,
	})
	if len(rows) != 1 { // workspace only, no tab
		t.Fatalf("expected [workspace] only, got %d rows: %+v", len(rows), rows)
	}
	if rows[0].Match != MatchDirect {
		t.Errorf("workspace row Match = %v, want MatchDirect", rows[0].Match)
	}
}

// TestBuildRows_EmptyQueryNeverDumpsChildren proves an empty query never
// shows a workspace's tabs/panes unless explicitly (manually) expanded —
// "do not dump all workspace descendants at empty query".
func TestBuildRows_EmptyQueryNeverDumpsChildren(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}},
		}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "", children: children})
	if len(rows) != 1 {
		t.Fatalf("empty query with children fetched but NOT expanded: expected [workspace] only, got %d: %+v", len(rows), rows)
	}

	expanded := buildRows(rowBuildInput{
		candidates: []source.Candidate{ws}, query: "", children: children,
		expandedWorkspaces: map[string]bool{"w1": true},
	})
	if len(expanded) != 2 {
		t.Fatalf("manually expanded workspace: expected [workspace, tab], got %d: %+v", len(expanded), expanded)
	}
}

// TestRowIdentity_StableAcrossRebuilds proves rowIdentity depends only on
// stable candidate fields (never slice position), so selection retention
// works across a rebuild.
func TestRowIdentity_StableAcrossRebuilds(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	if got, want := rowIdentity(ws), "ws:w1"; got != want {
		t.Errorf("rowIdentity(workspace) = %q, want %q", got, want)
	}
	tab := source.Candidate{Meta: map[string]string{"tab_id": "t1"}}
	if got, want := rowIdentity(tab), "tab:t1"; got != want {
		t.Errorf("rowIdentity(tab) = %q, want %q", got, want)
	}
	pane := source.Candidate{Meta: map[string]string{"pane_id": "p1"}}
	if got, want := rowIdentity(pane), "pane:p1"; got != want {
		t.Errorf("rowIdentity(pane) = %q, want %q", got, want)
	}
	zx := zoxideCandidate("dir", "/d")
	if got, want := rowIdentity(zx), config.SourceZoxide+":/d"; got != want {
		t.Errorf("rowIdentity(zoxide) = %q, want %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
