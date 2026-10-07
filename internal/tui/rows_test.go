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

func TestBuildRows_AliasMakesCandidateVisibleWithoutRenderingAlias(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Path: "/srv/kubernetes", Label: "Kubernetes", Aliases: []string{"k8s", "kube"}, Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{cand}, query: "k8s"})
	if len(rows) != 1 || rows[0].Candidate.Label != "Kubernetes" {
		t.Fatalf("alias rows = %+v, want Kubernetes candidate", rows)
	}
	m := newRenderTestModel(ThemePlain, FocusList)
	primary, _ := m.rowDisplayText(rows[0])
	if strings.Contains(primary, "k8s") || strings.Contains(primary, "kube") {
		t.Fatalf("primary row rendered aliases: %q", primary)
	}
}

func TestWorktreeRowPresentationAndSearch(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{
		Path: "/trees/api", Label: "api", Source: config.SourceProjects,
		Meta: map[string]string{"is_worktree": "true", "branch": "feat/super-long-branch"},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{cand}, query: "super-long"})
	if len(rows) != 1 {
		t.Fatalf("worktree branch search rows = %d, want 1", len(rows))
	}
	m := newRenderTestModel(ThemePlain, FocusList)
	primary, _ := m.rowDisplayText(rows[0])
	if primary != "\ue725  api" || m.rowAccessoryText(rows[0]) != "feat/super-long-branch" {
		t.Fatalf("worktree row = %q + accessory %q, want the branch icon, the name and the branch accessory", primary, m.rowAccessoryText(rows[0]))
	}
	line := stripANSI(m.renderRowLine(rows[0], false, 20))
	if lipglossWidth(line) != 20 || !strings.Contains(line, "\ue725  api") {
		t.Fatalf("narrow row = %q (width %d), want the icon and name within width 20 (the branch gives way)", line, lipglossWidth(line))
	}
}

// TestProjectsIcon_WorktreeGlyphOnlyForWorktrees proves the projects
// presentation's icon template draws the worktree glyph only on worktree
// rows; other projects keep the project glyph.
func TestProjectsIcon_WorktreeGlyphOnlyForWorktrees(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Path: "/srv/api", Label: "api", Source: config.SourceProjects}
	m := newRenderTestModel(ThemePlain, FocusList)
	primary, _ := m.rowDisplayText(Row{Kind: RowCandidate, Candidate: cand})
	if !strings.Contains(primary, "\ue702") || strings.Contains(primary, "\ue725") {
		t.Fatalf("standard project row = %q, want the project glyph only", primary)
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
	m = m.withPresentation(func(p *config.Presentations) { p.Projects.Label = "{{.Label}}" })
	primary, _ := m.rowDisplayText(rows[0])
	if !strings.Contains(primary, cand.Label) || strings.Contains(primary, "rendered-name") {
		t.Fatalf("TUI row presentation = %q, want candidate label without launch name", primary)
	}
}

// TestBuildRows_StableGroupAndParentOrder proves the DEFAULT source order
// (Herdr, Workspaces, Zoxide, Projects — mirroring config.defaultSourceOrder)
// applies when rowBuildInput.sourceOrder is empty. Without an active history
// snapshot, an empty query preserves each provider's original emitted order.
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

func TestBuildRows_OpenHerdrWinsWithinExactPrefixAndFuzzyLayers(t *testing.T) {
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
			open := herdrCandidate(tc.open, "/open", "open")
			other := projectCandidate(tc.other, "/other")
			rows := buildRows(rowBuildInput{
				query: tc.query, sourceOrder: []string{config.SourceProjects, config.SourceHerdr},
				candidates: []source.Candidate{other, open},
			})
			if len(rows) != 2 || rows[0].Candidate.Source != config.SourceHerdr {
				t.Fatalf("rows = %+v, want open Herdr workspace first", rows)
			}
		})
	}
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
// from cfg.General.SourceOrder at the Model layer) is honored verbatim instead
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

// === SPEC-NAV-2: metadata-aware search over existing keys ===

