package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

// This file exercises four related visual/behavioral changes requested
// after manual visual testing of the redesigned picker:
//
//  1. A RowPane's fixed-width ancestor-continuation column (the "│ "
//     vertical bar) aligns with its parent tab's branch, while its own tree
//     glyph is one level deeper.
//  2. Built-in provider rows are label-first, with a path fallback for
//     unlabeled candidates. Synthesized Herdr tab/pane rows still use
//     "<label> · <path>" for their nested context.
//  3. Fuzzy match visibility already works on both Label and Path for every
//     row kind via candidateHaystack — locked in here as a regression test.
//  4. The list pane's leading structural gutter/marker reservation shrinks
//     by two cells (cursorPrefixWidth 2->1, and rowLineParts' marker+
//     separator slot 2->1 char), while the preview pane and theme.go's
//     shared border Padding(0, 1) are entirely untouched.

// --- Change 1: RowPane ancestor-continuation column ---

// TestKindPrefix_RowPane_AncestorContinuationAlignsWithTabBranch proves a
// RowPane whose parent tab is NOT the last tab puts its ancestor connector
// right after the indent. That makes the connector
// share the tab branch's column and pushes the pane's own branch one level
// deeper.
func TestKindPrefix_RowPane_AncestorContinuationAlignsWithTabBranch(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: false}
	want := "  " + set.TreeVertical + set.TreeLast + " "
	if got := m.kindPrefix(row); got != want {
		t.Errorf("kindPrefix(non-last-ancestor pane) = %q, want %q", got, want)
	}
}

// TestKindPrefix_RowPane_AncestorLastSiblingBlank proves a RowPane whose
// parent tab IS the last tab (AncestorIsLast=true) renders a BLANK column of
// the same width as TreeVertical instead — no "│" leaks through once the
// branch above it has ended.
func TestKindPrefix_RowPane_AncestorLastSiblingBlank(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true}
	blank := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
	want := "  " + blank + set.TreeLast + " "
	got := m.kindPrefix(row)
	if got != want {
		t.Errorf("kindPrefix(last-ancestor pane) = %q, want %q", got, want)
	}
	if strings.Contains(got, set.TreeVertical) {
		t.Errorf("kindPrefix(last-ancestor pane) = %q, must not contain the ancestor bar %q", got, set.TreeVertical)
	}
}

// TestKindPrefix_RowPane_AncestorColumnWidthStable proves the ancestor
// column reservation is the SAME total width whether AncestorIsLast is true
// or false — only its glyph content (bar vs blank) varies, alignment never
// shifts between sibling panes.
func TestKindPrefix_RowPane_AncestorColumnWidthStable(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	notLast := m.kindPrefix(Row{Kind: RowPane, Depth: 2, AncestorIsLast: false})
	last := m.kindPrefix(Row{Kind: RowPane, Depth: 2, AncestorIsLast: true})
	if got, want := lipgloss.Width(notLast), lipgloss.Width(last); got != want {
		t.Errorf("ancestor column width = %d (AncestorIsLast=false) vs %d (AncestorIsLast=true), want identical", got, want)
	}
}

// TestKindPrefix_RowTab_NeverGetsAncestorColumn proves a RowTab's own
// kindPrefix width is completely unaffected by this change (a RowTab has no
// ancestor level that participates in the vertical-line convention — its
// only ancestor is the top-level workspace).
func TestKindPrefix_RowTab_NeverGetsAncestorColumn(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	got := m.kindPrefix(Row{Kind: RowTab, Depth: 1})
	if strings.Contains(got, set.TreeVertical) {
		t.Errorf("kindPrefix(RowTab) = %q, must never contain the ancestor bar %q", got, set.TreeVertical)
	}
}

// TestKindPrefix_RowPane_ReservesNestedTreeDepth proves the real (Depth=2)
// RowPane reserves the parent branch column plus its own child branch.
func TestKindPrefix_RowPane_ReservesNestedTreeDepth(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	pane := m.kindPrefix(Row{Kind: RowPane, Depth: 2, AncestorIsLast: true})
	blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
	wantWidth := lipgloss.Width("  " + blankAncestor + set.TreeMid + " ")
	if got := lipgloss.Width(pane); got != wantWidth {
		t.Errorf("RowPane kindPrefix width = %d, want %d (nested tree depth)", got, wantWidth)
	}
}

