package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

func TestNoFuzzyScoreReference(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("rows.go")
	if err != nil {
		t.Fatalf("read rows.go: %v", err)
	}
	const deletedWrapper = "fuzzy" + "Score"
	if strings.Contains(string(src), deletedWrapper) {
		t.Error("rows.go still references the deleted score wrapper")
	}
}

func TestWorkspaceNameRemainsOutsideTUISearchAndPresentation(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{
		Path:   "/srv/platform-api",
		Label:  "display-label",
		Source: config.SourceProjects,
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{cand}, query: "display-label"})
	if len(rows) != 1 || rows[0].Candidate.Label != cand.Label || rows[0].Candidate.Path != cand.Path {
		t.Fatalf("TUI search changed candidate fields: %+v", rows)
	}
	if got := candidateHaystack(cand); strings.Contains(got, "rendered-name") || !strings.Contains(got, cand.Label) {
		t.Fatalf("TUI search haystack = %q, want only candidate presentation fields", got)
	}
	m := newRenderTestModel(ThemePlain, FocusList)
	m.layout.LabelFormats = LabelFormats{Projects: "{{.Label}}"}
	primary, _ := m.rowDisplayText(rows[0])
	if !strings.Contains(primary, cand.Label) || strings.Contains(primary, "rendered-name") {
		t.Fatalf("TUI row presentation = %q, want candidate label without launch name", primary)
	}
}

// TestBuildRows_StableGroupAndParentOrder proves the DEFAULT source order
// (Herdr, Workspaces, Zoxide, Projects — mirroring config.defaultSourceOrder)
// applies when rowBuildInput.sourceOrder is empty. An empty query preserves
// each provider's original emitted order.
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

// TestBuildRows_FuzzyScoreRanksCandidates verifies non-empty queries rank
// visible candidates by fuzzy score, while equal scores retain their original
// provider position. The first case is the regression for "omp": a direct
// ~/.config/omp match must surface above a weaker scattered match.
func TestBuildRows_FuzzyScoreRanksCandidates(t *testing.T) {
	t.Parallel()
	t.Run("surfaces direct omp path above scattered match", func(t *testing.T) {
		rows := buildRows(rowBuildInput{
			query: "omp",
			candidates: []source.Candidate{
				zoxideCandidate("scattered", "/var/cache/xxomp"),
				zoxideCandidate("omp config", "~/.config/omp"),
			},
		})
		if len(rows) != 2 {
			t.Fatalf("visible rows = %d, want 2: %+v", len(rows), rows)
		}
		if got, want := rows[0].Candidate.Path, "~/.config/omp"; got != want {
			t.Errorf("first fuzzy result path = %q, want %q", got, want)
		}
		if got, want := rows[1].Candidate.Path, "/var/cache/xxomp"; got != want {
			t.Errorf("second fuzzy result path = %q, want %q", got, want)
		}
	})

	t.Run("equal scores retain original provider order", func(t *testing.T) {
		rows := buildRows(rowBuildInput{
			query: "omp",
			candidates: []source.Candidate{
				{Source: config.SourceZoxide, Label: "omp", Path: "/same", Meta: map[string]string{"id": "first"}},
				{Source: config.SourceZoxide, Label: "omp", Path: "/same", Meta: map[string]string{"id": "second"}},
			},
		})
		if len(rows) != 2 {
			t.Fatalf("visible rows = %d, want 2: %+v", len(rows), rows)
		}
		if got, want := rows[0].Candidate.Meta["id"], "first"; got != want {
			t.Errorf("first equal-score result = %q, want %q", got, want)
		}
		if got, want := rows[1].Candidate.Meta["id"], "second"; got != want {
			t.Errorf("second equal-score result = %q, want %q", got, want)
		}
	})

	t.Run("propagates indexes only for direct matches", func(t *testing.T) {
		workspace := herdrCandidate("workspace", "/workspace", "w1")
		rows := buildRows(rowBuildInput{
			query: "omp",
			candidates: []source.Candidate{
				workspace,
				zoxideCandidate("omp config", "~/.config/omp"),
			},
			children: map[string]workspaceChildren{
				"w1": {Tabs: []tabChildren{{
					Tab: source.Candidate{
						Label: "logs",
						Path:  "/workspace/logs",
						Meta:  map[string]string{"workspace_id": "w1", "tab_id": "t1"},
					},
					Panes: []source.Candidate{{
						Label: "omp pane",
						Path:  "/workspace/omp",
						Meta:  map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"},
					}},
				}}},
			},
		})

		if len(rows) != 4 {
			t.Fatalf("visible rows = %d, want workspace, tab, pane, and candidate: %+v", len(rows), rows)
		}
		if got := rows[0].Match; got != MatchDescendant {
			t.Errorf("workspace match = %v, want MatchDescendant", got)
		}
		if got := rows[0].MatchedIndexes; len(got) != 0 {
			t.Errorf("workspace matched indexes = %v, want empty for a non-match", got)
		}
		if got := rows[2].Match; got != MatchDirect {
			t.Errorf("pane match = %v, want MatchDirect", got)
		}
		if got := rows[2].MatchedIndexes; len(got) == 0 {
			t.Error("pane matched indexes are empty, want codepoint indexes for a direct match")
		}
		if got := rows[3].Match; got != MatchDirect {
			t.Errorf("candidate match = %v, want MatchDirect", got)
		}
		if got := rows[3].MatchedIndexes; len(got) == 0 {
			t.Error("candidate matched indexes are empty, want codepoint indexes for a direct match")
		}
	})
}