// paneChild builds a synthesized pane candidate with the given whitelisted
// metadata keys, mirroring synthesizeWorkspaceChildren's output shape.
func paneChild(label, path, tabID, paneID string, meta map[string]string) source.Candidate {
	m := map[string]string{"workspace_id": "w1", "tab_id": tabID, "pane_id": paneID}
	for k, v := range meta {
		m[k] = v
	}
	return source.Candidate{Label: label, Path: path, Meta: m}
}

// TestMatchRow_MetadataFallbackPerRowKind proves SPEC-NAV-2.1-2.6: matchRow
// first scores the original Label+Path domain and, ONLY when that fails,
// falls back to a guarded per-row-kind metadata projection over existing
// keys. Missing/empty metadata never matches; task/name are never read.
func TestMatchRow_MetadataFallbackPerRowKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		query       string
		cand        source.Candidate
		kind        RowKind
		wantMatch   bool
		wantOrig    bool // true when matched via original Label+Path domain
		wantNoIndex bool // metadata-only matches carry no label/path indexes
	}{
		{
			name:  "SPEC-NAV-2.1 pane matches agent_status",
			query: "working",
			cand:  paneChild("p1", "/svc/api", "t1", "p1", map[string]string{"agent_status": "working"}),
			kind:  RowPane, wantMatch: true, wantOrig: false, wantNoIndex: true,
		},
		{
			name:  "SPEC-NAV-2.2 pane matches tab_label",
			query: "build",
			cand:  paneChild("p1", "/svc/api", "t1", "p1", map[string]string{"tab_label": "build"}),
			kind:  RowPane, wantMatch: true, wantOrig: false, wantNoIndex: true,
		},
		{
			name:  "SPEC-NAV-2.3 pane matches pane_id fragment",
			query: "abcd",
			cand:  paneChild("shell", "/svc/api", "t1", "p_abcd1234", nil),
			kind:  RowPane, wantMatch: true, wantOrig: false, wantNoIndex: true,
		},
		{
			name:  "SPEC-NAV-2.4 session matches session_name (original-domain parity via Label)",
			query: "alpha",
			cand:  source.Candidate{Source: config.SourceSessions, Label: "alpha", Meta: map[string]string{"session_name": "alpha"}},
			kind:  RowCandidate, wantMatch: true, wantOrig: true, wantNoIndex: false,
		},
		{
			name:  "SPEC-NAV-2.5 missing agent_status never matches",
			query: "working",
			cand:  paneChild("shell", "/svc/api", "t1", "p1", nil),
			kind:  RowPane, wantMatch: false,
		},
		{
			name:  "original domain wins: pane label match keeps indexes",
			query: "shell",
			cand:  paneChild("shell", "/svc/api", "t1", "p1", map[string]string{"agent_status": "working"}),
			kind:  RowPane, wantMatch: true, wantOrig: true, wantNoIndex: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, idx, matched, original := matchRow(tc.query, tc.cand, tc.kind)
			if matched != tc.wantMatch {
				t.Fatalf("matched = %v, want %v (score=%d)", matched, tc.wantMatch, score)
			}
			if !tc.wantMatch {
				return
			}
			if original != tc.wantOrig {
				t.Errorf("original = %v, want %v", original, tc.wantOrig)
			}
			if tc.wantNoIndex && len(idx) != 0 {
				t.Errorf("metadata-only match must carry no label/path indexes, got %v", idx)
			}
			if !tc.wantNoIndex && tc.wantOrig && len(idx) == 0 {
				t.Errorf("original-domain match must carry label/path indexes, got none")
			}
		})
	}
}

// TestMatchRow_SessionSecondaryMetadata proves SPEC-NAV-2.4/session_dir:
// a session candidate whose Label/Path do not match still matches on
// session_dir via metadata fallback.
func TestMatchRow_SessionSecondaryMetadata(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Source: config.SourceSessions, Label: "alpha", Path: "", Meta: map[string]string{"session_name": "alpha", "session_dir": "/work/deploy"}}
	score, _, matched, original := matchRow("deploy", cand, RowCandidate)
	if !matched {
		t.Fatalf("expected session_dir metadata match, got no match (score=%d)", score)
	}
	if original {
		t.Errorf("session_dir match must be metadata-fallback (original=false), got original=true")
	}
}

