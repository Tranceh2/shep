package templates

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
)

// ratioTolerance is the float slack allowed when comparing computed split
// ratios (R9.2). Ratios are cumulative divisions, so exact equality is not a
// safe assertion for values such as 1/3.
const ratioTolerance = 1e-9

// testCwd is the cwd every table case compiles against. Propagation of a
// different cwd is pinned separately by TestCompileTab_CwdPropagates.
const testCwd = "/repo"
const testShell = "/bin/zsh"

// testPath is the PATH every table case compiles against.
const testPath = "/test/bin:/usr/bin"

// pane builds an expected leaf node at testCwd. command is the
// already-argv-shaped expectation; nil means "no command, plain shell".
func pane(paneID, label string, command []string) herdr.LayoutNode {
	var env map[string]string
	if len(command) > 0 && testPath != "" {
		env = map[string]string{"PATH": testPath}
	}
	return herdr.LayoutNode{
		Type:    herdr.NodeTypePane,
		PaneID:  paneID,
		Cwd:     testCwd,
		Label:   label,
		Command: command,
		Env:     env,
	}
}

// split builds an expected binary split node.
func split(direction string, ratio float64, first, second herdr.LayoutNode) herdr.LayoutNode {
	return herdr.LayoutNode{
		Type:      herdr.NodeTypeSplit,
		Direction: direction,
		Ratio:     ratio,
		First:     &first,
		Second:    &second,
	}
}

// shClose is the argv for a command that owns the pane process and closes
// naturally when the command exits.
func shClose(command string) []string { return []string{testShell, "-l", "-c", command} }

// shPrompt is the argv for a command that returns to an interactive shell.
func shPrompt(command string) []string {
	return []string{testShell, "-l", "-c", command + "; exec " + testShell}
}

func ptr(s string) *string { return &s }

// assertNode compares a compiled subtree against the expectation, reporting
// the structural path of the first mismatch.
func assertNode(t *testing.T, path string, got, want herdr.LayoutNode) {
	t.Helper()
	if got.Type != want.Type {
		t.Fatalf("%s: type = %q, want %q", path, got.Type, want.Type)
	}
	switch want.Type {
	case herdr.NodeTypePane:
		if got.PaneID != want.PaneID {
			t.Fatalf("%s: pane_id = %q, want %q", path, got.PaneID, want.PaneID)
		}
		if got.Cwd != want.Cwd {
			t.Fatalf("%s: cwd = %q, want %q", path, got.Cwd, want.Cwd)
		}
		if got.Label != want.Label {
			t.Fatalf("%s: label = %q, want %q", path, got.Label, want.Label)
		}
		assertArgv(t, path, got.Command, want.Command)
		assertEnv(t, path, got.Env, want.Env)
		if got.First != nil || got.Second != nil || got.Ratio != 0 || got.Direction != "" {
			t.Fatalf("%s: pane leaked split fields: %+v", path, got)
		}
	case herdr.NodeTypeSplit:
		if got.Direction != want.Direction {
			t.Fatalf("%s: direction = %q, want %q", path, got.Direction, want.Direction)
		}
		if math.Abs(got.Ratio-want.Ratio) > ratioTolerance {
			t.Fatalf("%s: ratio = %.17g, want %.17g (tolerance %g)", path, got.Ratio, want.Ratio, ratioTolerance)
		}
		if got.First == nil || got.Second == nil {
			t.Fatalf("%s: split missing branch: first=%v second=%v", path, got.First, got.Second)
		}
		if got.PaneID != "" || got.Command != nil || got.Env != nil {
			t.Fatalf("%s: split leaked pane fields: %+v", path, got)
		}
		assertNode(t, path+".first", *got.First, *want.First)
		assertNode(t, path+".second", *got.Second, *want.Second)
	default:
		t.Fatalf("%s: unexpected node type %q", path, want.Type)
	}
}

func assertArgv(t *testing.T, path string, got, want []string) {
	t.Helper()
	if (want == nil) != (got == nil) || !slices.Equal(got, want) {
		t.Fatalf("%s: command = %q, want %q", path, got, want)
	}
}

