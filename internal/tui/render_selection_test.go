package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

// newRenderTestModel builds the minimal Model state renderRowLine reads
// (styles, theme, focus) so selection-rendering tests can exercise the cursor
// row in isolation without driving a full Update/View lifecycle. width/height
// are set only so a stray renderList call would not panic; renderRowLine
// itself takes width as an argument.
func newRenderTestModel(themeName string, focus Focus) Model {
	m := Model{
		theme:  testTheme(themeName),
		focus:  focus,
		width:  40,
		height: 10,
		layout: Layout{Templates: tmpl.New("")},
	}
	return m.withPresentation(nil)
}

// renderRowLineText strips any residual ANSI so assertions see the structural
// text only (the headless test color profile already emits none, but this
// keeps the tests robust against a future profile-forcing helper).
func renderRowLineText(s string) string {
	return stripNonSGRANSI(s)
}

// TestRenderTabStrip_LongTabsKeepsActiveVisibleAndBounded proves a tab
// strip too long for its row keeps the active tab whole, stays within the
// row, and marks the hidden tabs on both sides with the overflow glyph.
func TestRenderTabStrip_LongTabsKeepsActiveVisibleAndBounded(t *testing.T) {
	tabs := []TabDefinition{{ID: "all", Kind: TabAll}}
	for i := range 8 {
		tabs = append(tabs, TabDefinition{ID: "group" + string(rune('a'+i)), Kind: TabGroup, Label: "Engineering Operations Very Long Group " + string(rune('a'+i))})
	}
	m := NewModelWithLayout(nil, nil, Layout{Tabs: tabs})
	m.activeTab = "groupd"
	strip := m.renderTabStrip(100)
	if strings.Contains(strip, "\n") || lipgloss.Width(strip) != 100 {
		t.Fatalf("tab strip width = %d, want exactly one 100-cell row: %q", lipgloss.Width(strip), strip)
	}
	if !strings.Contains(strip, " Engineering Operations Very Long Group d ") {
		t.Fatalf("active tab missing from the strip: %q", strip)
	}
	if plain := strings.TrimSpace(strip); !strings.HasPrefix(plain, "…") || !strings.HasSuffix(plain, "…") {
		t.Errorf("tab strip = %q, want overflow markers for the tabs hidden on both sides", strip)
	}
}

