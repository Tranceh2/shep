package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// This file exercises four related visual/behavioral changes requested
// after manual visual testing of the redesigned picker:
//
//  1. A RowPane's fixed-width ancestor-continuation column (the "│ "
//     vertical bar) aligns with its parent tab's branch, while its own tree
//     glyph is one level deeper.
//  2. Only synthesized Herdr workspace/tab/pane rows use
//     "<label> · <path>". Ordinary provider candidates show their path only;
//     RowTab and RowPane have no trailing secondary context.
//  3. Fuzzy match visibility already works on both Label and Path for every
//     row kind via candidateHaystack — locked in here as a regression test.
//  4. The list pane's leading structural gutter/marker reservation shrinks
//     by two cells (cursorPrefixWidth 2->1, and rowLineParts' marker+
//     separator slot 2->1 char), while the preview pane and theme.go's
//     shared border Padding(0, 1) are entirely untouched.

// --- Change 1: RowPane ancestor-continuation column ---

// TestKindPrefix_RowPane_AncestorContinuationAlignsWithTabBranch proves a
// RowPane whose parent tab is NOT the last tab puts its ancestor connector
// immediately after the fixed active-marker slot. That makes the connector
// share the tab branch's column and pushes the pane's own branch one level
// deeper.
func TestKindPrefix_RowPane_AncestorContinuationAlignsWithTabBranch(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: false}
	activeSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	want := "  " + activeSlot + set.TreeVertical + set.TreeLast + " "
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
	activeSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	blank := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
	want := "  " + activeSlot + blank + set.TreeLast + " "
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
	notLast := m.kindPrefix(Row{Kind: RowPane, AncestorIsLast: false})
	last := m.kindPrefix(Row{Kind: RowPane, AncestorIsLast: true})
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
// RowPane reserves the parent branch column plus its own child branch, while
// active-marker gutters remain a fixed width.
func TestKindPrefix_RowPane_ReservesNestedTreeDepth(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	pane := m.kindPrefix(Row{Kind: RowPane, Depth: 2, AncestorIsLast: true})
	activeSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
	wantWidth := lipgloss.Width("  " + activeSlot + blankAncestor + set.TreeMid + " ")
	if got := lipgloss.Width(pane); got != wantWidth {
		t.Errorf("RowPane kindPrefix width = %d, want %d (nested tree depth with fixed active gutter)", got, wantWidth)
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
	activeSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))

	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{"non-last tab", Row{Kind: RowTab, Depth: 1}, "  " + activeSlot + set.TreeMid + " "},
		{"first pane continues non-last tab", Row{Kind: RowPane, Depth: 2, AncestorIsLast: false}, "  " + activeSlot + set.TreeVertical + set.TreeMid + " "},
		{"last pane continues non-last tab", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: false}, "  " + activeSlot + set.TreeVertical + set.TreeLast + " "},
		{"last tab", Row{Kind: RowTab, Depth: 1, IsLast: true}, "  " + activeSlot + set.TreeLast + " "},
		{"pane under last tab", Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true}, "  " + activeSlot + blankAncestor + set.TreeLast + " "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.kindPrefix(tt.row); got != tt.want {
				t.Errorf("kindPrefix(%+v) = %q, want %q", tt.row, got, tt.want)
			}
		})
	}
}

// --- Change 2: labels apply only to synthesized Herdr rows ---

func TestRowPrimaryText_RowCandidate_PathOnly(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "backend", Path: "/srv/backend", Icon: "◆", Missing: true}}
	primary, _ := m.rowPrimaryText(row)
	if want := "◆ /srv/backend (missing)"; primary != want {
		t.Errorf("rowPrimaryText(candidate) = %q, want %q", primary, want)
	}
	if strings.Contains(primary, "◆ backend") || strings.Contains(primary, labelPathSeparator) {
		t.Errorf("rowPrimaryText(candidate) must render provider path only, got %q", primary)
	}
}

func TestRowPrimaryText_RowCandidate_EmptyLabelShowsPathOnly(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/srv/backend"}}
	primary, _ := m.rowPrimaryText(row)
	if want := "/srv/backend"; primary != want {
		t.Errorf("rowPrimaryText(no label) = %q, want %q (path alone, no separator artifact)", primary, want)
	}
	if strings.Contains(primary, "·") {
		t.Errorf("rowPrimaryText(no label) = %q, must not contain a dangling separator", primary)
	}
}