func assertEnv(t *testing.T, path string, got, want map[string]string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !maps.Equal(got, want) {
		t.Fatalf("%s: env = %+v, want %+v", path, got, want)
	}
}

// leaf is a shorthand for a template leaf node.
func leaf(id, command string) config.TemplateNode {
	return config.TemplateNode{ID: id, Command: command}
}

// branch is a shorthand for a template branch node.
func branch(id, splitDir string, sizes []int, children ...string) config.TemplateNode {
	return config.TemplateNode{ID: id, Split: splitDir, Sizes: sizes, Children: children}
}

func TestCompileTab(t *testing.T) {
	tests := []struct {
		name   string
		tab    config.TemplateTab
		tabIdx int
		want   herdr.LayoutNode
	}{
		{
			name: "single leaf pane maps command cwd and label",
			tab: config.TemplateTab{
				Name: "editor",
				Root: "main",
				Nodes: []config.TemplateNode{
					{ID: "main", Command: "nvim .", Label: ptr("edit")},
				},
			},
			want: pane("shep:t0:n0", "edit", shPrompt("nvim .")),
		},
		{
			name: "single leaf with close_on_exit runs as the pane process",
			tab: config.TemplateTab{
				Name: "run",
				Root: "main",
				Nodes: []config.TemplateNode{
					{ID: "main", Command: "go test ./...", CloseOnExit: true},
				},
			},
			want: pane("shep:t0:n0", "", shClose("go test ./...")),
		},
		{
			name: "empty command leaves a plain shell with no argv",
			tab: config.TemplateTab{
				Name:  "shell",
				Root:  "main",
				Nodes: []config.TemplateNode{leaf("main", "")},
			},
			want: pane("shep:t0:n0", "", nil),
		},
		{
			name: "close_on_exit without a command stays a plain shell",
			tab: config.TemplateTab{
				Name: "shell",
				Root: "main",
				Nodes: []config.TemplateNode{
					{ID: "main", CloseOnExit: true},
				},
			},
			want: pane("shep:t0:n0", "", nil),
		},
		{
			name:   "tab index scopes generated pane ids",
			tabIdx: 3,
			tab: config.TemplateTab{
				Name:  "third",
				Root:  "main",
				Nodes: []config.TemplateNode{leaf("main", "top")},
			},
			want: pane("shep:t3:n0", "", shPrompt("top")),
		},
		{
			name: "tab without nodes compiles to a single plain shell pane",
			tab:  config.TemplateTab{Name: "blank"},
			want: pane("shep:t0:n0", "", nil),
		},
		{
			name: "two rows split down at one half",
			tab: config.TemplateTab{
				Name: "stack",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitRows, nil, "a", "b"),
					leaf("a", "top"),
					leaf("b", "bottom"),
				},
			},
			want: split(herdr.DirectionDown, 0.5,
				pane("shep:t0:n0", "", shPrompt("top")),
				pane("shep:t0:n1", "", shPrompt("bottom")),
			),
		},
		{
			name: "three equal columns fold right-heavy with cumulative ratios",
			tab: config.TemplateTab{
				Name: "cols",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, nil, "a", "b", "c"),
					leaf("a", "one"),
					leaf("b", "two"),
					leaf("c", "three"),
				},
			},
			want: split(herdr.DirectionRight, 1.0/3.0,
				pane("shep:t0:n0", "", shPrompt("one")),
				split(herdr.DirectionRight, 0.5,
					pane("shep:t0:n1", "", shPrompt("two")),
					pane("shep:t0:n2", "", shPrompt("three")),
				),
			),
		},
		{
			name: "three unequal columns divide each size by the remaining sum",
			tab: config.TemplateTab{
				Name: "weighted",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, []int{20, 50, 30}, "a", "b", "c"),
					leaf("a", "one"),
					leaf("b", "two"),
					leaf("c", "three"),
				},
			},
			want: split(herdr.DirectionRight, 0.2,
				pane("shep:t0:n0", "", shPrompt("one")),
				split(herdr.DirectionRight, 0.625,
					pane("shep:t0:n1", "", shPrompt("two")),
					pane("shep:t0:n2", "", shPrompt("three")),
				),
			),
		},
		{
			name: "sizes length mismatch degrades to equal weights",
			tab: config.TemplateTab{
				Name: "mismatch",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, []int{80}, "a", "b"),
					leaf("a", "one"),
					leaf("b", "two"),
				},
			},
			want: split(herdr.DirectionRight, 0.5,
				pane("shep:t0:n0", "", shPrompt("one")),
				pane("shep:t0:n1", "", shPrompt("two")),
			),
		},
		{
			name: "all zero sizes degrade to equal weights",
			tab: config.TemplateTab{
				Name: "zero",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitRows, []int{0, 0}, "a", "b"),
					leaf("a", "one"),
					leaf("b", "two"),
				},
			},
			want: split(herdr.DirectionDown, 0.5,
				pane("shep:t0:n0", "", shPrompt("one")),
				pane("shep:t0:n1", "", shPrompt("two")),
			),
		},
		{
			name: "single child branch compiles the child without a split wrapper",
			tab: config.TemplateTab{
				Name: "lonely",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, nil, "a"),
					leaf("a", "only"),
				},
			},
			want: pane("shep:t0:n0", "", shPrompt("only")),
		},
		{
			name: "nested split of splits mirrors the declared geometry",
			tab: config.TemplateTab{
				Name: "nested",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, []int{30, 70}, "side", "body"),
					leaf("side", "sidebar"),
					branch("body", config.SplitRows, []int{60, 40}, "top", "bottom"),
					leaf("top", "editor"),
					leaf("bottom", "logs"),
				},
			},
			want: split(herdr.DirectionRight, 0.3,
				pane("shep:t0:n0", "", shPrompt("sidebar")),
				split(herdr.DirectionDown, 0.6,
					pane("shep:t0:n1", "", shPrompt("editor")),
					pane("shep:t0:n2", "", shPrompt("logs")),
				),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompileTab(tt.tab, tt.tabIdx, testCwd, testShell, testPath)
			if err != nil {
				t.Fatalf("CompileTab() error = %v, want nil", err)
			}
			assertNode(t, "root", got, tt.want)
		})
	}
}

