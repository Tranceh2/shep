package tui

import (
	"context"
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/source"
)

const panePreviewTimeout = 150 * time.Millisecond

// syncPreviewAfterSelectionChange compares the highlighted row before and
// after a key mutated cursor/query/expand state. When the highlight
// changed, it clears the previous row's previewText (so a stale render
// arriving out of order can never flash the wrong row's text), bumps
// previewSeq (invalidating any in-flight render), and dispatches a fresh
// async request appropriate to the new row's kind: previewCmd for a
// RowCandidate (via the injected Renderer), panePreviewCmd for a RowPane,
// or tabPreviewCmd for a RowTab (both via TreeExpander.ReadPane).
func (m *Model) syncPreviewAfterSelectionChange() tea.Cmd {
	cmd := m.previewCmdForCurrentRow()
	return tea.Batch(cmd, m.maybeStartSpinner())
}

// previewCmdForCurrentRow bumps previewSeq (invalidating any in-flight
// render for the row that was highlighted before) and dispatches a fresh
// async request for whatever row is now highlighted. Used by
// syncPreviewAfterSelectionChange, which is always called through a
// pointer receiver from a mutating key handler, so the seq bump persists.
func (m *Model) previewCmdForCurrentRow() tea.Cmd {
	m.previewSeq++
	m.previewText = ""
	m.previewSections = nil
	m.previewErr = ""
	_, cmd := m.dispatchPreviewForRow(m.previewSeq)
	return cmd
}

// initialPreviewCmd dispatches the FIRST async preview request at whatever
// previewSeq the model already carries (0 for a freshly constructed model)
// — deliberately WITHOUT incrementing it. Init has a value receiver (part
// of the tea.Model interface), so any mutation it performed would be
// discarded the instant it returns; incrementing here would tag the
// dispatched Cmd with a seq the real (unmutated) model driven by the
// Bubble Tea runtime never actually reaches, and its eventual response
// would be wrongly discarded as stale. Using the untouched previewSeq
// keeps Init's response acceptance symmetric with the model's real state.
func (m Model) initialPreviewCmd() tea.Cmd {
	_, cmd := m.dispatchPreviewForRow(m.previewSeq)
	return cmd
}

// dispatchPreviewForRow is the shared core: given the currently highlighted
// row and a target seq, returns whether a meaningful async request was
// dispatched and the Cmd to run it. A RowCandidate uses the injected
// Renderer; a RowPane uses TreeExpander.ReadPane; a RowTab resolves and reads
// its selected pane through TreeExpander; a missing selection does nothing.
func (m *Model) dispatchPreviewForRow(seq int) (bool, tea.Cmd) {
	row, ok := m.currentRow()
	if !ok {
		m.previewLoading = false
		return false, nil
	}
	switch row.Kind {
	case RowCandidate:
		if m.renderer == nil {
			m.previewLoading = false
			return false, nil
		}
		m.previewLoading = true
		return true, m.previewCmd(seq, row.Candidate)
	case RowPane:
		if m.tree == nil {
			m.previewLoading = false
			return false, nil
		}
		m.previewLoading = true
		return true, m.panePreviewCmd(seq, row.Candidate)
	case RowTab:
		if m.tree == nil {
			m.previewLoading = false
			return false, nil
		}
		m.previewLoading = true
		return true, m.tabPreviewCmd(seq, row.Candidate)
	default:
		m.previewLoading = false
		return false, nil
	}
}

// previewCmd builds the async Bubble Tea Cmd that renders cand through the
// injected Renderer and reports back as previewResponseMsg tagged with seq.
func (m Model) previewCmd(seq int, cand source.Candidate) tea.Cmd {
	renderer := m.renderer
	renderCtx := m.renderCtx
	return func() tea.Msg {
		res, err := renderer.Render(renderCtx, cand)
		return previewResponseMsg{seq: seq, result: res, err: err}
	}
}

// panePreviewCmd builds the async Bubble Tea Cmd that captures a RowPane's
// terminal buffer via TreeExpander.ReadPane and reports back as
// panePreviewMsg tagged with seq — the "existing visual capture where
// available" contract for a highlighted pane row.
func (m Model) panePreviewCmd(seq int, cand source.Candidate) tea.Cmd {
	renderCtx := m.renderCtx
	paneID := cand.Meta["pane_id"]
	return func() tea.Msg {
		text, err := m.readPane(renderCtx, paneID, panePreviewMaxLines)
		return panePreviewMsg{seq: seq, text: text, err: err}
	}
}