// TestBuildRows_FuzzyScoreTieAcrossSourcesKeepsSourceOrder verifies that a
// score tie is resolved by configured source order before provider-local index.
func TestBuildRows_FuzzyScoreTieAcrossSourcesKeepsSourceOrder(t *testing.T) {
	t.Parallel()
	rows := buildRows(rowBuildInput{
		query:       "tie",
		sourceOrder: []string{config.SourceHerdr, config.SourceZoxide},
		candidates: []source.Candidate{
			herdrCandidate("ignored", "/ignored", "w0"),
			herdrCandidate("tie", "/same", "w1"),
			zoxideCandidate("tie", "/same"),
		},
	})
	if len(rows) != 2 {
		t.Fatalf("visible rows = %d, want 2: %+v", len(rows), rows)
	}

	got := []string{rows[0].Candidate.Source, rows[1].Candidate.Source}
	want := []string{config.SourceHerdr, config.SourceZoxide}
	if !equalStrings(got, want) {
		t.Errorf("source order for equal fuzzy scores = %v, want %v", got, want)
	}
}

// TestBuildRows_FuzzyScoreRanksAcrossProviders verifies score ranking is
// applied to every visible candidate, not only within one provider group.
func TestBuildRows_FuzzyScoreRanksAcrossProviders(t *testing.T) {
	t.Parallel()
	rows := buildRows(rowBuildInput{
		query: "omp",
		candidates: []source.Candidate{
			herdrCandidate("scattered", "/var/cache/xxomp", "w1"),
			zoxideCandidate("omp config", "~/.config/omp"),
		},
	})
	if len(rows) != 2 {
		t.Fatalf("visible rows = %d, want 2: %+v", len(rows), rows)
	}
	if got, want := rows[0].Candidate.Path, "~/.config/omp"; got != want {
		t.Errorf("first cross-provider fuzzy result path = %q, want %q", got, want)
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

// TestBuildRows_FuzzyScoreRanksMatchingPanesWithinTab verifies that matching
// pane rows are score-sorted without breaking the workspace/tab context used
// for path-descendant matches.
func TestBuildRows_FuzzyScoreRanksMatchingPanesWithinTab(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "services", Path: "/svc", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{
				{Label: "scattered", Path: "/var/cache/xxomp", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}},
				{Label: "omp config", Path: "~/.config/omp", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p2"}},
			},
		}}},
	}
	rows := buildRows(rowBuildInput{
		candidates: []source.Candidate{ws},
		query:      "omp",
		children:   children,
	})
	if len(rows) != 4 {
		t.Fatalf("visible rows = %d, want workspace, tab, and two panes: %+v", len(rows), rows)
	}
	if rows[0].Match != MatchDescendant || rows[1].Match != MatchDescendant {
		t.Errorf("workspace/tab context = (%v, %v), want both MatchDescendant", rows[0].Match, rows[1].Match)
	}
	if got, want := rows[2].Candidate.Path, "~/.config/omp"; got != want {
		t.Errorf("first pane path = %q, want %q", got, want)
	}
	if got, want := rows[3].Candidate.Path, "/var/cache/xxomp"; got != want {
		t.Errorf("second pane path = %q, want %q", got, want)
	}
}

