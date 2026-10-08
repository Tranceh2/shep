package tui

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// isDoubleEsc reports two Esc presses that reached the terminal in the same
// read. Without key disambiguation the terminal sends them as "\x1b\x1b",
// which decodes as one alt-modified Esc ("alt+esc"), so a quick double tap —
// the usual way to back out of a popup — would otherwise match no binding
// and leave the picker open.
func isDoubleEsc(msg tea.KeyPressMsg) bool {
	return msg.Code == tea.KeyEscape && msg.Mod.Contains(tea.ModAlt)
}

// replayEsc applies n plain Esc presses in order, exactly as if they had
// arrived one by one: each may close help, cancel a pending close
// confirmation, clear the query or cancel the picker. It stops as soon as one
// press quits, so a cancel is never followed by further state changes.
func (m Model) replayEsc(n int) (Model, tea.Cmd) {
	cmds := make([]tea.Cmd, 0, n)
	for range n {
		var cmd tea.Cmd
		m, cmd = m.handleInput("esc", "")
		cmds = append(cmds, cmd)
		if m.cancelled {
			break
		}
	}
	return m, tea.Batch(cmds...)
}

// keyText returns the text a key press types into the query: none for a
// ctrl- or alt-modified chord ("alt+x" is a chord, never text).
func keyText(msg tea.KeyPressMsg) string {
	if msg.Mod.Contains(tea.ModCtrl) || msg.Mod.Contains(tea.ModAlt) {
		return ""
	}
	return msg.Text
}

// appendQueryText returns query extended with the printable runes of text,
// and whether anything was added. Control runes (C0, DEL, C1) are dropped
// instead of inserted: a bracketed paste delivers newlines, tabs and
// carriage returns verbatim, the launcher query is a single line, and a raw
// control character echoed back into the prompt row could drive the
// terminal. They vanish rather than turning into spaces so a pasted wrapped
// path stays one search term.
func appendQueryText(query, text string) (string, bool) {
	var added strings.Builder
	for _, r := range text {
		if !unicode.IsControl(r) {
			added.WriteRune(r)
		}
	}
	if added.Len() == 0 {
		return query, false
	}
	return query + added.String(), true
}

// deleteLastRune returns query without its last rune. Slicing off the last
// byte instead would split a multi-byte rune (á, ñ, emoji) and leave
// invalid UTF-8 in the query. An empty query is returned unchanged.
func deleteLastRune(query string) string {
	_, size := utf8.DecodeLastRuneInString(query)
	return query[:len(query)-size]
}

// deleteLastWord returns query without its last word, following readline's
// unix-word-rubout (ctrl+w): first the trailing whitespace, then the run of
// non-whitespace runes before it, keeping the whitespace that separates it
// from the previous word ("foo bar  " -> "foo "). Whitespace is the word
// boundary because it is also the term separator of the search syntax.
func deleteLastWord(query string) string {
	trimmed := strings.TrimRightFunc(query, unicode.IsSpace)
	i := strings.LastIndexFunc(trimmed, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	_, size := utf8.DecodeRuneInString(trimmed[i:])
	return trimmed[:i+size]
}

// setQuery replaces the query and applies the side effects every query edit
// shares: rows are refiltered, and the preview is re-synced to whichever row
// is now highlighted. The command is built before returning so the
// pointer-receiver mutations of applyFilter and
// syncPreviewAfterSelectionChange are guaranteed to land in the returned
// model.
func (m Model) setQuery(query string) (Model, tea.Cmd) {
	m.query = query
	cmd := tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
	return m, cmd
}

// deleteFromQuery applies a deletion edit (deleteLastRune for backspace,
// deleteLastWord for ctrl+w/alt+backspace) through setQuery. With an empty
// query there is nothing to delete: the preview is re-synced, but rows are
// not refiltered.
func (m Model) deleteFromQuery(edit func(string) string) (Model, tea.Cmd) {
	if m.query == "" {
		cmd := m.syncPreviewAfterSelectionChange()
		return m, cmd
	}
	return m.setQuery(edit(m.query))
}

// scrollViewport applies one scroll key to vp in place: up/down/ctrl+j/
// ctrl+k move one line, pgup/pgdown move one page, home/end jump to top/
// bottom. The help overlay takes them all; the list passes pgup/pgdown on to
// the preview pane. bubbles/viewport's own KeyMap does not
// recognize ctrl+j/ctrl+k/home/end at all (see its DefaultKeyMap), so this
// dispatches directly to the viewport's line/page/goto methods instead of
// delegating to viewport.Update — that also sidesteps the fact that
// viewport's default keymap binds bare "u"/"d" to half-page scroll, which
// would otherwise swallow the letters "u" and "d" out of a live query.
// Returns false (and touches nothing) for any key outside this set.
func scrollViewport(vp *viewport.Model, key string) bool {
	switch key {
	case "up", "ctrl+k":
		vp.ScrollUp(1)
	case "down", "ctrl+j":
		vp.ScrollDown(1)
	case "pgup":
		vp.PageUp()
	case "pgdown":
		vp.PageDown()
	case "home":
		vp.GotoTop()
	case "end":
		vp.GotoBottom()
	default:
		return false
	}
	return true
}

// handleKey applies one key press: its name selects a binding and its text,
// when no binding takes it, goes to the query.
func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if isDoubleEsc(msg) {
		return m.replayEsc(2)
	}
	return m.handleInput(msg.String(), keyText(msg))
}

