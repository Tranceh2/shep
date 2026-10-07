package tui

import (
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// expandInput is a two-tab workspace "backend" (tabs "api" and "web", one
// pane each) ready for buildRows.
func expandInput(query string, expanded bool) rowBuildInput {
	ws := herdrCandidate("backend", "/srv/backend", "w1")
	tabs := []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api", Number: 1}, {ID: "t2", WorkspaceID: "w1", Label: "web", Number: 2}}
	panes := []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}, {ID: "p2", WorkspaceID: "w1", TabID: "t2", CWD: "/srv/web"}}
	in := rowBuildInput{
		candidates:         []source.Candidate{ws},
		query:              query,
		children:           map[string]workspaceChildren{"w1": synthesizeWorkspaceChildren("w1", ws.Label, ws.Path, tabs, panes)},
		expandedWorkspaces: map[string]bool{},
	}
	if expanded {
		in.expandedWorkspaces["w1"] = true
	}
	return in
}

// rowSummary names each row by kind and label (or path) for compact
// assertions.
func rowSummary(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		name := r.Candidate.Label
		if name == "" {
			name = r.Candidate.Path
		}
		out[i] = [...]string{"ws", "tab", "pane"}[r.Kind] + ":" + name
	}
	return out
}

// TestBuildRows_DirectWorkspaceMatchStaysCollapsed proves a workspace that
// matches the query by itself does not unfold its tree, and reports so.
func TestBuildRows_DirectWorkspaceMatchStaysCollapsed(t *testing.T) {
	t.Parallel()
	rows := buildRows(expandInput("back", false))
	if got := rowSummary(rows); len(got) != 1 || got[0] != "ws:backend" {
		t.Fatalf("rows = %v, want the workspace alone", got)
	}
	if rows[0].Match != MatchDirect || rows[0].Expanded || !rows[0].Expandable {
		t.Errorf("workspace row = %+v, want a direct, collapsed, expandable match", rows[0])
	}
}

// TestBuildRows_DescendantOnlyMatchShowsItsBranch proves a workspace visible
// only through a descendant still opens along the matching branch alone.
func TestBuildRows_DescendantOnlyMatchShowsItsBranch(t *testing.T) {
	t.Parallel()
	rows := buildRows(expandInput("web", false))
	if got := rowSummary(rows); len(got) != 3 || got[0] != "ws:backend" || got[1] != "tab:web" || got[2] != "pane:/srv/web" {
		t.Fatalf("rows = %v, want the workspace and its web branch", got)
	}
	if rows[0].Match != MatchDescendant || !rows[0].Expanded {
		t.Errorf("workspace row = %+v, want a descendant match shown expanded", rows[0])
	}
}

// TestBuildRows_ManualExpandDuringQueryShowsAllChildren proves expanding a
// workspace during a query shows every child, the matching ones marked
// direct, and that collapsing it again folds the tree back.
func TestBuildRows_ManualExpandDuringQueryShowsAllChildren(t *testing.T) {
	t.Parallel()
	rows := buildRows(expandInput("api", true))
	got := rowSummary(rows)
	want := []string{"ws:backend", "tab:api", "pane:/srv/api", "tab:web", "pane:/srv/web"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want every child %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v (matching branch first)", got, want)
		}
	}
	matches := map[string]MatchKind{}
	for i, r := range rows {
		matches[got[i]] = r.Match
	}
	if matches["tab:api"] != MatchDirect || matches["pane:/srv/api"] != MatchDirect || matches["tab:web"] != MatchNone || matches["pane:/srv/web"] != MatchNone {
		t.Errorf("matches = %v, want only the api branch marked", matches)
	}
	if !rows[0].Expanded {
		t.Error("an expanded workspace must report Expanded")
	}

	direct := buildRows(expandInput("back", true))
	if len(direct) != 5 || !direct[0].Expanded {
		t.Errorf("expanded direct match rows = %v, want its whole tree", rowSummary(direct))
	}
	if collapsed := buildRows(expandInput("back", false)); len(collapsed) != 1 {
		t.Errorf("collapsed again: rows = %v, want the workspace alone", rowSummary(collapsed))
	}
}

// TestBuildRows_CollapsedDirectMatchKeepsItsOwnScore proves a collapsed,
// directly matching workspace ranks by its own score: descendants it does
// not show do not count.
func TestBuildRows_CollapsedDirectMatchKeepsItsOwnScore(t *testing.T) {
	t.Parallel()
	in := expandInput("back", false)
	in.matcher = newQueryMatcher(in.query)
	_, withTree, _, _ := buildCandidateRow(in, in.candidates[0])
	in.children = nil
	_, alone, _, _ := buildCandidateRow(in, in.candidates[0])
	if withTree != alone {
		t.Errorf("score with descendants = %d, alone = %d, want equal", withTree, alone)
	}
}

// TestModel_ExpandAndCollapseDuringQuery drives the keys: right expands a
// directly matching workspace during a query, left folds it again.
func TestModel_ExpandAndCollapseDuringQuery(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	m := NewModelWithTree([]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}, nil, treeFromFake(driver), Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 30))
	for _, r := range "back" {
		m, _ = update(t, m, key(string(r)))
	}
	if len(m.rows) != 1 {
		t.Fatalf("rows = %v, want the collapsed workspace", rowSummary(m.rows))
	}
	m, _ = update(t, m, key("right"))
	if len(m.rows) != 3 || !m.rows[0].Expanded {
		t.Fatalf("after right: rows = %v, want the whole tree", rowSummary(m.rows))
	}
	m, _ = update(t, m, key("left"))
	if len(m.rows) != 1 || m.rows[0].Expanded {
		t.Errorf("after left: rows = %v, want the workspace collapsed", rowSummary(m.rows))
	}
}
