package tui

import (
	"context"
	"time"

	"github.com/tranceh2/shep/internal/cache"
	"github.com/tranceh2/shep/internal/source"
)

// treeExpanderTimeout bounds each ListTabs/ListPanes pair issued by
// TreeExpander.Fetch, mirroring the herdrPreviewTimeout pattern
// (internal/preview/renderer.go:394-404) so a slow or hung Herdr daemon
// never blocks the picker's keystroke loop.
const treeExpanderTimeout = 100 * time.Millisecond

// workspaceTree is the cached (Tabs, Panes) snapshot of one Herdr
// workspace, keyed by workspace id in TreeExpander's cache.
type workspaceTree struct {
	Tabs  []source.Tab
	Panes []source.Pane
}

// TreeExpander fetches and caches a workspace's tabs/panes so the picker
// can synthesize child tab/pane rows without a driver round-trip on every
// keystroke that still matches the same workspace.
type TreeExpander struct {
	driver  source.HerdrDriver
	cache   *cache.Cache[workspaceTree]
	timeout time.Duration
}

// NewTreeExpander builds a TreeExpander backed by driver, caching each
// workspace's (Tabs, Panes) for ttl. Each Fetch's ListTabs/ListPanes pair is
// bounded by treeExpanderTimeout regardless of ttl.
func NewTreeExpander(driver source.HerdrDriver, ttl time.Duration) *TreeExpander {
	return &TreeExpander{
		driver:  driver,
		cache:   cache.New[workspaceTree](ttl),
		timeout: treeExpanderTimeout,
	}
}

// Fetch returns the cached (Tabs, Panes) for workspaceID, or fetches them
// via ListTabs then ListPanes under a treeExpanderTimeout-bounded context.
// Any error from either call degrades to ok=false and stores nothing — the
// picker treats this filter pass as having zero children rather than
// crashing or surfacing an error to the user.
func (e *TreeExpander) Fetch(ctx context.Context, workspaceID string) (workspaceTree, bool) {
	if tree, ok := e.cache.Get(workspaceID); ok {
		return tree, true
	}

	qctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tabs, err := e.driver.ListTabs(qctx, workspaceID)
	if err != nil {
		return workspaceTree{}, false
	}
	panes, err := e.driver.ListPanes(qctx, workspaceID)
	if err != nil {
		return workspaceTree{}, false
	}

	tree := workspaceTree{Tabs: tabs, Panes: panes}
	e.cache.Put(workspaceID, tree)
	return tree, true
}

// panePreviewTimeout bounds a single ReadPane call issued for a highlighted
// RowPane's "existing visual capture" preview section — independent of
// treeExpanderTimeout (which only bounds the ListTabs/ListPanes pair) since
// reading a pane's captured buffer is a different, separately-timed
// round-trip and must never block the keystroke loop either.
const panePreviewTimeout = 150 * time.Millisecond

// ReadPane returns paneID's captured terminal buffer (capped at lines
// trailing lines; lines <= 0 means the daemon default), bounded by
// panePreviewTimeout. Exposed on TreeExpander (rather than requiring Model
// to hold a second HerdrDriver reference) so a highlighted RowPane's preview
// can show its real captured content — the "existing visual capture where
// available" contract — using the exact same driver TreeExpander already
// holds for ListTabs/ListPanes.
func (e *TreeExpander) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	qctx, cancel := context.WithTimeout(ctx, panePreviewTimeout)
	defer cancel()
	return e.driver.ReadPane(qctx, paneID, lines)
}

// synthesizeWorkspaceChildren builds the full two-level (tab, then its own
// panes) child tree for one Herdr workspace, ready for rows.go's buildRows.
// Each tab candidate's Path resolves via primaryTabCWD; each pane candidate's
// Path is its own CWD/ForegroundCWD and its Label is the bare pane id (no
// appended status text — the corrective round moved agent status out of the
// label entirely; see render.go's rowDisplayText/agentStatusIcon, which
// render it as an icon derived from Meta["agent_status"] instead).
//
// These synthesized candidates carry NO Source (Source stays ""): they are not
// provider candidates and never flow through the flat Registry/Dedup/Match
// pipeline. Their semantics live on the Row (Kind RowTab/RowPane + Action
// RowActionFocusTab), not on a fake Source string — the old
// config.SourceHerdrTab / config.SourceHerdrPane constants were removed.
//
// Every synthesized candidate's Meta carries every id an ancestor might need
// (workspace_id always; workspace_label for concise parent-context display in
// the picker; tab_id on both tabs and panes; pane_id and agent_status only on
// panes) so a pane row's Enter can route straight to
// driver.FocusTab(tab_id) without a second lookup — Herdr has no per-pane
// focus command (see internal/herdr.Driver.FocusTab).
func synthesizeWorkspaceChildren(workspaceID, parentLabel, parentPath string, tabs []source.Tab, panes []source.Pane) workspaceChildren {
	out := workspaceChildren{Tabs: make([]tabChildren, 0, len(tabs))}
	for _, tab := range tabs {
		tabCand := source.Candidate{
			Label:  tab.Label,
			Path:   primaryTabCWD(tab, panes, parentPath),
			Meta: map[string]string{
				"workspace_id":    workspaceID,
				"workspace_label": parentLabel,
				"tab_id":          tab.ID,
			},
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
			paneCands = append(paneCands, source.Candidate{
				Label:  p.ID,
				Path:   path,
				Meta: map[string]string{
					"workspace_id":    workspaceID,
					"workspace_label": parentLabel,
					"tab_id":          tab.ID,
					"pane_id":         p.ID,
					"agent_status":    p.AgentStatus,
				},
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
