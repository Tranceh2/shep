package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/source"
)

// chrome.go renders the grid's non-body rows (see geometry.go): the tab
// strip, the prompt row with its result count, the preview title, the rule
// and the footer, plus the help overlay that reuses the same grid.

// helpTitle names the help overlay on the prompt row it replaces.
const helpTitle = "Keyboard & search"

// footerStatusGap is the minimum blank cells between the footer hints and a
// right-aligned status message.
const footerStatusGap = 2

// fitWidth pads s with spaces, or cuts it, to exactly width cells. Every
// grid cell is assembled by concatenation, so one line that measured wrong
// would shift the divider on that row; this is the guard that keeps the
// columns straight. Cutting adds no ellipsis: callers truncate meaningfully
// before this point and the cut is only a safety net.
func fitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := ansi.StringWidth(s)
	switch {
	case w == width:
		return s
	case w < width:
		return s + strings.Repeat(" ", width-w)
	default:
		return ansi.Truncate(s, width, "")
	}
}

// tabName is the text a tab is shown and referred to by: its configured
// label, else its identifier.
func tabName(tab TabDefinition) string {
	if tab.Label != "" {
		return tab.Label
	}
	return tab.ID
}

// renderTabStrip renders row 0 at width cells. Every tab is padded
// " label " so its width does not change when it becomes active; the active
// tab sits on the selected surface, the others read as secondary text, and
// the agents tab turns the blocked color while any agent is blocked. The
// strip is shown at every width: when the tabs do not fit, a window around
// the active tab is shown with an overflow marker on each side that hides
// tabs (see tabStripWindow).
func (m Model) renderTabStrip(width int) string {
	tabs := m.tabs()
	activeID := m.ActiveTab()
	active := 0
	names := make([]string, len(tabs))
	widths := make([]int, len(tabs))
	agentsTab := false
	for i, tab := range tabs {
		names[i] = tabName(tab)
		widths[i] = ansi.StringWidth(names[i]) + 2
		if tab.ID == activeID {
			active = i
		}
		agentsTab = agentsTab || tab.Kind == TabAgents
	}
	agentsBlocked := agentsTab && m.AgentCounts().Blocked > 0

	overflow := m.icons().Overflow
	overflowW := ansi.StringWidth(overflow)
	lo, hi := tabStripWindow(widths, active, width, overflowW)
	markers := 0
	if lo > 0 {
		markers += overflowW + 1
	}
	if hi < len(tabs) {
		markers += overflowW + 1
	}
	if hi-lo == 1 && widths[active]+markers > width {
		// Not even the active tab fits beside the markers: show it alone,
		// truncated, so the active view is always named.
		name := truncateToWidth(names[active], max(1, width-2))
		return fitWidth(m.tabStyle(tabs[active], true, agentsBlocked).Render(" "+name+" "), width)
	}

	var b strings.Builder
	if lo > 0 {
		b.WriteString(m.styles.mutedStyle.Render(overflow))
		b.WriteByte(' ')
	}
	for i := lo; i < hi; i++ {
		if i > lo {
			b.WriteByte(' ')
		}
		b.WriteString(m.tabStyle(tabs[i], i == active, agentsBlocked).Render(" " + names[i] + " "))
	}
	if hi < len(tabs) {
		b.WriteByte(' ')
		b.WriteString(m.styles.mutedStyle.Render(overflow))
	}
	return fitWidth(b.String(), width)
}

