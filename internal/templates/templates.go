// Package templates applies a config.TemplateConfig (tabs, panes, splits,
// sizes, commands) to a freshly created Herdr workspace.
//
// A template is a full recipe for a freshly created workspace only —
// existing (focused) workspaces never have a template applied. The first
// declared tab always reuses/renames the workspace's existing root tab and
// root pane (created by `herdr workspace create`) instead of leaving it as
// an unused default tab alongside new ones; every subsequent tab is created
// fresh. Within a tab, a branch node (Split set) is realised as a sequence
// of cascading two-way `herdr pane split` calls so an arbitrary number of
// children can be laid out from Herdr's binary split primitive; a leaf node
// (no Split) runs its Command in the pane assigned to it (an empty command
// leaves a plain shell).
//
// Focus is set entirely at tab/pane creation time via the --focus/--no-focus
// flags on CreateTab/SplitPane, NOT via post-hoc commands. Herdr's
// `pane focus` only accepts --direction (not a positional pane id), so the
// only reliable way to focus an arbitrary pane is at split time. The
// top-level config.TemplateFocus {tab, node} is resolved into a concrete tab
// index + node id BEFORE anything is created, then threaded through every
// CreateTab/SplitPane call so the right tab/pane ends up focused.
package templates

import (
	"context"
	"fmt"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// Target identifies the freshly created workspace a template applies to:
// the workspace id, its initial (root) tab and pane ids, the cwd new
// tabs/panes should inherit, and the herdr binary name used for the
// close_on_exit shell-chaining wrap (defaults to "herdr" when empty).
type Target struct {
	WorkspaceID string
	RootTabID   string
	RootPaneID  string
	CWD         string
	Binary      string
}

// Apply realises tpl against target. A Command-only template (no Tabs, the
// common case for "default"/"k8s"-style templates) runs directly in the
// existing root pane; an empty Command leaves it a plain shell. A
// Tabs-based template creates/renames tabs and recursively splits panes per
// each tab's node graph. Focus (from tpl.Focus) is resolved to a concrete
// tab index + node id before anything is created, then threaded through
// every CreateTab/SplitPane via the focus bool so the requested tab/pane
// receives keyboard focus — no post-hoc focus commands are issued.
func Apply(ctx context.Context, driver source.HerdrDriver, target Target, tpl config.TemplateConfig) error {
	if len(tpl.Tabs) == 0 {
		if tpl.Command == "" {
			return nil
		}
		return driver.RunPane(ctx, target.RootPaneID, tpl.Command)
	}

	// Resolve the focus target BEFORE creating anything: which tab index
	// should end up focused, and (optionally) which node id within it. When
	// Focus is nil/empty, the default is tab index 0 — the first tab, which
	// reuses the workspace's already-focused root tab.
	focusTabIndex, focusNodeID := resolveFocus(tpl)
	binary := target.Binary
	if binary == "" {
		binary = "herdr"
	}

	for i, tab := range tpl.Tabs {
		var rootPaneID string
		if i == 0 {
			if err := driver.RenameTab(ctx, target.RootTabID, tab.Name); err != nil {
				return fmt.Errorf("rename root tab to %q: %w", tab.Name, err)
			}
			rootPaneID = target.RootPaneID
		} else {
			// Only the focus-target tab is created with --focus; all others
			// get --no-focus so they never steal focus from the target.
			focusTab := i == focusTabIndex
			_, createdPane, err := driver.CreateTab(ctx, target.WorkspaceID, target.CWD, tab.Name, focusTab)
			if err != nil {
				return fmt.Errorf("create tab %q: %w", tab.Name, err)
			}
			rootPaneID = createdPane.ID
		}
		// Pane-level focus only matters within the focus tab; every other
		// tab passes an empty focusNodeID so all its splits are --no-focus.
		nodeFocus := ""
		if i == focusTabIndex {
			nodeFocus = focusNodeID
		}
		if err := applyTab(ctx, driver, target.CWD, binary, tab, rootPaneID, nodeFocus); err != nil {
			return fmt.Errorf("apply tab %q: %w", tab.Name, err)
		}
	}
	return nil
}

// resolveFocus maps the top-level config.TemplateFocus to a concrete tab
// index and node id. When Focus is nil or Tab is empty, the default is tab
// index 0 (the first tab, which reuses the workspace's already-focused root
// tab). The tab name → index resolution has already been validated at config
// Load, so a missing match here is treated as index 0 defensively.
func resolveFocus(tpl config.TemplateConfig) (focusTabIndex int, focusNodeID string) {
	focusTabIndex = 0
	focusNodeID = ""
	if tpl.Focus == nil || tpl.Focus.Tab == "" {
		return
	}
	for i, tab := range tpl.Tabs {
		if tab.Name == tpl.Focus.Tab {
			focusTabIndex = i
			break
		}
	}
	focusNodeID = tpl.Focus.Node
	return
}

// applyTab realises one tab's node graph starting at rootPaneID (the tab's
// already-existing root pane — either the reused workspace root pane or a
// freshly created tab's root pane). A tab with no nodes is a plain empty
// shell; nothing more to do. binary is the herdr binary name used for the
// close_on_exit shell-chaining wrap. focusNodeID is the node id within this
// tab that should end up with pane focus ("" when no specific pane is
// targeted, so all splits are --no-focus).
func applyTab(ctx context.Context, driver source.HerdrDriver, cwd, binary string, tab config.TemplateTab, rootPaneID, focusNodeID string) error {
	if len(tab.Nodes) == 0 {
		return nil
	}
	byID := make(map[string]config.TemplateNode, len(tab.Nodes))
	for _, n := range tab.Nodes {
		byID[n.ID] = n
	}
	return applyNode(ctx, driver, cwd, binary, byID, tab.Root, rootPaneID, focusNodeID)
}

// applyNode assigns nodeID's subtree to paneID: a leaf runs its command in
// paneID (wrapped with "; <binary> pane close <paneID>" when CloseOnExit is
// set, so the pane closes itself once the command's shell returns control);
// a branch splits paneID via cascading two-way splits, assigning each child
// in turn, then recurses into each child's own subtree.
//
// focusNodeID controls the --focus/--no-focus flag on every SplitPane: a
// split's NEW pane gets --focus only when the focus node is reachable through
// the children that will occupy that new pane (children[i+1..]); otherwise
// --no-focus so the current (kept) pane retains focus. This is the sole
// mechanism for pane-level focus because Herdr's `pane focus` command does
// not accept a positional pane id (only --direction).
func applyNode(ctx context.Context, driver source.HerdrDriver, cwd, binary string, byID map[string]config.TemplateNode, nodeID, paneID, focusNodeID string) error {
	node, ok := byID[nodeID]
	if !ok {
		return fmt.Errorf("node %q not found", nodeID)
	}
	if !node.IsBranch() {
		if node.Command != "" {
			cmd := node.Command
			// CloseOnExit wraps the command so the pane closes itself once
			// the command finishes. This is achieved via shell chaining
			// ("; <binary> pane close <id>") because Herdr's `pane run`
			// types into an already-running interactive shell rather than
			// spawning the command as the pane's root process, and Herdr's
			// CLI/socket API has no native close-on-exit primitive.
			if node.CloseOnExit {
				cmd = cmd + "; " + binary + " pane close " + paneID
			}
			if err := driver.RunPane(ctx, paneID, cmd); err != nil {
				return err
			}
		}
		return nil
	}

	direction := "right"
	if node.Split == config.SplitRows {
		direction = "down"
	}
	sizes := node.Sizes
	if len(sizes) == 0 {
		sizes = make([]int, len(node.Children))
		for i := range sizes {
			sizes[i] = 1
		}
	}
	remaining := 0
	for _, s := range sizes {
		remaining += s
	}

	currentPaneID := paneID
	for i := 0; i < len(node.Children)-1; i++ {
		ratio := float64(sizes[i]) / float64(remaining)
		// The new pane from this split will eventually hold children[i+1..].
		// If the focus node is reachable through any of those children, the
		// new pane is on the focus path and gets --focus; otherwise --no-focus
		// so the kept pane (holding child[i]) retains focus.
		focusNewPane := focusNodeID != "" && focusInSubtree(byID, node.Children[i+1:], focusNodeID)
		newPane, err := driver.SplitPane(ctx, currentPaneID, direction, ratio, cwd, focusNewPane)
		if err != nil {
			return fmt.Errorf("split for child %q: %w", node.Children[i], err)
		}
		if err := applyNode(ctx, driver, cwd, binary, byID, node.Children[i], currentPaneID, focusNodeID); err != nil {
			return err
		}
		currentPaneID = newPane.ID
		remaining -= sizes[i]
	}
	return applyNode(ctx, driver, cwd, binary, byID, node.Children[len(node.Children)-1], currentPaneID, focusNodeID)
}

// focusInSubtree reports whether focusNodeID is reachable from any of the
// given child ids by walking the branch→children graph. Used to decide
// whether a split's NEW pane is on the path to the focus node.
func focusInSubtree(byID map[string]config.TemplateNode, childIDs []string, focusNodeID string) bool {
	for _, childID := range childIDs {
		if nodeContainsID(byID, childID, focusNodeID) {
			return true
		}
	}
	return false
}

// nodeContainsID reports whether targetID is id itself or reachable from id's
// subtree. Cycles are guarded against (already validated at config Load) by
// tracking visited nodes.
func nodeContainsID(byID map[string]config.TemplateNode, id, targetID string) bool {
	visited := make(map[string]bool)
	var visit func(string) bool
	visit = func(cur string) bool {
		if cur == targetID {
			return true
		}
		if visited[cur] {
			return false
		}
		visited[cur] = true
		node, ok := byID[cur]
		if !ok {
			return false
		}
		for _, child := range node.Children {
			if visit(child) {
				return true
			}
		}
		return false
	}
	return visit(id)
}
