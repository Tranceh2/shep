package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/rowformat"
	"github.com/tranceh2/shep/internal/source"
)

// View renders the borderless grid (see geometry.go): the tab strip, the
// prompt row beside the preview title, the rule, the body and the footer.
// modeWide shows the list and preview columns side by side around the
// divider; modeListOnly keeps the same rows without the preview column. The
// "?" help overlay (m.focus == FocusHelp) keeps the tab strip and replaces
// everything below it. View is a pure projection of state settled by Update
// (mode, viewport sizes and content).
func (m Model) View() string {
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
	offset := clamp(m.viewport.YOffset, 0, max(0, len(lines)-height))
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
	def := m.tabPresentation()
	if def.EmptyState != nil {
		return def.EmptyState(m)
	}
	return []string{"No workspaces yet"}
}

// applySurface merges the selection surface treatment into ownStyle: a
// selectedSurface/unfocusedSurface background for color themes; the plain
// theme has no surface (its selection is bold, see newPalette), but a faint
// surface would still be honored. The row's own foreground/bold/italic
// survive because Background/Faint do not clobber them.
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

// kindPrefix returns the tree prefix drawn before a row's icon: the depth
// indent and the sibling-sensitive tree glyphs (see treePrefix). Top-level
// and flat (Depth 0) rows have none.
func (m Model) kindPrefix(row Row) string {
	indent, tree := m.treePrefix(row)
	return indent + tree
}

// containsCurrentPane reports whether row holds the Herdr pane shep is
// running inside (m.currentPane): that pane's own row, the tab row that
// contains it, or the open workspace row that contains it. Such rows carry a
// muted "current" accessory — a "you are here" that reads as text instead of
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

// The default formats intentionally match config.normalizeLabelFormats
// (TestLabelFormatDefaults_MatchConfig keeps them identical). They apply to
// direct zero-value Layout callers, which do not carry a loaded config.
const (
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
		// Flat agent scope rows (Depth == 0, or tagged with Meta["kind"] ==
		// "agent") render through the agents format, never the tree pane
		// format: their path is not part of an agent's title. Nested tree
		// pane rows (Depth >= 1) use formats.Pane.
		if row.Depth == 0 || row.Candidate.Meta["kind"] == "agent" {
			if formats.Agents != "" {
				return formats.Agents
			}
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
	case config.SourceAgents:
		return formats.Agents
	default:
		// A declared [[sources.custom]] source resolves its own configured (or
		// config.Load-defaulted) label_format via the open-ended CustomSources
		// map. Any other/unknown source (a direct --path candidate, or a
		// synthesized candidate built directly in Go) keeps a safe path fallback.
		if format, ok := formats.CustomSources[row.Candidate.Source]; ok && format != "" {
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
// silently blank row or crash when called directly. The result is what the
// row displays, so it is plain text (see plainText): the label, the Meta
// values a template interpolates and the path all come from outside shep.
// Matching never reads it — filtering scores the raw candidate data.
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
		Meta:        c.Meta,
		// Icon intentionally stays unset: c.Icon belongs to the row's fixed
		// prefix (see buildRowView), which truncation must never consume.
	})
	text = plainText(text)
	if err != nil || text == "" {
		return plainText(c.Path)
	}
	return text
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