// tabStripWindow returns the half-open range [lo, hi) of tabs that fits in
// budget cells around the active tab, counting one separator space between
// tabs and an overflow marker plus its space on each side that hides tabs.
// It grows toward the next tab first (where tab moves to), then the previous
// one, alternating, so the active tab stays visible and roughly centered.
// When the active tab cannot fit at all the range is just the active tab.
func tabStripWindow(widths []int, active, budget, overflowW int) (lo, hi int) {
	n := len(widths)
	cost := func(lo, hi, used int) int {
		if lo > 0 {
			used += overflowW + 1
		}
		if hi < n {
			used += overflowW + 1
		}
		return used
	}
	lo, hi = active, active+1
	used := widths[active]
	for grew := true; grew; {
		grew = false
		if hi < n && cost(lo, hi+1, used+1+widths[hi]) <= budget {
			used += 1 + widths[hi]
			hi++
			grew = true
		}
		if lo > 0 && cost(lo-1, hi, used+1+widths[lo-1]) <= budget {
			lo--
			used += 1 + widths[lo]
			grew = true
		}
	}
	return lo, hi
}

// tabStyle picks one tab's style: the selected surface when active, the
// blocked color for the agents tab while an agent is blocked, else the
// secondary role.
func (m Model) tabStyle(tab TabDefinition, active, agentsBlocked bool) lipgloss.Style {
	blocked := agentsBlocked && tab.Kind == TabAgents
	switch {
	case active && blocked:
		return m.styles.tabActiveBlockedStyle
	case active:
		return m.styles.tabActiveStyle
	case blocked:
		return m.styles.statusBlockedStyle
	default:
		return m.styles.secondaryStyle
	}
}

// renderPromptRow renders the list column of row 1 at width cells: the
// prompt glyph, the query in bold with a one-cell block cursor after it (or,
// with an empty query, the cursor and the view's placeholder), and the result
// count right-aligned. The query is what the user is reading, so it is never
// cut for the count: the count is dropped first, then the query loses its
// start — the cursor end, where typing happens, stays visible.
func (m Model) renderPromptRow(width int) string {
	if m.edit.open() {
		return m.renderEditPrompt(width)
	}
	glyph := m.icons().SearchPrompt
	prompt := m.styles.promptStyle.Render(glyph) + " "
	room := width - ansi.StringWidth(glyph) - 2 // cells left after the prompt and the cursor
	if room < 1 {
		return fitWidth(prompt, width)
	}
	cursor := m.styles.queryCursorStyle.Render(" ")
	count := m.renderResultCount(m.resultCount())
	countW := ansi.StringWidth(count)

	var left string
	if m.query == "" {
		placeholder, keepCount := fitPlaceholder(m.tabPresentation().Placeholder, room, countW)
		if !keepCount {
			count, countW = "", 0
		}
		left = prompt + cursor
		if placeholder != "" {
			left += m.styles.placeholderStyle.Render(placeholder)
		}
	} else {
		query := m.query
		queryW := ansi.StringWidth(query)
		if queryW+1+countW > room {
			count, countW = "", 0
		}
		if queryW > room {
			query = truncateFromLeftToWidth(query, room)
		}
		left = prompt + m.styles.queryTextStyle.Render(query) + cursor
	}
	if countW == 0 {
		return fitWidth(left, width)
	}
	return fitWidth(left, width-countW) + count
}

// renderEditPrompt renders the open line edit in place of the search prompt:
// what it is for, the prompt glyph, and the text with the block cursor after
// it. Like the query, the text keeps its end, where typing happens, when it
// does not fit.
func (m Model) renderEditPrompt(width int) string {
	lead := m.styles.warnStyle.Render(m.edit.prompt) + " " + m.styles.promptStyle.Render(m.icons().SearchPrompt) + " "
	room := max(0, width-ansi.StringWidth(lead)-1)
	text := m.edit.text
	if ansi.StringWidth(text) > room {
		text = truncateFromLeftToWidth(text, room)
	}
	return fitWidth(lead+m.styles.queryTextStyle.Render(text)+m.styles.queryCursorStyle.Render(" "), width)
}

// placeholderShort is the placeholder every view falls back to when its
// full one does not fit beside the count: a whole word reads better than a
// phrase cut mid-word.
const placeholderShort = "Search"