// handlePaste applies a bracketed paste: text only, never a binding, so a
// pasted "y" cannot confirm a close.
func (m Model) handlePaste(msg tea.PasteMsg) (Model, tea.Cmd) {
	return m.handleInput("", msg.Content)
}

// handleInput applies one input: chord is the pressed key's name ("" for a
// paste) and text what it types. FocusHelp is checked first and routes
// exclusively to handleHelpFocusedKey (help is modal: only "?"/Esc close it,
// only ctrl+c/ctrl+g cancel through it, everything else is swallowed).
// Otherwise the global bindings ("?"/esc/ctrl+c/ctrl+g/tab/shift+tab/ctrl+t/
// ctrl+p/ctrl+x/ctrl+f) are checked, then the list's row-cursor and
// query-editing keys (handleListFocusedKey). "q" is an ordinary query
// character, not a cancel key.
func (m Model) handleInput(chord, text string) (Model, tea.Cmd) {
	if m.closeConfirm != nil {
		target := *m.closeConfirm
		m.closeConfirm = nil
		if chord == keyChordConfirm {
			return m.startClose(target)
		}
		m.closeStatus = infoStatus("close cancelled")
		return m, nil
	}
	if !m.closePending {
		m.closeStatus = footerStatus{}
	}
	if m.focus == FocusHelp {
		return m.handleHelpFocusedKey(chord)
	}
	switch chord {
	case "?":
		m.focus = FocusHelp
		return m, nil
	case "esc":
		// Esc priority (FocusHelp already handled above, always wins):
		// a non-empty query is cleared before ever cancelling; only an
		// empty query cancels.
		if m.query != "" {
			return m.setQuery("")
		}
		m.cancelled = true
		return m, tea.Quit
	case "ctrl+c", "ctrl+g":
		m.cancelled = true
		return m, tea.Quit
	case "tab":
		return m.cycleTabForward()
	case "shift+tab":
		return m.cycleTabBackward()
	case "ctrl+t":
		return m.selectWithTarget("tab")
	case "ctrl+p":
		return m.selectWithTarget("pane")
	case keyChordClose:
		return m.closeSelectedRow()
	case "ctrl+f":
		if m.layout.PinToggler != nil {
			return m.togglePin()
		}
	}

	return m.handleListFocusedKey(chord, text)
}

// handleHelpFocusedKey handles input while FocusHelp owns focus. Only "?"
// and Esc close help, back to the list; ctrl+c/ctrl+g remain the
// unconditional cancel escape hatch;
// up/down/ctrl+j/ctrl+k/pgup/pgdown/home/end scroll helpViewport. Every
// other input — typed or pasted text, backspace, ctrl+w, alt+backspace,
// ctrl+u, enter, ctrl+r, ctrl+t, ctrl+p, left/right — is swallowed: reading
// help must never mutate the query, move the list cursor, change layout, or
// select anything.
func (m Model) handleHelpFocusedKey(chord string) (Model, tea.Cmd) {
	switch chord {
	case "?", "esc":
		m.focus = FocusList
		return m, nil
	case "ctrl+c", "ctrl+g":
		m.cancelled = true
		return m, tea.Quit
	case "up", "down", "ctrl+j", "ctrl+k", "pgup", "pgdown", "home", "end":
		scrollViewport(&m.helpViewport, chord)
		return m, nil
	}
	return m, nil
}

func (m Model) togglePin() (Model, tea.Cmd) {
	row, ok := m.currentRow()
	if !ok {
		return m, nil
	}
	if row.Kind != RowCandidate {
		m.pinStatus = errorStatus("child rows cannot be pinned")
		return m, nil
	}
	if m.layout.PinToggler == nil {
		m.pinStatus = errorStatus("pinning unavailable")
		return m, nil
	}
	key := ranking.PinKey(row.Candidate)
	if key == "" {
		m.pinStatus = errorStatus("row has no stable pin identity")
		return m, nil
	}
	if m.pinPending {
		return m, nil
	}
	m.pinPending = true
	m.pinKey = key
	candidate := row.Candidate
	ctx := m.renderCtx
	toggler := m.layout.PinToggler
	return m, func() tea.Msg {
		msg := toggler(ctx, candidate)
		msg.Key = key
		msg.Candidate = candidate
		return msg
	}
}

