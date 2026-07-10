package tui

import (
	"context"
	"time"

	"github.com/sahilm/fuzzy"
	"github.com/tranceh2/shep/internal/cache"
	"github.com/tranceh2/shep/internal/config"
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
// can synthesize child tab rows without a driver round-trip on every
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

// synthesizeChildren builds one child source.Candidate per tab, nested
// under workspaceID's parent workspace row. Each child's Path resolves via
// primaryTabCWD; Meta carries workspace_id/tab_id so PR3's launchChildTab
// can route Enter to driver.FocusTab without a second driver call.
func synthesizeChildren(workspaceID, parentPath string, tabs []source.Tab, panes []source.Pane) []source.Candidate {
	children := make([]source.Candidate, 0, len(tabs))
	for _, tab := range tabs {
		children = append(children, source.Candidate{
			Label:  tab.Label,
			Path:   primaryTabCWD(tab, panes, parentPath),
			Source: config.SourceHerdrTab,
			Meta: map[string]string{
				"workspace_id": workspaceID,
				"tab_id":       tab.ID,
			},
		})
	}
	return children
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

// matchingChildren filters children to those matching query, using the same
// fuzzy.FindFrom + candidateSource haystack contract applyFilter already
// uses for top-level candidates (model.go:515) — no second matching
// implementation. An empty query matches every child, mirroring
// applyFilter's own empty-query behavior. PR3 (Phase 5) wires this into the
// tree filter path.
func matchingChildren(query string, children []source.Candidate) []source.Candidate {
	if query == "" {
		return children
	}
	matches := fuzzy.FindFrom(query, candidateSource(children))
	out := make([]source.Candidate, 0, len(matches))
	for _, m := range matches {
		out = append(out, children[m.Index])
	}
	return out
}