// TestCompileTab_CwdPropagates proves defaultCwd reaches every leaf of a
// nested tree, not just the root pane. The table cases all compile against
// testCwd, so this is the case that would catch a hardcoded or dropped cwd.
func TestCompileTab_CwdPropagates(t *testing.T) {
	got, err := CompileTab(config.TemplateTab{
		Name: "nested", Root: "root",
		Nodes: []config.TemplateNode{
			branch("root", config.SplitCols, nil, "a", "body"),
			leaf("a", "one"),
			branch("body", config.SplitRows, nil, "b", "c"),
			leaf("b", "two"), leaf("c", "three"),
		},
	}, 0, "/elsewhere/deep", testShell, testPath)
	if err != nil {
		t.Fatalf("CompileTab() error = %v, want nil", err)
	}

	var leaves []string
	var walk func(herdr.LayoutNode)
	walk = func(n herdr.LayoutNode) {
		if n.Type == herdr.NodeTypePane {
			leaves = append(leaves, n.Cwd)
			return
		}
		walk(*n.First)
		walk(*n.Second)
	}
	walk(got)

	if len(leaves) != 3 {
		t.Fatalf("leaf count = %d, want 3", len(leaves))
	}
	for i, cwd := range leaves {
		if cwd != "/elsewhere/deep" {
			t.Fatalf("leaf %d cwd = %q, want /elsewhere/deep", i, cwd)
		}
	}
}

