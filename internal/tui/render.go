package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/rowformat"
	"github.com/tranceh2/shep/internal/source"
)

// minPreviewWidth is the terminal width below which the preview panel is
// hidden entirely (modeListOnly) to avoid breaking the layout. Also doubles
// as the AUTO responsive mode's mediumBreakpoint (layout_responsive.go).
// The visual redesign named the preview's usable window as width >= 88 AND
// height >= 12: below either bound a side-by-side preview only starves the
// list, so 100x10 is now list-only.
const minPreviewWidth = 88

// minPreviewHeight is the terminal height below which the preview panel is
// hidden entirely, mirroring minPreviewWidth on the row axis. Raised from 8
// to 12 by the visual redesign (preview requires height >= 12).
const minPreviewHeight = 12

// headerWideBreakpoint is the terminal width at/above which renderHeader
// renders its two-line wide header (\"SHEP  Switch workspace\" orientation
// line over the search line); below it a compact single search line with the
// right-aligned count.
const headerWideBreakpoint = 100

// minList/minPrev are the width-axis minimum pane floors (columns) for a
// wide (side-by-side) layout.
const minList = 20
const minPrev = 10

// footerSeparator separates shortcut groups without suggesting button chrome.
const footerSeparator = "   "

// View renders the responsive two-pane (or single-pane) UI plus a compact,
// contextual shortcut footer line. The active mode (m.mode, computed on
// tea.WindowSizeMsg — see nextResponsiveMode) decides the shape: modeWide
// splits list/preview side by side, modeListOnly shows only the list. The "?"
// help overlay (m.focus == FocusHelp) replaces the body entirely when active.
func (m Model) View() string {
	if m.focus == FocusHelp {
		return lipgloss.JoinVertical(lipgloss.Left, m.renderHelp(), m.renderFooter())
	}

	g := m.geometry()
	header := m.renderHeader(m.width)
	footer := m.renderFooter()

	var body string
	switch m.mode {
	case modeListOnly:
		body = m.paneBoxStyle(g.PaneOuterHeight, true).Render(m.renderList(m.paneContentWidth(m.width)))
	default: // modeWide, or "" (unknown/headless default — see model.go)
		listPane := m.paneBoxStyle(g.PaneOuterHeight, m.focus == FocusList).Render(m.renderList(m.paneContentWidth(g.ListWidth)))
		previewStyle := m.paneBoxStyle(g.PaneOuterHeight, m.focus == FocusPreview)
		previewPane := lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderPreviewTopBorder(previewStyle, g.PreviewWidth, m.previewTopBorderText()),
			previewStyle.BorderTop(false).Render(m.viewport.View()),
		)
		body = lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// renderList draws the visible grouped/nested rows with a cursor marker,
// scrolled to keep the cursor in view. The query line that used to live here
// was moved to the full-width header (renderHeader) in Phase 3; the body now
// starts directly with rows. Every rendered line is explicitly padded to
// width so the list pane never drifts from the split computed by View.
func (m Model) renderList(width int) string {
	if len(m.rows) == 0 {
		var lines []string
		for i, line := range m.emptyStateLines() {
			if i > 1 {
				continue // never exceed the reserved two-state-line budget
			}
			lines = append(lines, m.styles.mutedStyle.Width(width).Render(truncateToWidth(line, width)))
		}
		return strings.Join(lines, "\n")
	}
	full := m.rows
	g := m.geometry()
	maxRows := g.ListInnerRows
	if maxRows <= 0 {
		maxRows = len(full)
	}
	offset := 0
	visible := full
	if len(full) > maxRows && maxRows > 2 {
		offset = clamp(m.cursor-maxRows/2, 0, len(full)-maxRows)
		visible = full[offset:]
		if len(visible) > maxRows {
			visible = visible[:maxRows]
		}
	}
	lines := make([]string, len(visible))
	for i, row := range visible {
		lines[i] = m.renderRowLine(row, i+offset == m.cursor, width)
	}
	return strings.Join(lines, "\n")
}

// emptyStateLines picks the right "nothing to show" content: a query that
// matched nothing, no candidates at all (every source came back empty), vs.
// sources still loading asynchronously. Each state is two lines of structured
// text (message + what-to-do hint) replacing the old blank bordered pane.
func (m Model) emptyStateLines() []string {
	if m.query != "" {
		return []string{
			"No matches for \"" + m.query + "\"",
			"esc clear query",
		}
	}
	def := scopeDefinitionFor(m.scope)
	if def.EmptyState != nil {
		return def.EmptyState(m)
	}
	return []string{"No workspaces yet"}
}

// headerLineCount reports how many text lines renderHeader emits at the
// model's current width: 2 at/above headerWideBreakpoint, 1 below. View uses
// it for the body's vertical budget.
func (m Model) headerLineCount() int {
	return headerLineCountForWidth(m.width)
}

// headerCountText resolves the header's result-count text — the same states
// the pre-redesign header rendered, moved to its right-aligned slot: "M of
// N" when filtered, the per-source breakdown when a query matched more than
// one source, loading variants while producers stream, "N candidates"
// unfiltered.
func (m Model) headerCountText() string {
	if m.scope == ScopeAgents {
		n := len(m.rows)
		counts := m.AgentCounts()
		if m.query != "" {
			return strconv.Itoa(n) + " of " + strconv.Itoa(counts.Total) + " agents"
		}
		if n == 1 {
			return "1 agent"
		}
		return strconv.Itoa(n) + " agents"
	}
	n := len(m.baseFlatCandidates())
	switch {
	case m.loadingCandidates && n == 0:
		return "loading…"
	case m.loadingCandidates:
		if m.query != "" && len(m.visibleSourceCounts()) > 1 {
			return formatSourceCounts(m.visibleSourceCounts())
		} else if m.query != "" {
			return strconv.Itoa(m.visibleTopLevelCount()) + " of " + strconv.Itoa(n)
		}
		return strconv.Itoa(n) + " candidates (loading…)"
	case m.query != "":
		// Querying always uses the compact total form. The row icons distinguish
		// sources; the header's job is only to report N of M candidates.
		return strconv.Itoa(m.visibleTopLevelCount()) + " of " + strconv.Itoa(n)
	default:
		return strconv.Itoa(n) + " candidates"
	}
}

// renderHeader builds the app/search header above the body. Its search token
// remains bracketed because it is a search affordance, not a footer shortcut.
//
// Wide (>= headerWideBreakpoint): two lines —
//
//	SHEP  Switch workspace                                N candidates
//	⌕  query-or-placeholder
//
// Line 1 is a small orientation line (a text label, deliberately not a logo
// or stats bar): "SHEP" in the text role + bold, "Switch workspace" muted,
// with the result count right-aligned muted. Line 2 is the search line: the
// search prompt glyph (⌕ / >) muted, then the live query (accent+bold) or the muted
// placeholder at empty query.
//
// Compact (< headerWideBreakpoint): a single "⌕ query-or-placeholder" line
// with the count right-aligned muted.
//
// The query is what the user is actively reading and must never be silently
// cut in favor of the count: when the two cannot share a line, the count is
// dropped.
func (m Model) renderHeader(width int) string {
	count := m.styles.mutedStyle.Render(m.headerCountText())
	var query string
	if m.query != "" {
		query = m.styles.queryStyle.Render(m.query)
	} else {
		placeholder := scopeDefinitionFor(m.scope).Placeholder
		if placeholder == "" {
			placeholder = "type to filter…"
		}
		query = m.styles.mutedStyle.Render(placeholder)
	}
	prompt := m.icons().SearchPrompt
	if prompt == "" {
		prompt = "⌕"
	}
	search := m.styles.mutedStyle.Render(prompt) + " " + query

	if width < headerWideBreakpoint {
		if width < lipgloss.Width(search)+1+lipgloss.Width(count) {
			return lipgloss.NewStyle().Width(width).Render(truncateToWidth(search, width))
		}
		return lipgloss.NewStyle().Width(width).Render(rightPadToWidth(search, count, width))
	}

	brand := m.styles.keycapStyle.Render("SHEP") + "  " + m.renderScopeTabs()
	top := rightPadToWidth(brand, count, width)
	return lipgloss.NewStyle().Width(width).Render(top) + "\n" +
		lipgloss.NewStyle().Width(width).Render(rightPadToWidth(search, "", width))
}

func (m Model) renderScopeTabs() string {
	counts := m.AgentCounts()
	var tabs []string
	for _, def := range scopeRegistry {
		isActive := m.scope == def.ID
		label := def.Label(m)
		if isActive {
			label = "● " + label
		}
		var rendered string
		if isActive {
			if def.ID == ScopeAgents && counts.Blocked > 0 {
				rendered = m.styles.statusBlockedStyle.Render(label)
			} else {
				rendered = m.styles.keycapStyle.Render(label)
			}
		} else {
			if def.ID == ScopeAgents && counts.Blocked > 0 {
				rendered = m.styles.statusBlockedStyle.Render(label)
			} else {
				rendered = m.styles.mutedStyle.Render(label)
			}
		}
		tabs = append(tabs, rendered)
	}
	return strings.Join(tabs, "   ")
}

// sourceCount is one source's visible top-level match count, used by
// visibleSourceCounts/formatSourceCounts for the header's per-source
// breakdown.
type sourceCount struct {
	source string
	count  int
}

// visibleSourceCounts returns, for a non-empty query, the visible RowCandidate
// match count per source that contributed at least one match, in the
// resolved source order (see resolvedSourceOrder) — so the breakdown reads
// left-to-right in the same order the rows themselves appear. Empty for an
// empty query (every candidate is shown; there is nothing to break down).
func (m Model) visibleSourceCounts() []sourceCount {
	if m.query == "" {
		return nil
	}
	counts := make(map[string]int)
	for _, r := range m.rows {
		if r.Kind == RowCandidate {
			counts[r.Candidate.Source]++
		}
	}
	var out []sourceCount
	for _, src := range m.resolvedSourceOrder() {
		if n := counts[src]; n > 0 {
			out = append(out, sourceCount{source: src, count: n})
		}
	}
	return out
}

// resolvedSourceOrder returns m.sourceOrder when configured, else
// defaultSourceOrder (rows.go) — the same fallback buildRows' own
// effectiveSourceOrder applies, kept as a Model-level accessor for callers
// (like visibleSourceCounts) that only have a Model, not a rowBuildInput.
func (m Model) resolvedSourceOrder() []string {
	if len(m.sourceOrder) > 0 {
		return m.sourceOrder
	}
	return defaultSourceOrder
}

// formatSourceCounts joins a per-source breakdown into a single compact
// header segment, e.g. "herdr 2, zoxide 1".
func formatSourceCounts(counts []sourceCount) string {
	parts := make([]string, len(counts))
	for i, c := range counts {
		parts[i] = c.source + " " + strconv.Itoa(c.count)
	}
	return strings.Join(parts, ", ")
}

// visibleTopLevelCount counts visible RowCandidate rows only (top-level
// candidates), excluding synthesized RowTab/RowPane descendants — the same
// unit as the denominator (baseFlatCandidates), so the header's "M of N"
// compares like for like. A descendant-only query (e.g. a tab label match
// under a workspace) shows 1, not 1+children.
func (m Model) visibleTopLevelCount() int {
	n := 0
	for _, r := range m.rows {
		if r.Kind == RowCandidate {
			n++
		}
	}
	return n
}

// rightPadToWidth pads spaces between left and right so the combined line
// occupies exactly width visible cells, right-aligning right.
func rightPadToWidth(left, right string, width int) string {
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 0 {
		pad = 0
	}
	return left + strings.Repeat(" ", pad) + right
}

// cursorGlyphUnicode and cursorGlyphASCII are the FocusList-only cursor
// markers. cursorPrefixWidth reserves two stable leading cells for every row:
// the selected row uses its marker plus one space, and other rows use two
// blanks.
const (
	cursorGlyphUnicode = "❯"
	cursorGlyphASCII   = ">"
	cursorPrefixWidth  = 2
)

// rowPart is one styled segment of a row line. Most rows have a single part;
// source rows may use a second dim part when a renderer supplies secondary
// context.
type rowPart struct {
	text           string
	style          lipgloss.Style
	rendered       bool
	rawText        string
	highlighted    []bool
	highlightStyle lipgloss.Style
	// fixedPrefixRunes is the codepoint count of this part's leading
	// structural prefix — kindPrefix's tree glyph/indent/active-marker slot and
	// the row's own icon (source icon, agent-status icon, or tab number) —
	// never the row's actual label/path text. Left truncation (truncateFromLeftPreservingPrefix,
	// rowPart.renderHighlighted) must never cut into this prefix, so a
	// row's icon always survives even when its label/path is severely
	// truncated (TRL bug: a long zoxide/project path used to eat the row's
	// own icon before touching a single label character). Zero for parts
	// with no protected prefix, e.g. the secondary part.
	fixedPrefixRunes int
}

// renderRowLine renders one row with a stable two-cell marker gutter. The
// selected FocusList row fills it with the configured cursor plus one space;
// all other rows keep it blank while preserving their own text styles. Source
// identity is communicated by the row icon, so the full width is available to
// the label/path text and no redundant right-side badge slot is reserved.
func (m Model) renderRowLine(row Row, isCursor bool, width int) string {
	parts := m.rowLineParts(row)
	if isCursor {
		return m.renderSelectedFromParts(parts, width)
	}
	return m.renderUnselectedFromParts(parts, width)
}

// rowLineParts builds the styled parts for one row, independent of
// selection: depth/kind-prefixed primary text for a candidate/tab/pane — plus
// an optional dim secondary (a RowPane's own pane id, see rowDisplayText) for
// rows that carry one. The marker belongs exclusively to the leading gutter
// assembled by the render functions below; adding another marker here would
// restore the stale two-cell layout and shift content by focus state.
func (m Model) rowLineParts(row Row) []rowPart {
	style := m.styles.rowStyle
	if row.Match == MatchDescendant {
		style = m.styles.rowDescendantStyle
	}
	primary, prefixRunes := m.rowPrimaryText(row)
	secondary := m.rowSecondaryText(row)
	primaryText := primary
	fixedPrefixRunes := prefixRunes
	primaryPart := rowPart{text: primaryText, style: style, fixedPrefixRunes: fixedPrefixRunes}
	if row.Kind == RowCandidate && row.Match == MatchDirect && len(row.MatchedIndexes) > 0 {
		if rawText, highlighted, ok := m.highlightedRowRunes(row, fixedPrefixRunes, primaryText); ok {
			primaryPart = rowPart{
				style:            style,
				rendered:         true,
				rawText:          rawText,
				highlighted:      highlighted,
				highlightStyle:   m.styles.queryStyle,
				fixedPrefixRunes: fixedPrefixRunes,
			}
		}
	}
	parts := []rowPart{primaryPart}
	if secondary != "" {
		parts = append(parts, rowPart{text: "  " + secondary, style: m.styles.mutedStyle})
	}
	return parts
}

// highlightedRowRunes reports which runes of rawText (a RowCandidate's full
// marker+prefix+visible row text) should render with the query accent, and
// returns rawText itself unchanged as the first result for convenience.
//
// It deliberately does NOT reuse row.MatchedIndexes for this: those indexes
// are scored by fuzzyMatch against candidateHaystack (Label+" "+Path) for
// visibility and ranking. Highlighting must rescore the text the row actually
// renders, which is now label-first for the built-in directory sources.
func (m Model) highlightedRowRunes(row Row, fixedPrefixRunes int, rawText string) (string, []bool, bool) {
	runes := []rune(rawText)
	if fixedPrefixRunes >= len(runes) {
		return "", nil, false
	}
	visible := string(runes[fixedPrefixRunes:])
	_, visibleIndexes := fuzzy.Score(m.query, visible)
	if len(visibleIndexes) == 0 {
		return "", nil, false
	}

	matched := make(map[int]bool, len(visibleIndexes))
	for _, index := range visibleIndexes {
		if index >= 0 && index < len(runes)-fixedPrefixRunes {
			matched[fixedPrefixRunes+index] = true
		}
	}
	if len(matched) == 0 {
		return "", nil, false
	}

	highlighted := make([]bool, len(runes))
	for index := range runes {
		highlighted[index] = matched[index]
	}
	return rawText, highlighted, true
}

// renderUnselectedFromParts renders a non-cursor row from its parts, padded
// to width. Every focus state reserves a blank cursorPrefixWidth-cell gutter
// so content begins in one stable column. Single-part rows use the simple
// path; multi-part rows compose the primary and secondary with truncation
// priority (secondary dropped first).
func (m Model) renderUnselectedFromParts(parts []rowPart, width int) string {
	prefixWidth := cursorPrefixWidth
	contentW := width - prefixWidth
	if contentW < 1 {
		contentW = 1
	}
	gutter := strings.Repeat(" ", prefixWidth)

	if len(parts) == 1 {
		p := parts[0]
		if p.rendered {
			return gutter + lipgloss.NewStyle().Width(contentW).Render(p.renderHighlighted(contentW, lipgloss.Style{}, false))
		}
		return gutter + p.style.Width(contentW).Render(truncateFromLeftPreservingPrefix(p.text, p.fixedPrefixRunes, contentW))
	}
	return gutter + m.composeMultiPartRow(parts, contentW, lipgloss.Style{}, false)
}

// renderSelectedFromParts renders the selected row with the selection
// treatment: a two-cell gutter leading every row, a FocusList-only cursor plus
// trailing space, selectedSurface across the pane width, and the row's own
// text styles preserved.
func (m Model) renderSelectedFromParts(parts []rowPart, width int) string {
	gutterStyle := m.styles.cursorGutterStyle
	surfaceStyle := m.styles.cursorSurfaceStyle
	if m.focus != FocusList {
		gutterStyle = m.styles.cursorGutterUnfocusedStyle
		surfaceStyle = m.styles.cursorSurfaceUnfocusedStyle
	}

	prefixWidth := cursorPrefixWidth
	gutterText := strings.Repeat(" ", prefixWidth)
	if m.focus == FocusList {
		glyph := cursorGlyphUnicode
		if m.icons().Name == IconsASCII {
			glyph = cursorGlyphASCII
		}
		gutterText = glyph + " "
	}
	gutter := gutterStyle.Render(gutterText)

	contentW := width - prefixWidth
	if contentW < 1 {
		contentW = 1
	}
	if len(parts) == 1 {
		p := parts[0]
		if p.rendered {
			content := p.renderHighlighted(contentW, surfaceStyle, true)
			return gutter + applySurface(lipgloss.NewStyle(), surfaceStyle).Width(contentW).Render(content)
		}
		styled := applySurface(p.style, surfaceStyle).Width(contentW).Render(truncateFromLeftPreservingPrefix(p.text, p.fixedPrefixRunes, contentW))
		return gutter + styled
	}
	styled := m.composeMultiPartRow(parts, contentW, surfaceStyle, true)
	return gutter + styled
}

// renderHighlighted renders a highlighted (p.rendered) part's raw runes with
// per-rune base/highlight styling, protecting p.fixedPrefixRunes from left
// truncation: only the label content AFTER that prefix is ever shortened,
// and the ellipsis lands immediately after the prefix — never inside it
// (TRL bug: the old whole-string truncateFromLeftToWidth call ate the
// marker/icon prefix before a single label rune). When hasSurface is true,
// surface is merged into every rune's own style (the cursor row's selection
// tint); per-rune lipgloss renders reset terminal state between runes, so an
// outer surface alone would be lost after the first one.
func (p rowPart) renderHighlighted(maxW int, surface lipgloss.Style, hasSurface bool) string {
	runes := []rune(p.rawText)
	prefixRunes := p.fixedPrefixRunes
	if prefixRunes > len(runes) {
		prefixRunes = len(runes)
	}
	prefixText := string(runes[:prefixRunes])
	contentRunes := runes[prefixRunes:]
	contentHighlighted := p.highlighted[prefixRunes:]

	styleFor := func(s lipgloss.Style) lipgloss.Style {
		if hasSurface {
			return applySurface(s, surface)
		}
		return s
	}

	build := func(startIdx int, ellipsis bool) string {
		var b strings.Builder
		b.WriteString(styleFor(p.style).Render(prefixText))
		if ellipsis {
			b.WriteString(styleFor(p.style).Render("…"))
		}
		for i := startIdx; i < len(contentRunes); i++ {
			style := p.style
			if contentHighlighted[i] {
				style = p.highlightStyle
			}
			b.WriteString(styleFor(style).Render(string(contentRunes[i])))
		}
		return b.String()
	}

	if maxW <= 0 {
		return build(0, false)
	}
	budget := maxW - ansi.StringWidth(prefixText)
	if budget < 1 {
		// Not enough room even for the fixed prefix: degrade to rendering
		// everything rather than producing an empty/garbled row.
		return build(0, false)
	}

	contentText := string(contentRunes)
	truncatedContent := truncateFromLeftToWidth(contentText, budget)
	if truncatedContent == contentText {
		return build(0, false)
	}
	survivingRunes := []rune(strings.TrimPrefix(truncatedContent, "…"))
	startIdx := len(contentRunes) - len(survivingRunes)
	return build(startIdx, true)
}

// composeMultiPartRow renders a multi-part row (primary + secondary) at the
// given width. The secondary is truncated/dropped first; the primary (label)
// survives. When hasSurface is true, each part and the padding get the
// selection surface background (or faint for no-color) merged into their own
// style so the background is continuous across the whole line.
func (m Model) composeMultiPartRow(parts []rowPart, width int, surface lipgloss.Style, hasSurface bool) string {
	primary := parts[0]
	secondary := parts[1]

	pStyle := primary.style
	sStyle := secondary.style
	padStyle := lipgloss.NewStyle()
	if hasSurface {
		pStyle = applySurface(pStyle, surface)
		sStyle = applySurface(sStyle, surface)
		padStyle = applySurface(padStyle, surface)
	}

	primaryW := lipgloss.Width(primary.text)
	if primaryW >= width {
		return pStyle.Width(width).Render(truncateFromLeftPreservingPrefix(primary.text, primary.fixedPrefixRunes, width))
	}

	secondaryW := lipgloss.Width(secondary.text)
	if primaryW+secondaryW <= width {
		line := pStyle.Render(primary.text) + sStyle.Render(secondary.text)
		return padStyle.Width(width).Render(line)
	}

	remaining := width - primaryW
	if remaining < 1 {
		return padStyle.Width(width).Render(pStyle.Render(primary.text))
	}
	truncSecondary := truncateToWidth(secondary.text, remaining)
	line := pStyle.Render(primary.text) + sStyle.Render(truncSecondary)
	return padStyle.Width(width).Render(line)
}

// applySurface merges the selection surface treatment into ownStyle: a
// selectedSurface/unfocusedSurface background for color themes, or a faint
// emphasis for the no-color theme (the closest structural attribute to a
// "tinted row background" lipgloss offers without color — see newPalette's
// Plain cursorSurfaceStyle). The row's own foreground/bold/italic survive
// because Background/Faint do not clobber them.
func applySurface(ownStyle, surface lipgloss.Style) lipgloss.Style {
	noColor := lipgloss.NoColor{}
	if bg := surface.GetBackground(); bg != noColor {
		return ownStyle.Background(bg)
	}
	if surface.GetFaint() {
		return ownStyle.Faint(true)
	}
	return ownStyle
}

// kindPrefix returns the depth indent + kind marker prefix for a row, drawn
// from the Model's configured icon fallback tier (see icons.go): a
// sibling-sensitive tree marker for a RowTab/RowPane, "" otherwise. The
// indent is repeated 2-space per depth level.
//
// TRL-3: a RowCandidate (top-level workspace row) never gets an
// expand/collapse glyph (the old ▸/▾ IconSet.ExpandClosed/ExpandOpen) — the
// per-source icon (see rowDisplayText's c.Icon handling) already
// differentiates row types, so the glyph was redundant and explicitly
// removed. Left/Right/Enter still toggle Row.Expandable/Expanded (see
// keys.go's toggleExpand); only the rendered glyph is gone. ExpandOpen/
// ExpandClosed remain declared on IconSet (icons_test.go still exercises
// them as part of the resolved tier's data) but no production code path
// surfaces them anymore.
//
// TRL-4: the active-focus marker (IconSet.ActiveMarker, see isActiveFocusRow)
// gets a FIXED-width leading slot on every RowTab/RowPane — blank space on
// every sibling except the one row that identifies where shep is currently
// running. Filling that slot with the marker only on the active row (with
// no reserved space on the others) would shift just that one row's tree
// glyph (├─/└─) rightward relative to its siblings, breaking the vertical
// kindPrefix returns the depth indent + kind marker prefix for a row, drawn
// before its icon/label. Flat scope views (like the Agents scope) synthesize
// depth-zero pane rows (Depth == 0); depth-zero rows skip the entire tree-decoration
// branch and return an empty prefix, matching top-level candidate rows. Tree rows
// (Depth >= 1) render their active marker gutter, ancestor column, and branch glyph.
// lipgloss.Width (not len/rune count) measures the slot so a multi-byte marker glyph
// still reserves the correct terminal cell width.
func (m Model) kindPrefix(row Row) string {
	if row.Depth == 0 {
		return ""
	}
	set := m.icons()
	prefix := ""
	indentDepth := row.Depth
	switch row.Kind {
	case RowTab, RowPane:
		treeGlyph := set.TreeMid
		if row.IsLast {
			treeGlyph = set.TreeLast
		}
		activeSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
		if m.isActiveFocusRow(row) {
			activeSlot = set.ActiveMarker + " "
		}
		prefix += activeSlot
		// A RowPane's ancestor connector is placed after the fixed active
		// marker gutter, in the same column as its parent tab's branch. Its
		// own branch then follows one level deeper. Keeping the same-width
		// blank under a final tab preserves all sibling alignment.
		if row.Kind == RowPane {
			ancestorCol := set.TreeVertical
			if row.AncestorIsLast {
				ancestorCol = strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
			}
			prefix += ancestorCol
			indentDepth--
		}
		prefix += treeGlyph + " "
	}
	if indentDepth < 0 {
		indentDepth = 0
	}
	indent := strings.Repeat("  ", indentDepth)
	return indent + prefix
}

// isActiveFocusRow reports whether row identifies the Herdr tab/pane shep is
// currently running inside (m.currentPane), so kindPrefix can mark it with a
// visible active-focus indicator (IconSet.ActiveMarker). A RowPane matches on
// its own pane_id; a RowTab matches on tab_id (the containing tab) — the same
// "focus the containing tab" truthful-action Herdr's per-pane-focus gap
// forces on launchChildTab, now made visible in the row list too: selecting a
// pane can only ever focus its parent tab, never claim to focus the exact
// pane. Never true for a RowCandidate — the indicator is scoped to
// synthesized tab/pane rows.
func (m Model) isActiveFocusRow(row Row) bool {
	if m.currentPane == nil {
		return false
	}
	switch row.Kind {
	case RowPane:
		return m.currentPane.ID != "" && row.Candidate.Meta["pane_id"] == m.currentPane.ID
	case RowTab:
		return m.currentPane.TabID != "" && row.Candidate.Meta["tab_id"] == m.currentPane.TabID
	default:
		return false
	}
}

// The default formats intentionally match config.normalizeLabelFormats. They
// apply to direct zero-value Layout callers, which do not carry a loaded config.
const (
	labelPathSeparator                 = " · "
	defaultLabelWithPathFormat         = "{{if .Label}}{{.Label}}" + labelPathSeparator + "{{end}}{{.Path}}"
	defaultLabelWithPathFallbackFormat = "{{if .Label}}{{.Label}}{{else}}{{.Path}}{{end}}"
	defaultPathLabelFormat             = "{{.Path}}"
	defaultLabelOnlyFormat             = "{{.Label}}"
)

// tabLabelPortion resolves a RowTab's label text for the unified primary
// layout: "<tab_number> <label>" when a tab number is configured and the
// tab's own label differs from it, or just the bare tab number when the
// label is empty OR is itself literally the tab number as a string (Herdr's
// default/unnamed tab label equals its own number, e.g. Label="3" and
// tab_number="3" — showing both would render the redundant "3 3"). With no
// tab_number at all, the raw label is used as-is.
func tabLabelPortion(c source.Candidate) string {
	number := c.Meta["tab_number"]
	if number == "" {
		return c.Label
	}
	if c.Label == "" || c.Label == number {
		return number
	}
	return number + " " + c.Label
}

// rowLabelFormat selects the resolved template for row. Ordinary candidates
// use their provider's label_format; synthesized Herdr rows use their dedicated
// tab_label_format or pane_label_format.
func (m Model) rowLabelFormat(row Row) string {
	formats := m.labelFormats()
	switch row.Kind {
	case RowTab:
		return formats.Tab
	case RowPane:
		// Flat agent scope rows (Depth == 0, or tagged with Meta["kind"] == "agent")
		// render label-only so they do not inherit the default pane format which
		// appends " · <path>". Nested tree pane rows (Depth >= 1) continue to use
		// formats.Pane unchanged.
		if row.Depth == 0 || row.Candidate.Meta["kind"] == "agent" {
			return defaultLabelOnlyFormat
		}
		return formats.Pane
	}

	switch row.Candidate.Source {
	case config.SourceHerdr:
		return formats.Herdr
	case config.SourceSessions:
		return formats.Sessions
	case config.SourceWorkspaces:
		return formats.Workspaces
	case config.SourceZoxide:
		return formats.Zoxide
	case config.SourceProjects:
		return formats.Projects
	default:
		// A declared [[integrations]] source resolves its own configured (or
		// config.Load-defaulted) label_format via the open-ended Integrations
		// map. Any other/unknown source (a direct --path candidate, or a
		// synthesized candidate built directly in Go) keeps a safe path fallback.
		if format, ok := formats.Integrations[row.Candidate.Source]; ok && format != "" {
			return format
		}
		return defaultPathLabelFormat
	}
}

// renderRowLabel builds a template context for row and defensively falls back
// to the raw path if an invalid format somehow reaches render time, or if a
// valid format renders to an empty string (e.g. a bare {{.Label}} format
// evaluated against a candidate whose Label is empty -- reachable only via
// direct/programmatic Candidate construction, since config.Load enforces
// non-empty names/labels for every source that ships a label-only default).
// Config.Load rejects unparsable formats, but the TUI must never show a
// silently blank row or crash when called directly.
func (m Model) renderRowLabel(row Row) string {
	c := row.Candidate
	label := c.Label
	if row.Kind == RowTab {
		// Preserve the current Herdr-specific tab-number/label de-duplication
		// before templates position the label.
		label = tabLabelPortion(c)
	}
	text, err := rowformat.Render(m.rowLabelFormat(row), rowformat.Context{
		Path:        c.Path,
		Label:       label,
		TabNumber:   c.Meta["tab_number"],
		AgentStatus: c.Meta["agent_status"],
		// Icon intentionally stays unset: c.Icon belongs to rowPrimaryText's
		// fixed prefix, which left truncation must never consume.
	})
	if err != nil || text == "" {
		return c.Path
	}
	return text
}

// rowPrimaryText builds a row's primary display text, split into the
// structural prefix (kindPrefix's tree glyph/ancestor-column/indent/active-
// marker slot, plus the row's own icon — a source icon for a RowCandidate, a
// dedicated tab icon for a RowTab, or an agent-status icon for a RowPane) and
// prefixRunes, the codepoint length of that leading prefix ALONE — everything
// before the row's actual label/path text begins. renderRowLine's truncation
// path (see truncateFromLeftPreservingPrefix, rowPart.renderHighlighted) must
// never cut into that prefix, so a row's icon always survives even when its
// label/path is severely left-truncated.
//
// All row bodies render through their resolved source template. A RowTab first
// passes tabLabelPortion(c) as its Context.Label so the existing number/label
// de-duplication remains intact. RowCandidate and RowPane use their raw label.
func (m Model) rowPrimaryText(row Row) (primary string, prefixRunes int) {
	c := row.Candidate

	if row.Kind == RowPane {
		prefix := m.kindPrefix(row)
		if icon := m.agentStatusIcon(c.Meta["agent_status"]); icon != "" {
			prefix += icon + " "
		}
		return prefix + m.renderRowLabel(row), len([]rune(prefix))
	}

	prefix := m.kindPrefix(row)
	if row.Kind == RowTab {
		prefix += m.icons().TabIcon + " "
		text := m.renderRowLabel(row)
		return prefix + text, len([]rune(prefix))
	} else if c.Meta["is_worktree"] == "true" {
		prefix += " "
	} else if c.Icon != "" {
		prefix += c.Icon + " "
	}
	if row.Kind == RowCandidate && m.rankingSnapshot.IsPinned(c) {
		prefix += m.styles.pinStyle.Render(pinBadge(m.icons().Name == IconsASCII)) + " "
	}
	text := m.renderRowLabel(row)
	if c.Meta["is_worktree"] == "true" && c.Meta["branch"] != "" {
		text += " [" + c.Meta["branch"] + "]"
	}
	if c.Source == config.SourceSessions {
		text += sessionStatusSuffix(c)
	}
	if c.Missing {
		text += " (missing)"
	}
	return prefix + text, len([]rune(prefix))
}

// sessionStatusSuffix renders the sessions source's known state metadata in a
// fixed order, independent of a user's label template.
func sessionStatusSuffix(c source.Candidate) string {
	state := "stopped"
	if c.Meta["running"] == "true" {
		state = "running"
	}
	if c.Meta["default"] == "true" {
		return " (" + state + ", default)"
	}
	return " (" + state + ")"
}

// rowSecondaryText returns no content. A tab's parent workspace context would
// duplicate the path already shown in its primary text; pane and provider rows
// likewise have no trailing context.
func (m Model) rowSecondaryText(row Row) string {
	return ""
}

// rowDisplayText returns the primary and secondary display text for a
// candidate/tab/pane row — a thin combination of rowPrimaryText (dropping
// its prefixRunes, which only renderRowLine's truncation path needs) and
// rowSecondaryText. Kept as the stable external shape every existing
// rowDisplayText caller/test already depends on.
func (m Model) rowDisplayText(row Row) (primary, secondary string) {
	primary, _ = m.rowPrimaryText(row)
	return primary, m.rowSecondaryText(row)
}

// agentStatusIcon returns the styled glyph for a pane row's agent_status,
// matching herdr's own verified agent_icon convention (herdr's
// src/ui/status.rs): an animated spinner (the model's shared MiniDot
// spinner — see maybeStartSpinner's arming rules in preview.go), colored
// warn/yellow, for "working"; a check-mark in success/green for "idle"; a
// filled circle in teal for "done"; a fisheye in err/red for "blocked"; and
// a hollow circle in muted for the literal "unknown" status (herdr reporting
// it could not classify the agent) — never a literal status word. Any OTHER
// value, including "" (no status reported at all) or an unrecognized
// string, renders no icon, per the "no status = no icon" contract; the
// caller skips prepending it when this returns "". "unknown" is a real
// reported value and must not be conflated with the empty/absent case.
//
// "working" normally uses the animated spinner, but a tier whose terminal
// can't render its Unicode Braille dots (IconSet.StatusWorking non-empty —
// currently only IconsASCII) falls back to a static styled marker instead,
// so [tui].icons = "ascii" never emits a non-ASCII glyph for any status.
//
// The animated branch renders through statusWorkingStyle (warn/bold), not
// m.spinner's own configured Style (previewLoadingStyle, wired at
// construction in newModelWithLayout for the shared preview-loading
// indicator): the working-status pane icon and the preview-loading spinner
// share one animated Bubble Tea component and its single tick loop, but
// they are two distinct UI affordances that must not visually collapse
// into the same color/weight. A local copy with Style overridden keeps
// m.spinner's own animation state (and previewLoadingStyle) untouched for
// its real caller — the preview-loading indicator, see preview_body.go.
func (m Model) agentStatusIcon(status string) string {
	set := m.icons()
	switch status {
	case "working":
		if set.StatusWorking != "" {
			return m.styles.statusWorkingStyle.Render(set.StatusWorking)
		}
		working := m.spinner
		working.Style = m.styles.statusWorkingStyle
		return working.View()
	case "idle":
		return m.styles.statusIdleStyle.Render(set.StatusIdle)
	case "done":
		return m.styles.statusDoneStyle.Render(set.StatusDone)
	case "blocked":
		return m.styles.statusBlockedStyle.Render(set.StatusBlocked)
	case "unknown":
		return m.styles.statusUnknownStyle.Render(set.StatusUnknown)
	default:
		return ""
	}
}

// truncateToWidth trims s so it never exceeds maxW cells of visible width,
// appending an ellipsis ("…") when truncation occurs. Delegates to
// ansi.Truncate, which is ANSI-escape-aware and measures wide characters
// (nerd font icons, emoji, East-Asian glyphs) as their real cell width.
func truncateToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return s
	}
	return ansi.Truncate(s, maxW, "…")
}