// TestBuildRows_FuzzyScoreRanksParentsByMatchingDescendants verifies a
// workspace inherits its best matching descendant's score while retaining its
// tab and pane rows as one contiguous structural group.
func TestBuildRows_FuzzyScoreRanksParentsByMatchingDescendants(t *testing.T) {
	t.Parallel()
	weak := herdrCandidate("weak workspace", "/svc/weak", "w1")
	strong := herdrCandidate("strong workspace", "/svc/strong", "w2")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "services", Path: "/svc/weak", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{{Label: "scattered", Path: "/var/cache/xxomp", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}}},
		}}},
		"w2": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "services", Path: "/svc/strong", Meta: map[string]string{"workspace_id": "w2", "tab_id": "t2"}},
			Panes: []source.Candidate{{Label: "omp config", Path: "~/.config/omp", Meta: map[string]string{"workspace_id": "w2", "tab_id": "t2", "pane_id": "p2"}}},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{weak, strong}, query: "omp", children: children})
	if len(rows) != 6 {
		t.Fatalf("visible rows = %d, want two workspace/tab/pane groups: %+v", len(rows), rows)
	}
	if got, want := rows[0].Candidate.Label, "strong workspace"; got != want {
		t.Errorf("first workspace = %q, want %q", got, want)
	}
	if rows[0].Match != MatchDescendant || rows[1].Match != MatchDescendant || rows[2].Match != MatchDirect {
		t.Errorf("strong workspace group matches = (%v, %v, %v), want descendant, descendant, direct", rows[0].Match, rows[1].Match, rows[2].Match)
	}
	if got, want := rows[3].Candidate.Label, "weak workspace"; got != want {
		t.Errorf("second workspace = %q, want %q", got, want)
	}
}

// TestBuildRows_EqualScorePanesRetainProviderOrder verifies score ties use
// each pane's original index in its tab's emitted pane slice.
func TestBuildRows_EqualScorePanesRetainProviderOrder(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "services", Path: "/svc", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{
				{Label: "omp", Path: "/same", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}},
				{Label: "omp", Path: "/same", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p2"}},
			},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "omp", children: children})
	if len(rows) != 4 {
		t.Fatalf("visible rows = %d, want workspace, tab, and two panes: %+v", len(rows), rows)
	}
	if got, want := rows[2].Candidate.Meta["pane_id"], "p1"; got != want {
		t.Errorf("first equal-score pane = %q, want %q", got, want)
	}
	if got, want := rows[3].Candidate.Meta["pane_id"], "p2"; got != want {
		t.Errorf("second equal-score pane = %q, want %q", got, want)
	}
}

