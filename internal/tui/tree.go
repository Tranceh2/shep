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
