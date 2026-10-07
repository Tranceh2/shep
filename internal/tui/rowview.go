package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/tmpl"
)

// rowview.go builds a list row's display model (rowView) — once per row,
// not once per frame — and keeps the visible window's models across frames
// (rowWindow). The parts come from the row's presentation templates (see
// rowparts.go); rendering a rowView lives in rowrender.go.

// rowView is one list row's display model: everything rendering needs that
// depends only on the row and the model's configuration — the tree prefix and
// the rendered icon, label, detail and marker parts with their highlight
// masks and cell widths. What changes without a row rebuild (the spinner
// frame, selection, the column width) is resolved at render time. Every text
// it holds that came from outside shep (a label, a terminal title, a Meta
// value, a custom source's icon) has been through plainText, here, once per
// build: a control character or escape sequence in it would move the
// terminal's cursor mid-frame.
type rowView struct {
	indent, tree string // the tree prefix (see treePrefix)
	icon         part
	// iconStyle indexes rowStyles.icons: the presentation's icon_color.
	iconStyle int
	// fixedW is the cells of the prefix and the icon with its separating
	// space: never truncated.
	fixedW int

	label, detail, marker part
	// keepStart truncates the label on the right (keeping its start); a
	// path-like label keeps its end instead.
	keepStart  bool
	descendant bool
	// kind names what the row is in the preview title (see kindLabel).
	kind string
}

// buildRowView builds row's display model from its presentation.
func (m Model) buildRowView(row Row) rowView {
	set := m.icons()
	f := m.rowFormat(row)
	data := rowTemplateData(row)
	raw, ok := renderParts(m.layout.Templates, &f, data)
	live := m.liveValues(row)
	var v rowView
	v.indent, v.tree = m.treePrefix(row)
	v.descendant = row.Match == MatchDescendant
	v.iconStyle = f.icon
	v.icon = buildPart(raw[partIcon], &live, &set, shapeIcon)
	if ok[partLabel] {
		v.label = buildPart(raw[partLabel], &live, &set, shapeText)
	} else {
		// A template that fails for this row (validation runs every
		// template against representative rows first) still names the row.
		v.label = plainPart(plainText(m.layout.Templates.Tilde(row.Candidate.Path)))
	}
	v.detail = buildPart(raw[partDetail], &live, &set, shapeText)
	v.marker = buildPart(raw[partMarker], &live, &set, shapeMarker)
	v.fixedW = ansi.StringWidth(v.indent) + ansi.StringWidth(v.tree)
	if v.icon.width > 0 {
		v.fixedW += v.icon.width + 1
	}
	m.highlight(row, &v.label, &v.detail)
	v.keepStart = !v.label.pathLike()
	v.kind = plainText(kindLabel(data))
	return v
}

// kindLabel names what a row is, for the preview title: its template Kind
// (workspace, configured, group, folder, project, worktree, session, tab,
// pane), an agent's program, or a custom source's own name.
func kindLabel(d tmpl.Data) string {
	switch d.Kind {
	case tmpl.KindAgent:
		if d.Agent != "" {
			return d.Agent
		}
	case tmpl.KindCustom:
		return d.Source
	}
	return d.Kind
}

// treePrefix returns a row's depth indent and its sibling-sensitive tree
// glyphs. Top-level and flat (Depth 0) rows have none.
func (m Model) treePrefix(row Row) (indent, tree string) {
	if row.Depth == 0 {
		return "", ""
	}
	set := m.icons()
	depth := row.Depth
	switch row.Kind {
	case RowTab, RowPane:
		// A RowPane's ancestor connector sits in the same column as its
		// parent tab's branch; its own branch follows one level deeper. A
		// final tab leaves a same-width blank so every sibling stays aligned.
		if row.Kind == RowPane {
			if row.AncestorIsLast {
				tree = strings.Repeat(" ", ansi.StringWidth(set.TreeVertical))
			} else {
				tree = set.TreeVertical
			}
			depth--
		}
		glyph := set.TreeMid
		if row.IsLast {
			glyph = set.TreeLast
		}
		tree += glyph + " "
	}
	return strings.Repeat("  ", max(0, depth)), tree
}

// hasStatusGlyph reports whether status renders a glyph (see statusGlyph).
// "" and unrecognized values render none.
func hasStatusGlyph(status string) bool {
	switch status {
	case "idle", "working", "blocked", "done", "unknown":
		return true
	}
	return false
}

