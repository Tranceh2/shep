package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// minPreviewWidth is the terminal width below which the preview panel is
// hidden entirely (modeListOnly) to avoid breaking the layout. Also doubles
// as the AUTO responsive mode's mediumBreakpoint (layout_responsive.go).
const minPreviewWidth = 80

// minPreviewHeight is the terminal height below which the preview panel is
// hidden entirely, mirroring minPreviewWidth on the row axis.
const minPreviewHeight = 8

// minListH/minPrevH are the height-axis minimum pane floors (rows) used when
// splitting a stacked layout's vertical share — see minList/minPrev for the
// width-axis equivalents used by a wide (side-by-side) layout. minListH
// covers the list pane's own chrome (chromeRows) plus 3 candidate rows;
// minPrevH reuses minPreviewHeight, the terminal's own real minimum for a
// usable preview pane.
const minListH = chromeRows + 3
const minPrevH = minPreviewHeight

// minPortraitHeight is the raw terminal height below which a stacked layout
// falls back to list-only instead of attempting a split that cannot honour
// minListH+minPrevH without overflowing.
const minPortraitHeight = minListH + minPrevH + 2

// chromeRows is the fixed vertical overhead of the list pane deducted from
// the reported terminal height before capping visible rows: the border's
// top+bottom edges (2). The old query line that used to live inside the list
// pane was removed in Phase 3 (moved to the full-width header above the
// body), so the count dropped from 4 to 3.
const chromeRows = 3

// previewChromeRows is the fixed vertical overhead of the preview pane: 2
// (border top+bottom). The preview pane has no header/help line of its own.
const previewChromeRows = 2

// minList/minPrev are the width-axis minimum pane floors (columns) for a
// wide (side-by-side) layout.
const minList = 20
const minPrev = 10

// footerSeparator joins footer hint segments.
const footerSeparator = " / "