// truncateFromLeftToWidth trims s from the LEFT so it never exceeds maxW
// cells of visible width, prepending an ellipsis ("…") when truncation
// occurs — the mirror of truncateToWidth (which truncates from the right).
// Used by the footer's restored full-path segment so the more identifying
// tail of a long path (its deepest directory) survives truncation instead
// of its shared root prefix.
func truncateFromLeftToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return s
	}
	w := ansi.StringWidth(s)
	if w <= maxW {
		return s
	}
	ellipsisW := ansi.StringWidth("…")
	n := w - maxW + ellipsisW
	return ansi.TruncateLeft(s, n, "…")
}

// truncateFromLeftPreservingPrefix behaves like truncateFromLeftToWidth
// except the leading prefixRunes codepoints of s are NEVER truncated — only
// the remainder (a row's own label/path text) is shortened from the left.
// Used so a row's structural prefix (cursor/descendant marker, kindPrefix's
// tree glyph, and its own icon) always survives truncation intact; only the
// label/path content following it may lose characters, with the ellipsis
// landing right after the prefix. Degrades to the old whole-string behavior
// when maxW cannot even fit the prefix itself — an extreme-narrow-width edge
// case with no good outcome either way.
func truncateFromLeftPreservingPrefix(s string, prefixRunes, maxW int) string {
	runes := []rune(s)
	if prefixRunes > len(runes) {
		prefixRunes = len(runes)
	}
	prefix := string(runes[:prefixRunes])
	rest := string(runes[prefixRunes:])
	budget := maxW - ansi.StringWidth(prefix)
	if budget < 1 {
		return truncateFromLeftToWidth(s, maxW)
	}
	return prefix + truncateFromLeftToWidth(rest, budget)
}