// TestCompileTab_TenSiblingsRatioSeries pins the full cumulative ratio series
// for a wide N-ary split: every ratio must be 1/(n-i) within tolerance, and
// the spine must stay right-heavy down to a final leaf.
func TestCompileTab_TenSiblingsRatioSeries(t *testing.T) {
	const n = 10
	nodes := []config.TemplateNode{branch("root", config.SplitCols, nil)}
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		nodes[0].Children = append(nodes[0].Children, id)
		nodes = append(nodes, leaf(id, "cmd-"+id))
	}

	got, err := CompileTab(config.TemplateTab{Name: "wide", Root: "root", Nodes: nodes}, 0, testCwd, testShell, testPath)
	if err != nil {
		t.Fatalf("CompileTab() error = %v, want nil", err)
	}

	cur := got
	for i := 0; i < n-1; i++ {
		if cur.Type != herdr.NodeTypeSplit {
			t.Fatalf("depth %d: type = %q, want split", i, cur.Type)
		}
		want := 1.0 / float64(n-i)
		if math.Abs(cur.Ratio-want) > ratioTolerance {
			t.Fatalf("depth %d: ratio = %.17g, want %.17g", i, cur.Ratio, want)
		}
		if cur.First.Type != herdr.NodeTypePane {
			t.Fatalf("depth %d: first = %q, want pane", i, cur.First.Type)
		}
		cur = *cur.Second
	}
	if cur.Type != herdr.NodeTypePane {
		t.Fatalf("spine tail: type = %q, want pane", cur.Type)
	}
	if cur.PaneID != "shep:t0:n9" {
		t.Fatalf("spine tail: pane_id = %q, want shep:t0:n9", cur.PaneID)
	}
}

// TestCompileTab_RatioToleranceIsNotAWildcard proves the 1e-9 tolerance used
// by the suite still discriminates a wrong ratio: a naive n-th-share ratio
// (1/n at every level) differs from the correct cumulative ratio by far more
// than the tolerance.
func TestCompileTab_RatioToleranceIsNotAWildcard(t *testing.T) {
	got, err := CompileTab(config.TemplateTab{
		Name: "cols",
		Root: "root",
		Nodes: []config.TemplateNode{
			branch("root", config.SplitCols, nil, "a", "b", "c"),
			leaf("a", "one"), leaf("b", "two"), leaf("c", "three"),
		},
	}, 0, testCwd, testShell, testPath)
	if err != nil {
		t.Fatalf("CompileTab() error = %v, want nil", err)
	}

	if diff := math.Abs(got.Ratio - 1.0/3.0); diff > ratioTolerance {
		t.Fatalf("root ratio = %.17g, want 1/3 (diff %g)", got.Ratio, diff)
	}
	inner := got.Second
	if diff := math.Abs(inner.Ratio - 1.0/3.0); diff <= ratioTolerance {
		t.Fatalf("inner ratio = %.17g must NOT equal 1/3; tolerance is too loose", inner.Ratio)
	}
	if diff := math.Abs(inner.Ratio - 0.5); diff > ratioTolerance {
		t.Fatalf("inner ratio = %.17g, want 0.5 (diff %g)", inner.Ratio, diff)
	}
}