// TestBuildRows_TreeSiblingMetadata proves each expanded workspace marks last
// tab/pane siblings after their final display order is determined. A pane
// carries its parent tab's IsLast as AncestorIsLast; this is the current
// two-level tree simplification.
func TestBuildRows_TreeSiblingMetadata(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{
				Tab: source.Candidate{Label: "api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
				Panes: []source.Candidate{
					{Label: "api-worker", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}},
					{Label: "api-shell", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p2"}},
				},
			},
			{
				Tab: source.Candidate{Label: "db", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2"}},
				Panes: []source.Candidate{
					{Label: "db-worker", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2", "pane_id": "p3"}},
					{Label: "db-shell", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2", "pane_id": "p4"}},
				},
			},
		}},
	}
	rows := buildRows(rowBuildInput{
		candidates:         []source.Candidate{ws},
		children:           children,
		expandedWorkspaces: map[string]bool{"w1": true},
	})
	if len(rows) != 7 {
		t.Fatalf("visible rows = %d, want workspace, two tabs, and four panes: %+v", len(rows), rows)
	}

	for _, tt := range []struct {
		index              int
		name               string
		kind               RowKind
		wantIsLast         bool
		wantAncestorIsLast bool
	}{
		{index: 1, name: "first tab", kind: RowTab, wantIsLast: false, wantAncestorIsLast: false},
		{index: 2, name: "first tab first pane", kind: RowPane, wantIsLast: false, wantAncestorIsLast: false},
		{index: 3, name: "first tab last pane", kind: RowPane, wantIsLast: true, wantAncestorIsLast: false},
		{index: 4, name: "last tab", kind: RowTab, wantIsLast: true, wantAncestorIsLast: false},
		{index: 5, name: "last tab first pane", kind: RowPane, wantIsLast: false, wantAncestorIsLast: true},
		{index: 6, name: "last tab last pane", kind: RowPane, wantIsLast: true, wantAncestorIsLast: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := rows[tt.index]
			if row.Kind != tt.kind {
				t.Fatalf("row %d kind = %v, want %v", tt.index, row.Kind, tt.kind)
			}
			if row.IsLast != tt.wantIsLast {
				t.Errorf("row %d IsLast = %t, want %t", tt.index, row.IsLast, tt.wantIsLast)
			}
			if row.AncestorIsLast != tt.wantAncestorIsLast {
				t.Errorf("row %d AncestorIsLast = %t, want %t", tt.index, row.AncestorIsLast, tt.wantAncestorIsLast)
			}
		})
	}

	t.Run("marks final siblings after fuzzy sorting", func(t *testing.T) {
		rows := buildRows(rowBuildInput{
			query:      "omp",
			candidates: []source.Candidate{ws},
			children: map[string]workspaceChildren{
				"w1": {Tabs: []tabChildren{
					{
						Tab:   source.Candidate{Label: "weak tab", Path: "/svc/weak", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
						Panes: []source.Candidate{{Label: "scattered", Path: "/var/cache/xxomp", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}}},
					},
					{
						Tab:   source.Candidate{Label: "strong tab", Path: "/svc/strong", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2"}},
						Panes: []source.Candidate{{Label: "omp pane", Path: "~/.config/omp", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t2", "pane_id": "p2"}}},
					},
				}},
			},
		})
		if len(rows) != 5 {
			t.Fatalf("visible rows = %d, want workspace and two tab/pane groups: %+v", len(rows), rows)
		}
		if got, want := rows[1].Candidate.Meta["tab_id"], "t2"; got != want {
			t.Fatalf("first sorted tab = %q, want %q", got, want)
		}
		if rows[1].IsLast || !rows[2].IsLast || rows[2].AncestorIsLast {
			t.Errorf("first sorted tab/pane flags = tab:%t pane:(last:%t ancestor:%t), want false / (true:false)", rows[1].IsLast, rows[2].IsLast, rows[2].AncestorIsLast)
		}
		if got, want := rows[3].Candidate.Meta["tab_id"], "t1"; got != want {
			t.Fatalf("last sorted tab = %q, want %q", got, want)
		}
		if !rows[3].IsLast || !rows[4].IsLast || !rows[4].AncestorIsLast {
			t.Errorf("last sorted tab/pane flags = tab:%t pane:(last:%t ancestor:%t), want true / (true:true)", rows[3].IsLast, rows[4].IsLast, rows[4].AncestorIsLast)
		}
	})
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

// TestSessionRows_AreFlatAndUseSessionNameIdentity ensures directory-less
// sessions never collide during selection retention and never synthesize tree
// descendants.
func TestSessionRows_AreFlatAndUseSessionNameIdentity(t *testing.T) {
	t.Parallel()
	alpha := source.Candidate{Source: config.SourceSessions, Label: "alpha", Meta: map[string]string{"session_name": "alpha"}}
	beta := source.Candidate{Source: config.SourceSessions, Label: "beta", Meta: map[string]string{"session_name": "beta"}}
	if got, want := rowIdentity(alpha), "session:alpha"; got != want {
		t.Errorf("alpha identity = %q, want %q", got, want)
	}
	if got, want := rowIdentity(beta), "session:beta"; got != want {
		t.Errorf("beta identity = %q, want %q", got, want)
	}
	rows := buildRows(rowBuildInput{
		candidates:  []source.Candidate{alpha, beta},
		sourceOrder: []string{config.SourceSessions},
		children: map[string]workspaceChildren{
			"ignored": {Tabs: []tabChildren{{Tab: source.Candidate{Label: "must not appear"}}}},
		},
	})
	if len(rows) != 2 || rows[0].Kind != RowCandidate || rows[1].Kind != RowCandidate {
		t.Fatalf("session rows = %+v, want two flat candidate rows", rows)
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