// fitPlaceholder picks the placeholder for room cells (after the prompt and
// the cursor) beside a count of countW cells. The placeholder is only a
// hint, so it yields to the count: the full one when both fit, else the
// short one, else the count alone. Only when even the count does not fit is
// it dropped for the (possibly truncated) short placeholder.
func fitPlaceholder(full string, room, countW int) (placeholder string, keepCount bool) {
	for _, p := range []string{full, placeholderShort} {
		if ansi.StringWidth(p)+1+countW <= room {
			return p, true
		}
	}
	if countW <= room {
		return "", true
	}
	if ansi.StringWidth(full) <= room {
		return full, false
	}
	return truncateToWidth(placeholderShort, room), false
}

// resultCount is the prompt row's tally: how many top-level results the
// active view shows out of how many it holds.
type resultCount struct {
	shown, total int
	// filtered reports an active query: the tally reads "shown/total".
	filtered bool
	// loading reports producers still streaming candidates in.
	loading bool
}

// awaiting reports producers streaming with nothing loaded yet: there is no
// number to show.
func (c resultCount) awaiting() bool { return c.loading && c.total == 0 }

// text renders the tally without styling: "651" unfiltered, "12/651"
// filtered, or "loading…" while producers stream with nothing loaded yet.
func (c resultCount) text() string {
	switch {
	case c.awaiting():
		return "loading…"
	case c.filtered:
		return strconv.Itoa(c.shown) + "/" + strconv.Itoa(c.total)
	default:
		return strconv.Itoa(c.total)
	}
}

// resultCount resolves the active view's tally. The agents view counts its
// own agent rows against every detected agent; every other view counts
// visible top-level candidates against the view's candidate set — the same
// unit on both sides, so synthesized tab/pane rows never inflate it.
func (m Model) resultCount() resultCount {
	c := resultCount{filtered: m.query != ""}
	if m.activeDefinition().Kind == TabAgents {
		c.shown = len(m.rows)
		c.total = c.shown
		if c.filtered {
			c.total = m.AgentCounts().Total
		}
		return c
	}
	c.loading = m.loadingCandidates
	c.shown = m.visibleTopLevelCount()
	c.total = m.candidateTotal()
	return c
}

// renderResultCount styles a tally: the shown count in the secondary role
// and the rest muted, prefixed by the shared spinner frame while producers
// are still loading.
func (m Model) renderResultCount(c resultCount) string {
	var b strings.Builder
	if c.loading {
		b.WriteString(m.spinner.View())
		b.WriteByte(' ')
	}
	if c.filtered && !c.awaiting() {
		b.WriteString(m.styles.secondaryStyle.Render(strconv.Itoa(c.shown)))
		b.WriteString(m.styles.mutedStyle.Render("/" + strconv.Itoa(c.total)))
	} else {
		b.WriteString(m.styles.mutedStyle.Render(c.text()))
	}
	return b.String()
}

// candidateTotal counts the active view's top-level candidates: the
// denominator of the prompt count.
func (m Model) candidateTotal() int {
	switch m.activeDefinition().Kind {
	case TabAll:
		if len(m.layout.Tabs) > 0 {
			return len(m.allTabCandidates())
		}
		return len(m.baseFlatCandidates())
	case TabGroup:
		return len(m.groupCandidates[m.ActiveTab()])
	}
	if cands, ok := m.candidatesBySource[m.ActiveTab()]; ok {
		return len(cands)
	}
	n := 0
	for _, c := range m.baseFlatCandidates() {
		if c.Source == m.ActiveTab() {
			n++
		}
	}
	return n
}

// visibleTopLevelCount counts visible RowCandidate rows only (top-level
// candidates), excluding synthesized RowTab/RowPane descendants — the same
// unit as candidateTotal, so the prompt's "M/N" compares like for like. A
// descendant-only query (e.g. a tab label match under a workspace) shows 1,
// not 1+children.
func (m Model) visibleTopLevelCount() int {
	n := 0
	for _, r := range m.rows {
		if r.Kind == RowCandidate {
			n++
		}
	}
	return n
}

