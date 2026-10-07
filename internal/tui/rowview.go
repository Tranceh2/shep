package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

// rowview.go builds a list row's display model (rowView) — once per row,
// not once per frame — and keeps the visible window's models across frames
// (rowWindow). Rendering a rowView lives in rowrender.go.

// iconRole colors a row's icon by what the row is (see newIconStyles).
type iconRole uint8

const (
	iconRoleNone iconRole = iota
	iconRoleHerdr
	iconRoleWorkspaces
	iconRoleZoxide
	iconRoleProjects
	iconRoleSessions
	iconRoleAgents
	iconRoleCustom
	iconRoleTab
	iconRoleCount
)

// sourceIconRole maps a candidate source to its icon role. Every source that
// is not built in is a [[sources.custom]] provider.
func sourceIconRole(src string) iconRole {
	switch src {
	case config.SourceHerdr:
		return iconRoleHerdr
	case config.SourceWorkspaces:
		return iconRoleWorkspaces
	case config.SourceZoxide:
		return iconRoleZoxide
	case config.SourceProjects:
		return iconRoleProjects
	case config.SourceSessions:
		return iconRoleSessions
	case config.SourceAgents:
		return iconRoleAgents
	default:
		return iconRoleCustom
	}
}

// worktreeIcon is the git-branch glyph (Nerd Font U+E725) a worktree row
// shows instead of its source icon. Like the default source icons it
// carries a trailing space (Nerd Font glyphs often draw wider than their one
// measured cell), so worktree rows align with their siblings.
const worktreeIcon = "\ue725 "

// accessoryRole selects how one accessory part renders.
type accessoryRole uint8

const (
	accessoryMuted accessoryRole = iota
	accessoryError
	accessoryPin
	// accessoryStatus holds an agent status word; its glyph (the shared
	// spinner frame for "working") is resolved at render time.
	accessoryStatus
)

// accessory is one part of a row's right-aligned accessory group.
type accessory struct {
	text  string
	role  accessoryRole
	width int // cells; a status glyph is always one cell
	// shrink marks an agent row's workspace label: it gives up cells from
	// its end down to its cap, and yields entirely to the title (see
	// fitAccessories and fitRow).
	shrink bool
}

// An agent row's workspace label accessory takes at most
// agentLabelMaxPercent of the row and agentLabelMaxCells, and is dropped
// when the title would keep fewer than agentTitleMinCells: the title is what
// the user reads, the workspace only places it.
const (
	agentLabelMaxPercent = 30
	agentLabelMaxCells   = 24
	agentTitleMinCells   = 24
)

// rowView is one list row's display model: everything rendering needs that
// depends only on the row and the model's configuration — the tree prefix,
// the icon and its role, the label split filename-first with its highlight
// masks, and the accessories — with cell widths precomputed. What changes
// without a row rebuild (the spinner frame, selection, the column width) is
// resolved at render time. Every text it holds that came from outside shep
// (a label, a terminal title, a Meta value, a custom source's icon) has been
// through plainText, here, once per build: a control character or escape
// sequence in it would move the terminal's cursor mid-frame.
type rowView struct {
	indent, tree string // the tree prefix (see treePrefix)
	icon         string
	iconRole     iconRole
	// status is the agent status word whose glyph follows the icon on pane
	// and agent rows; statusGlyph reports whether that word has a glyph.
	status      string
	statusGlyph bool
	// fixedW is the cells of the prefix, icon and status glyph with their
	// separating spaces: never truncated.
	fixedW int

	primary     string
	primaryHL   []bool // per-rune highlight mask; nil when nothing matched
	primaryW    int
	secondary   string // the parent of a filename-first label; "" when single-part
	secondaryHL []bool
	secondaryW  int
	// lead is the primary's leading tab number, drawn muted so the tab's
	// label reads first; "" for every other row.
	lead string
	// keepStart truncates the primary on the right instead of the left.
	keepStart  bool
	descendant bool

	accessories []accessory
	// kind names what the row is in the preview title (see rowKindLabel).
	kind string
}

