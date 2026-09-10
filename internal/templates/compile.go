package templates

// compile.go turns Shep's N-ary template trees into Herdr Protocol 22 binary
// layout trees. It is deliberately PURE: no context, no driver, no socket, no
// filesystem. Every geometry decision, validation and pane-id assignment
// happens here, in memory, before a single byte reaches the daemon — which is
// what makes the fail-closed guarantee structural rather than a matter of
// discipline (R8.1), and what makes the ratio math table-testable (R9.2).
//
// Two shape mismatches are reconciled here:
//
//  1. Arity. A Shep branch node holds N children with N weights; a Herdr
//     split node is strictly binary. The compiler folds N children into a
//     right-heavy binary spine (see compiler.children).
//
//  2. Command form. Herdr Protocol 22 executes LayoutNode.command as an argv
//     slice with NO shell, while Shep templates declare a shell command string and
//     rely on shell semantics (pipes, chaining, and an interactive shell
//     fallback). The compiler therefore emits an explicit login-shell argv using
//     the invoking user's shell, which preserves profile initialization and the
//     command's shell semantics without relying on Herdr to type into a
//     pre-existing shell.
//
//  3. Environment propagation. Because the Herdr daemon launches native layout

//     commands in its own daemon environment (e.g. launchd default PATH
//     /usr/bin:/bin:/usr/sbin:/sbin), the invoking Shep process explicitly propagates
//     its PATH into LayoutNode.Env for command-backed panes. This ensures commands
//     like nvim and opencode resolve correctly, while avoiding forwarding secrets
//     or the entire environment. Commandless panes retain nil Env.

import (
	"fmt"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
)

const (
	// maxCompileDepth bounds recursion into a template's node graph so a
	// deeply nested or malformed user layout cannot exhaust the stack.
	// Far above any practical hand-authored layout.
	maxCompileDepth = 32

	// defaultShell gives an argv command the shell semantics Shep templates
	// are written against.
	defaultShell = "/bin/sh"
)

// CompileTab compiles one tab's node graph into a single binary LayoutNode
// tree rooted at the tab's Root node.
//
// tabIdx scopes the deterministic pane ids this tab emits ("shep:t<tab>:n<n>",
// D4): the daemon applies the whole tree atomically, so no id can be learned
// after the fact. The ids remain useful for deterministic layout inspection,
// but command lifecycle is owned by the pane process itself. defaultCwd is the
// cwd every pane inherits.
//
// userShell selects the login shell used for command-backed panes. An empty
// value falls back to /bin/sh. pathEnv, when non-empty, is propagated to
// LayoutNode.Env["PATH"] on command-backed panes so server-launched shells
// resolve user binaries in minimal server environments (such as launchd).
// Commandless panes leave Env nil.
//
// A tab with no nodes compiles to a single plain-shell pane, matching the
// previous subprocess behaviour where an empty tab was simply left alone.
func CompileTab(tab config.TemplateTab, tabIdx int, defaultCwd, userShell, pathEnv string) (herdr.LayoutNode, error) {
	if len(tab.Nodes) == 0 {
		return newPane(paneID(tabIdx, 0), defaultCwd, "", nil, nil), nil
	}
	if tab.Root == "" {
		return herdr.LayoutNode{}, fmt.Errorf("root node is not set")
	}

	byID := make(map[string]config.TemplateNode, len(tab.Nodes))
	for _, n := range tab.Nodes {
		byID[n.ID] = n
	}

	c := &compiler{byID: byID, tabIdx: tabIdx, cwd: defaultCwd, userShell: userShell, pathEnv: pathEnv, onPath: make(map[string]bool)}
	return c.node(tab.Root, 0)
}