// TestMatchRow_NeverReadsForbiddenKeys proves SPEC-NAV-2.6: task/name are
// never consulted, even if present. A candidate whose ONLY field carrying the
// query is Meta["task"]/Meta["name"] must NOT match.
func TestMatchRow_NeverReadsForbiddenKeys(t *testing.T) {
	t.Parallel()
	forbidden := paneChild("shell", "/svc/api", "t1", "p1", map[string]string{"task": "deployment", "name": "deployment"})
	if _, _, matched, _ := matchRow("deployment", forbidden, RowPane); matched {
		t.Errorf("matchRow must never read task/name metadata keys, but matched %q", "deployment")
	}
}

// TestBuildRows_PaneMatchesViaMetadataOnly proves SPEC-NAV-3.2/3.4: a pane
// matching Q ONLY via metadata (agent_status) is included, and its parent
// workspace + tab context is preserved as MatchDescendant.
func TestBuildRows_PaneMatchesViaMetadataOnly(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{
				paneChild("shell", "/svc/api", "t1", "p1", map[string]string{"agent_status": "working"}),
			},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "working", children: children})
	if len(rows) != 3 {
		t.Fatalf("expected workspace, tab, metadata-matched pane; got %d rows: %+v", len(rows), rows)
	}
	if rows[0].Match != MatchDescendant || rows[0].Candidate.Label != "backend" {
		t.Errorf("row 0 = %+v, want backend MatchDescendant", rows[0])
	}
	if rows[1].Kind != RowTab || rows[1].Match != MatchDescendant {
		t.Errorf("row 1 = %+v, want tab MatchDescendant context", rows[1])
	}
	if rows[2].Kind != RowPane || rows[2].Candidate.Meta["pane_id"] != "p1" {
		t.Errorf("row 2 = %+v, want the metadata-matched pane p1", rows[2])
	}
	// Metadata-only match must not carry label/path highlight indexes.
	if len(rows[2].MatchedIndexes) != 0 {
		t.Errorf("metadata-only pane must have no MatchedIndexes, got %v", rows[2].MatchedIndexes)
	}
}

// TestBuildRows_MissingMetadataPaneExcluded proves SPEC-NAV-2.5 end-to-end:
// a pane whose agent_status is absent is NOT pulled in by a query that would
// only match that field.
func TestBuildRows_MissingMetadataPaneExcluded(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{paneChild("shell", "/svc/api", "t1", "p1", nil)},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "working", children: children})
	if len(rows) != 0 {
		t.Fatalf("expected no rows (nothing matches \"working\"), got %d: %+v", len(rows), rows)
	}
}

// === SPEC-NAV-3: ranking preservation over the original domain ===