// TestKindPrefix_HerdrTreeGeometry locks the complete two-level tree layout:
// a non-last tab's branch aligns with its panes' ancestor continuation, every
// pane's own branch is one level deeper, and a last tab has a blank but
// fixed-width continuation column.
func TestKindPrefix_HerdrTreeGeometry(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))

	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{"non-last tab", Row{Kind: RowTab, Depth: 1}, "  " + set.TreeMid + " "},
		{"first pane continues non-last tab", Row{Kind: RowPane, Depth: 2, AncestorIsLast: false}, "  " + set.TreeVertical + set.TreeMid + " "},
		{"last pane continues non-last tab", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: false}, "  " + set.TreeVertical + set.TreeLast + " "},
		{"last tab", Row{Kind: RowTab, Depth: 1, IsLast: true}, "  " + set.TreeLast + " "},
		{"pane under last tab", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true}, "  " + blankAncestor + set.TreeLast + " "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.kindPrefix(tt.row); got != tt.want {
				t.Errorf("kindPrefix(%+v) = %q, want %q", tt.row, got, tt.want)
			}
		})
	}
}

// --- Change 2: label composition per row kind ---

// TestRowView_RowCandidate_PathFallbackIsFilenameFirst proves a candidate
// whose label falls back to its path leads with the last directory, shows
// the parent as the secondary text, and moves "missing" to the accessories
// instead of suffixing the label.
func TestRowView_RowCandidate_PathFallbackIsFilenameFirst(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "backend", Path: "/srv/backend", Icon: "◆", Missing: true}}
	primary, secondary := m.rowDisplayText(row)
	if primary != "◆ backend" || secondary != "/srv" {
		t.Errorf("rowDisplayText(candidate) = %q + %q, want \"◆ backend\" + \"/srv\"", primary, secondary)
	}
	if got := m.rowAccessoryText(row); got != "missing" {
		t.Errorf("accessories = %q, want missing", got)
	}

	row = Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/srv/backend"}}
	if primary, secondary := m.rowDisplayText(row); primary != "backend" || secondary != "/srv" {
		t.Errorf("rowDisplayText(no label) = %q + %q, want backend + /srv", primary, secondary)
	}
}

func TestRowPrimaryText_RowCandidate_HerdrLabelComposition(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Herdr.Icon, p.Zoxide.Icon = "◆", "◆"
	})

	for _, tt := range []struct {
		name          string
		row           Row
		want, wantSec string
	}{
		{
			name: "labeled Herdr workspace keeps its label",
			row:  Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "backend", Path: "/srv/backend", Icon: "◆"}},
			want: "◆ backend",
		},
		{
			name: "unlabeled Herdr workspace falls back to its path, filename first",
			row:  Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Path: "/srv/backend", Icon: "◆"}},
			want: "◆ backend", wantSec: "/srv",
		},
		{
			name: "labeled ordinary provider keeps its label",
			row:  Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "backend", Path: "/srv/backend", Icon: "◆"}},
			want: "◆ backend",
		},
		{
			name: "a label that is not path-like stays single-part",
			row:  Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "Proyectos/shep", Path: "/srv/shep", Icon: "◆"}},
			want: "◆ Proyectos/shep",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary, secondary := m.rowDisplayText(tt.row)
			if primary != tt.want || secondary != tt.wantSec {
				t.Errorf("rowDisplayText(%s) = %q + %q, want %q + %q", tt.name, primary, secondary, tt.want, tt.wantSec)
			}
		})
	}
}

// wantRowPrimary composes the expected primary text for row as
// m.kindPrefix(row) + iconPart + body, so these tests assert the LABEL/PATH
// composition logic in isolation without hardcoding kindPrefix's own
// indent/active-marker/tree-glyph arithmetic (covered separately by the
// Change 1 kindPrefix tests above).
func wantRowPrimary(m Model, row Row, iconPart, body string) string {
	return m.kindPrefix(row) + iconPart + body
}