// buildRowView builds row's display model.
func (m Model) buildRowView(row Row) rowView {
	set := m.icons()
	c := row.Candidate
	agent := c.Source == config.SourceAgents
	var v rowView
	v.indent, v.tree = m.treePrefix(row)
	v.descendant = row.Match == MatchDescendant
	switch {
	case row.Kind == RowTab:
		v.icon, v.iconRole = set.TabIcon, iconRoleTab
	case agent:
		v.icon, v.iconRole = plainText(c.Icon), iconRoleAgents
		v.status = c.Meta["agent_status"]
	case row.Kind == RowPane:
		v.status = c.Meta["agent_status"]
	case c.Meta["is_worktree"] == "true":
		v.icon, v.iconRole = worktreeIcon, iconRoleProjects
	default:
		// A custom source's icon comes from its command's JSON.
		v.icon, v.iconRole = plainText(c.Icon), sourceIconRole(c.Source)
	}
	v.statusGlyph = hasStatusGlyph(v.status)
	v.fixedW = ansi.StringWidth(v.indent) + ansi.StringWidth(v.tree)
	if v.icon != "" {
		v.fixedW += ansi.StringWidth(v.icon) + 1
	}
	if v.statusGlyph {
		v.fixedW += 2
	}

	label := abbreviateHome(m.renderRowLabel(row), m.homeDir)
	mask := m.highlightMask(row, label)
	if (row.Kind == RowCandidate || row.Kind == RowPane) && !agent {
		v.primary, v.primaryHL, v.secondary, v.secondaryHL = splitFilenameFirst(label, mask)
	} else {
		v.primary, v.primaryHL = label, mask
	}
	// Agent titles and tree tab/pane names read like titles, and their first
	// words carry the meaning, so they keep their start; a path (or the name
	// split out of one) keeps its tail, the deepest directory. Keeping the
	// start of agent titles deliberately reverts commit 00ace1a's shared
	// left truncation for agent rows.
	v.keepStart = agent || (row.Kind != RowCandidate && v.secondary == "" && !isPathLike(label))
	if n := c.Meta["tab_number"]; row.Kind == RowTab && n != "" && (v.primary == n || strings.HasPrefix(v.primary, n+" ")) {
		v.lead = n
	}
	v.primaryW = ansi.StringWidth(v.primary)
	v.secondaryW = ansi.StringWidth(v.secondary)
	v.accessories = m.rowAccessories(row, set)
	v.kind = plainText(rowKindLabel(row))
	return v
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

// highlightMask marks the runes of label the query matched. Highlighting
// rescores the text the row actually shows — row.MatchedIndexes index the
// matching haystack (label + path), not the rendered label — and only
// direct matches of top-level and agent rows are highlighted.
func (m Model) highlightMask(row Row, label string) []bool {
	if row.Match != MatchDirect || len(row.MatchedIndexes) == 0 ||
		(row.Kind != RowCandidate && row.Candidate.Source != config.SourceAgents) {
		return nil
	}
	_, indexes := fuzzy.Score(m.query, label)
	if len(indexes) == 0 {
		return nil
	}
	mask := make([]bool, utf8.RuneCountInString(label))
	for _, i := range indexes {
		if i >= 0 && i < len(mask) {
			mask[i] = true
		}
	}
	return mask
}

// isPathLike reports a label that is a path: absolute or home-relative.
func isPathLike(label string) bool {
	return strings.HasPrefix(label, "/") || strings.HasPrefix(label, "~")
}

// splitFilenameFirst splits a path-like label ("~/…" or "/…") at its last
// separator so the row leads with the name the user scans for: primary is
// the last element, secondary its parent ("~" for "~/foo", "/" for "/foo").
// One trailing "/" is ignored and the separating "/" is not shown. The
// highlight mask, computed on the whole label, is split with it. Any other
// label stays single-part.
func splitFilenameFirst(label string, mask []bool) (primary string, primaryHL []bool, secondary string, secondaryHL []bool) {
	if !strings.HasPrefix(label, "~/") && !strings.HasPrefix(label, "/") {
		return label, mask, "", nil
	}
	runes := []rune(label)
	end := len(runes)
	if end > 1 && runes[end-1] == '/' {
		end--
	}
	sep := -1
	for i := end - 1; i >= 0; i-- {
		if runes[i] == '/' {
			sep = i
			break
		}
	}
	if sep < 0 || sep == end-1 {
		return label, mask, "", nil
	}
	primary, primaryHL = string(runes[sep+1:end]), subMask(mask, sep+1, end)
	if sep == 0 {
		return primary, primaryHL, "/", subMask(mask, 0, 1)
	}
	return primary, primaryHL, string(runes[:sep]), subMask(mask, 0, sep)
}

// subMask returns mask[lo:hi], or nil for no mask.
func subMask(mask []bool, lo, hi int) []bool {
	if mask == nil {
		return nil
	}
	return mask[lo:hi]
}

// abbreviateHome shows a path under home with "~" in place of home. home ""
// (unresolved) or "/" (it would rewrite every absolute path) disables it.
func abbreviateHome(s, home string) string {
	if home == "" || home == "/" || !strings.HasPrefix(s, home) {
		return s
	}
	if rest := s[len(home):]; rest == "" || rest[0] == '/' {
		return "~" + rest
	}
	return s
}

// rowAccessories lists a row's right-aligned accessories, in display order:
// textual context first (an agent's workspace, a worktree's branch, a
// session's state, a missing path), then the open workspace's agent status
// glyph, the pin star and the group chevron.
func (m Model) rowAccessories(row Row, set IconSet) []accessory {
	c := row.Candidate
	var out []accessory
	add := func(text string, role accessoryRole, shrink bool) {
		w := 1
		if role != accessoryStatus {
			w = ansi.StringWidth(text)
		}
		out = append(out, accessory{text: text, role: role, width: w, shrink: shrink})
	}
	if c.Source == config.SourceAgents {
		if label := agentWorkspaceLabel(plainText(c.Meta["workspace_label"])); label != "" {
			add(label, accessoryMuted, true)
		}
		return out
	}
	if m.containsCurrentPane(row) {
		add(currentAccessory, accessoryMuted, false)
	}
	switch row.Kind {
	case RowPane:
		// A pane titled after its agent ("opencode") would repeat the name.
		if agent := plainText(c.Meta["agent"]); agent != "" && !strings.Contains(strings.ToLower(c.Label), strings.ToLower(agent)) {
			add(agent, accessoryMuted, false)
		}
		return out
	case RowTab:
		return out
	}
	if c.Meta["is_worktree"] == "true" {
		if branch := plainText(c.Meta["branch"]); branch != "" {
			add(branch, accessoryMuted, false)
		}
	}
	if c.Source == config.SourceSessions {
		add(sessionState(c, set), accessoryMuted, false)
	}
	if c.Missing {
		add("missing", accessoryError, false)
	}
	if c.Source == config.SourceHerdr {
		if status := m.tree.WorkspaceAgentStatus(c.Meta["workspace_id"]); status != "" {
			add(status, accessoryStatus, false)
		}
	}
	if m.rankingSnapshot.IsPinned(c) {
		add(set.Pinned, accessoryPin, false)
	}
	if c.Meta["group"] == "true" {
		add(set.Group, accessoryMuted, false)
	}
	return out
}

// currentAccessory marks the rows that hold the pane shep runs in (see
// containsCurrentPane).
const currentAccessory = "current"

// agentWorkspaceLabel shortens an agent row's workspace label for its
// accessory: a path-like label (a workspace named after its directory)
// shows only its last element, since the parent path is the part the user
// does not scan for; any other label is kept whole.
func agentWorkspaceLabel(label string) string {
	if !isPathLike(label) {
		return label
	}
	trimmed := strings.TrimRight(label, "/")
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 && i < len(trimmed)-1 {
		return trimmed[i+1:]
	}
	return label
}

// sessionState renders a session's known state: running or stopped, and
// whether it is the default session.
func sessionState(c source.Candidate, set IconSet) string {
	state := "stopped"
	if c.Meta["running"] == "true" {
		state = "running"
	}
	if c.Meta["default"] == "true" {
		state += " " + set.HintSeparator + " default"
	}
	return state
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
