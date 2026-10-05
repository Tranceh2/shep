package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// FilterScope preserves the historical all/agents initial-view API. Configured
// tabs use Model.ActiveTab for their exact identifier.
type FilterScope int

const (
	ScopeAll FilterScope = iota
	ScopeAgents
)

// TabKind selects how a top-level tab obtains its candidate rows.
type TabKind string

const (
	TabAll         TabKind = "all"
	TabAgents      TabKind = "agents"
	TabSource      TabKind = "source"
	TabIntegration TabKind = "integration"
	TabGroup       TabKind = "group"
)

// TabDefinition describes one configured tab. Group loaders are invoked only
// when that tab is activated; source tabs reuse the current candidate snapshot.
type TabDefinition struct {
	ID          string
	Kind        TabKind
	Label       string
	SourceOrder []string
	Load        func(context.Context, *source.Snapshot) ([]source.Candidate, error)
}

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
			if len(m.layout.Tabs) > 0 && !(len(m.layout.Tabs) == 2 && m.layout.Tabs[0].ID == "all" && m.layout.Tabs[1].ID == "agents") {
				return []string{"No active agents detected", "tab switch to " + m.adjacentTab(1).ID}
			}
			return []string{
				"No active agents detected",
				"tab switch to all workspaces",
			}
		},
	},
}

func scopeForTab(id string) FilterScope {
	if id == "agents" {
		return ScopeAgents
	}
	return ScopeAll
}

func (m Model) tabs() []TabDefinition {
	if len(m.layout.Tabs) == 0 {
		return []TabDefinition{{ID: "all", Kind: TabAll}, {ID: "agents", Kind: TabAgents}}
	}
	return m.layout.Tabs
}

// ActiveTab is the identifier of the currently displayed view.
func (m Model) ActiveTab() string {
	if m.activeTab != "" {
		return m.activeTab
	}
	if m.scope == ScopeAgents {
		return "agents"
	}
	return m.tabs()[0].ID
}

func (m Model) activeDefinition() TabDefinition {
	for _, tab := range m.tabs() {
		if tab.ID == m.ActiveTab() {
			return tab
		}
	}
	if m.ActiveTab() == "agents" {
		return TabDefinition{ID: "agents", Kind: TabAgents}
	}
	return m.tabs()[0]
}

func (m Model) adjacentTab(delta int) TabDefinition {
	tabs := m.tabs()
	for i, tab := range tabs {
		if tab.ID == m.ActiveTab() {
			return tabs[(i+delta+len(tabs))%len(tabs)]
		}
	}
	if delta < 0 {
		return tabs[len(tabs)-1]
	}
	return tabs[0]
}

func (m Model) groupNeedsSnapshot() bool {
	return m.groupUsesSnapshot(m.activeDefinition())
}

func (m Model) groupUsesSnapshot(tab TabDefinition) bool {
	for _, name := range tab.SourceOrder {
		if name == config.SourceHerdr || name == config.SourceAgents {
			return true
		}
	}
	return false
}

func (m Model) tabPresentation() scopeDefinition {
	tab := m.activeDefinition()
	switch tab.Kind {
	case TabAgents:
		return scopeDefinitionFor(ScopeAgents)
	case TabAll:
		def := scopeDefinitionFor(ScopeAll)
		if len(m.layout.Tabs) > 0 {
			def.EmptyState = func(m Model) []string {
				if m.loadingCandidates && len(m.allTabCandidates()) == 0 {
					return []string{"No workspaces yet", "Sources are still loading…"}
				}
				if len(m.allTabCandidates()) == 0 {
					return []string{"No candidates available"}
				}
				return []string{"No workspaces yet"}
			}
		}
		return def
	default:
		kind := "source"
		if tab.Kind == TabIntegration {
			kind = "integration"
		}
		if tab.Kind == TabGroup {
			kind = "group"
		}
		return scopeDefinition{Name: tab.ID, Placeholder: "filter " + kind + "…", FooterLabel: tab.ID,
			EmptyState: func(m Model) []string {
				if tab.Kind == TabGroup && m.groupNeedsSnapshot() && m.snapshotUnavailable != nil {
					return []string{"Group candidates unavailable", "Herdr snapshot unavailable: " + m.snapshotUnavailable.Error()}
				}
				if m.groupLoading[tab.ID] {
					return []string{"Loading " + kind + " candidates…"}
				}
				if err := m.groupErrors[tab.ID]; err != nil {
					return []string{"Group candidates unavailable", err.Error()}
				}
				if m.loadingCandidates && tab.Load == nil {
					return []string{"Sources are still loading…"}
				}
				return []string{"No " + kind + " candidates available"}
			},
		}
	}
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
		isPrior        bool
		paneRecentRank int
		mruRank        int
	}

	currentID := m.currentPaneID()
	priorID := m.priorAgentPaneID(candidates, currentID)

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
			isPrior:        paneID == priorID,
			paneRecentRank: paneRecentRank,
			mruRank:        mruRank,
		})
	}

	sort.SliceStable(matched, func(i, j int) bool {
		left, right := matched[i], matched[j]
		leftNew, rightNew := left.tier == 1, right.tier == 1
		if leftNew != rightNew {
			return leftNew
		}
		if leftNew {
			if m.query != "" && left.score != right.score {
				return left.score > right.score
			}
			if left.isCurrent != right.isCurrent {
				return !left.isCurrent
			}
		} else {
			if left.isPrior != right.isPrior {
				return left.isPrior
			}
			if left.isCurrent != right.isCurrent {
				return !left.isCurrent
			}
			// Working agents precede acknowledged attention and idle agents.
			if left.tier != right.tier {
				return left.tier < right.tier
			}
			if m.query != "" && left.score != right.score {
				return left.score > right.score
			}
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

// priorAgentPaneID promotes only the sole agent in the immediately preceding
// workspace. Herdr workspace MRU does not record which pane was focused.
func (m Model) priorAgentPaneID(candidates []source.Candidate, currentID string) string {
	byWorkspace := make(map[string][]string)
	currentWorkspace := ""
	for _, c := range candidates {
		paneID, workspaceID := c.Meta["pane_id"], c.Meta["workspace_id"]
		if paneID == currentID {
			currentWorkspace = workspaceID
		}
		if paneID != "" && workspaceID != "" {
			byWorkspace[workspaceID] = append(byWorkspace[workspaceID], paneID)
		}
	}
	if currentWorkspace == "" {
		for _, p := range m.allSnapshotPanes() {
			if p.ID == currentID {
				currentWorkspace = p.WorkspaceID
				break
			}
		}
	}
	if currentWorkspace == "" {
		return ""
	}
	mru := m.rankingSnapshot.WorkspaceMRU()
	// Require the current workspace at the head: otherwise the MRU cannot
	// establish which entry immediately preceded the current focus.
	if len(mru) < 2 || mru[0] != currentWorkspace {
		return ""
	}
	panes := byWorkspace[mru[1]]
	if len(panes) == 1 && panes[0] != currentID {
		return panes[0]
	}
	// A shell-only workspace stops the walk. Multiple agents are ambiguous:
	// a Shep selection could predate a later Herdr-only visit to this workspace.
	return ""
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