func (m Model) cycleTabForward() (Model, tea.Cmd) {
	next := m.adjacentTab(1)
	m.activeTab = next.ID
	m.cursor = 0
	m.cursorTouched = false
	return m, tea.Batch(m.maybeLoadGroup(), m.applyFilter(), m.syncPreviewAfterSelectionChange())
}

func (m Model) cycleTabBackward() (Model, tea.Cmd) {
	next := m.adjacentTab(-1)
	m.activeTab = next.ID
	m.cursor = 0
	m.cursorTouched = false
	return m, tea.Batch(m.maybeLoadGroup(), m.applyFilter(), m.syncPreviewAfterSelectionChange())
}

// handleListFocusedKey applies one key press while FocusList owns focus:
// the classic row-cursor/query-editing key set, extended with Left/Right
// (or ctrl+h/ctrl+l) expand-collapse, Enter to select, ctrl+r to toggle the
// preview and pgup/pgdown to scroll the preview.
// ctrl+j/ctrl+k always move the cursor regardless of focus's usual up/down
// mapping (kept as a stable alternate binding); plain "j"/"k" are
// intentionally NOT bound to movement here so they fall through to the
// query instead. ctrl+u stays half-page up (it does not clear the query);
// backspace deletes the last rune and ctrl+w/alt+backspace the last word.
func (m Model) handleListFocusedKey(chord, text string) (Model, tea.Cmd) {
	switch chord {
	case "enter":
		return m.handleEnter()
	case keyChordLayout:
		m.toggleLayout()
		return m, nil
	case "pgup", "pgdown":
		scrollViewport(&m.viewport, chord)
		return m, nil
	case "down", "ctrl+j":
		return m.moveListCursor(1)
	case "up", "ctrl+k":
		return m.moveListCursor(-1)
	case "ctrl+d":
		return m.moveListCursor(max(1, m.geometry().ListInnerRows/2))
	case "ctrl+u":
		return m.moveListCursor(-max(1, m.geometry().ListInnerRows/2))
	case "right", "ctrl+l":
		m.cursorTouched = true
		return m, tea.Batch(m.expandCurrent(), m.syncPreviewAfterSelectionChange())
	case "left", "ctrl+h":
		m.cursorTouched = true
		return m, tea.Batch(m.collapseCurrent(), m.syncPreviewAfterSelectionChange())
	case "backspace":
		return m.deleteFromQuery(deleteLastRune)
	case "ctrl+w", "alt+backspace":
		return m.deleteFromQuery(deleteLastWord)
	default:
		if query, ok := appendQueryText(m.query, text); ok {
			return m.setQuery(query)
		}
		return m, m.syncPreviewAfterSelectionChange()
	}
}

// moveListCursor shares the row navigation side effects for single-step and
// half-page movement. The cursor always stays inside the available rows.
func (m Model) moveListCursor(delta int) (Model, tea.Cmd) {
	m.cursorTouched = true
	if len(m.rows) > 0 {
		m.cursor = max(0, min(len(m.rows)-1, m.cursor+delta))
	}
	return m, m.syncPreviewAfterSelectionChange()
}

// handleEnter selects the highlighted candidate/tab/pane row and quits. A
// row with nothing highlighted (empty rows) is a no-op. The dispatched
// selectedAction is sourced from the shared rowActionDescriptor — the same
// single source of truth the footer and help overlay read (SPEC-NAV-1.8
// parity) — so Enter behavior can never drift from the displayed copy.
func (m Model) handleEnter() (Model, tea.Cmd) {
	row, ok := m.currentRow()
	if !ok {
		return m, nil
	}
	m.selected = row.Candidate
	m.hasSelected = true
	m.selectedAction = rowActionDescriptor(row).Action
	return m, tea.Quit
}

// selectWithTarget handles ctrl+t ("tab") and ctrl+p ("pane"): see
// source.SupportsCurrentWorkspaceTarget for the exact eligibility contract.
// A row whose candidate does not support a current-workspace target is a
// silent no-op — mirrors the previous picker's behavior, now expressed over
// Row instead of a raw candidate index.
func (m Model) selectWithTarget(target string) (Model, tea.Cmd) {
	// Eligibility is checked BEFORE the missing-pane diagnostic (SPEC-NAV-4.1):
	// an invalid/ineligible row can never target the current workspace, so it
	// is a fully silent no-op even when there is no focused pane — it must not
	// raise the "no focused Herdr pane" error it could never act on.
	cand, ok := m.currentCandidate()
	if !ok || !source.SupportsCurrentWorkspaceTarget(cand) {
		return m, nil
	}
	// The row IS eligible but there is no focused pane to target: preserve the
	// existing diagnostic so an otherwise-actionable row still gives feedback.
	if m.currentPane == nil {
		m.previewErr = "no focused Herdr pane"
		return m, nil
	}
	m.selected = cand
	m.hasSelected = true
	m.selectedAction = RowActionOpen
	m.chosenTarget = target
	return m, tea.Quit
}

