package tui

import (
	"fmt"
	"slices"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// isPrintable returns true for single-rune printable input that should
// extend the query. We avoid pulling in unicode classes for the v1 picker.
func isPrintable(s string) bool {
	if s == "" || len([]rune(s)) != 1 {
		return false
	}
	r := []rune(s)[0]
	return r >= 0x20 && r != 0x7f
}

// scrollViewport applies one scroll key to vp in place: up/down/ctrl+j/
// ctrl+k move one line, pgup/pgdown move one page, home/end jump to top/
// bottom. Shared by the preview pane and the help overlay so both scroll
// under the identical key contract. bubbles/viewport's own KeyMap does not
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

// handleKey applies one key press. FocusHelp is checked first and routes
// exclusively to handleHelpFocusedKey (help is modal: only "?"/Esc close it,
// only ctrl+c/ctrl+g cancel through it, everything else is swallowed).
// Otherwise, global bindings ("?"/esc/ctrl+c/ctrl+g/tab/shift+tab/ctrl+t/
// ctrl+p/ctrl+f) are checked next regardless of focus, and the remainder branches
// on m.focus: FocusPreview routes navigation to the preview viewport and any
// printable rune (including "q" — it is an ordinary query character, NOT a
// cancel key; only esc/ctrl+c/ctrl+g cancel) bounces focus back to the list
// before extending the query (so a user can start typing again straight out
// of the preview without an extra Tab); FocusList is the classic
// row-cursor/query-editing behavior, extended with Left/Right for
// expand/collapse, Enter for selection, and ctrl+l for the layout cycle —
// all three are List-only actions, no-ops from FocusPreview (see
// handlePreviewFocusedKey).
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.closeConfirm != nil {
		target := *m.closeConfirm
		m.closeConfirm = nil
		if msg.String() == "y" {
			return m.startClose(target)
		}
		m.closeStatus = "close cancelled"
		return m, nil
	}
	if !m.closePending {
		m.closeStatus = ""
	}
	if m.focus == FocusHelp {
		return m.handleHelpFocusedKey(msg)
	}
	switch msg.String() {
	case "?":
		m.prevFocus = m.focus
		m.focus = FocusHelp
		return m, nil
	case "esc":
		// Esc priority (FocusHelp already handled above, always wins):
		// a non-empty query is cleared and focus returns to the list
		// before ever cancelling; only an empty query cancels.
		if m.query != "" {
			m.query = ""
			m.focus = FocusList
			return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
		}
		m.cancelled = true
		return m, tea.Quit
	case "ctrl+c", "ctrl+g":
		m.cancelled = true
		return m, tea.Quit
	case "tab":
		return m.cycleScopeForward()
	case "shift+tab":
		return m.cycleScopeBackward()
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

	if m.focus == FocusPreview {
		return m.handlePreviewFocusedKey(msg)
	}
	return m.handleListFocusedKey(msg)
}

// handleHelpFocusedKey handles input while FocusHelp owns focus. Only "?"
// and Esc close help, restoring m.prevFocus (the state recorded when "?"
// opened it); ctrl+c/ctrl+g remain the unconditional cancel escape hatch;
// up/down/ctrl+j/ctrl+k/pgup/pgdown/home/end scroll helpViewport. Every
// other key — printable runes, backspace, ctrl+u, enter, ctrl+l, ctrl+t,
// ctrl+p, left/right — is swallowed: reading help must never mutate the
// query, move the list cursor, change layout, or select anything.
func (m Model) handleHelpFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "?", "esc":
		m.focus = m.prevFocus
		return m, nil
	case "ctrl+c", "ctrl+g":
		m.cancelled = true
		return m, tea.Quit
	case "up", "down", "ctrl+j", "ctrl+k", "pgup", "pgdown", "home", "end":
		scrollViewport(&m.helpViewport, msg.String())
		return m, nil
	}
	return m, nil
}

func (m Model) togglePin() (tea.Model, tea.Cmd) {
	row, ok := m.currentRow()
	if !ok {
		return m, nil
	}
	if row.Kind != RowCandidate {
		m.pinStatus = "child rows cannot be pinned"
		return m, nil
	}
	if m.layout.PinToggler == nil {
		m.pinStatus = "pinning unavailable"
		return m, nil
	}
	key := ranking.PinKey(row.Candidate)
	if key == "" {
		m.pinStatus = "row has no stable pin identity"
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

// handlePreviewFocusedKey routes scroll keys to the viewport while the
// preview pane owns focus. ctrl+u and backspace both return focus to the
// list, mutating the query (clear vs. delete-last) in the same step. Any
// other printable rune does the same before extending the query — the
// non-negotiable "printable rune returns to list and searches" contract.
// enter/ctrl+l/left/right are List-only actions (selection, layout cycle,
// expand/collapse) and are explicit no-ops here.
func (m Model) handlePreviewFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "ctrl+j", "ctrl+k", "pgup", "pgdown", "home", "end":
		scrollViewport(&m.viewport, msg.String())
		return m, nil
	case "ctrl+u":
		m.focus = FocusList
		m.query = ""
		return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
	case "backspace":
		m.focus = FocusList
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
		}
		return m, m.syncPreviewAfterSelectionChange()
	case "enter", "ctrl+l", "left", "right":
		return m, nil
	}
	if isPrintable(msg.String()) {
		m.focus = FocusList
		m.query += msg.String()
		return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
	}
	return m, nil
}