// renderPreviewTitle renders the preview column of row 1 at width cells: the
// highlighted row's label — the text its label part shows, without live
// markers — in bold (a muted or accent part of it keeps that style), and its
// kind right-aligned and muted. The kind is dropped first when the two do
// not fit; the name then keeps its start (a title) or its end (a path). The
// body below carries everything else, so the title never repeats a
// "preview" caption.
func (m Model) renderPreviewTitle(width int) string {
	row, ok := m.currentRow()
	if !ok || width <= 0 {
		return strings.Repeat(" ", max(0, width))
	}
	v := m.cursorRowView()
	name := v.label.withoutLive()
	name.hl = nil
	if name.width == 0 {
		name = plainPart(plainText(row.Candidate.Label))
	}
	if name.width == 0 {
		name = plainPart(m.layout.Templates.Tilde(plainText(row.Candidate.Path)))
	}
	kind := v.kind
	kindW := ansi.StringWidth(kind)
	if kind == "" || name.width+1+kindW > width {
		return fitWidth(m.renderTitleName(truncatePart(name, width, !name.pathLike())), width)
	}
	return fitWidth(m.renderTitleName(name), width-kindW) + m.styles.mutedStyle.Render(kind)
}

// renderTitleName draws the title's name in the title style, the runs its
// label template muted or accented in those styles.
func (m Model) renderTitleName(name part) string {
	if name.runs == nil {
		return m.styles.titleStyle.Render(name.text)
	}
	var b strings.Builder
	start := 0
	for _, r := range name.runs {
		style := m.styles.titleStyle
		switch r.role {
		case roleMuted:
			style = m.styles.mutedStyle
		case roleAccent:
			style = m.styles.accentStyle
		}
		if r.bold {
			style = style.Bold(true)
		}
		b.WriteString(style.Render(name.text[start:r.end]))
		start = r.end
	}
	return b.String()
}

// renderRule renders row 2 across the content width. With the preview
// column shown, the divider crosses the rule with a junction one cell past
// the list column (the divider's leading space becomes rule, too).
func (m Model) renderRule(g pickerGeometry, junction bool) string {
	set := m.icons()
	if !junction || g.PreviewWidth == 0 {
		return m.styles.ruleStyle.Render(strings.Repeat(set.RuleHorizontal, g.ContentWidth))
	}
	return m.styles.ruleStyle.Render(strings.Repeat(set.RuleHorizontal, g.ListWidth+1) +
		set.RuleJunction + strings.Repeat(set.RuleHorizontal, g.PreviewWidth+1))
}

// renderDivider renders the vertical divider between the columns: the rule
// glyph with one space on each side (dividerWidth cells).
func (m Model) renderDivider() string {
	return " " + m.styles.ruleStyle.Render(m.icons().RuleVertical) + " "
}

// --- footer ---

// Footer hint priorities: when the footer cannot fit every hint, the hint
// with the highest number is dropped first. Display order is independent and
// fixed by footerHints, so dropping a hint never reorders the others.
const (
	hintPriorityEnter = iota + 1
	hintPriorityTab
	hintPriorityHelp
	hintPriorityEsc
	hintPriorityPin
	hintPriorityClose
	hintPriorityNewTab
	hintPriorityNewPane
	hintPriorityRename
	hintPriorityBlocked
)

// footerHint is one "key label" pair of the footer.
type footerHint struct {
	key, label string
	priority   int
}