// renderPreviewTopBorder builds the preview pane's top edge separately so the
// current path can occupy the border without changing the remaining chrome.
func (m Model) renderPreviewTopBorder(style lipgloss.Style, outerWidth int, text string) string {
	if outerWidth <= 0 {
		return ""
	}

	border, _, _, _, _ := style.GetBorder()
	left, edge, right := border.TopLeft, border.Top, border.TopRight
	if m.icons().Name == IconsASCII {
		left, edge, right = "+", "-", "+"
	}

	edgeWidth := outerWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if edgeWidth <= 0 {
		return lipgloss.NewStyle().Foreground(style.GetBorderTopForeground()).Render(truncateToWidth(left+right, outerWidth))
	}

	text = truncateFromLeftToWidth(text, edgeWidth)
	fillWidth := edgeWidth - lipgloss.Width(text)
	top := left + text + strings.Repeat(edge, fillWidth) + right
	return lipgloss.NewStyle().Foreground(style.GetBorderTopForeground()).Render(top)
}

// previewSeparatorUnicode and previewSeparatorASCII are the preview top
// border title's label separator — a shape-only marker with an ASCII
// fallback so the title stays readable in every tier.
const (
	previewSeparatorUnicode = "·"
	previewSeparatorASCII   = "-"
)

// previewTopBorderText returns the preview pane's border title:
// " PREVIEW · <label> " (a small orientation title, not the full path — the
// path is shown inside the body's identity section). A missing label falls
// back to the path; no selected row leaves the preview edge empty. The
// separator degrades to "-" under the ASCII icon tier so a Unicode-only
// glyph never reaches an ASCII terminal.
func (m Model) previewTopBorderText() string {
	cand, ok := m.currentCandidate()
	if !ok {
		return ""
	}
	title := cand.Label
	if title == "" {
		title = cand.Path
	}
	if title == "" {
		return ""
	}
	separator := previewSeparatorUnicode
	if m.icons().Name == IconsASCII {
		separator = previewSeparatorASCII
	}
	return " PREVIEW " + separator + " " + title + " "
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func gap() string { return " " }

// splitWidths divides the total reported width into list/preview pane
// budgets, honouring layout.ListWidth/PreviewWidth when set to a percentage,
// then clamps to minList/minPrev (the wide side-by-side layout's minimum pane
// floors).
func splitWidths(width int, layout Layout) (int, int) {
	if width <= 0 {
		width = 80
	}
	listFrac, listOK := config.PercentOrAuto(layout.ListWidth)
	prevFrac, prevOK := config.PercentOrAuto(layout.PreviewWidth)

	var a, b int
	switch {
	case listOK && prevOK:
		a, b = splitBothPercent(width, listFrac, prevFrac)
	case listOK:
		a = int(float64(width) * listFrac)
		b = width - a - 1
	case prevOK:
		b = int(float64(width) * prevFrac)
		a = width - b - 1
	default:
		a = width * 55 / 100
		b = width - a - 1
	}
	return clampSizes(width, a, b)
}

// clampSizes enforces the minList/minPrev minimum floors and the invariant
// that a+gap+b never exceeds total, reconciling any overflow by shrinking
// whichever share is still above its own floor (a first, then b).
func clampSizes(total, a, b int) (int, int) {
	minA, minB := minList, minPrev
	if a < minA {
		a = minA
	}
	if b < minB {
		b = minB
	}
	if overflow := a + b + 1 - total; overflow > 0 {
		if room := a - minA; room > 0 {
			shrink := room
			if shrink > overflow {
				shrink = overflow
			}
			a -= shrink
			overflow -= shrink
		}
		if overflow > 0 {
			if room := b - minB; room > 0 {
				shrink := room
				if shrink > overflow {
					shrink = overflow
				}
				b -= shrink
			}
		}
	}
	return a, b
}

// splitBothPercent computes list/preview widths when both list_width and
// preview_width are configured percentages, scaling listFrac down
// proportionally whenever the two fractions would sum past 1 (100%).
func splitBothPercent(width int, listFrac, prevFrac float64) (int, int) {
	if total := listFrac + prevFrac; total > 1 {
		listFrac /= total
	}
	list := int(float64(width) * listFrac)
	prev := width - list - 1
	return list, prev
}

// pinBadge returns the pinned-candidate marker glyph for the model's icon
// tier — "•" for Unicode tiers, a plain ASCII "*" for the ASCII tier — so a
// pinned row keeps its shape marker in every mode (ASCII
// terminals never emit a Unicode-only glyph).
func pinBadge(isASCII bool) string {
	if isASCII {
		return "*"
	}
	return "•"
}

// renderKeycap renders one clean footer shortcut: an accented/bold chord token
// followed by a secondary action label. It intentionally has no brackets or
// box-like decoration.
func renderKeycap(s styleSet, key, label string) string {
	return s.keycapStyle.Render(key) + " " + s.keycapLabelStyle.Render(label)
}

// paneContentWidth converts a pane's outer width budget into the inner
// content width available once the border+padding are subtracted.
func (m Model) paneContentWidth(outer int) int {
	inner := outer - m.styles.borderStyle.GetHorizontalFrameSize()
	if inner < 1 {
		inner = 1
	}
	return inner
}

// previewBodyHeight converts a preview pane's outer height budget into the
// content line budget available once the border is subtracted
// (previewChromeRows).
func previewBodyHeight(outer int) int {
	inner := outer - previewChromeRows
	if inner < 1 {
		inner = 1
	}
	return inner
}

// paneBoxStyle returns the border style for a pane, fixed at outerHeight
// rows of content regardless of how many lines the pane's own content
// naturally renders. focused selects the theme's focusedBorderStyle
// (visually distinct accent border) so keyboard focus is obvious without
// relying on color alone — see theme.go's NoColor variant, which still
// bolds the focused border.
func (m Model) paneBoxStyle(outerHeight int, focused bool) lipgloss.Style {
	style := m.styles.borderStyle
	if focused {
		style = m.styles.focusedBorderStyle
	}
	if outerHeight <= 0 {
		return style
	}
	inner := outerHeight - style.GetVerticalFrameSize()
	if inner < 1 {
		inner = 1
	}
	return style.Height(inner).MaxHeight(outerHeight)
}

// footerNarrowKeycapWidth is the width below which the footer keeps only the
// highest-value hints, dropping preview and Herdr-target actions first.
const footerNarrowKeycapWidth = 72

// renderFooter builds the compact, contextual shortcut footer. The selected
// path is deliberately absent: the row and preview already show it.
func (m Model) renderFooter() string {
	return m.renderFooterHintsOnly(m.footerHints())
}

func (m Model) renderFooterHintsOnly(hints string) string {
	if m.width <= 0 {
		return m.styles.mutedStyle.Render(hints)
	}
	return lipgloss.NewStyle().Width(m.width).Render(m.styles.mutedStyle.Render(truncateToWidth(hints, m.width)))
}

// footerHints assembles the footer's clean shortcut groups as styled tokens
// and secondary action labels. The same keyBinding values feed the help
// overlay, so user-facing key notation cannot drift between surfaces.
func (m Model) footerHints() string {
	narrow := m.width >= 0 && m.width < footerNarrowKeycapWidth
	type keycap struct{ key, label string }
	caps := []keycap{}
	// The Enter keycap's label is derived from the highlighted row's action
	// descriptor (see rowActionDescriptor), so its label is truthful per row
	// kind and always matches what handleEnter dispatches. With nothing
	// highlighted (empty rows) there is no row-specific action, so the Enter
	// keycap is omitted entirely (SPEC-NAV-1.7).
	if row, ok := m.currentRow(); ok {
		caps = append(caps, keycap{keyBindingEnter.footerChord, rowActionDescriptor(row).FooterLabel})
	}
	nextScope := m.scope.Next()
	nextDef := scopeDefinitionFor(nextScope)
	tabLabel := nextDef.FooterLabel
	if tabLabel == "" {
		tabLabel = nextDef.Name
	}
	caps = append(caps, keycap{keyBindingTab.footerChord, tabLabel})
	if row, ok := m.currentRow(); ok && m.layout.PinToggler != nil {
		if row.Kind == RowCandidate {
			label := keyBindingPin.footerLabel
			if m.rankingSnapshot.IsPinned(row.Candidate) {
				label = "unpin"
			}
			caps = append(caps, keycap{keyBindingPin.footerChord, label})
		} else {
			caps = append(caps, keycap{keyBindingPin.footerChord, "unavailable"})
		}
	}
	if !narrow {
		if cand, ok := m.currentCandidate(); ok && m.currentPane != nil && source.SupportsCurrentWorkspaceTarget(cand) {
			caps = append(caps,
				keycap{keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel},
				keycap{keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel},
			)
		}
	}
	caps = append(caps,
		keycap{keyBindingHelp.footerChord, keyBindingHelp.footerLabel},
		keycap{keyBindingEsc.footerChord, keyBindingEsc.footerLabel},
	)
	var segments []string
	for _, c := range caps {
		segments = append(segments, renderKeycap(m.styles, c.key, c.label))
	}
	if m.pinStatus != "" {
		segments = append(segments, m.pinStatus)
	}
	return strings.Join(segments, footerSeparator)
}

// renderHelp renders the full-pane "?" progressive help overlay: every
// binding, grouped by concern, replacing the list+preview body entirely
// while active. The body is rendered through helpViewport (see
// syncHelpViewport) instead of being padded/truncated directly, so at a
// short terminal height the bottom of the help content is reachable via
// scroll instead of being silently cut off.
func (m Model) renderHelp() string {
	g := m.geometry()
	height := max(0, m.height-g.FooterLines)
	return m.paneBoxStyle(height, true).Render(m.helpViewport.View())
}

// helpBodyText builds the full help overlay's content: every binding,
// grouped by concern, rendered from keyMap (see keymap.go) — the same
// single source of truth footerHints reads its shared segments from. Fed
// into helpViewport.SetContent by syncHelpViewport so the overlay scrolls
// instead of clipping at a short terminal height.
func (m Model) helpBodyText() string {
	// The Enter binding's help description is rendered from the highlighted
	// row's action descriptor (see rowActionDescriptor), so the "?" overlay
	// tells the same truth as the footer and as handleEnter's dispatch
	// (SPEC-NAV-1.8 parity). Every other binding renders its static keyMap
	// help text. With nothing highlighted, the Enter binding falls back to its
	// static description.
	enterHelp := keyBindingEnter.help
	if row, ok := m.currentRow(); ok {
		enterHelp = rowActionDescriptor(row).HelpText
	}
	var lines []string
	for i, section := range keyMap {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.styles.helpHeadingStyle.Render(section.heading))
		for _, b := range section.bindings {
			if b.chord == keyChordEnter {
				b.help = enterHelp
			}
			lines = append(lines, renderHelpLine(b))
		}
	}
	return strings.Join(lines, "\n")
}