// rowShowsWorking reports whether row draws a working status glyph — on the
// row itself (a pane or agent row) or as its open workspace's aggregate — so
// the shared spinner must keep ticking.
func (m Model) rowShowsWorking(row Row) bool {
	c := row.Candidate
	if row.Kind == RowPane || c.Source == config.SourceAgents {
		return c.Meta["agent_status"] == "working"
	}
	return row.Kind == RowCandidate && c.Source == config.SourceHerdr &&
		m.tree.WorkspaceAgentStatus(c.Meta["workspace_id"]) == "working"
}

// --- scrolling and the visible window ---

// listScrollOff is the rows kept between the cursor and either edge of the
// list window, so the list scrolls just before the cursor reaches an edge
// instead of re-centering on every move.
const listScrollOff = 3

// scrollOffset returns the first visible row of a window of visible rows
// over total rows, moving offset only as far as needed to keep the cursor
// min(listScrollOff, (visible-1)/2) rows away from either edge. It is pure:
// Update stores its result in m.listOffset and View re-derives it from
// there, which is a no-op for an already settled offset.
func scrollOffset(offset, cursor, total, visible int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	margin := min(listScrollOff, (visible-1)/2)
	offset = min(offset, cursor-margin)
	offset = max(offset, cursor-visible+1+margin)
	return clamp(offset, 0, total-visible)
}

// windowLen is how many rows the list window shows from offset: every row
// when the height is unknown (visible <= 0).
func windowLen(total, offset, visible int) int {
	if visible <= 0 {
		return total
	}
	return max(0, min(visible, total-offset))
}

// ensureCursorVisible scrolls the list window just enough to keep the
// cursor inside it (see scrollOffset). Called at the end of every Update, so
// key handling, refiltering, resizes and arriving sources all settle it.
func (m *Model) ensureCursorVisible() {
	m.listOffset = scrollOffset(m.listOffset, m.cursor, len(m.rows), m.geometry().ListInnerRows)
}

// rowWindow caches the rowViews of the rows in the list window, so a frame
// that changes no row (a spinner tick, a resize) renders without rebuilding
// labels, templates or highlight masks. Only the window is built: a query
// can match hundreds of rows, but only the visible ones are ever shown.
type rowWindow struct {
	// rows is &m.rows[0] when built: every rebuild of m.rows allocates a new
	// slice, so a different pointer means the views describe stale rows.
	rows   *Row
	total  int
	offset int
	views  []rowView
}

// covers reports whether w holds the views of rows[offset:offset+n].
func (w *rowWindow) covers(rows []Row, offset, n int) bool {
	return len(rows) > 0 && w.rows == &rows[0] && w.total == len(rows) && w.offset == offset && len(w.views) == n
}

// syncRowWindow rebuilds the window cache when the rows, the window
// position or its size changed since it was built.
func (m *Model) syncRowWindow() {
	n := windowLen(len(m.rows), m.listOffset, m.geometry().ListInnerRows)
	if n == 0 || m.rowWindow.covers(m.rows, m.listOffset, n) {
		return
	}
	views := make([]rowView, n)
	for i := range views {
		views[i] = m.buildRowView(m.rows[m.listOffset+i])
	}
	m.rowWindow = rowWindow{rows: &m.rows[0], total: len(m.rows), offset: m.listOffset, views: views}
}

// invalidateRowWindow drops the window cache after a change that keeps
// m.rows but alters what its rows show (a row mutated in place, a pane's
// agent status, the current pane).
func (m *Model) invalidateRowWindow() { m.rowWindow = rowWindow{} }

// cursorRowView returns the highlighted row's view, from the window cache
// when it holds it (the cursor is always inside the window after Update).
func (m Model) cursorRowView() rowView {
	w := &m.rowWindow
	if w.covers(m.rows, w.offset, len(w.views)) && m.cursor >= w.offset && m.cursor < w.offset+len(w.views) {
		return w.views[m.cursor-w.offset]
	}
	return m.buildRowView(m.rows[m.cursor])
}

// windowViews returns the views of rows[offset:offset+n]: the cache when it
// covers them, else views built on the spot (a model that has not been
// through Update yet).
func (m Model) windowViews(offset, n int) []rowView {
	if m.rowWindow.covers(m.rows, offset, n) {
		return m.rowWindow.views
	}
	views := make([]rowView, n)
	for i := range views {
		views[i] = m.buildRowView(m.rows[offset+i])
	}
	return views
}