// TestRenderTabStrip_NarrowShowsActiveTab proves the tab strip is shown at
// every width: at 72 columns (list-only) the active view is still named, and
// a strip that fits shows every tab padded with no overflow marker.
func TestRenderTabStrip_NarrowShowsActiveTab(t *testing.T) {
	t.Parallel()
	tabs := []TabDefinition{
		{ID: "all", Kind: TabAll}, {ID: "agents", Kind: TabAgents},
		{ID: "projects", Kind: TabSource}, {ID: "zoxide", Kind: TabSource},
		{ID: "team", Kind: TabGroup, Label: "Platform engineering"},
	}
	m := NewModelWithLayout(nil, nil, Layout{Tabs: tabs, Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(72, 20))
	m.activeTab = "zoxide"
	line := viewLines(m)[0]
	if !strings.Contains(line, " zoxide ") {
		t.Errorf("tab strip at 72 columns = %q, want the active tab", line)
	}
	if got := strings.TrimSpace(line); got != "all   agents   projects   zoxide   Platform engineering" {
		t.Errorf("tab strip = %q, want every tab padded and separated by one space", got)
	}

	m.activeTab = "team"
	if got := strings.TrimSpace(m.renderTabStrip(34)); got != "…  zoxide   Platform engineering" {
		t.Errorf("narrow tab strip = %q, want the active tab with the hidden tabs marked", got)
	}
}

// TestTabStripWindow_GrowsAroundActive proves the overflow window: it keeps
// the active tab, grows toward the next tab first, reserves room for the
// overflow markers it needs, and falls back to the active tab alone.
func TestTabStripWindow_GrowsAroundActive(t *testing.T) {
	t.Parallel()
	widths := []int{5, 8, 6, 10, 7}
	for _, tc := range []struct {
		name           string
		active, budget int
		lo, hi         int
	}{
		{"everything fits", 2, 40, 0, 5},
		{"next tab first", 2, 22, 2, 4},
		{"first tab grows right", 0, 20, 0, 2},
		{"last tab grows left", 4, 20, 3, 5},
		{"active alone", 3, 4, 3, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := tabStripWindow(widths, tc.active, tc.budget, 1)
			if lo != tc.lo || hi != tc.hi {
				t.Errorf("tabStripWindow(active %d, budget %d) = [%d,%d), want [%d,%d)", tc.active, tc.budget, lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

// TestRenderRowLine_SelectedDescendantUsesCursorMarker proves the two-cell
// marker gutter belongs to the FocusList cursor. A descendant match cannot add
// a second leading marker and shift its content.
func TestRenderRowLine_SelectedDescendantUsesCursorMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDescendant,
		Expandable: true,
		Expanded:   true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, "❯ ") {
		t.Errorf("selected descendant: expected FocusList cursor prefix %q, got %q", "❯ ", got)
	}
	if strings.Contains(got, "~") {
		t.Errorf("selected descendant: descendant marker must not occupy a second leading cell: %q", got)
	}
	if strings.Contains(got, ">") {
		t.Errorf("selected descendant: old > cursor marker present, want gutter instead: %q", got)
	}
}

// TestRenderRowLine_SelectedNormalCandidateDropsOldMarker proves a selected
// direct-match candidate shows the FocusList cursor prefix, and that a
// RowCandidate never gets an expand/collapse glyph (TRL-3: per-source icons
// already differentiate row types, so the ▸/▾ marker was removed from
// workspace rows entirely — Left/Right/Enter still toggle Expandable/
// Expanded state, only the glyph is gone).
func TestRenderRowLine_SelectedNormalCandidateDropsOldMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, "❯ ") {
		t.Errorf("selected candidate: expected FocusList cursor prefix %q, got %q", "❯ ", got)
	}
	if strings.Contains(got, ">") {
		t.Errorf("selected candidate: old > cursor marker present, want gutter: %q", got)
	}
	if strings.Contains(got, "▸") || strings.Contains(got, "▾") {
		t.Errorf("selected candidate: expand/collapse glyph present, want none for RowCandidate: %q", got)
	}
}

// TestRenderRowLine_PlainSelectedUsesUnicodeGutter proves the no-color theme
// still uses the Unicode cursor when the configured icon tier is Unicode.
func TestRenderRowLine_PlainSelectedUsesUnicodeGutter(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, "❯ ") {
		t.Errorf("plain selected: expected Unicode gutter %q, got %q", "❯ ", got)
	}
	if strings.HasPrefix(got, ">") {
		t.Errorf("plain selected: ASCII gutter leaked into Unicode render: %q", got)
	}
}

// TestRenderRowLine_ASCIISelectedUsesASCIIGutter proves the ASCII icon tier
// replaces the Unicode cursor without depending on the selected theme.
func TestRenderRowLine_ASCIISelectedUsesASCIIGutter(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.Icons = IconsASCII
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, "> ") {
		t.Errorf("ASCII selected: expected ASCII gutter %q, got %q", "> ", got)
	}
	if strings.HasPrefix(got, "❯") {
		t.Errorf("ASCII selected: Unicode gutter leaked into ASCII render: %q", got)
	}
}

