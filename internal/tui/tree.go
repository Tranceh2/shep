package tui

import (
	"context"
	"strconv"

	"github.com/tranceh2/shep/internal/source"
)

// workspaceTree is the immutable (Tabs, Panes) snapshot of one Herdr
// workspace, keyed by workspace id in TreeExpander's generation map.
type workspaceTree struct {
	Tabs  []source.Tab
	Panes []source.Pane
}

// TreeExpander indexes one immutable snapshot generation for child-row
// synthesis. It never calls the Herdr driver or owns a TTL cache.
type TreeExpander struct {
	trees map[string]workspaceTree
}

// NewTreeExpanderFromSnapshot constructs a pure tree view over one complete
// snapshot generation. It intentionally filters orphan tabs and panes at the
// boundary so later row synthesis cannot mix records from unrelated state.
func NewTreeExpanderFromSnapshot(snapshot source.Snapshot) *TreeExpander {
	validWorkspaces := make(map[string]struct{}, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		if workspace.ID != "" {
			validWorkspaces[workspace.ID] = struct{}{}
		}
	}
	trees := make(map[string]workspaceTree, len(validWorkspaces))
	for workspaceID := range validWorkspaces {
		trees[workspaceID] = workspaceTree{}
	}
	validTabs := make(map[string]string, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		if tab.ID == "" {
			continue
		}
		if _, ok := validWorkspaces[tab.WorkspaceID]; !ok {
			continue
		}
		tree := trees[tab.WorkspaceID]
		tree.Tabs = append(tree.Tabs, tab)
		trees[tab.WorkspaceID] = tree
		validTabs[tab.ID] = tab.WorkspaceID
	}
	for _, pane := range snapshot.Panes {
		if pane.ID == "" {
			continue
		}
		if workspaceID, ok := validTabs[pane.TabID]; !ok || workspaceID != pane.WorkspaceID {
			continue
		}
		tree := trees[pane.WorkspaceID]
		tree.Panes = append(tree.Panes, pane)
		trees[pane.WorkspaceID] = tree
	}
	return &TreeExpander{trees: trees}
}

// Fetch returns workspaceID's precomputed tree from this generation.
func (e *TreeExpander) Fetch(_ context.Context, workspaceID string) (workspaceTree, bool) {
	if e == nil {
		return workspaceTree{}, false
	}
	tree, ok := e.trees[workspaceID]
	return tree, ok
}

// UpdatePaneAgentStatus updates a pane's AgentStatus in-place across all
// tracked workspaces in this generation without re-sorting or rebuilding rows.
// Returns true if the pane was found and updated, false otherwise.
func (e *TreeExpander) UpdatePaneAgentStatus(paneID, status string) bool {
	if e == nil || paneID == "" {
		return false
	}
	for wsID, tree := range e.trees {
		for i, p := range tree.Panes {
			if p.ID == paneID {
				tree.Panes[i].AgentStatus = status
				e.trees[wsID] = tree
				return true
			}
		}
	}
	return false
}

// WorkspaceAgentStatus returns the most urgent agent status among
// workspaceID's panes in this generation — blocked > working > done > idle,
// the same attention order the preview's agent status section uses — or ""
// when none of them reports one. Panes without a status (plain shells) and
// "unknown" ones are ignored, so a workspace row shows a definite state or
// nothing. Live updates applied by UpdatePaneAgentStatus are included.
func (e *TreeExpander) WorkspaceAgentStatus(workspaceID string) string {
	if e == nil || workspaceID == "" {
		return ""
	}
	best, bestRank := "", 0
	for _, p := range e.trees[workspaceID].Panes {
		if rank := agentStatusRank(p.AgentStatus); rank > bestRank {
			best, bestRank = p.AgentStatus, rank
		}
	}
	return best
}