// CompileTemplate compiles a whole template into one LayoutApplyParams per
// tab, in declaration order. Exactly one entry carries Focus: the tab named by
// tpl.Focus, or the first tab when no focus is declared (R3.2).
//
// pathEnv, when non-empty, is propagated into LayoutNode.Env["PATH"] for every
// command-backed pane.
//
// Every tab carries its declared label. Deciding that tab 0 should instead
// reuse the workspace's existing root tab requires a live root tab id, which
// only the dispatcher knows (D5); the compiler stays pure and leaves TabID
// unset for the dispatcher to fill in.
//
// A template with no Tabs is the common Command-only case: it compiles to a
// single focused pane running that command (or a plain shell when empty). It
// carries no label because there is no declared tab name to apply.
//
// Any per-tab failure aborts the whole compile and returns nil params, so a
// caller can never dispatch a partially valid layout (R8.1).
func CompileTemplate(tpl config.TemplateConfig, defaultCwd, userShell, pathEnv string) ([]herdr.LayoutApplyParams, error) {
	if len(tpl.Tabs) == 0 {
		id := paneID(0, 0)
		cmd := commandArgv(userShell, tpl.Command, tpl.CloseOnExit)
		var env map[string]string
		if len(cmd) > 0 && pathEnv != "" {
			env = map[string]string{"PATH": pathEnv}
		}
		root := newPane(id, defaultCwd, "", cmd, env)
		return []herdr.LayoutApplyParams{{Focus: true, Root: root}}, nil
	}

	focusTabIdx := resolveFocusTab(tpl)
	params := make([]herdr.LayoutApplyParams, 0, len(tpl.Tabs))
	for i, tab := range tpl.Tabs {
		root, err := CompileTab(tab, i, defaultCwd, userShell, pathEnv)
		if err != nil {
			return nil, fmt.Errorf("compile tab %q: %w", tab.Name, err)
		}
		params = append(params, herdr.LayoutApplyParams{
			TabLabel: tab.Name,
			Focus:    i == focusTabIdx,
			Root:     root,
		})
	}
	return params, nil
}

// resolveFocusTab maps tpl.Focus.Tab to a tab index, defaulting to 0. A name
// that matches no tab also degrades to 0: the config loader already validates
// focus targets, so an unmatched name here is defensive, not a user error to
// re-report at compile time.
func resolveFocusTab(tpl config.TemplateConfig) int {
	if tpl.Focus == nil || tpl.Focus.Tab == "" {
		return 0
	}
	for i, tab := range tpl.Tabs {
		if tab.Name == tpl.Focus.Tab {
			return i
		}
	}
	return 0
}

// compiler carries the per-tab state of one compilation: the node lookup, the
// tab index and cwd every pane inherits, the pathEnv propagated to
// command-backed panes, the running leaf counter that makes pane ids
// deterministic, and the ancestor set used as a cycle guard.
type compiler struct {
	byID      map[string]config.TemplateNode
	tabIdx    int
	cwd       string
	userShell string
	pathEnv   string
	leafN     int
	onPath    map[string]bool
}

// node compiles nodeID's subtree. depth is the current recursion depth, used
// by the depth guard.
//
// onPath holds the ids of the ancestors currently being compiled, so a child
// pointing back at an ancestor is reported as a cycle instead of recursing
// until the depth guard or the stack gives out.
func (c *compiler) node(nodeID string, depth int) (herdr.LayoutNode, error) {
	if depth > maxCompileDepth {
		return herdr.LayoutNode{}, fmt.Errorf("layout nesting exceeds max depth %d at node %q", maxCompileDepth, nodeID)
	}
	if c.onPath[nodeID] {
		return herdr.LayoutNode{}, fmt.Errorf("cycle detected at node %q", nodeID)
	}
	node, ok := c.byID[nodeID]
	if !ok {
		return herdr.LayoutNode{}, fmt.Errorf("node %q not found", nodeID)
	}

	if !node.IsBranch() {
		return c.leaf(node), nil
	}

	direction, err := splitDirection(node.Split)
	if err != nil {
		return herdr.LayoutNode{}, fmt.Errorf("node %q: %w", nodeID, err)
	}
	if len(node.Children) == 0 {
		return herdr.LayoutNode{}, fmt.Errorf("split node %q has no children", nodeID)
	}
	sizes, err := normalizeSizes(node.Sizes, len(node.Children))
	if err != nil {
		return herdr.LayoutNode{}, fmt.Errorf("node %q: %w", nodeID, err)
	}

	c.onPath[nodeID] = true
	defer delete(c.onPath, nodeID)

	return c.children(node.Children, sizes, direction, depth)
}