// TestRenderRowLine_NonSelectedUsesBlankMarker proves non-selected rows keep
// the two-cell marker gutter blank. Descendant matches cannot introduce a
// second leading marker.
func TestRenderRowLine_NonSelectedUsesBlankMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)

	// Non-selected descendant keeps the marker cell blank.
	desc := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDescendant,
		Expandable: true,
		Expanded:   true,
	}
	got := renderRowLineText(m.renderRowLine(desc, false, 40))
	if strings.HasPrefix(got, "❯") || strings.HasPrefix(got, ">") {
		t.Errorf("non-selected descendant: unexpected gutter in %q", got)
	}
	if !strings.HasPrefix(got, "  ") || strings.HasPrefix(got, "   ") {
		t.Errorf("non-selected descendant: want exactly two blank marker cells, got %q", got)
	}

	// Non-selected expandable workspace gets no expand/collapse glyph
	// (TRL-3) and keeps two blank marker cells.
	ws := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Expandable: true,
	}
	got = renderRowLineText(m.renderRowLine(ws, false, 40))
	if strings.HasPrefix(got, "❯") || strings.HasPrefix(got, ">") {
		t.Errorf("non-selected workspace: unexpected gutter in %q", got)
	}
	if strings.Contains(got, "▸") || strings.Contains(got, "▾") {
		t.Errorf("non-selected workspace: expand/collapse glyph present, want none for RowCandidate: %q", got)
	}
	if !strings.HasPrefix(got, "  ") || strings.HasPrefix(got, "   ") {
		t.Errorf("non-selected workspace: want exactly two blank marker cells, got %q", got)
	}
}

// TestRenderRowLine_MarkerGutterContract locks the cursor-gutter contract:
// every row has exactly two leading cells; it contains the configured cursor
// plus one space only for the selected FocusList row, and is blank otherwise.
// The content starts in the same column in every focus state.
func TestRenderRowLine_MarkerGutterContract(t *testing.T) {
	t.Parallel()
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "backend"}}
	for _, tt := range []struct {
		name     string
		focus    Focus
		selected bool
		icons    string
		gutter   string
	}{
		{name: "selected Unicode list row", focus: FocusList, selected: true, gutter: "❯ "},
		{name: "non-selected list row", focus: FocusList, gutter: "  "},
		{name: "selected ASCII list row", focus: FocusList, selected: true, icons: IconsASCII, gutter: "> "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, tt.focus)
			m.layout.Icons = tt.icons
			got := renderRowLineText(m.renderRowLine(row, tt.selected, 40))
			if !strings.HasPrefix(got, tt.gutter+"backend") {
				t.Fatalf("row = %q, want two-cell gutter %q immediately followed by content", got, tt.gutter)
			}
			if gotWidth := ansi.StringWidth(tt.gutter); gotWidth != 2 {
				t.Fatalf("gutter %q width = %d, want 2", tt.gutter, gotWidth)
			}
			if strings.ContainsAny(got, "▌|") {
				t.Errorf("row = %q, legacy gutter glyph rendered", got)
			}
			if tt.icons == IconsASCII && strings.HasPrefix(got, cursorGlyphUnicode) {
				t.Errorf("ASCII row = %q, Unicode cursor leaked", got)
			}
			if tt.icons != IconsASCII && strings.HasPrefix(got, cursorGlyphASCII) {
				t.Errorf("Unicode row = %q, ASCII cursor leaked", got)
			}
		})
	}
}

// TestRenderRowLine_TruncationOrder proves how a row gives up width: the
// detail shrinks from the left down to minDetailCells and is then dropped;
// the marker is capped at 30% of the row and goes when the label would
// otherwise shrink below minLabelCells; only then is the label itself
// truncated, keeping its start (it is not a path). Expected values include
// the two-cell gutter; the zoxide icon is blanked to keep them short.
func TestRenderRowLine_TruncationOrder(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) { p.Zoxide.Icon = "" })
	pinned := func(path string) Row {
		c := source.Candidate{Path: path, Source: config.SourceZoxide, Missing: true}
		m.rankingSnapshot = m.rankingSnapshot.WithPinned(ranking.PinKey(c), true)
		return Row{Kind: RowCandidate, Candidate: c}
	}
	plain := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/workspace/services/catalog/filename.go"}}
	short := pinned("/srv/projects/shep")
	long := pinned("/srv/a-very-long-name")
	longer := pinned("/srv/a-very-very-long-folder-name")
	for _, tt := range []struct {
		name  string
		row   Row
		width int
		want  string
	}{
		{"everything fits", short, 40, "  shep  /srv/projects" + strings.Repeat(" ", 10) + "missing ★"},
		{"detail shrinks from the left", plain, 40, "  filename.go  …rkspace/services/catalog"},
		{"detail keeps its minimum", plain, 21, "  filename.go  …talog"},
		{"detail below its minimum is dropped", plain, 20, "  filename.go"},
		{"detail shrinks beside the marker", short, 24, "  shep  …jects missing ★"},
		{"detail dropped before the marker", short, 23, "  shep" + strings.Repeat(" ", 8) + "missing ★"},
		{"label shrinks while it keeps its minimum beside the marker", longer, 30, "  a-very-very-long-… missing ★"},
		{"marker dropped before the label goes below its minimum", long, 22, "  a-very-long-name"},
		{"marker dropped at the label's minimum", long, 19, "  a-very-long-name"},
		{"label keeps its start", plain, 12, "  filename.…"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := renderRowLineText(m.renderRowLine(tt.row, false, tt.width))
			if got := ansi.StringWidth(line); got != tt.width {
				t.Errorf("row width = %d, want %d", got, tt.width)
			}
			if got := strings.TrimRight(line, " "); got != tt.want {
				t.Errorf("renderRowLine(width=%d) = %q, want %q", tt.width, got, tt.want)
			}
		})
	}
}