func TestCompileTab_Errors(t *testing.T) {
	// deepNodes builds a chain of single-child splits n levels deep, ending in
	// a leaf, so the depth guard is the only thing that can stop compilation.
	deepNodes := func(n int) []config.TemplateNode {
		id := func(i int) string { return fmt.Sprintf("b%d", i) }
		nodes := make([]config.TemplateNode, 0, n+1)
		for i := 0; i < n; i++ {
			nodes = append(nodes, branch(id(i), config.SplitCols, nil, id(i+1)))
		}
		return append(nodes, leaf(id(n), "deep"))
	}

	tests := []struct {
		name    string
		tab     config.TemplateTab
		wantSub string
	}{
		{
			name:    "root node id is not declared",
			tab:     config.TemplateTab{Name: "t", Root: "ghost", Nodes: []config.TemplateNode{leaf("main", "")}},
			wantSub: `node "ghost" not found`,
		},
		{
			name: "tab declares nodes but no root",
			tab: config.TemplateTab{
				Name:  "t",
				Nodes: []config.TemplateNode{leaf("main", "")},
			},
			wantSub: "root node is not set",
		},
		{
			name: "child id is not declared",
			tab: config.TemplateTab{
				Name: "t", Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, nil, "a", "ghost"),
					leaf("a", ""),
				},
			},
			wantSub: `node "ghost" not found`,
		},
		{
			name: "negative size is rejected before any ratio math",
			tab: config.TemplateTab{
				Name: "t", Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, []int{50, -10}, "a", "b"),
					leaf("a", ""), leaf("b", ""),
				},
			},
			wantSub: "negative size",
		},
		{
			name: "split without children is rejected",
			tab: config.TemplateTab{
				Name: "t", Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, nil),
				},
			},
			wantSub: "has no children",
		},
		{
			name: "unknown split direction is rejected",
			tab: config.TemplateTab{
				Name: "t", Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", "diagonal", nil, "a", "b"),
					leaf("a", ""), leaf("b", ""),
				},
			},
			wantSub: `invalid split "diagonal"`,
		},
		{
			name: "cyclic children are rejected instead of recursing forever",
			tab: config.TemplateTab{
				Name: "t", Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitCols, nil, "a", "b"),
					leaf("a", ""),
					branch("b", config.SplitRows, nil, "root", "c"),
					leaf("c", ""),
				},
			},
			wantSub: "cycle",
		},
		{
			name:    "nesting deeper than the depth guard is rejected",
			tab:     config.TemplateTab{Name: "t", Root: "b0", Nodes: deepNodes(maxCompileDepth + 2)},
			wantSub: "depth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompileTab(tt.tab, 0, testCwd, testShell, testPath)
			if err == nil {
				t.Fatalf("CompileTab() error = nil, want error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("CompileTab() error = %q, want it to contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestCompileTemplate(t *testing.T) {
	twoTabs := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "shell", Root: "main", Nodes: []config.TemplateNode{leaf("main", "")}},
			{
				Name: "editor",
				Root: "root",
				Nodes: []config.TemplateNode{
					branch("root", config.SplitRows, []int{70, 30}, "top", "bottom"),
					leaf("top", "nvim ."),
					leaf("bottom", "git status"),
				},
			},
		},
	}

	t.Run("multi-tab template compiles one apply per tab with focus on the target tab", func(t *testing.T) {
		tpl := twoTabs
		tpl.Focus = &config.TemplateFocus{Tab: "editor"}

		got, err := CompileTemplate(tpl, testCwd, testShell, testPath)
		if err != nil {
			t.Fatalf("CompileTemplate() error = %v, want nil", err)
		}
		if len(got) != 2 {
			t.Fatalf("len(params) = %d, want 2", len(got))
		}
		if got[0].TabLabel != "shell" || got[1].TabLabel != "editor" {
			t.Fatalf("tab labels = %q/%q, want shell/editor", got[0].TabLabel, got[1].TabLabel)
		}
		if got[0].Focus {
			t.Fatalf("tab 0 focus = true, want false")
		}
		if !got[1].Focus {
			t.Fatalf("tab 1 focus = false, want true")
		}
		assertNode(t, "tab0", got[0].Root, pane("shep:t0:n0", "", nil))
		assertNode(t, "tab1", got[1].Root, split(herdr.DirectionDown, 0.7,
			pane("shep:t1:n0", "", shPrompt("nvim .")),
			pane("shep:t1:n1", "", shPrompt("git status")),
		))
	})

	t.Run("template without explicit focus focuses the first tab", func(t *testing.T) {
		got, err := CompileTemplate(twoTabs, testCwd, testShell, testPath)
		if err != nil {
			t.Fatalf("CompileTemplate() error = %v, want nil", err)
		}
		if !got[0].Focus {
			t.Fatalf("tab 0 focus = false, want true")
		}
		if got[1].Focus {
			t.Fatalf("tab 1 focus = true, want false")
		}
	})

	t.Run("command-only template compiles to a single focused pane tab", func(t *testing.T) {
		got, err := CompileTemplate(config.TemplateConfig{Command: "lazygit", CloseOnExit: true}, testCwd, testShell, testPath)
		if err != nil {
			t.Fatalf("CompileTemplate() error = %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(params) = %d, want 1", len(got))
		}
		if got[0].TabLabel != "" {
			t.Fatalf("tab label = %q, want empty (root tab is reused)", got[0].TabLabel)
		}
		if !got[0].Focus {
			t.Fatalf("focus = false, want true")
		}
		assertNode(t, "root", got[0].Root,
			pane("shep:t0:n0", "", shClose("lazygit")))
	})

	t.Run("empty template compiles to a single plain shell pane", func(t *testing.T) {
		got, err := CompileTemplate(config.TemplateConfig{}, testCwd, testShell, testPath)
		if err != nil {
			t.Fatalf("CompileTemplate() error = %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(params) = %d, want 1", len(got))
		}
		assertNode(t, "root", got[0].Root, pane("shep:t0:n0", "", nil))
	})

	t.Run("a bad tab fails the whole compile with the tab name in the error", func(t *testing.T) {
		tpl := config.TemplateConfig{
			Tabs: []config.TemplateTab{
				{Name: "ok", Root: "main", Nodes: []config.TemplateNode{leaf("main", "")}},
				{Name: "broken", Root: "ghost", Nodes: []config.TemplateNode{leaf("main", "")}},
			},
		}
		got, err := CompileTemplate(tpl, testCwd, testShell, testPath)
		if err == nil {
			t.Fatalf("CompileTemplate() error = nil, want a compile failure")
		}
		if got != nil {
			t.Fatalf("params = %v, want nil on failure (fail closed before dispatch)", got)
		}
		if !strings.Contains(err.Error(), `tab "broken"`) || !strings.Contains(err.Error(), `node "ghost" not found`) {
			t.Fatalf("error = %q, want it to name both the tab and the missing node", err.Error())
		}
	})
}