// TestBuildRows_MetadataGroupUsesLowestTextualLayer proves metadata-only
// groups remain below label fuzzy matches even when their metadata token has a
// stronger raw fuzzy score. Textual quality is intentionally ranked before the
// score within a layer.
func TestBuildRows_MetadataGroupUsesLowestTextualLayer(t *testing.T) {
	t.Parallel()
	// Two original-domain groups whose OWN labels contain the query with
	// different strength, plus one metadata-only group whose fuzzy score on
	// its metadata token lands between them. Under pure score-descending
	// stable order the metadata group must interleave between the two
	// original groups, not be forced after both.
	//
	// Query "omp": a workspace label matches directly on the original domain
	// with a score that depends on label composition. We construct:
	//   strongOrig: label "omp" (tight, high original score)
	//   weakOrig:   label "a-o-m-p-x scattered" (loose, low original score)
	//   metaOnly:   label/path lack "omp"; agent_status pane carries "omp"
	// and assert the metadata group is NOT last purely because of provenance.
	strongOrig := herdrCandidate("omp", "/strong", "w1")
	metaOnly := herdrCandidate("beta-ws", "/b", "w2")
	// weakOrig matches "omp" on its own label but loosely (o..m..p scattered
	// across the label with gaps), yielding a low original-domain score.
	weakOrig := herdrCandidate("o zz m zz p scattered ws", "/weak", "w3")
	children := map[string]workspaceChildren{
		"w2": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "t", Path: "/b", Meta: map[string]string{"workspace_id": "w2", "tab_id": "t2"}},
			Panes: []source.Candidate{paneChild("shell", "/b", "t2", "p2", map[string]string{"agent_status": "omp"})},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{strongOrig, metaOnly, weakOrig}, query: "omp", children: children})
	var wsOrder []string
	for _, r := range rows {
		if r.Kind == RowCandidate {
			wsOrder = append(wsOrder, r.Candidate.Label)
		}
	}
	if len(wsOrder) != 3 {
		t.Fatalf("expected all three workspaces visible, got %v", wsOrder)
	}
	// The metadata-only group is LayerPathOrMeta, so it follows both label
	// fuzzy groups regardless of the raw metadata fuzzy score.
	posMeta, posWeak := indexOf(wsOrder, "beta-ws"), indexOf(wsOrder, "o zz m zz p scattered ws")
	if posMeta < posWeak {
		t.Errorf("workspace order = %v: metadata-only group must remain in the lowest textual layer", wsOrder)
	}
	posStrong := indexOf(wsOrder, "omp")
	if posStrong > posWeak {
		t.Errorf("workspace order = %v: stronger label fuzzy match must precede weaker label fuzzy match", wsOrder)
	}
}

// TestBuildRows_OriginalSubsetRelativeOrderPreserved proves SPEC-NAV-3.1
// narrowly: for two original-domain groups A and B, adding a metadata-only
// group into the mix never changes A's position relative to B. Only the
// original-match subset's mutual order is contractually preserved.
func TestBuildRows_OriginalSubsetRelativeOrderPreserved(t *testing.T) {
	t.Parallel()
	// A and B both match "omp" on their own labels; A scores higher than B.
	a := herdrCandidate("omp", "/a", "w1")
	b := herdrCandidate("omp scattered elsewhere", "/b", "w2")
	// Baseline: no metadata group present.
	baseline := buildRows(rowBuildInput{candidates: []source.Candidate{a, b}, query: "omp"})
	var baseOrder []string
	for _, r := range baseline {
		if r.Kind == RowCandidate {
			baseOrder = append(baseOrder, r.Candidate.Label)
		}
	}
	// With a metadata-only group interleaved, A and B keep their mutual order.
	meta := herdrCandidate("beta-ws", "/m", "w3")
	children := map[string]workspaceChildren{
		"w3": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "t", Path: "/m", Meta: map[string]string{"workspace_id": "w3", "tab_id": "t3"}},
			Panes: []source.Candidate{paneChild("shell", "/m", "t3", "p3", map[string]string{"agent_status": "omp"})},
		}}},
	}
	withMeta := buildRows(rowBuildInput{candidates: []source.Candidate{a, b, meta}, query: "omp", children: children})
	var aPos, bPos int
	var order []string
	for _, r := range withMeta {
		if r.Kind == RowCandidate {
			order = append(order, r.Candidate.Label)
		}
	}
	aPos, bPos = indexOf(order, "omp"), indexOf(order, "omp scattered elsewhere")
	if aPos == -1 || bPos == -1 {
		t.Fatalf("both original groups must remain visible: order=%v", order)
	}
	if (aPos < bPos) != (indexOf(baseOrder, "omp") < indexOf(baseOrder, "omp scattered elsewhere")) {
		t.Errorf("original subset relative order changed: baseline=%v withMeta=%v", baseOrder, order)
	}
}