// footerHints lists the actions available right now, in display order
// (enter, tab, ctrl+f, ctrl+t, ctrl+p, ctrl+x, ?, esc). An action that would
// do nothing on the highlighted row is left out instead of shown as
// unavailable. Every key and label comes from the shared keyBinding values
// (keymap.go) or the row's action descriptor, so the footer and the help
// overlay cannot drift.
func (m Model) footerHints() []footerHint {
	hints := make([]footerHint, 0, 8)
	row, hasRow := m.currentRow()
	if hasRow {
		hints = append(hints, footerHint{keyBindingEnter.footerChord, rowActionDescriptor(row).FooterLabel, hintPriorityEnter})
	}
	if len(m.tabs()) > 1 {
		hints = append(hints, footerHint{keyBindingTab.footerChord, tabName(m.adjacentTab(1)), hintPriorityTab})
	}
	if hasRow && row.Kind == RowCandidate && m.layout.PinToggler != nil {
		label := keyBindingPin.footerLabel
		if m.rankingSnapshot.IsPinned(row.Candidate) {
			label = keyBindingPin.footerAltLabel
		}
		hints = append(hints, footerHint{keyBindingPin.footerChord, label, hintPriorityPin})
	}
	if cand, ok := m.currentCandidate(); ok && m.currentPane != nil && source.SupportsCurrentWorkspaceTarget(cand) {
		hints = append(hints,
			footerHint{keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel, hintPriorityNewTab},
			footerHint{keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel, hintPriorityNewPane},
		)
	}
	if _, isItem := herdrItemFor(row); hasRow && isItem {
		if m.layout.Closer != nil {
			hints = append(hints, footerHint{keyBindingClose.footerChord, keyBindingClose.footerLabel, hintPriorityClose})
		}
		if m.layout.Renamer != nil {
			hints = append(hints, footerHint{keyBindingRename.footerChord, keyBindingRename.footerLabel, hintPriorityRename})
		}
	}
	if m.AgentCounts().Blocked > 0 {
		hints = append(hints, footerHint{keyBindingBlocked.footerChord, keyBindingBlocked.footerLabel, hintPriorityBlocked})
	}
	escLabel := keyBindingEsc.footerLabel
	if m.query != "" {
		escLabel = keyBindingEsc.footerAltLabel
	}
	return append(hints,
		footerHint{keyBindingHelp.footerChord, keyBindingHelp.footerLabel, hintPriorityHelp},
		footerHint{keyBindingEsc.footerChord, escLabel, hintPriorityEsc},
	)
}

// renderFooter renders the footer row at width cells. A pending close
// confirmation, or a close problem or progress message, replaces the hints:
// none of them may hide behind keycaps. Otherwise the available hints are
// shown, with success feedback ("closed tab", "pinned") right-aligned when
// it fits — or in place of the lowest-priority hints when it does not.
func (m Model) renderFooter(width int) string {
	switch {
	case m.edit.open():
		return m.renderHintLine([]footerHint{
			{keyChordEnter, m.edit.submitLabel(), hintPriorityEnter},
			{keyBindingEditCancel.footerChord, keyBindingEditCancel.footerLabel, hintPriorityEsc},
		}, footerStatus{}, width)
	case m.closeConfirm != nil:
		return fitWidth(m.renderCloseConfirm(*m.closeConfirm), width)
	case m.actionStatus.text != "" && m.actionStatus.tone != toneSuccess:
		return fitWidth(m.renderStatus(m.actionStatus), width)
	}
	status := m.actionStatus
	if status.text == "" {
		status = m.pinStatus
	}
	return m.renderHintLine(m.footerHints(), status, width)
}

// renderHintLine joins hints into one line of width cells, dropping hints by
// priority until they fit beside the right-aligned status (if any).
func (m Model) renderHintLine(hints []footerHint, status footerStatus, width int) string {
	sep := " " + m.icons().HintSeparator + " "
	budget := width
	statusW := ansi.StringWidth(status.text)
	if statusW > 0 {
		budget -= statusW + footerStatusGap
	}
	line := m.joinHints(fitHints(hints, budget, ansi.StringWidth(sep)), sep)
	if statusW == 0 {
		return fitWidth(line, width)
	}
	return fitWidth(fitWidth(line, max(0, width-statusW))+m.renderStatus(status), width)
}

