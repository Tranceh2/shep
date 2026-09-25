package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// applyFilter recomputes m.rows from the current query/expand state. A query
// change starts selection at the first visible row; other rebuilds retain the
// previously highlighted row when the user has explicitly navigated, or pin to
// the first visible row otherwise.
func (m *Model) applyFilter() tea.Cmd {
	queryChanged := m.query != m.lastAppliedQuery
	prevID := m.currentRowID()
	if m.scope == ScopeAgents {
		m.rows = m.buildAgentRows()
	} else {
		m.rows = buildRows(rowBuildInput{
			candidates:         ranking.SortBySourceOrder(m.baseFlatCandidates(), m.query, m.resolvedSourceOrder(), m.rankingSnapshot),
			query:              m.query,
			children:           m.fetchAllChildren(),
			expandedWorkspaces: m.expandedWorkspaces,
			sourceOrder:        m.sourceOrder,
			rankingSnapshot:    m.rankingSnapshot,
		})
	}
	m.lastAppliedQuery = m.query
	if queryChanged {
		m.cursor = 0
		m.cursorTouched = false
		return m.maybeRefreshSnapshot()
	}
	if !m.cursorTouched {
		m.cursor = 0
		return m.maybeRefreshSnapshot()
	}
	m.retainSelection(prevID)
	return m.maybeRefreshSnapshot()
}

func (m *Model) maybeRefreshSnapshot() tea.Cmd {
	if m.snapshotDriver == nil || m.snapshotRefreshing || m.lastSnapshotAt.IsZero() || m.now().Sub(m.lastSnapshotAt) < snapshotTTL {
		return nil
	}
	m.snapshotRefreshing = true
	m.snapshotSeq++
	m.snapshotRequestSeq = m.liveSeq
	seq := m.snapshotSeq
	driver := m.snapshotDriver
	ctx := m.renderCtx
	return func() tea.Msg {
		snapshotCtx, cancel := context.WithTimeout(ctx, source.SnapshotTimeout)
		defer cancel()
		snapshot, err := driver.Snapshot(snapshotCtx)
		return snapshotResponseMsg{seq: seq, snapshot: snapshot, err: err}
	}
}

// baseFlatCandidates returns the flat candidate set buildRows groups: the
// immutable baseCandidates for a tree-wired model, else the plain candidate
// list.
func (m *Model) baseFlatCandidates() []source.Candidate {
	if m.tree != nil {
		return m.baseCandidates
	}
	return m.candidates
}

// fetchAllChildren returns, keyed by workspace_id, the synthesized tab/pane
// tree for every SourceHerdr candidate that needs one THIS filter pass:
// only when the query is non-empty (a descendant might match) or the
// workspace was manually expanded (progressive disclosure at an empty
// query — see expandedWorkspaces' doc comment). A collapsed, non-matching
// workspace at an empty query is never fetched, so opening the picker never
// dumps (or even fetches) every workspace's tabs/panes by default. A nil
// tree, or a Fetch miss (cache miss + driver error/timeout), leaves that
// workspace absent from the map — buildRows degrades to zero children for
// it, never a crash.
func (m *Model) fetchAllChildren() map[string]workspaceChildren {
	if m.tree == nil {
		return nil
	}
	out := make(map[string]workspaceChildren)
	for _, cand := range m.baseFlatCandidates() {
		if cand.Source != config.SourceHerdr {
			continue
		}
		wsID := cand.Meta["workspace_id"]
		if m.query == "" && !m.expandedWorkspaces[wsID] {
			continue
		}
		tree, ok := m.tree.Fetch(m.renderCtx, wsID)
		if !ok {
			continue
		}
		out[wsID] = synthesizeWorkspaceChildren(wsID, cand.Label, cand.Path, tree.Tabs, tree.Panes)
	}
	return out
}

// currentRowID returns the ID of the currently highlighted row, or "" when
// there is nothing highlighted (empty rows).
func (m *Model) currentRowID() string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ""
	}
	return m.rows[m.cursor].ID
}

// retainSelection restores the cursor to the row identified by prevID when
// it is still present in the freshly rebuilt m.rows (selection retention
// across a rebuild); otherwise it clamps onto the nearest valid row —
// every row is selectable now that group headers are gone, so the only
// remaining concern is staying within [0, len(m.rows)) (e.g. the "no
// matches" state, where m.rows is empty).
func (m *Model) retainSelection(prevID string) {
	if prevID != "" {
		for i, r := range m.rows {
			if r.ID == prevID {
				m.cursor = i
				return
			}
		}
	}
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.rows[m.cursor].Selectable() {
		return
	}
	for i := m.cursor; i < len(m.rows); i++ {
		if m.rows[i].Selectable() {
			m.cursor = i
			return
		}
	}
	for i := m.cursor; i >= 0; i-- {
		if m.rows[i].Selectable() {
			m.cursor = i
			return
		}
	}
}

// currentRow returns the row under the cursor, or ok=false when rows is
// empty.
func (m Model) currentRow() (Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return Row{}, false
	}
	return m.rows[m.cursor], true
}

// currentCandidate returns the candidate under the cursor, or ok=false when
// nothing is highlighted (empty rows).
func (m Model) currentCandidate() (source.Candidate, bool) {
	row, ok := m.currentRow()
	if !ok || !row.Selectable() {
		return source.Candidate{}, false
	}
	return row.Candidate, true
}