// TestRowPrimaryText_TabAndPaneDefaultsAreLabelOnly proves the default tree
// child formats name the tab or pane instead of repeating the workspace path
// on every child: a tab shows "<number> <label>" (deduplicated when the
// label is the number), a pane its label, or its path when it has none.
func TestRowPrimaryText_TabAndPaneDefaultsAreLabelOnly(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	for _, tc := range []struct {
		name string
		row  Row
		icon string
		want string
	}{
		{"tab label equals number", Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "1", Path: "/svc", Meta: map[string]string{"tab_number": "1"}}}, set.TabIcon + " ", "1"},
		{"tab number and label", Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc", Meta: map[string]string{"tab_number": "3"}}}, set.TabIcon + " ", "3 deploy"},
		{"tab without number", Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}, set.TabIcon + " ", "deploy"},
		{"pane label", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true, Candidate: source.Candidate{Label: "worker", Path: "/srv/api", Meta: map[string]string{"pane_id": "w4W:p1"}}}, "", "worker"},
		{"pane without label shows its path, filename first", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true, Candidate: source.Candidate{Path: "/srv/api"}}, "", "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, _ := m.rowPrimaryText(tc.row)
			if want := wantRowPrimary(m, tc.row, tc.icon, tc.want); primary != want {
				t.Errorf("rowPrimaryText = %q, want %q", primary, want)
			}
			if strings.Contains(primary, "w4W:p1") || strings.Contains(primary, " · ") {
				t.Errorf("rowPrimaryText = %q, must not carry a pane id or a path suffix", primary)
			}
		})
	}
}

// TestRowView_TabAndPaneSecondary proves tree children never carry the
// parent workspace's context; only a pane shown by its path fallback is
// drawn name-first, and labels that are not paths keep their start when
// truncated.
func TestRowView_TabAndPaneSecondary(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.Templates = tmpl.New("/home/dev")
	for _, tc := range []struct {
		row       Row
		secondary string
		keepStart bool
	}{
		{Row{Kind: RowPane, Candidate: source.Candidate{Label: "p1", Path: "/srv/api", Meta: map[string]string{"workspace_label": "backend"}}}, "", true},
		{Row{Kind: RowTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"workspace_label": "backend"}}}, "", true},
		{Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Path: "/home/dev/allsafe/ECORP/tech/whiterose-db"}}, "~/allsafe/ECORP/tech", true},
	} {
		v := m.buildRowView(tc.row)
		if v.detail.text != tc.secondary || v.keepStart != tc.keepStart {
			t.Errorf("row %q: detail %q keepStart %v, want %q %v", v.label.text, v.detail.text, v.keepStart, tc.secondary, tc.keepStart)
		}
	}
}

// --- Change 3: fuzzy match already works on Label AND Path per row kind ---

// TestBuildRows_RowCandidate_MatchesOnLabelOrPathAlone locks in that a
// RowCandidate is visible when the query matches ONLY its label, and
// separately when it matches ONLY its path.
func TestBuildRows_RowCandidate_MatchesOnLabelOrPathAlone(t *testing.T) {
	t.Parallel()
	cand := zoxideCandidate("uniquelabel", "/var/uniquepath")
	labelOnly := buildRows(rowBuildInput{query: "uniquelabel", candidates: []source.Candidate{cand}})
	if len(labelOnly) != 1 {
		t.Fatalf("label-only query: visible rows = %d, want 1", len(labelOnly))
	}
	pathOnly := buildRows(rowBuildInput{query: "uniquepath", candidates: []source.Candidate{cand}})
	if len(pathOnly) != 1 {
		t.Fatalf("path-only query: visible rows = %d, want 1", len(pathOnly))
	}
}

func TestBuildRows_HerdrRowCandidate_MatchesOnLabelOrPathAlone(t *testing.T) {
	t.Parallel()
	cand := herdrCandidate("uniquelabel", "/var/uniquepath", "w1")
	labelOnly := buildRows(rowBuildInput{query: "uniquelabel", candidates: []source.Candidate{cand}})
	if len(labelOnly) != 1 {
		t.Fatalf("label-only Herdr query: visible rows = %d, want 1", len(labelOnly))
	}
	pathOnly := buildRows(rowBuildInput{query: "uniquepath", candidates: []source.Candidate{cand}})
	if len(pathOnly) != 1 {
		t.Fatalf("path-only Herdr query: visible rows = %d, want 1", len(pathOnly))
	}
}