// fitHints returns hints without the lowest-priority ones, dropped one at a
// time until the joined line fits budget cells; display order is kept.
func fitHints(hints []footerHint, budget, sepW int) []footerHint {
	width := func(hints []footerHint) int {
		w := max(0, len(hints)-1) * sepW
		for _, h := range hints {
			w += ansi.StringWidth(h.key) + 1 + ansi.StringWidth(h.label)
		}
		return w
	}
	if width(hints) <= budget {
		return hints
	}
	kept := slices.Clone(hints)
	for len(kept) > 0 && width(kept) > budget {
		drop := 0
		for i, h := range kept {
			if h.priority > kept[drop].priority {
				drop = i
			}
		}
		kept = slices.Delete(kept, drop, drop+1)
	}
	return kept
}

// joinHints renders hints as "key label" pairs separated by sep in the rule
// color.
func (m Model) joinHints(hints []footerHint, sep string) string {
	styledSep := m.styles.ruleStyle.Render(sep)
	var b strings.Builder
	for i, h := range hints {
		if i > 0 {
			b.WriteString(styledSep)
		}
		b.WriteString(renderKeycap(m.styles, h.key, h.label))
	}
	return b.String()
}

// renderKeycap renders one footer shortcut: a bold key token followed by a
// secondary action label. It intentionally has no brackets or box-like
// decoration.
func renderKeycap(s *styleSet, key, label string) string {
	return s.keycapStyle.Render(key) + " " + s.keycapLabelStyle.Render(label)
}

// renderCloseConfirm renders the close confirmation that replaces the hints:
// the question in the warn color, y as the one confirming key, and a muted
// reminder that any other key backs out.
func (m Model) renderCloseConfirm(target herdrItem) string {
	sep := m.styles.ruleStyle.Render(" " + m.icons().HintSeparator + " ")
	return m.styles.warnStyle.Render(target.question()) + "  " +
		renderKeycap(m.styles, keyBindingConfirmClose.footerChord, keyBindingConfirmClose.footerLabel) + sep +
		m.styles.mutedStyle.Render(closeCancelHint)
}

// statusTone selects how a transient footer message reads.
type statusTone int

const (
	// toneInfo is neutral progress or feedback ("closing tab...").
	toneInfo statusTone = iota
	// toneSuccess is a completed action ("closed tab", "pinned").
	toneSuccess
	// toneError is a failed or refused action ("close failed: ...").
	toneError
)

// footerStatus is one transient footer message. The zero value is "none".
type footerStatus struct {
	text string
	tone statusTone
}

// A footer message can carry an error's text (a failed Herdr command's
// output), so each is made plain once, when it is set (see plainText).
func infoStatus(text string) footerStatus {
	return footerStatus{text: plainText(text), tone: toneInfo}
}

func successStatus(text string) footerStatus {
	return footerStatus{text: plainText(text), tone: toneSuccess}
}

func errorStatus(text string) footerStatus {
	return footerStatus{text: plainText(text), tone: toneError}
}

// renderStatus styles a footer message by its tone.
func (m Model) renderStatus(s footerStatus) string {
	switch s.tone {
	case toneSuccess:
		return m.styles.successStyle.Render(s.text)
	case toneError:
		return m.styles.previewErrStyle.Render(s.text)
	default:
		return m.styles.mutedStyle.Render(s.text)
	}
}

// question is the confirmation prompt for closing target.
func (t herdrItem) question() string {
	return fmt.Sprintf("close %s %q?", t.kind, t.label)
}

// --- help overlay ---

// writeHelp writes the "?" overlay below the tab strip, in the same grid:
// the overlay's title on the prompt row, a full rule, the scrollable help
// body (helpViewport, sized by syncHelpViewport) and the overlay's footer.
func (m Model) writeHelp(f *frame, g pickerGeometry) {
	f.line(fitWidth(m.styles.titleStyle.Render(helpTitle), g.ContentWidth))
	f.line(m.renderRule(g, false))
	body := strings.Split(m.helpViewport.View(), "\n")
	rows := g.ListInnerRows
	if m.height <= 0 {
		rows = len(body)
	}
	blank := strings.Repeat(" ", g.ContentWidth)
	for i := range rows {
		line := blank
		if i < len(body) {
			line = fitWidth(body[i], g.ContentWidth)
		}
		f.line(line)
	}
	f.line(m.renderHintLine(m.helpFooterHints(), footerStatus{}, g.ContentWidth))
}