func TestRowPrimaryText_RowCandidate_HerdrLabelComposition(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)

	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{
			name: "labeled Herdr workspace composes label and path",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceHerdr,
				Label:  "backend",
				Path:   "/srv/backend",
				Icon:   "◆",
			}},
			want: "◆ backend · /srv/backend",
		},
		{
			name: "unlabeled Herdr workspace keeps path only",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceHerdr,
				Path:   "/srv/backend",
				Icon:   "◆",
			}},
			want: "◆ /srv/backend",
		},
		{
			name: "labeled ordinary provider keeps path only",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceZoxide,
				Label:  "backend",
				Path:   "/srv/backend",
				Icon:   "◆",
			}},
			want: "◆ /srv/backend",
		},
		{
			name: "labeled Herdr workspace preserves missing suffix",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source:  config.SourceHerdr,
				Label:   "backend",
				Path:    "/srv/backend",
				Icon:    "◆",
				Missing: true,
			}},
			want: "◆ backend · /srv/backend (missing)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary, _ := m.rowPrimaryText(tt.row)
			if primary != tt.want {
				t.Errorf("rowPrimaryText(%s) = %q, want %q", tt.name, primary, tt.want)
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

func TestRowPrimaryText_RowTab_DedupTabNumber(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{
		Kind: RowTab, Depth: 1, IsLast: true,
		Candidate: source.Candidate{Label: "1", Path: "/svc", Meta: map[string]string{"tab_number": "1"}},
	}
	primary, _ := m.rowPrimaryText(row)
	want := wantRowPrimary(m, row, set.TabIcon+" ", "1 · /svc")
	if primary != want {
		t.Errorf("rowPrimaryText(tab, label==number) = %q, want %q (no duplication)", primary, want)
	}
}

func TestRowPrimaryText_RowTab_NumberAndDifferentLabel(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{
		Kind: RowTab, Depth: 1, IsLast: true,
		Candidate: source.Candidate{Label: "deploy", Path: "/svc", Meta: map[string]string{"tab_number": "3"}},
	}
	primary, _ := m.rowPrimaryText(row)
	want := wantRowPrimary(m, row, set.TabIcon+" ", "3 deploy · /svc")
	if primary != want {
		t.Errorf("rowPrimaryText(tab, number+label) = %q, want %q", primary, want)
	}
}

func TestRowPrimaryText_RowTab_NoNumber(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}
	primary, _ := m.rowPrimaryText(row)
	want := wantRowPrimary(m, row, set.TabIcon+" ", "deploy · /svc")
	if primary != want {
		t.Errorf("rowPrimaryText(tab, no number) = %q, want %q", primary, want)
	}
}

func TestRowPrimaryText_RowPane_HumanLabelDotPath(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true,
		Candidate: source.Candidate{
			Label: "worker", Path: "/srv/api", Meta: map[string]string{"pane_id": "w4W:p1"},
		},
	}
	primary, _ := m.rowPrimaryText(row)
	want := wantRowPrimary(m, row, "", "worker · /srv/api")
	if primary != want {
		t.Errorf("rowPrimaryText(pane) = %q, want %q", primary, want)
	}
	if strings.Contains(primary, "w4W:p1") {
		t.Errorf("rowPrimaryText(pane) must not render stable pane ID as a label: %q", primary)
	}
}

func TestRowPrimaryText_RowPane_EmptyLabelShowsPathOnly(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowPane, Depth: 2, IsLast: true, AncestorIsLast: true, Candidate: source.Candidate{Path: "/srv/api"}}
	primary, _ := m.rowPrimaryText(row)
	want := wantRowPrimary(m, row, "", "/srv/api")
	if primary != want {
		t.Errorf("rowPrimaryText(pane, no label) = %q, want %q", primary, want)
	}
}

// TestRowSecondaryText_RowPane_AlwaysEmpty proves a RowPane's secondary text
// (the old "pane-id · in <workspace>" trailing dim text) is gone entirely —
// folded into the primary text instead.
func TestRowSecondaryText_RowPane_AlwaysEmpty(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api",
			Meta: map[string]string{"workspace_label": "backend"},
		},
	}
	if got := m.rowSecondaryText(row); got != "" {
		t.Errorf("rowSecondaryText(pane) = %q, want empty (no secondary for RowPane anymore)", got)
	}
}

// TestRowSecondaryText_RowTab_IsEmpty proves a tab's parent workspace context
// is not rendered as the duplicated trailing "in <path>" content.
func TestRowSecondaryText_RowTab_IsEmpty(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:      RowTab,
		Candidate: source.Candidate{Label: "api", Meta: map[string]string{"workspace_label": "backend"}},
	}
	if got := m.rowSecondaryText(row); got != "" {
		t.Errorf("rowSecondaryText(tab) = %q, want empty (no trailing workspace context)", got)
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

// TestPreviewPane_BorderPaddingUnchanged proves theme.go's shared
// borderStyle/focusedBorderStyle horizontal padding (Padding(0, 1)) — used
// by BOTH the list and preview panes via paneBoxStyle — is untouched by
// TRL-1: the actual cursor-gutter width comes entirely from
// render.go's list-exclusive gutter/marker reservation (rowLineParts,
// renderSelectedFromParts/renderUnselectedFromParts), which the preview pane
// never invokes at all, so the preview pane's own leading padding can never
// regress from this change.
func TestPreviewPane_BorderPaddingUnchanged(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	if got := m.styles.borderStyle.GetPaddingLeft(); got != 1 {
		t.Errorf("borderStyle left padding = %d, want 1 (unchanged, shared with preview, never zeroed)", got)
	}
	if got := m.styles.focusedBorderStyle.GetPaddingLeft(); got != 1 {
		t.Errorf("focusedBorderStyle left padding = %d, want 1 (unchanged)", got)
	}
}