func TestTruncateFromLeftToWidth_IsRuneSafe(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		input string
		width int
		want  string
	}{
		{name: "Greek runes", input: "αβγδε", width: 3, want: "…δε"},
		{name: "wide rune", input: "中ab", width: 3, want: "…ab"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateFromLeftToWidth(tt.input, tt.width); got != tt.want {
				t.Errorf("truncateFromLeftToWidth(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

// TestRenderRowLine_TruncationMatchesCursorAtSameInnerWidth proves a
// cursor and non-cursor row given the IDENTICAL total width produce
// identical content once the two-cell gutter is stripped.
func TestRenderRowLine_TruncationMatchesCursorAtSameInnerWidth(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/workspace/services/catalog/filename.go"}}
	const width = 12

	nonCursor := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, width)), " ")
	nonCursor = strings.TrimPrefix(nonCursor, strings.Repeat(" ", cursorPrefixWidth))
	cursor := strings.TrimRight(renderRowLineText(m.renderRowLine(row, true, width)), " ")
	cursor = strings.TrimPrefix(cursor, cursorGlyphUnicode+" ")
	if cursor != nonCursor {
		t.Errorf("cursor path = %q, want non-cursor path %q at the same total width", cursor, nonCursor)
	}
	if cursor != "filename.…" {
		t.Errorf("cursor label = %q, want its start and a trailing ellipsis", cursor)
	}
}

// --- Bug 1: left truncation must never eat a row's own icon ---

// TestRenderRowLine_LeftTruncationPreservesIcon proves a RowCandidate's
// icon is a fixed, non-truncatable prefix: at a width too narrow for the
// full label, the icon glyph survives fully intact and only the label after
// it is truncated — never inside/eating the icon itself (the confirmed bug:
// truncateFromLeftToWidth used to cut the marker+icon prefix first, before
// touching a single label character).
func TestRenderRowLine_LeftTruncationPreservesIcon(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	const icon = "\uf07b" // a representative single-cell nerd font glyph
	row := Row{
		Kind:      RowCandidate,
		Candidate: source.Candidate{Path: "/workspace/services/catalog/very-long-filename.go", Icon: icon},
	}

	const width = 16
	got := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, width)), " ")

	// The two-cell marker gutter precedes the row's protected icon prefix.
	wantPrefix := strings.Repeat(" ", cursorPrefixWidth) + icon + " "
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("truncated row = %q, want it to start with the intact icon prefix %q (icon must survive left truncation)", got, wantPrefix)
	}
	rest := strings.TrimPrefix(got, wantPrefix)
	if rest != "very-long-f…" {
		t.Errorf("truncated row content = %q, want the label's start right after the icon prefix", rest)
	}
	if strings.Count(got, icon) != 1 {
		t.Errorf("truncated row = %q, want exactly one intact icon glyph, got %d", got, strings.Count(got, icon))
	}
}

