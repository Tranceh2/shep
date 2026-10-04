package tui

import (
	"fmt"
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

type scopeDefinition struct {
	ID          FilterScope
	Name        string
	Label       func(m Model) string
	Placeholder string
	FooterLabel string
	EmptyState  func(m Model) []string
}

var scopeRegistry = []scopeDefinition{
	{
		ID:          ScopeAll,
		Name:        "all",
		Label:       func(m Model) string { return "all" },
		Placeholder: "type to filter…",
		FooterLabel: "all",
		EmptyState: func(m Model) []string {
			if m.loadingCandidates && len(m.baseFlatCandidates()) == 0 {
				return []string{
					"No workspaces yet",
					"Sources are still loading…",
				}
			}
			if len(m.baseFlatCandidates()) == 0 {
				return []string{"No candidates available"}
			}
			return []string{"No workspaces yet"}
		},
	},
	{
		ID:   ScopeAgents,
		Name: "agents",
		Label: func(m Model) string {
			counts := m.AgentCounts()
			return fmt.Sprintf("agents (%d)", counts.Total)
		},
		Placeholder: "filter agents…",
		FooterLabel: "agents",
		EmptyState: func(m Model) []string {
			return []string{
				"No active agents detected",
				"tab switch to all workspaces",
			}
		},
	},
}

func scopeDefinitionFor(scope FilterScope) scopeDefinition {
	for _, def := range scopeRegistry {
		if def.ID == scope {
			return def
		}
	}
	return scopeRegistry[0]
}

func (s FilterScope) Next() FilterScope {
	for i, def := range scopeRegistry {
		if def.ID == s {
			return scopeRegistry[(i+1)%len(scopeRegistry)].ID
		}
	}
	return scopeRegistry[0].ID
}

func (s FilterScope) Prev() FilterScope {
	for i, def := range scopeRegistry {
		if def.ID == s {
			return scopeRegistry[(i-1+len(scopeRegistry))%len(scopeRegistry)].ID
		}
	}
	return scopeRegistry[0].ID
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

func (m Model) snapshotForAgents() source.Snapshot {
	var snap source.Snapshot
	if m.startupSnapshot != nil {
		snap = *m.startupSnapshot
	} else if m.tree != nil {
		for wsID, tree := range m.tree.trees {
			snap.Workspaces = append(snap.Workspaces, source.Workspace{ID: wsID})
			snap.Tabs = append(snap.Tabs, tree.Tabs...)
			snap.Panes = append(snap.Panes, tree.Panes...)
		}
	}
	if m.currentPane != nil && m.currentPane.ID != "" {
		snap.FocusedPaneID = m.currentPane.ID
	}
	if len(snap.Panes) == 0 {
		return snap
	}
	effectivePanes := make([]source.Pane, len(snap.Panes))
	for i, p := range snap.Panes {
		effectivePanes[i] = p
		effectivePanes[i].AgentStatus = m.effectivePaneStatus(p)
	}
	snap.Panes = effectivePanes
	return snap
}

func (m Model) currentPaneID() string {
	if m.currentPane != nil && m.currentPane.ID != "" {
		return m.currentPane.ID
	}
	if m.startupSnapshot != nil && m.startupSnapshot.FocusedPaneID != "" {
		return m.startupSnapshot.FocusedPaneID
	}
	return ""
}

// collectAgentCandidates collects flat candidate representations of active Herdr agents.
func (m Model) collectAgentCandidates() []source.Candidate {
	snap := m.snapshotForAgents()
	return source.AgentCandidates(snap)
}

func (m *Model) buildAgentRows() []Row {
	candidates := m.collectAgentCandidates()
	if len(candidates) == 0 {
		return nil
	}

	type scoredAgent struct {
		cand           source.Candidate
		tier           int
		score          int
		indexes        []int
		isCurrent      bool
		paneRecentRank int
		mruRank        int
	}

	currentID := m.currentPaneID()

	var matched []scoredAgent
	for _, c := range candidates {
		paneID := c.Meta["pane_id"]
		status := c.Meta["agent_status"]
		isAcked := m.rankingSnapshot.IsPaneAcknowledged(paneID, status)
		tier := agentAttentionTier(status, isAcked)

		candForMRU := c
		candForMRU.Source = config.SourceHerdr
		mruRank := m.rankingSnapshot.WorkspaceMRURank(candForMRU)

		isCurrent := currentID != "" && paneID == currentID
		paneRecentRank := m.rankingSnapshot.PaneRecentRank(paneID)

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
			cand:           c,
			tier:           tier,
			score:          score,
			indexes:        indexes,
			isCurrent:      isCurrent,
			paneRecentRank: paneRecentRank,
			mruRank:        mruRank,
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
		if left.isCurrent != right.isCurrent {
			return !left.isCurrent
		}
		if left.paneRecentRank != right.paneRecentRank {
			return left.paneRecentRank < right.paneRecentRank
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
