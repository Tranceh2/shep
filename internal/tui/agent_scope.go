package tui

import (
	"sort"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// FilterScope describes the active top-level filter scope in the picker.
type FilterScope int

const (
	ScopeAll FilterScope = iota
	ScopeAgents
)

func (s FilterScope) Next() FilterScope {
	if s == ScopeAll {
		return ScopeAgents
	}
	return ScopeAll
}

func (s FilterScope) Prev() FilterScope {
	return s.Next()
}

// AgentCounts summarizes detected agent panes by status.
type AgentCounts struct {
	Total   int
	Blocked int
	Working int
	Idle    int
	Done    int
}

// AgentCounts returns current agent counts from snapshot and live observations.
func (m Model) AgentCounts() AgentCounts {
	var counts AgentCounts
	panes := m.allSnapshotPanes()
	for _, p := range panes {
		status := m.effectivePaneStatus(p)
		if p.Agent == "" && (status == "" || status == "unknown") {
			continue
		}
		counts.Total++
		switch strings.ToLower(status) {
		case "blocked":
			counts.Blocked++
		case "working":
			counts.Working++
		case "idle":
			counts.Idle++
		case "done":
			counts.Done++
		}
	}
	return counts
}

func (m Model) allSnapshotPanes() []source.Pane {
	if m.startupSnapshot != nil && len(m.startupSnapshot.Panes) > 0 {
		return m.startupSnapshot.Panes
	}
	if m.tree != nil {
		var out []source.Pane
		for _, tree := range m.tree.trees {
			out = append(out, tree.Panes...)
		}
		return out
	}
	return nil
}

func (m Model) effectivePaneStatus(p source.Pane) string {
	if m.liveStatuses != nil {
		if obs, ok := m.liveStatuses[p.ID]; ok && obs.status != "" {
			return obs.status
		}
	}
	return p.AgentStatus
}

// collectAgentCandidates collects flat candidate representations of active Herdr agents.
func (m Model) collectAgentCandidates() []source.Candidate {
	panes := m.allSnapshotPanes()
	if len(panes) == 0 {
		return nil
	}

	wsLabels := make(map[string]string)
	wsCWDs := make(map[string]string)
	if m.startupSnapshot != nil {
		for _, ws := range m.startupSnapshot.Workspaces {
			if ws.ID != "" {
				wsLabels[ws.ID] = ws.Label
				wsCWDs[ws.ID] = ws.CWD
			}
		}
	}

	tabLabels := make(map[string]string)
	if m.startupSnapshot != nil {
		for _, tab := range m.startupSnapshot.Tabs {
			if tab.ID != "" {
				tabLabels[tab.ID] = tab.Label
			}
		}
	}

	var out []source.Candidate
	for _, p := range panes {
		status := m.effectivePaneStatus(p)
		isAgent := p.Agent != "" || (status != "" && status != "unknown")
		if !isAgent {
			continue
		}

		path := p.ForegroundCWD
		if path == "" {
			path = p.CWD
		}
		if path == "" {
			path = wsCWDs[p.WorkspaceID]
		}

		wsLabel := wsLabels[p.WorkspaceID]
		if wsLabel == "" {
			wsLabel = p.WorkspaceID
		}

		tabLabel := tabLabels[p.TabID]

		label := p.TerminalTitle
		if label == "" {
			label = p.Label
		}
		if label == "" {
			label = "agent " + p.ID
		}

		meta := map[string]string{
			"workspace_id":    p.WorkspaceID,
			"workspace_label": wsLabel,
			"tab_id":          p.TabID,
			"tab_label":       tabLabel,
			"pane_id":         p.ID,
			"agent":           p.Agent,
			"agent_status":    status,
			"terminal_title":  p.TerminalTitle,
			"kind":            "agent",
		}

		out = append(out, source.Candidate{
			Path:   path,
			Label:  label,
			Source: config.SourceHerdr,
			Meta:   meta,
		})
	}

	return out
}

func (m *Model) buildAgentRows() []Row {
	candidates := m.collectAgentCandidates()
	if len(candidates) == 0 {
		return nil
	}

	type scoredAgent struct {
		cand    source.Candidate
		tier    int
		score   int
		indexes []int
		mruRank int
	}

	var matched []scoredAgent
	for _, c := range candidates {
		paneID := c.Meta["pane_id"]
		status := c.Meta["agent_status"]
		isAcked := m.rankingSnapshot.IsPaneAcknowledged(paneID, status)
		tier := agentAttentionTier(status, isAcked)
		mruRank := m.rankingSnapshot.WorkspaceMRURank(c)
		score := 0
		var indexes []int

		if m.query != "" {
			haystack := candidateHaystack(c) + " " + c.Meta["workspace_label"] + " " + c.Meta["agent"] + " " + c.Meta["agent_status"]
			s, idxs, ok := extendedMatch(m.query, c, haystack)
			if !ok {
				continue
			}
			score = s
			indexes = idxs
		}

		matched = append(matched, scoredAgent{
			cand:    c,
			tier:    tier,
			score:   score,
			indexes: indexes,
			mruRank: mruRank,
		})
	}

	sort.SliceStable(matched, func(i, j int) bool {
		left, right := matched[i], matched[j]
		if left.tier != right.tier {
			return left.tier < right.tier
		}
		if m.query != "" && left.score != right.score {
			return left.score > right.score
		}
		if left.mruRank != right.mruRank {
			return left.mruRank < right.mruRank
		}
		return left.cand.Meta["pane_id"] < right.cand.Meta["pane_id"]
	})

	rows := make([]Row, len(matched))
	for i, item := range matched {
		match := MatchDirect
		if m.query == "" {
			match = MatchNone
		}
		rows[i] = Row{
			Kind:           RowPane,
			Action:         RowActionFocusTab,
			Candidate:      item.cand,
			Depth:          0,
			Match:          match,
			MatchedIndexes: item.indexes,
			ID:             "agent:" + item.cand.Meta["pane_id"],
			Expandable:     false,
		}
	}

	return rows
}

func agentAttentionTier(status string, isAcked bool) int {
	switch strings.ToLower(status) {
	case "blocked", "done":
		if isAcked {
			return 3 // acknowledged attention
		}
		return 1 // unacknowledged attention
	case "working":
		return 2
	case "idle":
		return 4
	default:
		return 5
	}
}