// children folds N siblings into a right-heavy binary spine.
//
// At step i the split keeps child i in `first` and the compiled remainder
// [i+1..n] in `second`, so the ratio is child i's share of what is still
// undivided:
//
//	ratio_i = sizes[i] / sum(sizes[i:])
//
// The last child needs no wrapper and is emitted directly, which is also why
// a single-child branch compiles to that child with no split at all.
//
// Children are compiled left to right so the leaf counter — and therefore the
// generated pane ids — follow declaration order.
func (c *compiler) children(childIDs []string, sizes []int, direction string, depth int) (herdr.LayoutNode, error) {
	first, err := c.node(childIDs[0], depth+1)
	if err != nil {
		return herdr.LayoutNode{}, err
	}
	if len(childIDs) == 1 {
		return first, nil
	}

	remaining := 0
	for _, s := range sizes {
		remaining += s
	}
	second, err := c.children(childIDs[1:], sizes[1:], direction, depth+1)
	if err != nil {
		return herdr.LayoutNode{}, err
	}

	return herdr.LayoutNode{
		Type:      herdr.NodeTypeSplit,
		Direction: direction,
		Ratio:     float64(sizes[0]) / float64(remaining),
		First:     &first,
		Second:    &second,
	}, nil
}

// leaf maps a template leaf onto a pane node, consuming the next pane id.
func (c *compiler) leaf(node config.TemplateNode) herdr.LayoutNode {
	id := paneID(c.tabIdx, c.leafN)
	c.leafN++

	label := ""
	if node.Label != nil {
		label = *node.Label
	}
	cmd := commandArgv(c.userShell, node.Command, node.CloseOnExit)
	var env map[string]string
	if len(cmd) > 0 && c.pathEnv != "" {
		env = map[string]string{"PATH": c.pathEnv}
	}
	return newPane(id, c.cwd, label, cmd, env)
}

// newPane builds a pane LayoutNode from an already-assigned pane id.
func newPane(id, cwd, label string, command []string, env map[string]string) herdr.LayoutNode {
	return herdr.LayoutNode{
		Type:    herdr.NodeTypePane,
		PaneID:  id,
		Cwd:     cwd,
		Label:   label,
		Command: command,
		Env:     env,
	}
}

// paneID renders the deterministic pane id for a leaf. Herdr may assign a
// different runtime id when materializing the layout, so this id is only a
// client-side deterministic label and must not be used for daemon commands.
func paneID(tabIdx, nodeIdx int) string {
	return fmt.Sprintf("shep:t%d:n%d", tabIdx, nodeIdx)
}

// commandArgv converts a Shep shell command into a Protocol 22 argv.
//
// An empty command yields nil: the daemon then starts the pane's default
// interactive shell. A command with close_on_exit starts directly as the pane
// process, so its exit naturally ends the pane and no synthetic pane close
// command is appended. Without close_on_exit, exec an interactive shell after
// the command so the pane remains usable at a prompt.
func commandArgv(userShell, command string, closeOnExit bool) []string {
	if command == "" {
		return nil
	}
	if userShell == "" {
		userShell = defaultShell
	}
	if closeOnExit {
		return []string{userShell, "-l", "-c", command}
	}
	return []string{userShell, "-l", "-c", command + "; exec " + userShell}
}

// splitDirection maps a Shep split axis onto a Herdr split direction: "rows"
// stacks children top/bottom ("down"), "cols" places them side by side
// ("right"). An empty split defaults to columns, matching the previous
// subprocess behaviour. Anything else is rejected rather than silently
// defaulted, so a typo cannot quietly produce the wrong geometry.
func splitDirection(split string) (string, error) {
	switch split {
	case config.SplitRows:
		return herdr.DirectionDown, nil
	case config.SplitCols, "":
		return herdr.DirectionRight, nil
	default:
		return "", fmt.Errorf("invalid split %q", split)
	}
}

// normalizeSizes returns the per-child weights to use for ratio math.
//
// Sizes are advisory: a length mismatch or an all-zero set degrades to equal
// weights rather than failing, because a layout that is merely under-specified
// should still open. A negative size is different — it has no sane geometric
// reading and would produce a negative or out-of-range ratio, so it is
// rejected before any division happens.
func normalizeSizes(sizes []int, childCount int) ([]int, error) {
	for _, s := range sizes {
		if s < 0 {
			return nil, fmt.Errorf("negative size %d", s)
		}
	}
	total := 0
	if len(sizes) == childCount {
		for _, s := range sizes {
			total += s
		}
	}
	if len(sizes) != childCount || total == 0 {
		equal := make([]int, childCount)
		for i := range equal {
			equal[i] = 1
		}
		return equal, nil
	}
	return sizes, nil
}
