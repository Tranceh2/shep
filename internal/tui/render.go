package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

// View draws the picker on the alternate screen (see screen).
func (m Model) View() tea.View {
	v := tea.NewView(m.screen())
	v.AltScreen = true
	return v
}

// screen renders the borderless grid (see geometry.go): the tab strip, the
// prompt row beside the preview title, the rule, the body and the footer.
// modeWide shows the list and preview columns side by side around the
// divider; modeListOnly keeps the same rows without the preview column. The
// "?" help overlay (m.focus == FocusHelp) keeps the tab strip and replaces
// everything below it. It is a pure projection of state settled by Update
// (mode, viewport sizes and content).
func (m Model) screen() string {
	g := m.geometry()
	f := newFrame(g.Margin)
	f.line(m.renderTabStrip(g.ContentWidth))
	if m.focus == FocusHelp {
		m.writeHelp(&f, g)
		return f.String()
	}
	divider := ""
	if g.PreviewWidth > 0 {
		divider = m.renderDivider()
		f.line(m.renderPromptRow(g.ListWidth), divider, m.renderPreviewTitle(g.PreviewWidth))
	} else {
		f.line(m.renderPromptRow(g.ListWidth))
	}
	f.line(m.renderRule(g, true))
	m.writeBody(&f, g, divider)
	f.line(m.renderFooter(g.ContentWidth))
	return f.String()
}

// frame accumulates the grid's lines, each wrapped in the side margins.
type frame struct {
	b      strings.Builder
	margin string
	lines  int
}

func newFrame(margin int) frame {
	return frame{margin: strings.Repeat(" ", margin)}
}

// line appends one grid row built from parts, already fitted to their
// columns by the caller.
func (f *frame) line(parts ...string) {
	if f.lines > 0 {
		f.b.WriteByte('\n')
	}
	f.b.WriteString(f.margin)
	for _, p := range parts {
		f.b.WriteString(p)
	}
	f.b.WriteString(f.margin)
	f.lines++
}

func (f *frame) String() string { return f.b.String() }

// writeBody writes the body rows: list rows beside the preview viewport's
// lines, every cell fitted to its column so the divider stays straight. The
// body fills g.ListInnerRows; before the first size message (height unknown)
// it is as tall as its tallest column. When the rows overflow the window,
// the divider doubles as the list's scrollbar (in list-only mode, the list's
// last column does, one blank cell clear of the rows).
func (m Model) writeBody(f *frame, g pickerGeometry, divider string) {
	rows := g.ListInnerRows
	offset := scrollOffset(m.listOffset, m.cursor, len(m.rows), rows)
	thumbStart, thumbLen := scrollThumb(offset, len(m.rows), rows)
	listWidth := g.ListWidth
	if thumbLen > 0 && g.PreviewWidth == 0 {
		listWidth -= 2 // list-only: a gap cell, then the track in the last column
	}
	list := m.listLines(listWidth, rows, offset)
	var preview []string
	if g.PreviewWidth > 0 {
		preview = m.previewWindow(g.PreviewWidth, max(1, g.PreviewInnerRows))
	}
	if m.height <= 0 {
		rows = max(len(list), len(preview))
	}
	set := m.icons()
	track, thumb := divider, divider
	if thumbLen > 0 {
		track = m.styles.ruleStyle.Render(set.RuleVertical)
		thumb = m.styles.secondaryStyle.Render(set.ScrollThumb)
		if g.PreviewWidth > 0 {
			track, thumb = " "+track+" ", " "+thumb+" "
		} else {
			track, thumb = " "+track, " "+thumb
		}
	}
	blankList := strings.Repeat(" ", listWidth)
	blankPreview := strings.Repeat(" ", g.PreviewWidth)
	for i := range rows {
		left := blankList
		if i < len(list) {
			left = list[i]
		}
		bar := track
		if i >= thumbStart && i < thumbStart+thumbLen {
			bar = thumb
		}
		if g.PreviewWidth == 0 {
			if thumbLen > 0 {
				f.line(left, bar)
			} else {
				f.line(left)
			}
			continue
		}
		right := blankPreview
		if i < len(preview) {
			right = preview[i] // already fitted to the column
		}
		f.line(left, bar, right)
	}
}