// agentStatusRank orders agent states by how urgently they need the user;
// anything else ranks 0 and never wins.
func agentStatusRank(status string) int {
	switch status {
	case "blocked":
		return 4
	case "working":
		return 3
	case "done":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

// ResolveActivePaneID returns the focused pane in tabID from the cached
// workspace tree, falling back to that tab's first pane in Herdr list order.
// A missing tree or matching pane is unavailable rather than selecting a pane
// from another tab.
func (e *TreeExpander) ResolveActivePaneID(ctx context.Context, workspaceID, tabID string) (string, bool) {
	tree, ok := e.Fetch(ctx, workspaceID)
	if !ok {
		return "", false
	}
	return selectTabPaneID(tree.Panes, tabID)
}

// selectTabPaneID returns the selected tab's focused pane, or its first pane
// in the supplied Herdr list order. It never selects a pane from another tab.
func selectTabPaneID(panes []source.Pane, tabID string) (string, bool) {
	var fallback string
	for _, pane := range panes {
		if pane.TabID != tabID || pane.ID == "" {
			continue
		}
		if pane.Focused {
			return pane.ID, true
		}
		if fallback == "" {
			fallback = pane.ID
		}
	}
	if fallback == "" {
		return "", false
	}
	return fallback, true
}

// synthesizeWorkspaceChildren builds the full two-level (tab, then its own
// panes) child tree for one Herdr workspace, ready for rows.go's buildRows.
// Each tab candidate's Path resolves via primaryTabCWD; each pane candidate's
// Path is its own CWD/ForegroundCWD and its Label is Herdr's optional pane
// label. Pane IDs remain in metadata for stable identity and actions, never as
// a user-facing label. Agent status is rendered as an icon derived from
// Meta["agent_status"] instead of being appended to the label.
//
// These synthesized candidates carry NO Source (Source stays ""): they are not
// provider candidates and never flow through the flat Registry/Dedup/Match
// pipeline. Their semantics live on the Row (Kind RowTab/RowPane + Action
// RowActionFocusTab), not on a fake Source string — the old
// config.SourceHerdrTab / config.SourceHerdrPane constants were removed.
//
// Every synthesized candidate's Meta carries every id an ancestor might need
// (workspace_id always; workspace_label for parent identity; tab_id on both
// tabs and panes; tab_label, pane_id, and agent_status on panes) so a pane
// row's Enter can route straight to
// driver.FocusTab(tab_id) without a second lookup — Herdr has no per-pane
// focus command (see internal/herdr.Driver.FocusTab).
func synthesizeWorkspaceChildren(workspaceID, parentLabel, parentPath string, tabs []source.Tab, panes []source.Pane) workspaceChildren {
	out := workspaceChildren{Tabs: make([]tabChildren, 0, len(tabs))}
	for _, tab := range tabs {
		tabMeta := map[string]string{
			"workspace_id":    workspaceID,
			"workspace_label": parentLabel,
			"tab_id":          tab.ID,
		}
		if tab.Number != 0 {
			// tab_number renders the inline "<number> <label>" RowTab text.
			tabMeta["tab_number"] = strconv.Itoa(tab.Number)
		}
		if tab.Label != "" {
			tabMeta["tab_label"] = tab.Label
		}
		tabCand := source.Candidate{
			Label: tab.Label,
			Path:  primaryTabCWD(tab, panes, parentPath),
			Meta:  tabMeta,
		}
		var paneCands []source.Candidate
		for _, p := range panes {
			if p.TabID != tab.ID {
				continue
			}
			path := p.ForegroundCWD
			if path == "" {
				path = p.CWD
			}
			label := p.Label
			if label == "" && p.TerminalTitle != "" {
				label = p.TerminalTitle
			}
			meta := map[string]string{
				"workspace_id":    workspaceID,
				"workspace_label": parentLabel,
				"tab_id":          tab.ID,
				"tab_label":       tab.Label,
				"pane_id":         p.ID,
				"agent_status":    p.AgentStatus,
			}
			if p.Agent != "" {
				meta["agent"] = p.Agent
			}
			if p.TerminalTitle != "" {
				meta["terminal_title"] = p.TerminalTitle
			}
			paneCands = append(paneCands, source.Candidate{
				Label: label,
				Path:  path,
				Meta:  meta,
			})
		}
		out.Tabs = append(out.Tabs, tabChildren{Tab: tabCand, Panes: paneCands})
	}
	return out
}

// primaryTabCWD resolves a tab's display path from its panes: the first
// matching pane's ForegroundCWD when non-empty (a live foreground process),
// else that pane's own CWD. When no pane in the slice belongs to tab at
// all, it falls back to parentPath so a synthesized row never renders an
// empty path.
func primaryTabCWD(tab source.Tab, panes []source.Pane, parentPath string) string {
	for _, p := range panes {
		if p.TabID != tab.ID {
			continue
		}
		if p.ForegroundCWD != "" {
			return p.ForegroundCWD
		}
		return p.CWD
	}
	return parentPath
}
