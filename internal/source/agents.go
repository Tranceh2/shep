package source

import (
	"context"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
)

type agentsProvider struct {
	driver   HerdrDriver
	probes   config.Probes
	cfg      *config.Config
	snapshot *Snapshot
	// scoped distinguishes a nested group with an invalid empty root from
	// the intentionally unfiltered top-level registry.
	scoped   bool
	root     string
	disabled bool
}

func (p *agentsProvider) Name() string { return config.SourceAgents }

func (p *agentsProvider) enabled(_ *config.Config, probes config.Probes) bool {
	return probes.Herdr && !p.disabled
}

func (p *agentsProvider) List(ctx context.Context) ([]Candidate, error) {
	if p.disabled || (p.scoped && p.root == "") {
		return nil, nil
	}
	if p.snapshot != nil {
		return p.candidates(*p.snapshot), nil
	}
	if p.driver == nil {
		return nil, nil
	}
	snapshot, err := p.driver.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return p.candidates(snapshot), nil
}

func (p *agentsProvider) candidates(snapshot Snapshot) []Candidate {
	if p.scoped {
		return AgentCandidatesInRoot(snapshot, p.root)
	}
	return AgentCandidates(snapshot)
}

// AgentCandidates derives candidate rows for all detected agent panes in a
// snapshot.
func AgentCandidates(snapshot Snapshot) []Candidate {
	return agentCandidates(snapshot, "")
}

// AgentCandidatesInRoot derives agent candidates restricted to root for a
// group workspace's nested picker. A pane is kept when its derived candidate
// path (ForegroundCWD first, then CWD, then the workspace's own CWD) or its
// workspace CWD lies at or beneath root: the two can disagree — an agent's
// foreground process may wander outside its own workspace, and a workspace
// elsewhere may host a pane rooted in the group — and both are membership
// signals. Panes tied to neither are cross-root and never leak into the
// scoped picker. The top-level registry uses AgentCandidates, which applies
// no global filter.
func AgentCandidatesInRoot(snapshot Snapshot, root string) []Candidate {
	if root == "" {
		return nil
	}
	return agentCandidates(snapshot, root)
}

func agentCandidates(snapshot Snapshot, root string) []Candidate {
	if len(snapshot.Panes) == 0 {
		return nil
	}
	if root != "" {
		canonical, err := pathutil.Normalize(root)
		if err != nil {
			return nil
		}
		root = canonical
	}

	wsLabels := make(map[string]string)
	wsCWDs := make(map[string]string)
	for _, ws := range snapshot.Workspaces {
		if ws.ID != "" {
			wsLabels[ws.ID] = ws.Label
			wsCWDs[ws.ID] = ws.CWD
		}
	}

	tabLabels := make(map[string]string)
	for _, tab := range snapshot.Tabs {
		if tab.ID != "" {
			tabLabels[tab.ID] = tab.Label
		}
	}

	var out []Candidate
	for _, p := range snapshot.Panes {
		status := p.AgentStatus
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
		if root != "" && !agentPathWithinRoot(root, path) && !agentPathWithinRoot(root, wsCWDs[p.WorkspaceID]) {
			continue
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
		if p.Focused || (snapshot.FocusedPaneID != "" && snapshot.FocusedPaneID == p.ID) {
			meta["focused"] = "true"
		}

		out = append(out, Candidate{
			Path:   path,
			Label:  label,
			Source: config.SourceAgents,
			Meta:   meta,
		})
	}

	return out
}

func agentPathWithinRoot(root, path string) bool {
	if path == "" {
		return false
	}
	canonical, err := pathutil.Normalize(path)
	return err == nil && isWithinRoot(root, canonical)
}
