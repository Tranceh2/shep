package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
)

// applyFilter recomputes m.rows from the current query/expand state. A query
// change starts selection at the first visible row; other rebuilds retain the
// previously highlighted row when the user has explicitly navigated, or pin to
// the first visible row otherwise.
func (m *Model) applyFilter() tea.Cmd {
	queryChanged := m.query != m.lastAppliedQuery
	prevID := m.currentRowID()
	tab := m.activeDefinition()
	if tab.Kind == TabAgents {
		m.rows = m.buildAgentRows()
	} else {
		candidates := m.baseFlatCandidates()
		order := m.resolvedSourceOrder()
		if tab.Kind == TabAll && len(m.layout.Tabs) > 0 {
			candidates = m.allTabCandidates()
		}
		if tab.Kind == TabGroup {
			candidates = m.groupCandidates[tab.ID]
			if len(tab.SourceOrder) > 0 {
				order = tab.SourceOrder
			}
		} else if tab.Kind == TabSource || tab.Kind == TabCustomSource {
			// Streaming producers retain undeduplicated provider results; a
			// synchronous tab-only source instead uses its lazy result.
			if tab.Load != nil {
				candidates = m.groupCandidates[tab.ID]
			} else if rows, ok := m.candidatesBySource[tab.ID]; ok {
				candidates = rows
			}
			filtered := make([]source.Candidate, 0)
			for _, c := range candidates {
				if c.Source == tab.ID {
					filtered = append(filtered, c)
				}
			}
			candidates = filtered
		}
		if tab.Kind == TabSource || tab.Kind == TabCustomSource {
			order = []string{tab.ID}
		}

		m.rows = buildRows(rowBuildInput{
			candidates:         ranking.SortBySourceOrder(candidates, m.query, order, m.rankingSnapshot),
			query:              m.query,
			children:           m.fetchChildrenFor(candidates),
			expandedWorkspaces: m.expandedWorkspaces,
			sourceOrder:        order,
			rankingSnapshot:    m.rankingSnapshot,
		})
		// Agents in a group/source tab are flat provider rows, not tree panes.
		// Keep their focus action and status rendering consistent with Agents.
		for i := range m.rows {
			if m.rows[i].Kind == RowCandidate && m.rows[i].Candidate.Source == config.SourceAgents {
				m.rows[i].Action = RowActionFocusTab
			}
		}
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

// allTabCandidates deduplicates only enabled sources. An independently
// requested tab must not make its provider visible in all or suppress an
// enabled provider's candidate with the same path.
func (m *Model) allTabCandidates() []source.Candidate {
	if m.candidatesBySource == nil {
		return m.baseFlatCandidates()
	}
	var enabled []source.Candidate
	order := m.sourceOrder
	if len(order) == 0 {
		order = m.resolvedSourceOrder()
	}
	for _, name := range order {
		enabled = append(enabled, m.candidatesBySource[name]...)
	}
	return resolver.Dedup(enabled)
}

// fetchAllChildren keeps the legacy all-candidate helper for direct callers.
// fetchChildrenFor returns, keyed by workspace_id, the synthesized tab/pane
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
	return m.fetchChildrenFor(m.baseFlatCandidates())
}

func (m *Model) fetchChildrenFor(candidates []source.Candidate) map[string]workspaceChildren {
	if m.tree == nil {
		return nil
	}
	out := make(map[string]workspaceChildren)
	for _, cand := range candidates {
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