// TestRenderRowLine_LeftTruncationPreservesWideIcon is the rune/width-safety
// companion: a wide (2-cell) emoji icon must never be partially cut, and the
// total rendered width must still respect the requested width exactly.
func TestRenderRowLine_LeftTruncationPreservesWideIcon(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	const icon = "\U0001F525" // 🔥, a 2-cell-wide emoji glyph
	row := Row{
		Kind:      RowCandidate,
		Candidate: source.Candidate{Path: "/workspace/services/catalog/very-long-filename.go", Icon: icon},
	}

	const width = 18
	got := renderRowLineText(m.renderRowLine(row, false, width))
	if lipglossWidth(got) != width {
		t.Fatalf("rendered width = %d, want exactly %d: %q", lipglossWidth(got), width, got)
	}
	trimmed := strings.TrimRight(got, " ")
	wantPrefix := strings.Repeat(" ", cursorPrefixWidth) + icon + " "
	if !strings.HasPrefix(trimmed, wantPrefix) {
		t.Fatalf("truncated row = %q, want it to start with the intact wide icon prefix %q", trimmed, wantPrefix)
	}
	if strings.Count(trimmed, icon) != 1 {
		t.Errorf("truncated row = %q, want exactly one intact icon glyph, got %d", trimmed, strings.Count(trimmed, icon))
	}
}

// TestRenderRowLine_LeftTruncationPreservesTabTreeGlyph proves the same
// protection extends to a RowTab's kindPrefix (tree glyph + indent): the
// composeMultiPartRow path (RowTab/RowPane carry a secondary part) must also
// never truncate its own structural prefix away.
func TestRenderRowLine_LeftTruncationPreservesTabTreeGlyph(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{
		Kind:   RowTab,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "a-very-long-tab-label-that-needs-truncating",
			Path:  "/workspace/services/catalog",
		},
	}

	const width = 20
	got := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, width)), " ")
	// two-cell marker gutter + kindPrefix's tree glyph + the tab icon — the
	// whole thing is the row's protected fixed prefix; only the label/path body
	// after it may be truncated.
	wantPrefix := strings.Repeat(" ", cursorPrefixWidth) + m.kindPrefix(row) + set.TabIcon + " "
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("truncated tab row = %q, want it to start with the intact tree-glyph prefix %q", got, wantPrefix)
	}
}

// TestComposeMultiPartRow_PrimaryTruncationPreservesIcon proves a RowPane's
// agent-status icon (also rendered via composeMultiPartRow, since a RowPane
// always carries a secondary pane-id part) survives left truncation of its
// own primary (path) text.
func TestComposeMultiPartRow_PrimaryTruncationPreservesIcon(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/workspace/services/catalog/very/long/path/segment",
			Meta: map[string]string{"agent_status": "idle"},
		},
	}

	const width = 20
	got := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, width)), " ")
	if !strings.Contains(got, set.StatusIdle) {
		t.Fatalf("truncated pane row = %q, want the intact idle status icon %q", got, set.StatusIdle)
	}
	if strings.Count(got, set.StatusIdle) != 1 {
		t.Errorf("truncated pane row = %q, want exactly one status icon, got %d", got, strings.Count(got, set.StatusIdle))
	}
}

// lipglossWidth is a tiny local alias kept next to these tests for
// readability; it measures the visible cell width of s (ANSI/wide-rune
// aware), matching what renderRowLine's own width budgeting targets.
func lipglossWidth(s string) int {
	return ansi.StringWidth(s)
}

// --- Bug 2: cursor row content must not shift relative to non-cursor rows ---