// previewWindow returns the preview lines the column shows: height lines
// from the viewport's scroll offset, each already fitted to width.
func (m Model) previewWindow(width, height int) []string {
	lines := m.previewLines(width, height)
	offset := clamp(m.viewport.YOffset(), 0, max(0, len(lines)-height))
	return lines[offset:min(len(lines), offset+height)]
}

// listLines renders the rows of the window starting at offset, at exactly
// width cells each, or the two-line empty state when there are no rows.
// maxRows <= 0 means the height is unknown and every row is rendered.
func (m Model) listLines(width, maxRows, offset int) []string {
	if len(m.rows) == 0 {
		return m.emptyStateRender(width)
	}
	views := m.windowViews(offset, windowLen(len(m.rows), offset, maxRows))
	r := m.newRowRenderer()
	lines := make([]string, len(views))
	for i := range views {
		lines[i] = r.render(&views[i], offset+i == m.cursor, width)
	}
	return lines
}

// emptyStateRender renders the empty state's lines aligned with row
// content (after the two-cell gutter): the message in the row text style,
// the hint below it muted.
func (m Model) emptyStateRender(width int) []string {
	state := m.emptyStateLines()
	lines := make([]string, 0, 2)
	gutter := strings.Repeat(" ", cursorPrefixWidth)
	for i, line := range state[:min(2, len(state))] {
		style := m.styles.rowStyle
		if i > 0 {
			style = m.styles.mutedStyle
		}
		lines = append(lines, fitWidth(gutter+style.Render(truncateToWidth(line, width-cursorPrefixWidth)), width))
	}
	return lines
}

// emptyStateLines picks the right "nothing to show" content: a query that
// matched nothing, no candidates at all (every source came back empty), vs.
// sources still loading asynchronously. Each state is at most two lines of
// structured text (message + what-to-do hint). The query is quoted with
// typographic quotes, or plain ones under the ASCII icon tier.
func (m Model) emptyStateLines() []string {
	if m.query != "" {
		open, end := "“", "”"
		if m.icons().Name == IconsASCII {
			open, end = `"`, `"`
		}
		return []string{
			"No matches for " + open + m.query + end,
			"esc clears the search",
		}
	}
	return m.tabPresentation().EmptyState(m)
}

// applySurface merges the selection surface treatment into ownStyle: the
// selection background for color themes; the plain theme has no surface (its
// selection is bold, see newPalette), but a faint surface would still be
// honored. The row's own foreground/bold/italic survive because
// Background/Faint do not clobber them.
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

// containsCurrentPane reports whether row holds the Herdr pane shep is
// running inside (m.currentPane): that pane's own row, the tab row that
// contains it, or the open workspace row that contains it. Such rows show
// the "current" live marker — a "you are here" that reads as text instead of
// a glyph that could pass for a status. A tab's match is also the truthful
// action: Herdr focuses tabs, never one exact pane.
func (m Model) containsCurrentPane(row Row) bool {
	p, c := m.currentPane, row.Candidate
	if p == nil {
		return false
	}
	switch row.Kind {
	case RowPane:
		return p.ID != "" && c.Meta["pane_id"] == p.ID
	case RowTab:
		return p.TabID != "" && c.Meta["tab_id"] == p.TabID
	default:
		return c.Source == config.SourceHerdr && p.WorkspaceID != "" && c.Meta["workspace_id"] == p.WorkspaceID
	}
}

// rowTemplateData is the template data for row: the candidate's shared data
// (source.TemplateData), with the Kind of the Herdr rows the picker
// synthesizes itself (tabs and nested panes have no source).
func rowTemplateData(row Row) tmpl.Data {
	c := row.Candidate
	data := source.TemplateData(c)
	switch row.Kind {
	case RowTab:
		data.Kind = tmpl.KindTab
	case RowPane:
		// Flat agent rows are agents, nested tree panes are panes (the same
		// split rowFormat makes).
		if row.Depth == 0 || c.Meta["kind"] == "agent" {
			data.Kind = tmpl.KindAgent
		} else {
			data.Kind = tmpl.KindPane
		}
	}
	return data
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

// enterHelpText is the Enter binding's help description for the highlighted
// row, from the same action descriptor the footer and handleEnter read;
// with nothing highlighted, the binding's static description.
func (m Model) enterHelpText() string {
	if row, ok := m.currentRow(); ok {
		return rowActionDescriptor(row).HelpText
	}
	return keyBindingEnter.help
}