// TestBuildRows_RowTab_MatchesOnLabelOrPathAlone locks in the same guarantee
// for a synthesized RowTab.
func TestBuildRows_RowTab_MatchesOnLabelOrPathAlone(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{
			{Tab: source.Candidate{Label: "uniquetablabel", Path: "/svc/uniquetabpath", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}},
		}},
	}
	labelOnly := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "uniquetablabel", children: children})
	if len(labelOnly) != 2 { // workspace (descendant) + tab (direct)
		t.Fatalf("label-only tab query: visible rows = %d, want 2: %+v", len(labelOnly), labelOnly)
	}
	pathOnly := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "uniquetabpath", children: children})
	if len(pathOnly) != 2 {
		t.Fatalf("path-only tab query: visible rows = %d, want 2: %+v", len(pathOnly), pathOnly)
	}
}

// TestBuildRows_RowPane_MatchesOnLabelOrPathAlone locks in the same
// guarantee for a synthesized RowPane.
func TestBuildRows_RowPane_MatchesOnLabelOrPathAlone(t *testing.T) {
	t.Parallel()
	ws := herdrCandidate("backend", "/svc", "w1")
	children := map[string]workspaceChildren{
		"w1": {Tabs: []tabChildren{{
			Tab: source.Candidate{Label: "api", Path: "/svc/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}},
			Panes: []source.Candidate{
				{Label: "uniquepanelabel", Path: "/svc/uniquepanepath", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "pane_id": "p1"}},
			},
		}}},
	}
	labelOnly := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "uniquepanelabel", children: children})
	if len(labelOnly) != 3 { // workspace + tab (descendants) + pane (direct)
		t.Fatalf("label-only pane query: visible rows = %d, want 3: %+v", len(labelOnly), labelOnly)
	}
	pathOnly := buildRows(rowBuildInput{candidates: []source.Candidate{ws}, query: "uniquepanepath", children: children})
	if len(pathOnly) != 3 {
		t.Fatalf("path-only pane query: visible rows = %d, want 3: %+v", len(pathOnly), pathOnly)
	}
}

// --- TRL-1: list pane reserves exactly two marker cells ---

// TestCursorPrefixWidth_ReservesTwoCells proves the reserved cursor gutter
// is two cells wide.
func TestCursorPrefixWidth_ReservesTwoCells(t *testing.T) {
	t.Parallel()
	if cursorPrefixWidth != 2 {
		t.Errorf("cursorPrefixWidth = %d, want 2", cursorPrefixWidth)
	}
}

// TestRenderRowLine_LeadingBlankCellIsStable proves a non-cursor RowCandidate
// row (no icon, no label) reserves the two-cell marker gutter before its
// content. Pane border padding is a separate layer and is not counted here.
func TestRenderRowLine_LeadingBlankCellIsStable(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "backend"}}
	const width = 40
	got := renderRowLineText(m.renderRowLine(row, false, width))
	trimmed := strings.TrimLeft(got, " ")
	leading := len(got) - len(trimmed)
	if leading != 2 {
		t.Errorf("leading blank cells before label = %d, want exactly two marker cells: %q", leading, got)
	}
}

// TestKindPrefix_DepthZero_FlatScopeRowsEmptyPrefix proves that depth-zero
// pane/tab rows (such as synthesized flat agent rows) get an empty kindPrefix
// with none of TreeVertical, TreeMid or TreeLast,
// identical to a top-level RowCandidate.
func TestKindPrefix_DepthZero_FlatScopeRowsEmptyPrefix(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()

	for _, tt := range []struct {
		name string
		row  Row
	}{
		{"depth-zero RowPane", Row{Kind: RowPane, Depth: 0}},
		{"depth-zero RowPane isLast", Row{Kind: RowPane, Depth: 0, IsLast: true}},
		{"depth-zero RowTab", Row{Kind: RowTab, Depth: 0}},
		{"depth-zero RowTab isLast", Row{Kind: RowTab, Depth: 0, IsLast: true}},
		{"depth-zero RowCandidate", Row{Kind: RowCandidate, Depth: 0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := m.kindPrefix(tt.row)
			if got != "" {
				t.Errorf("kindPrefix(%+v) = %q, want empty string", tt.row, got)
			}
			if strings.Contains(got, set.TreeVertical) || strings.Contains(got, set.TreeMid) || strings.Contains(got, set.TreeLast) {
				t.Errorf("kindPrefix(%+v) = %q, must not contain tree glyphs", tt.row, got)
			}
		})
	}
}