// TestRenderRowLine_NonCursorReservesSameGutterAsCursorInFocusList proves
// that in FocusList, EVERY row — cursor or not — reserves the identical
// cursorPrefixWidth leading gutter, so a row's own content (icon/label)
// starts at the exact same column whether or not the cursor currently sits
// on it. Before the fix, only the cursor row reserved this gutter, so
// selecting a row visibly shifted its content cursorPrefixWidth cells to the
// right (and back left when the cursor moved off).
func TestRenderRowLine_NonCursorReservesSameGutterAsCursorInFocusList(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "backend"}}
	const width = 40

	cursorLine := renderRowLineText(m.renderRowLine(row, true, width))
	nonCursorLine := renderRowLineText(m.renderRowLine(row, false, width))

	if got, want := ansi.StringWidth(cursorLine), ansi.StringWidth(nonCursorLine); got != want {
		t.Fatalf("cursor line width = %d, non-cursor line width = %d; want identical total width %d", got, want, width)
	}

	cursorContent := strings.TrimPrefix(cursorLine, cursorGlyphUnicode+" ")
	if cursorContent == cursorLine {
		t.Fatalf("setup: cursor line %q did not start with the expected cursor glyph prefix", cursorLine)
	}
	blankGutter := "  "
	nonCursorContent := strings.TrimPrefix(nonCursorLine, blankGutter)
	if nonCursorContent == nonCursorLine {
		t.Fatalf("non-cursor row = %q, want it to reserve a %d-cell blank gutter in FocusList so its content column matches the cursor row's", nonCursorLine, cursorPrefixWidth)
	}
	if cursorContent != nonCursorContent {
		t.Errorf("cursor content = %q, non-cursor content = %q; want them identical (same column, same text) so the cursor never shifts row content", cursorContent, nonCursorContent)
	}
}

// --- Row width and selection invariants ---

// newRenderTestModelWidth clones the render-test model with a width override.
func newRenderTestModelWidth(themeName string, width int) Model {
	m := newRenderTestModel(themeName, FocusList)
	m.width = width
	return m
}

// TestRenderRowLine_ReclaimsBadgeWidth proves source-name badges no longer
// consume row width and configured icons remain the source distinction.
func TestRenderRowLine_ReclaimsBadgeWidth(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWidth(ThemeMocha, 120)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/workspace/services/catalog/filename.go", Source: config.SourceHerdr}}
	got := stripNonSGRANSI(m.renderRowLine(row, false, 40))
	if strings.Contains(got, "HERDR") {
		t.Errorf("row = %q, source badge must be absent", got)
	}
	if icon := config.DefaultPresentations("").Herdr.Icon; !strings.HasPrefix(got, "  "+icon+" ") {
		t.Errorf("row = %q, the source icon %q must remain", got, icon)
	}
}

// TestRenderRowLine_SelectionVisibleInPlain proves the plain theme keeps the
// visible selection marker with source badge chrome removed.
func TestRenderRowLine_SelectionVisibleInPlain(t *testing.T) {
	t.Parallel()
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Path: "/a", Source: config.SourceHerdr}}
	m := newRenderTestModelWidth(ThemePlain, 120)
	got := stripNonSGRANSI(m.renderRowLine(row, true, 120))
	if !strings.HasPrefix(got, "❯ ") {
		t.Errorf("plain selected row = %q, want the ❯ gutter", got)
	}
	if strings.Contains(got, "HERDR") {
		t.Errorf("plain selected row = %q, source badge must be absent", got)
	}

	m.layout.Icons = IconsASCII
	gotASCII := stripNonSGRANSI(m.renderRowLine(row, true, 120))
	if !strings.HasPrefix(gotASCII, "> ") {
		t.Errorf("ASCII selected row = %q, want the > gutter", gotASCII)
	}
}

// TestRenderRowLine_ASCIIEmitsNoUnicodeOnlyGlyphs proves the ASCII icon tier
// never renders a Unicode-only glyph in the row affordances: the pin star
// degrades to "*" and the group chevron to ">".
func TestRenderRowLine_ASCIIEmitsNoUnicodeOnlyGlyphs(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWidth(ThemeMocha, 120)
	m.layout.Icons = IconsASCII
	m = m.withPresentation(nil)
	group := source.Candidate{Label: "team", Path: "/a", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	m.rankingSnapshot = m.rankingSnapshot.WithPinned(ranking.PinKey(group), true)
	got := stripNonSGRANSI(m.renderRowLine(Row{Kind: RowCandidate, Candidate: group}, false, 120))
	if reNonASCII.MatchString(got) {
		t.Errorf("ASCII row = %q, contains a non-ASCII glyph", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, " "), "* >") {
		t.Errorf("ASCII row = %q, want the * pin and > group accessories", got)
	}
}