// TestBuildRows_MetadataDescendantDoesNotInflateOriginalGroupScore proves the
// aggregate-score guard (retained, subtree-local, NOT a global comparator): a
// metadata-only descendant pane must NOT raise its parent workspace group's
// aggregate score above a competing original-domain group. The original group
// ranks by its own original-domain score; a metadata descendant can add rows
// but cannot inflate the group's rank.
func TestBuildRows_MetadataDescendantDoesNotInflateOriginalGroupScore(t *testing.T) {
	t.Parallel()
	// Group X: matches "omp" ONLY via a strong original-domain pane label.
	strongOrig := herdrCandidate("x-ws", "/x", "w1")
	// Group Y: matches "omp" via a WEAK original pane label AND additionally
	// has a metadata pane whose agent_status token would score very high if it
	// leaked into the group aggregate. It must not.
	mixedOrig := herdrCandidate("y-ws", "/y", "w2")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "t", Path: "/x", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{paneChild("omp", "/x", "t1", "p1", nil)},
		}}},
		"w2": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "t", Path: "/y", Meta: map[string]string{"workspace_id": "w2", "tab_id": "t2"}},
			Panes: []source.Candidate{
				paneChild("a x m o p weak", "/y", "t2", "p2", nil),                             // weak original match
				paneChild("shell", "/y", "t2", "p3", map[string]string{"agent_status": "omp"}), // metadata-only, high token score
			},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{strongOrig, mixedOrig}, query: "omp", children: children})
	var wsOrder []string
	for _, r := range rows {
		if r.Kind == RowCandidate {
			wsOrder = append(wsOrder, r.Candidate.Label)
		}
	}
	// x-ws (strong original) must precede y-ws: y's metadata descendant must
	// not inflate y's aggregate score above x's original-domain score.
	if indexOf(wsOrder, "x-ws") > indexOf(wsOrder, "y-ws") {
		t.Errorf("workspace order = %v: metadata descendant must not inflate y-ws's aggregate score above x-ws's original-domain score", wsOrder)
	}
}

// indexOf returns the position of s in xs, or -1.
func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// TestBuildRows_LabelPathOnlyOrderUnchangedWithMetadataAvailable proves
// SPEC-NAV-3.1: for a query where every match is on the original Label+Path
// domain, adding the metadata fallback capability does NOT change the
// relative order of those matches (metadata is only consulted on original
// failure, so it is a no-op here).
func TestBuildRows_LabelPathOnlyOrderUnchangedWithMetadataAvailable(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "services", Path: "/svc", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{
				// Both carry agent_status metadata, but both ALSO match on label.
				paneChild("scattered", "/var/cache/xxomp", "t1", "p1", map[string]string{"agent_status": "omp"}),
				paneChild("omp config", "~/.config/omp", "t1", "p2", map[string]string{"agent_status": "idle"}),
			},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "omp", children: children})
	if len(rows) != 4 {
		t.Fatalf("visible rows = %d, want workspace, tab, two panes: %+v", len(rows), rows)
	}
	// Same order as the pre-metadata TestBuildRows_FuzzyScoreRanksMatchingPanesWithinTab.
	if got, want := rows[2].Candidate.Path, "~/.config/omp"; got != want {
		t.Errorf("first pane path = %q, want %q (label/path order must be unchanged)", got, want)
	}
	if got, want := rows[3].Candidate.Path, "/var/cache/xxomp"; got != want {
		t.Errorf("second pane path = %q, want %q", got, want)
	}
}

// TestBuildRows_RankedEmptyQueryUntouchedByMetadata proves SPEC-NAV-3.3: the
// ranked empty-query branch is not affected by the metadata matcher — order
// equals the caller's pre-ranked candidate order and no children are pulled.
func TestBuildRows_RankedEmptyQueryUntouchedByMetadata(t *testing.T) {
	t.Parallel()
	a := herdrCandidate("first", "/a", "w1")
	b := herdrCandidate("second", "/b", "w2")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab:   source.Candidate{Label: "t", Path: "/a", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{paneChild("shell", "/a", "t1", "p1", map[string]string{"agent_status": "working"})},
		}}},
	}
	rows := buildRows(rowBuildInput{candidates: []source.Candidate{a, b}, query: "", ranked: true, children: children})
	var order []string
	for _, r := range rows {
		order = append(order, r.Candidate.Label)
	}
	if !equalStrings(order, []string{"first", "second"}) {
		t.Errorf("ranked empty-query order = %v, want [first second] (pre-ranked order, no children)", order)
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