// View renders the responsive two-pane (or single-pane) UI plus a compact,
// contextual footer line. The active mode (m.mode, computed on
// tea.WindowSizeMsg — see nextResponsiveMode) decides the shape: modeWide
// splits list/preview side by side, modeStacked stacks them, modeListOnly
// shows only the list. The "?" help overlay (m.focus == FocusHelp) replaces
// the body entirely when active.
func (m Model) View() string {
	if m.focus == FocusHelp {
		return lipgloss.JoinVertical(lipgloss.Left, m.renderHelp(), m.renderFooter())
	}

	// paneHeight accounts for the header (1) + footer (1) above/below the body.
	paneHeight := m.height
	if paneHeight > 0 {
		paneHeight -= 2
	}
	paneModel := m
	paneModel.height = paneHeight

	header := m.renderHeader(m.width)
	footer := m.renderFooter()

	var body string
	switch m.mode {
	case modeListOnly:
		body = m.paneBoxStyle(paneHeight, true).Render(paneModel.renderList(m.paneContentWidth(m.width)))
	case modeStacked:
		body = paneModel.renderStacked()
	default: // modeWide, or "" (unknown/headless default — see model.go)
		listW, prevW := splitWidths(m.width, m.layout)
		listPane := m.paneBoxStyle(paneHeight, m.focus == FocusList).Render(paneModel.renderList(m.paneContentWidth(listW)))
		previewPane := m.paneBoxStyle(paneHeight, m.focus == FocusPreview).Render(m.viewport.View())
		_ = prevW
		body = lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// renderStacked stacks the list pane above the preview pane, each spanning
// the full terminal width (modeStacked).
func (m Model) renderStacked() string {
	listH, prevH := splitSizes(m.height, m.layout, minListH, minPrevH)
	listModel := m
	listModel.height = listH
	listPane := m.paneBoxStyle(listH, m.focus == FocusList).Render(listModel.renderList(m.paneContentWidth(m.width)))
	previewPane := m.paneBoxStyle(prevH, m.focus == FocusPreview).Render(m.viewport.View())
	return lipgloss.JoinVertical(lipgloss.Left, listPane, previewPane)
}

// renderList draws the visible grouped/nested rows with a cursor marker,
// scrolled to keep the cursor in view. The query line that used to live here
// was moved to the full-width header (renderHeader) in Phase 3; the body now
// starts directly with rows. Every rendered line is explicitly padded to
// width so the list pane never drifts from the split computed by View.
func (m Model) renderList(width int) string {
	var b strings.Builder
	if len(m.rows) == 0 {
		b.WriteString(m.styles.mutedStyle.Width(width).Render(truncateToWidth(m.emptyStateText(), width)))
		b.WriteString("\n")
		return b.String()
	}
	full := m.rows
	maxRows := m.height
	if maxRows > 0 {
		maxRows -= chromeRows
	}
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
	for i, row := range visible {
		b.WriteString(m.renderRowLine(row, i+offset == m.cursor, width))
		b.WriteString("\n")
	}
	return b.String()
}

// emptyStateText picks the right "nothing to show" message: no candidates
// at all (every source came back empty), vs. a query that matched nothing.
func (m Model) emptyStateText() string {
	if len(m.baseFlatCandidates()) == 0 {
		return "no candidates available"
	}
	if m.query != "" {
		return "no matches for \"" + m.query + "\""
	}
	return "nothing to show"
}

// renderHeader builds the full-width app/search header line above the body:
// a muted "Search:" role label, the live query text (accent+bold) or a
// muted placeholder at empty query, and a right-aligned count — "M of N"
// when filtered, "N candidates" unfiltered. The corrective round removed the
// "shep" brand mark entirely — the header is a single "Search: <query>"
// line, nothing else. At a width too narrow to fit both, the count is
// dropped and the left side is truncated; the query is what the user is
// actively reading and must never be silently cut in favor of the count.
func (m Model) renderHeader(width int) string {
	role := m.styles.mutedStyle.Render("Search: ")
	var query string
	if m.query != "" {
		query = m.styles.queryStyle.Render(m.query)
	} else {
		query = m.styles.mutedStyle.Render("type to filter\u2026")
	}
	left := role + query

	n := len(m.baseFlatCandidates())
	var right string
	if m.query != "" {
		right = m.styles.mutedStyle.Render(strconv.Itoa(m.visibleTopLevelCount()) + " of " + strconv.Itoa(n))
	} else {
		right = m.styles.mutedStyle.Render(strconv.Itoa(n) + " candidates")
	}

	if width < lipgloss.Width(left)+1+lipgloss.Width(right) {
		return lipgloss.NewStyle().Width(width).Render(truncateToWidth(left, width))
	}
	return lipgloss.NewStyle().Width(width).Render(rightPadToWidth(left, right, width))
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

// cursorGutterGlyph is the 1-column selection gutter cell for color themes: a
// solid left-half block rendered on the accent (focused) or rule (unfocused)
// background. cursorGutterGlyphPlain is the no-color fallback — a visible "|"
// (reverse-video when focused, faint when unfocused) so the cursor stays
// marked in TERM=dumb / $NO_COLOR where the old full-row cursor style was
// invisible. cursorGutterWidth is the visible cell width of either glyph.
const (
	cursorGutterGlyph      = "▌"
	cursorGutterGlyphPlain = "|"
	cursorGutterWidth      = 1
)

// rowPart is one styled segment of a row line. Most rows have a single part;
// SourceZoxide/SourceProjects rows with a full-path label get a second dim
// part for the shortened parent path.
type rowPart struct {
	text  string
	style lipgloss.Style
}

// renderRowLine renders one row. The cursor (selected) row gets the Phase 2
// selection treatment (a 1-col gutter + selectedSurface background, with the
// row's OWN text styles preserved); every other row renders unchanged.
func (m Model) renderRowLine(row Row, isCursor bool, width int) string {
	parts := m.rowLineParts(row)
	if isCursor {
		return m.renderSelectedFromParts(parts, width)
	}
	return m.renderUnselectedFromParts(parts, width)
}

// rowLineParts builds the styled parts for one row, independent of
// selection: the 2-char marker slot ("  " normally, " ~" for a
// descendant-only match) + separator + depth/kind-prefixed primary text for
// a candidate/tab/pane — plus an optional dim secondary (a RowPane's own
// pane id, see rowDisplayText) for rows that carry one.
func (m Model) rowLineParts(row Row) []rowPart {
	marker := "  "
	if row.Match == MatchDescendant {
		marker = " ~"
	}
	style := m.styles.rowStyle
	if row.Match == MatchDescendant {
		style = m.styles.rowDescendantStyle
	}
	primary, secondary := m.rowDisplayText(row)
	parts := []rowPart{{text: marker + " " + primary, style: style}}
	if secondary != "" {
		parts = append(parts, rowPart{text: "  " + secondary, style: m.styles.mutedStyle})
	}
	return parts
}

// renderUnselectedFromParts renders a non-cursor row from its parts, padded
// to width. Single-part rows use the simple path; multi-part rows compose
// the primary and secondary with truncation priority (secondary dropped first).
func (m Model) renderUnselectedFromParts(parts []rowPart, width int) string {
	if len(parts) == 1 {
		p := parts[0]
		return p.style.Width(width).Render(truncateToWidth(p.text, width))
	}
	return m.composeMultiPartRow(parts, width, lipgloss.Style{}, false)
}

// renderSelectedFromParts renders the cursor row with the selection treatment:
// a 1-column gutter cell leading the row, the row background tinted
// selectedSurface across the pane width, and the row's OWN text styles
// preserved. When the list does NOT own focus (preview or help focused)
// the gutter and surface drop to their unfocused variants.
func (m Model) renderSelectedFromParts(parts []rowPart, width int) string {
	gutterStyle := m.styles.cursorGutterStyle
	surfaceStyle := m.styles.cursorSurfaceStyle
	if m.focus != FocusList {
		gutterStyle = m.styles.cursorGutterUnfocusedStyle
		surfaceStyle = m.styles.cursorSurfaceUnfocusedStyle
	}

	glyph := cursorGutterGlyph
	if m.theme.NoColor {
		glyph = cursorGutterGlyphPlain
	}
	gutter := gutterStyle.Render(glyph)

	contentW := width - cursorGutterWidth
	if contentW < 1 {
		contentW = 1
	}
	if len(parts) == 1 {
		p := parts[0]
		styled := applySurface(p.style, surfaceStyle).Width(contentW).Render(truncateToWidth(p.text, contentW))
		return gutter + styled
	}
	styled := m.composeMultiPartRow(parts, contentW, surfaceStyle, true)
	return gutter + styled
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
		return pStyle.Width(width).Render(truncateToWidth(primary.text, width))
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
// from the Model's configured icon fallback tier (see icons.go): the
// expand/collapse marker for an expandable candidate, the tab marker for a
// RowTab, the pane marker for a RowPane, "" otherwise. The indent is
// repeated 2-space per depth level.
func (m Model) kindPrefix(row Row) string {
	set := m.icons()
	prefix := ""
	switch row.Kind {
	case RowCandidate:
		if row.Expandable {
			if row.Expanded {
				prefix = set.ExpandOpen + " "
			} else {
				prefix = set.ExpandClosed + " "
			}
		}
	case RowTab:
		prefix = set.TabPrefix + " "
	case RowPane:
		prefix = set.PanePrefix + " "
	}
	indent := strings.Repeat("  ", row.Depth)
	return indent + prefix
}

// rowDisplayText returns the primary and secondary display text for a
// candidate/tab/pane row.
//
// Corrective round: SourceZoxide/SourceProjects no longer get the
// basename-first/shortened-parent-path treatment — every non-pane row shows
// its full label (falling back to its full Path when Label is empty), with
// no secondary at all, matching every other source.
//
// A RowPane is the one exception: primary is the pane's real CWD/foreground
// path (the most useful thing to scan a list of panes by), prefixed with its
// agent-status icon (see agentStatusIcon) when one applies; secondary is the
// pane's own id (Candidate.Label) as a muted disambiguator.
func (m Model) rowDisplayText(row Row) (primary, secondary string) {
	c := row.Candidate
	label := c.Label
	if label == "" {
		label = c.Path
	}

	if row.Kind == RowPane {
		primary = c.Path
		if primary == "" {
			primary = label
		}
		if icon := m.agentStatusIcon(c.Meta["agent_status"]); icon != "" {
			primary = icon + " " + primary
		}
		primary = m.kindPrefix(row) + primary
		return primary, label
	}

	primary = label
	if c.Icon != "" {
		primary = c.Icon + " " + primary
	}
	if c.Missing {
		primary += " (missing)"
	}
	primary = m.kindPrefix(row) + primary
	return primary, ""
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
// budgets, honouring layout.ListWidth/PreviewWidth when set to a percentage.
func splitWidths(width int, layout Layout) (int, int) {
	return splitSizes(width, layout, minList, minPrev)
}

// splitSizes is the axis-agnostic core: it computes list/preview shares of
// total from layout's percent config, then clamps to whichever minimum
// floor pair the caller supplies — minList/minPrev (width axis) or
// minListH/minPrevH (height axis, renderStacked).
func splitSizes(total int, layout Layout, minA, minB int) (int, int) {
	if total <= 0 {
		total = 80
	}
	listFrac, listOK := config.PercentOrAuto(layout.ListWidth)
	prevFrac, prevOK := config.PercentOrAuto(layout.PreviewWidth)

	var a, b int
	switch {
	case listOK && prevOK:
		a, b = splitBothPercent(total, listFrac, prevFrac)
	case listOK:
		a = int(float64(total) * listFrac)
		b = total - a - 1
	case prevOK:
		b = int(float64(total) * prevFrac)
		a = total - b - 1
	default:
		a = total * 3 / 5
		b = total - a - 1
	}
	return clampSizes(total, a, b, minA, minB)
}

// clampSizes enforces minA/minB minimum floors and the invariant that
// a+gap+b never exceeds total, reconciling any overflow by shrinking
// whichever share is still above its own floor (a first, then b).
func clampSizes(total, a, b, minA, minB int) (int, int) {
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
	return style.Height(inner)
}

// formatHint builds one "<key> <label>" footer hint segment.
func formatHint(key, label string) string {
	return key + " " + label
}

// footerPathHintGap separates the footer's restored full-path segment from
// its keybinding hints.
const footerPathHintGap = "  "

// footerMinPathWidth is the smallest width the full-path segment is ever
// granted before the footer gives up on it entirely and falls back to
// hints-only — a handful of cells is not enough to show anything useful, so
// there is no point rendering a nearly-all-ellipsis fragment.
const footerMinPathWidth = 4

// renderFooter builds the compact, contextual footer line: a restored
// left-aligned full-path segment for the currently highlighted row (see
// footerPathSegment — the minimal fix scoped to this corrective round; the
// broader KeyMap/contextual-hint-visibility unification stays a separate
// future phase) followed by the keybinding hints. ctrl+t/ctrl+p hints only
// appear when both shep is running inside a Herdr pane AND the highlighted
// candidate supports a current-workspace target. The hints are never the
// first thing truncated: the path segment truncates from the LEFT (keeping
// the more identifying tail of a long path) before the hints ever lose a
// single character, and is dropped entirely rather than squeezed into an
// unreadable sliver.
func (m Model) renderFooter() string {
	hints := m.footerHints()
	path := m.footerPathSegment()
	if path == "" || m.width <= 0 {
		return m.renderFooterHintsOnly(hints)
	}
	avail := m.width - lipgloss.Width(hints) - lipgloss.Width(footerPathHintGap)
	if avail < footerMinPathWidth {
		return m.renderFooterHintsOnly(hints)
	}
	line := truncateFromLeftToWidth(path, avail) + footerPathHintGap + hints
	return lipgloss.NewStyle().Width(m.width).Render(m.styles.mutedStyle.Render(line))
}

// renderFooterHintsOnly renders the hints-only footer (no room, or nothing
// highlighted to show a path for) — identical to the pre-corrective-round
// behavior.
func (m Model) renderFooterHintsOnly(hints string) string {
	if m.width <= 0 {
		return m.styles.mutedStyle.Render(hints)
	}
	return lipgloss.NewStyle().Width(m.width).Render(m.styles.mutedStyle.Render(truncateToWidth(hints, m.width)))
}

// footerPathSegment returns the full Path of the currently highlighted row,
// or "" when nothing is highlighted (empty rows) or the row has no path —
// the minimal restoration of visible full-path text in the footer.
func (m Model) footerPathSegment() string {
	cand, ok := m.currentCandidate()
	if !ok {
		return ""
	}
	return cand.Path
}

// footerHints assembles the footer's hint segments. Every segment is built
// from a shared keyBinding value (see keymap.go) so its chord/label text is
// never independently retyped here — the same values feed the "?" help
// overlay's matching lines in helpBodyText.
func (m Model) footerHints() string {
	segments := []string{
		formatHint(keyBindingEnter.footerChord, keyBindingEnter.footerLabel),
		formatHint(keyBindingTab.footerChord, keyBindingTab.footerLabel),
	}
	if cand, ok := m.currentCandidate(); ok && m.currentPane != nil && source.SupportsCurrentWorkspaceTarget(cand) {
		segments = append(segments,
			formatHint(keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel),
			formatHint(keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel),
		)
	}
	segments = append(segments,
		formatHint(keyBindingEsc.footerChord, keyBindingEsc.footerLabel),
		formatHint(keyBindingHelp.footerChord, keyBindingHelp.footerLabel),
	)
	return strings.Join(segments, footerSeparator)
}

// renderHelp renders the full-pane "?" progressive help overlay: every
// binding, grouped by concern, replacing the list+preview body entirely
// while active. The body is rendered through helpViewport (see
// syncHelpViewport) instead of being padded/truncated directly, so at a
// short terminal height the bottom of the help content is reachable via
// scroll instead of being silently cut off.
func (m Model) renderHelp() string {
	height := m.height
	if height > 0 {
		height--
	}
	return m.paneBoxStyle(height, true).Render(m.helpViewport.View())
}

// helpBodyText builds the full help overlay's content: every binding,
// grouped by concern, rendered from keyMap (see keymap.go) — the same
// single source of truth footerHints reads its shared segments from. Fed
// into helpViewport.SetContent by syncHelpViewport so the overlay scrolls
// instead of clipping at a short terminal height.
func (m Model) helpBodyText() string {
	var lines []string
	for i, section := range keyMap {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.styles.helpHeadingStyle.Render(section.heading))
		for _, b := range section.bindings {
			lines = append(lines, renderHelpLine(b))
		}
	}
	return strings.Join(lines, "\n")
}

// candidateDisplayText builds the plain "icon label-or-path (missing)" text
// for a bare source.Candidate — used by preview_body.go's built-in summaries
// (identity-style sections), independent of any Row wrapper.
func candidateDisplayText(c source.Candidate) string {
	row := c.Label
	if row == "" {
		row = c.Path
	}
	if c.Icon != "" {
		row = c.Icon + " " + row
	}
	if c.Missing {
		row += " (missing)"
	}
	return row
}