// handleListFocusedKey is the classic row-cursor/query-editing key set,
// extended with Left/Right expand-collapse, Enter to select, and ctrl+l to
// cycle the layout override — all List-only actions (see
// handlePreviewFocusedKey's explicit no-ops for the same three while the
// preview pane is focused). ctrl+j/ctrl+k always move the cursor regardless
// of focus's usual up/down mapping (kept as a stable alternate binding);
// plain "j"/"k" are intentionally NOT bound to movement here so they fall
// through to the query instead.
func (m Model) cycleScopeForward() (tea.Model, tea.Cmd) {
	next := m.adjacentTab(1)
	m.activeTab = next.ID
	m.scope = scopeForTab(next.ID)
	m.cursor = 0
	m.cursorTouched = false
	return m, tea.Batch(m.maybeLoadGroup(), m.applyFilter(), m.syncPreviewAfterSelectionChange())
}

func (m Model) cycleScopeBackward() (tea.Model, tea.Cmd) {
	next := m.adjacentTab(-1)
	m.activeTab = next.ID
	m.scope = scopeForTab(next.ID)
	m.cursor = 0
	m.cursorTouched = false
	return m, tea.Batch(m.maybeLoadGroup(), m.applyFilter(), m.syncPreviewAfterSelectionChange())
}

// handleListFocusedKey applies one key press while FocusList owns focus.
func (m Model) handleListFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.handleEnter()
	case "ctrl+l":
		m.cycleOrientationOverride()
		return m, nil
	case "pgup", "pgdown":
		if scrollViewport(&m.viewport, msg.String()) {
			return m, nil
		}
		return m, nil
	case "down", "ctrl+j":
		m.cursorTouched = true
		if len(m.rows) > 0 && m.cursor < len(m.rows)-1 {
			m.cursor++
		}
		return m, m.syncPreviewAfterSelectionChange()
	case "up", "ctrl+k":
		m.cursorTouched = true
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.syncPreviewAfterSelectionChange()
	case "right":
		m.cursorTouched = true
		return m, tea.Batch(m.expandCurrent(), m.syncPreviewAfterSelectionChange())
	case "left":
		m.cursorTouched = true
		return m, tea.Batch(m.collapseCurrent(), m.syncPreviewAfterSelectionChange())
	case "ctrl+u":
		m.query = ""
		return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
		}
		return m, m.syncPreviewAfterSelectionChange()
	default:
		if isPrintable(msg.String()) {
			m.query += msg.String()
			return m, tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
		}
		return m, m.syncPreviewAfterSelectionChange()
	}
}

// handleEnter selects the highlighted candidate/tab/pane row and quits. A
// row with nothing highlighted (empty rows) is a no-op. The dispatched
// selectedAction is sourced from the shared rowActionDescriptor — the same
// single source of truth the footer and help overlay read (SPEC-NAV-1.8
// parity) — so Enter behavior can never drift from the displayed copy.
func (m Model) handleEnter() (tea.Model, tea.Cmd) {
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
func (m Model) selectWithTarget(target string) (tea.Model, tea.Cmd) {
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

// cycleOrientationOverride toggles the session-only layout override between
// auto and landscape (ctrl+l): auto -> landscape -> auto. "auto" is the empty
// Orientation value, which re-engages the responsive width-based mode (see
// nextResponsiveMode). m.mode is recomputed immediately against the current
// width/height so the visible layout reacts to ctrl+l in the same step,
// instead of staying stale until the next WindowSizeMsg/render. The
// stacked/portrait third state was removed along with the stacked layout.
func (m *Model) cycleOrientationOverride() {
	if m.layout.Orientation == "" {
		m.layout.Orientation = LayoutLandscape
	} else {
		m.layout.Orientation = ""
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
func (m Model) closeSelectedRow() (tea.Model, tea.Cmd) {
	if m.closePending {
		return m, nil
	}
	row, ok := m.currentRow()
	if !ok {
		m.closeStatus = "not an open Herdr item"
		return m, nil
	}
	target := closeTarget{label: row.Candidate.Label}
	switch {
	case row.Kind == RowPane && (row.Candidate.Source == config.SourceHerdr || row.Candidate.Source == config.SourceAgents),
		row.Kind == RowCandidate && row.Candidate.Source == config.SourceAgents:
		target.kind, target.id = "pane", row.Candidate.Meta["pane_id"]
	case row.Kind == RowTab && row.Candidate.Source == config.SourceHerdr:
		target.kind, target.id = "tab", row.Candidate.Meta["tab_id"]
	case row.Kind == RowCandidate && row.Candidate.Source == config.SourceHerdr:
		target.kind, target.id = "workspace", row.Candidate.Meta["workspace_id"]
	}
	if target.id == "" {
		m.closeStatus = "not an open Herdr item"
		return m, nil
	}
	if m.layout.Closer == nil {
		m.closeStatus = "Herdr close unavailable"
		return m, nil
	}
	if slices.Contains(m.layout.ConfirmClose, target.kind) {
		m.closeConfirm = &target
		m.closeStatus = fmt.Sprintf("close %s %q? y/n", target.kind, target.label)
		return m, nil
	}
	return m.startClose(target)
}

func (m Model) startClose(target closeTarget) (tea.Model, tea.Cmd) {
	m.closePending = true
	m.closeStatus = "closing " + target.kind + "..."
	closer, ctx := m.layout.Closer, m.renderCtx
	return m, func() tea.Msg {
		result := closer(ctx, target.kind, target.id)
		result.Kind, result.ID = target.kind, target.id
		return result
	}
}