func TestCompileTab_EnvPropagation(t *testing.T) {
	customPath := "/opt/homebrew/bin:/run/current-system/sw/bin:/usr/bin:/bin"

	t.Run("command-backed nvim and opencode nodes carry exact PATH", func(t *testing.T) {
		tab := config.TemplateTab{
			Name: "tools",
			Root: "split",
			Nodes: []config.TemplateNode{
				branch("split", config.SplitCols, nil, "editor", "ai"),
				leaf("editor", "nvim ."),
				leaf("ai", "opencode"),
			},
		}
		got, err := CompileTab(tab, 0, testCwd, testShell, customPath)
		if err != nil {
			t.Fatalf("CompileTab error = %v", err)
		}

		if got.First == nil || got.Second == nil {
			t.Fatalf("expected split with two children, got %+v", got)
		}
		if got.First.Env == nil || got.First.Env["PATH"] != customPath {
			t.Errorf("first pane env = %+v, want PATH = %q", got.First.Env, customPath)
		}
		if got.Second.Env == nil || got.Second.Env["PATH"] != customPath {
			t.Errorf("second pane env = %+v, want PATH = %q", got.Second.Env, customPath)
		}
		// Prove no leaked environment keys
		if len(got.First.Env) != 1 || len(got.Second.Env) != 1 {
			t.Errorf("env has extra keys: first=%+v second=%+v", got.First.Env, got.Second.Env)
		}
	})

	t.Run("commandless pane carries nil env", func(t *testing.T) {
		tab := config.TemplateTab{
			Name:  "shell",
			Root:  "term",
			Nodes: []config.TemplateNode{leaf("term", "")},
		}
		got, err := CompileTab(tab, 0, testCwd, testShell, customPath)
		if err != nil {
			t.Fatalf("CompileTab error = %v", err)
		}
		if got.Env != nil {
			t.Errorf("commandless pane env = %+v, want nil", got.Env)
		}
	})

	t.Run("empty PATH leaves env nil", func(t *testing.T) {
		tab := config.TemplateTab{
			Name:  "editor",
			Root:  "ed",
			Nodes: []config.TemplateNode{leaf("ed", "nvim .")},
		}
		got, err := CompileTab(tab, 0, testCwd, testShell, "")
		if err != nil {
			t.Fatalf("CompileTab error = %v", err)
		}
		if got.Env != nil {
			t.Errorf("empty PATH pane env = %+v, want nil", got.Env)
		}
	})
}