// Help cheat sheet layout: the search syntax sits beside the shortcuts from
// helpTwoColumnWidth content columns, helpColumnGap cells apart; entries are
// indented helpEntryIndent cells under their section title.
const (
	helpTwoColumnWidth = 100
	helpColumnGap      = 4
	helpEntryIndent    = 2
)

// helpBodyText renders the help cheat sheet for a body width cells wide:
// the shortcut sections (keyMap) and the search syntax (searchSyntax),
// side by side from helpTwoColumnWidth and stacked below it. The Enter line
// describes the highlighted row's action, the same truth the footer and
// handleEnter tell. Fed into helpViewport by syncHelpViewport, so a short
// terminal scrolls the sheet instead of clipping it.
func (m Model) helpBodyText(width int) string {
	ascii := m.icons().Name == IconsASCII
	enter := m.enterHelpText()
	sections := make([]helpSection, len(keyMap))
	for i, s := range keyMap {
		sections[i] = helpSection{title: s.title, bindings: slices.Clone(s.bindings)}
		for j := range sections[i].bindings {
			if sections[i].bindings[j].chord == keyChordEnter {
				sections[i].bindings[j].help = enter
			}
		}
	}
	if width < helpTwoColumnWidth {
		return strings.Join(m.helpColumn(append(sections, searchSyntax), width, ascii), "\n")
	}
	leftW := (width - helpColumnGap) / 2
	left := m.helpColumn(sections, leftW, ascii)
	right := m.helpColumn([]helpSection{searchSyntax}, width-helpColumnGap-leftW, ascii)
	lines := make([]string, max(len(left), len(right)))
	gap := strings.Repeat(" ", helpColumnGap)
	for i := range lines {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines[i] = fitWidth(l, leftW) + gap + r
	}
	return strings.Join(lines, "\n")
}

// helpColumn renders sections as one column at most width cells wide: each
// section's title, then its entries — the keys as keycaps padded to the
// column's widest keys (measured from the content, not a fixed width), then
// the description, word-wrapped under itself when it does not fit — with a
// blank line between sections.
func (m Model) helpColumn(sections []helpSection, width int, ascii bool) []string {
	keyW := 0
	for _, s := range sections {
		for _, b := range s.bindings {
			keyW = max(keyW, ansi.StringWidth(b.chordFor(ascii)))
		}
	}
	indent := strings.Repeat(" ", helpEntryIndent)
	hanging := strings.Repeat(" ", helpEntryIndent+keyW+2)
	descW := max(1, width-len(hanging))
	var lines []string
	for i, s := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.styles.previewHeadingStyle.Render(s.title))
		for _, b := range s.bindings {
			for j, desc := range strings.Split(ansi.Wrap(b.help, descW, ""), "\n") {
				lead := hanging
				if j == 0 {
					lead = indent + m.styles.keycapStyle.Render(fitWidth(b.chordFor(ascii), keyW)) + "  "
				}
				lines = append(lines, lead+m.styles.keycapLabelStyle.Render(desc))
			}
		}
	}
	return lines
}

// helpFooterHints lists the overlay's own actions: close it, scroll it. The
// ASCII icon tier spells the arrows out.
func (m Model) helpFooterHints() []footerHint {
	scroll := keyBindingHelpScroll.footerChord
	if m.icons().Name == IconsASCII {
		scroll = keyChordScrollArrowsASCII
	}
	return []footerHint{
		{key: keyBindingHelpClose.footerChord, label: keyBindingHelpClose.footerLabel, priority: 1},
		{key: scroll, label: keyBindingHelpScroll.footerLabel, priority: 2},
	}
}