// toggleLayout switches what the picker shows (ctrl+r): the list alone while
// the preview is showing, list and preview side by side otherwise. The
// override lasts for the session and is dropped whenever the configured
// orientation already shows what was asked for, so the responsive layout
// keeps following the terminal size. A terminal too short for the preview
// keeps the list alone. m.mode is recomputed immediately so the layout
// changes in the same step, not at the next WindowSizeMsg.
func (m *Model) toggleLayout() {
	want, force := modeWide, LayoutLandscape
	if m.mode == modeWide || m.mode == "" {
		want, force = modeListOnly, LayoutListOnly
	}
	m.layout.Orientation = m.baseOrientation
	if nextResponsiveMode(*m, m.mode) != want {
		m.layout.Orientation = force
	}
	m.mode = nextResponsiveMode(*m, m.mode)
}

// expandCurrent handles Right on the highlighted row: manually expands a
// RowCandidate Herdr workspace's tab/pane children (progressive disclosure
// at an empty query — see expandedWorkspaces' doc comment). A no-op for any
// other row kind.
func (m *Model) expandCurrent() tea.Cmd {
	row, ok := m.currentRow()
	if !ok || row.Kind != RowCandidate || !row.Expandable {
		return nil
	}
	wsID := row.Candidate.Meta["workspace_id"]
	if wsID == "" {
		return nil
	}
	m.expandedWorkspaces[wsID] = true
	return m.applyFilter()
}

// collapseCurrent handles Left on the highlighted row: manually collapses a
// RowCandidate Herdr workspace's expanded tab/pane children. A no-op for any
// other row kind (including a RowTab/RowPane row: only the owning workspace
// collapses, there is nothing to collapse on a leaf).
func (m *Model) collapseCurrent() tea.Cmd {
	row, ok := m.currentRow()
	if !ok || row.Kind != RowCandidate || !row.Expandable {
		return nil
	}
	wsID := row.Candidate.Meta["workspace_id"]
	if wsID == "" {
		return nil
	}
	delete(m.expandedWorkspaces, wsID)
	return m.applyFilter()
}

// closeSelectedRow resolves the displayed row before starting any side effect.
func (m Model) closeSelectedRow() (Model, tea.Cmd) {
	if m.closePending {
		return m, nil
	}
	row, ok := m.currentRow()
	if !ok {
		m.closeStatus = errorStatus("not an open Herdr item")
		return m, nil
	}
	target, ok := closeTargetFor(row)
	if !ok {
		m.closeStatus = errorStatus("not an open Herdr item")
		return m, nil
	}
	if m.layout.Closer == nil {
		m.closeStatus = errorStatus("Herdr close unavailable")
		return m, nil
	}
	if slices.Contains(m.layout.ConfirmClose, target.kind) {
		m.closeConfirm = &target
		m.closeStatus = footerStatus{}
		return m, nil
	}
	return m.startClose(target)
}

// closeTargetFor resolves what ctrl+x would close on row: an open Herdr pane
// (a tree pane row or an agent row), tab or workspace. Tree rows are
// synthesized under an open workspace with no Source of their own (see
// synthesizeWorkspaceChildren) and are recognized by the workspace id they
// carry. ok is false for every other row; closeSelectedRow refuses it and
// the footer leaves the close hint out.
func closeTargetFor(row Row) (closeTarget, bool) {
	c := row.Candidate
	herdrChild := c.Source == config.SourceHerdr || (c.Source == "" && c.Meta["workspace_id"] != "")
	target := closeTarget{label: c.Label}
	switch {
	case row.Kind == RowPane && (herdrChild || c.Source == config.SourceAgents),
		row.Kind == RowCandidate && c.Source == config.SourceAgents:
		target.kind, target.id = "pane", c.Meta["pane_id"]
	case row.Kind == RowTab && herdrChild:
		target.kind, target.id = "tab", c.Meta["tab_id"]
	case row.Kind == RowCandidate && c.Source == config.SourceHerdr:
		target.kind, target.id = "workspace", c.Meta["workspace_id"]
	}
	return target, target.id != ""
}

func (m Model) startClose(target closeTarget) (Model, tea.Cmd) {
	m.closePending = true
	m.closeStatus = infoStatus("closing " + target.kind + "...")
	closer, ctx := m.layout.Closer, m.renderCtx
	return m, func() tea.Msg {
		result := closer(ctx, target.kind, target.id)
		result.Kind, result.ID = target.kind, target.id
		return result
	}
}
