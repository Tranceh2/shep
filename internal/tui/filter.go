package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
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
	m.pinColumn = false
	if tab.Kind == TabAgents {
		m.rows = m.buildAgentRows()
	} else {
		candidates := m.baseFlatCandidates()
		order := m.resolvedSourceOrder()
		if tab.Kind == TabAll && len(m.layout.Tabs) > 0 {
			candidates = m.allTabCandidates()
		}
		onlySource := ""
		switch tab.Kind {
		case TabGroup:
			candidates = m.groupCandidates[tab.ID]
			if len(tab.SourceOrder) > 0 {
				order = tab.SourceOrder
			}
		case TabSource, TabCustomSource:
			// Streaming producers retain undeduplicated provider results; a
			// synchronous tab-only source instead uses its lazy result.
			if tab.Load != nil {
				candidates = m.groupCandidates[tab.ID]
			} else if rows, ok := m.candidatesBySource[tab.ID]; ok {
				candidates = rows
			}
			onlySource = tab.ID
			order = []string{tab.ID}
		}
		features := m.featuresFor(candidates)
		if onlySource != "" {
			candidates, features = keepSource(candidates, features, onlySource)
		}
		for _, f := range features {
			if f.Pinned() {
				m.pinColumn = true
				break
			}
		}

		in := m.rankedInput(candidates, features, order)
		in.children = m.fetchChildrenFor(candidates)
		m.rows = buildRows(in)
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
		m.listOffset = 0
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

// featureMemo is the ranking features of one candidate slice under one
// ranking snapshot generation. Candidate slices are never modified in place
// (every change installs a new slice), so the slice's first element and
// length identify it.
type featureMemo struct {
	first    *source.Candidate
	gen      int
	features []ranking.Features
}

// setRankingSnapshot installs a new ranking snapshot.
func (m *Model) setRankingSnapshot(snapshot ranking.Snapshot) {
	m.rankingSnapshot = snapshot
	m.rankingGen++
}

// featuresFor returns candidates' ranking features. They hash every
// candidate's identity, so they are computed once per candidate set and
// ranking snapshot instead of on every keystroke.
func (m *Model) featuresFor(candidates []source.Candidate) []ranking.Features {
	var first *source.Candidate
	if len(candidates) > 0 {
		first = &candidates[0]
	}
	memo := m.rankFeatures
	if memo.gen == m.rankingGen && memo.first == first && len(memo.features) == len(candidates) {
		return memo.features
	}
	features := m.rankingSnapshot.FeaturesOf(candidates)
	m.rankFeatures = featureMemo{first: first, gen: m.rankingGen, features: features}
	return features
}

// keepSource keeps the candidates of one source, with their features.
func keepSource(candidates []source.Candidate, features []ranking.Features, name string) ([]source.Candidate, []ranking.Features) {
	var keptCandidates []source.Candidate
	var keptFeatures []ranking.Features
	for i, c := range candidates {
		if c.Source == name {
			keptCandidates = append(keptCandidates, c)
			keptFeatures = append(keptFeatures, features[i])
		}
	}
	return keptCandidates, keptFeatures
}

// rankedInput ranks the candidates the active view shows for the current
// query. A non-empty query first drops every candidate it cannot show — its
// own label, path, aliases and metadata do not match and it has no Herdr
// tabs or panes that could — and ranks the rest: the ranking is a stable sort
// on per-candidate keys, so ranking the shown subset orders it exactly as
// ranking every candidate would, and the matches computed here are reused
// for the rows.
func (m *Model) rankedInput(candidates []source.Candidate, features []ranking.Features, order []string) rowBuildInput {
	in := rowBuildInput{
		query:              m.query,
		expandedWorkspaces: m.expandedWorkspaces,
		sourceOrder:        order,
		rankingSnapshot:    m.rankingSnapshot,
		matcher:            newQueryMatcher(m.query),
	}
	shown := make([]int, 0, len(candidates))
	var matches []rowMatch
	if m.query == "" {
		for i := range candidates {
			shown = append(shown, i)
		}
	} else {
		matches = make([]rowMatch, len(candidates))
		for i, c := range candidates {
			matches[i] = in.matcher.matchCandidate(c)
			if matches[i].matched || c.Source == config.SourceHerdr {
				shown = append(shown, i)
			}
		}
	}
	indexes := ranking.SortIndexesOf(candidates, features, shown, m.query, order, m.rankingSnapshot)
	in.candidates = make([]source.Candidate, len(indexes))
	in.features = make([]ranking.Features, len(indexes))
	if matches != nil {
		in.selfMatches = make([]rowMatch, len(indexes))
	}
	for k, i := range indexes {
		in.candidates[k] = candidates[i]
		in.features[k] = features[i]
		if matches != nil {
			in.selfMatches[k] = matches[i]
		}
	}
	return in
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
	resolve := m.layout.Resolve
	return func() tea.Msg {
		snapshotCtx, cancel := context.WithTimeout(ctx, source.SnapshotTimeout)
		defer cancel()
		snapshot, err := driver.Snapshot(snapshotCtx)
		if err != nil {
			return snapshotResponseMsg{seq: seq, err: err}
		}
		return resolvedGeneration(seq, snapshot, resolve)
	}
}

// resolvedGeneration derives a refreshed generation's herdr and agent
// candidates and resolves them (resolve may be nil), off the UI thread: the
// response carries them ready for Update to splice.
func resolvedGeneration(seq int, snapshot source.Snapshot, resolve func([]source.Candidate)) snapshotResponseMsg {
	msg := snapshotResponseMsg{
		seq:      seq,
		snapshot: snapshot,
		herdr:    source.HerdrCandidates(snapshot),
		agents:   source.AgentCandidates(snapshot),
	}
	if resolve != nil {
		resolve(msg.herdr)
		resolve(msg.agents)
	}
	msg.agentPresentations = AgentPresentations(msg.agents)
	msg.normalized = resolver.NormalizedPaths(msg.herdr)
	for path, norm := range resolver.NormalizedPaths(msg.agents) {
		msg.normalized[path] = norm
	}
	return msg
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

// allTabCandidates returns the all tab's set while tabs are configured: the
// enabled sources only, so an independently requested tab never makes its
// provider visible in all or suppresses an enabled provider's candidate with
// the same path. It reads the set rebuildAllTab stored, because
// deduplicating touches the filesystem and this runs on every frame and
// keystroke. A model built from a plain candidate list has no per-source
// results to deduplicate and shows its base candidates.
func (m Model) allTabCandidates() []source.Candidate {
	if m.candidatesBySource == nil {
		return m.baseFlatCandidates()
	}
	return m.allTab
}

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
	if !ok {
		return source.Candidate{}, false
	}
	return row.Candidate, true
}

// resolvedSourceOrder returns m.sourceOrder when configured, else
// defaultSourceOrder (rows.go) — the same fallback buildRows' own
// effectiveSourceOrder applies, kept as a Model-level accessor for callers
// that only have a Model, not a rowBuildInput.
func (m Model) resolvedSourceOrder() []string {
	if len(m.sourceOrder) > 0 {
		return m.sourceOrder
	}
	return defaultSourceOrder
}