func TestCompileTemplate_EnvPropagation_MultiTab(t *testing.T) {
	customPath := "/etc/profiles/per-user/tranceh2/bin:/usr/bin:/bin"
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{
				Name: "code",
				Root: "main",
				Nodes: []config.TemplateNode{
					branch("main", config.SplitCols, nil, "nvim", "term"),
					leaf("nvim", "nvim ."),
					leaf("term", ""),
				},
			},
			{
				Name: "ai",
				Root: "ai-node",
				Nodes: []config.TemplateNode{
					leaf("ai-node", "opencode"),
				},
			},
		},
	}

	got, err := CompileTemplate(tpl, testCwd, testShell, customPath)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tabs, want 2", len(got))
	}

	// Tab 0: nvim has PATH env, term has nil env
	tab0 := got[0].Root
	if tab0.First.Env["PATH"] != customPath {
		t.Errorf("tab 0 nvim pane PATH = %q, want %q", tab0.First.Env["PATH"], customPath)
	}
	if tab0.Second.Env != nil {
		t.Errorf("tab 0 term pane env = %+v, want nil", tab0.Second.Env)
	}

	// Tab 1: opencode has PATH env
	tab1 := got[1].Root
	if tab1.Env["PATH"] != customPath {
		t.Errorf("tab 1 opencode pane PATH = %q, want %q", tab1.Env["PATH"], customPath)
	}
	if len(tab1.Env) != 1 {
		t.Errorf("tab 1 env leaked keys: %+v", tab1.Env)
	}
}

func TestNativeLayoutCommand_HermeticExecutionWithPropagatedPath(t *testing.T) {
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	fakeToolPath := filepath.Join(binDir, "fake-tool-for-test")
	fakeScript := "#!/bin/sh\necho \"fake-tool-executed-ok\"\n"
	if err := os.WriteFile(fakeToolPath, []byte(fakeScript), 0755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	fakeShellPath := filepath.Join(binDir, "fake-login-shell")
	fakeShellScript := "#!/bin/sh\nif [ \"$1\" = \"-l\" ]; then shift; fi\nif [ \"$1\" = \"-c\" ]; then shift; fi\nexec /bin/sh -c \"$1\"\n"
	if err := os.WriteFile(fakeShellPath, []byte(fakeShellScript), 0755); err != nil {
		t.Fatalf("write fake shell: %v", err)
	}

	propagatedPath := binDir + ":/usr/bin:/bin"
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{
				Name: "main",
				Root: "run",
				Nodes: []config.TemplateNode{
					{ID: "run", Command: "fake-tool-for-test", CloseOnExit: true},
				},
			},
		},
	}

	params, err := CompileTemplate(tpl, tmpDir, fakeShellPath, propagatedPath)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	if len(params) != 1 {
		t.Fatalf("got %d params, want 1", len(params))
	}

	node := params[0].Root
	if node.Env == nil || node.Env["PATH"] != propagatedPath {
		t.Fatalf("node Env = %+v, want PATH=%q", node.Env, propagatedPath)
	}

	// Step 1: Negative control — running under a minimal server-like environment without binDir in PATH fails with exit 127
	minimalServerEnv := []string{"PATH=/usr/bin:/bin"}
	var stderrNeg bytes.Buffer
	cmdNeg := exec.Command(node.Command[0], node.Command[1:]...)
	cmdNeg.Env = minimalServerEnv
	cmdNeg.Stderr = &stderrNeg
	if err := cmdNeg.Run(); err == nil {
		t.Fatalf("expected command to fail under minimal server PATH, but succeeded")
	}

	// Step 2: Positive hermetic execution — executing node.Command with node.Env succeeds
	var stdoutPos, stderrPos bytes.Buffer
	cmdPos := exec.Command(node.Command[0], node.Command[1:]...)
	// Simulate daemon honoring node.Env
	cmdPos.Env = []string{"PATH=" + node.Env["PATH"], "HOME=" + os.Getenv("HOME")}
	cmdPos.Stdout = &stdoutPos
	cmdPos.Stderr = &stderrPos
	if err := cmdPos.Run(); err != nil {
		t.Fatalf("command failed with propagated PATH: %v (stderr: %s)", err, stderrPos.String())
	}
	if !strings.Contains(stdoutPos.String(), "fake-tool-executed-ok") {
		t.Fatalf("output = %q, want 'fake-tool-executed-ok'", stdoutPos.String())
	}
}