var errNoResolvedPane = errors.New("herdr tab has no resolved pane")

// tabPreviewCmd resolves a RowTab's focused pane (or its deterministic
// same-tab fallback), then reads that pane's captured terminal buffer. It
// reports through panePreviewMsg to retain the existing stale-result and error
// handling shared with RowPane previews.
func (m Model) tabPreviewCmd(seq int, cand source.Candidate) tea.Cmd {
	tree := m.tree
	renderCtx := m.renderCtx
	workspaceID := cand.Meta["workspace_id"]
	tabID := cand.Meta["tab_id"]
	return func() tea.Msg {
		paneID, ok := tree.ResolveActivePaneID(renderCtx, workspaceID, tabID)
		if !ok {
			return panePreviewMsg{seq: seq, err: errNoResolvedPane}
		}
		text, err := m.readPane(renderCtx, paneID, panePreviewMaxLines)
		return panePreviewMsg{seq: seq, text: text, err: err}
	}
}

func (m Model) readPane(ctx context.Context, paneID string, lines int) (string, error) {
	if m.snapshotDriver != nil {
		qctx, cancel := context.WithTimeout(ctx, panePreviewTimeout)
		defer cancel()
		return m.snapshotDriver.ReadPane(qctx, paneID, lines)
	}
	return "", errors.New("pane reader unavailable")
}

// panePreviewMaxLines caps the trailing lines captured for a highlighted
// RowPane's "existing visual capture" preview section — generous enough to
// show real context without risking a very tall pane blowing out the
// preview pane's own capPreviewBodyLines truncation budget on every render.
const panePreviewMaxLines = 200

// maybeStartSpinner issues the spinner's first Tick only on the false->true
// edge of "something needs it" — the preview render is loading, producers
// are still streaming candidates (the prompt count's loading frame), or at
// least one currently VISIBLE row is a pane with agent_status=="working"
// (the corrective-round icon animation). spinnerRunning guards against ever
// having two live tick loops in flight regardless of which condition armed
// it — see spinnerRunning's doc comment on Model.
func (m *Model) maybeStartSpinner() tea.Cmd {
	if !m.spinnerNeeded() || m.spinnerRunning {
		return nil
	}
	m.spinnerRunning = true
	return m.spinner.Tick
}

// spinnerNeeded reports whether the shared spinner tick loop should be
// running right now: a preview render in flight, producers still loading
// (the prompt row shows the spinner frame before its count), or a visible
// working-status pane row's icon animating. Shared by maybeStartSpinner
// (arm) and handleSpinnerTick (de-arm) so both sides of the single-tick-loop
// invariant agree on the same condition.
func (m Model) spinnerNeeded() bool {
	return m.previewLoading || m.loadingCandidates || m.anyVisibleRowWorking()
}

// anyVisibleRowWorking reports whether at least one row (m.rows) draws a
// working status glyph — a pane or agent row's own, or an open workspace's
// aggregate status marker (see rowShowsWorking) — the condition that keeps the
// shared spinner tick loop armed for the animated glyph even when no preview
// render is in flight.
func (m Model) anyVisibleRowWorking() bool {
	for _, r := range m.rows {
		if m.rowShowsWorking(r) {
			return true
		}
	}
	return false
}

// refreshPreviewLoadingFlag sets previewLoading to match whether a
// meaningful async request is in flight for the currently highlighted row
// (a RowCandidate with a renderer, or a RowPane/RowTab with a tree). Used at
// construction so the first frame shows the loading indicator immediately
// when Init's async render is in flight.
func (m *Model) refreshPreviewLoadingFlag() {
	row, ok := m.currentRow()
	if !ok {
		m.previewLoading = false
		return
	}
	switch row.Kind {
	case RowCandidate:
		m.previewLoading = m.renderer != nil
	case RowPane, RowTab:
		m.previewLoading = m.tree != nil
	default:
		m.previewLoading = false
	}
}

var _ = spinner.TickMsg{}
