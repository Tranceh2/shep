package templates

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
)

// fakeApplier records every ApplyLayout dispatch so a test can assert what
// reached the socket boundary — and, just as importantly, how many times.
// failAt makes the Nth call (0-indexed) fail, which is how the fail-closed
// tests prove that a later tab is never dispatched after an earlier one
// fails.
type fakeApplier struct {
	sockets []string
	calls   []herdr.LayoutApplyParams
	failAt  int
	err     error
}

func newFakeApplier() *fakeApplier { return &fakeApplier{failAt: -1} }

func (f *fakeApplier) ApplyLayout(_ context.Context, socketPath string, params herdr.LayoutApplyParams) (*herdr.LayoutApplyResult, error) {
	idx := len(f.calls)
	f.sockets = append(f.sockets, socketPath)
	f.calls = append(f.calls, params)
	if f.err != nil && idx == f.failAt {
		return nil, f.err
	}
	return &herdr.LayoutApplyResult{TabID: "applied"}, nil
}

// fakeRunner records the pane commands issued by RunCommand, the one
// remaining path that still types into an existing pane.
type fakeRunner struct {
	ran []string
	err error
}

func (f *fakeRunner) RunPane(_ context.Context, paneID, command string) error {
	f.ran = append(f.ran, "run:"+paneID+":"+command)
	return f.err
}

func stringPtr(value string) *string { return &value }

// countPanes counts the leaf terminals in a dispatched layout tree. It is how
// the atomicity tests prove a whole multi-pane tab travelled in ONE call
// instead of being dribbled out one split at a time.
func countPanes(n *herdr.LayoutNode) int {
	switch {
	case n == nil:
		return 0
	case n.Type == herdr.NodeTypePane:
		return 1
	default:
		return countPanes(n.First) + countPanes(n.Second)
	}
}

// fourPaneTemplate is a two-tab template whose first tab holds four panes.
// Under layout.apply that tab must cost exactly one round trip, not three
// `pane split` calls plus four `pane run` calls.
func fourPaneTemplate() config.TemplateConfig {
	return config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitCols, Children: []string{"a", "b", "c", "d"}},
				{ID: "a", Command: "nvim"},
				{ID: "b", Command: "lazygit"},
				{ID: "c", Label: stringPtr("logs"), Command: "tail -f app.log"},
				{ID: "d", Command: ""},
			}},
			{Name: "term", Root: "sh", Nodes: []config.TemplateNode{{ID: "sh", Command: ""}}},
		},
	}
}

func testTarget() Target {
	return Target{WorkspaceID: "w1", RootTabID: "w1:t1", CWD: "/proj", SocketPath: "/tmp/herdr.sock"}
}

// TestApply_OneAtomicDispatchPerTab is the core R7.1 guarantee: a four-pane
// tab is applied in a single socket round trip carrying the whole tree, and a
// two-tab template costs exactly two round trips — not one per pane.
func TestApply_OneAtomicDispatchPerTab(t *testing.T) {
	t.Parallel()
	a := newFakeApplier()
	if err := Apply(context.Background(), a, testTarget(), fourPaneTemplate()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(a.calls) != 2 {
		t.Fatalf("ApplyLayout calls = %d, want exactly one per tab (2)", len(a.calls))
	}
	if got := countPanes(&a.calls[0].Root); got != 4 {
		t.Errorf("tab 0 dispatched %d panes, want all 4 in the same call", got)
	}
	if got := countPanes(&a.calls[1].Root); got != 1 {
		t.Errorf("tab 1 dispatched %d panes, want 1", got)
	}
	if a.calls[0].Root.Type != herdr.NodeTypeSplit {
		t.Errorf("tab 0 root type = %q, want a split tree", a.calls[0].Root.Type)
	}
}

// TestApply_RootTabReuse is design D5, pinned in both directions plus the
// live context the pure compiler cannot supply.
//
// With a root tab available, tab 0 targets it by id so `workspace create`'s
// default tab is renamed and reused instead of being left dangling, while
// later tabs leave tab_id empty so the daemon creates them. Setting tab_id on
// every tab would silently collapse a multi-tab template into one repeatedly
// overwritten tab, so the empty case is asserted too. With no root tab to
// donate, every tab falls back to plain creation.
//
// Each case also checks the workspace id and socket path, which the compiler
// leaves unset and only the dispatcher knows.
func TestApply_RootTabReuse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		rootTabID        string
		wantTabIDs       []string
		wantWorkspaceIDs []string
	}{
		{
			name:             "root tab reused by first tab only",
			rootTabID:        "w1:t1",
			wantTabIDs:       []string{"w1:t1", ""},
			wantWorkspaceIDs: []string{"", "w1"},
		},
		{
			name:             "no root tab available creates every tab",
			rootTabID:        "",
			wantTabIDs:       []string{"", ""},
			wantWorkspaceIDs: []string{"w1", "w1"},
		},
	}
	wantLabels := []string{"code", "term"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newFakeApplier()
			target := testTarget()
			target.RootTabID = tc.rootTabID
			if err := Apply(context.Background(), a, target, fourPaneTemplate()); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(a.calls) != len(tc.wantTabIDs) {
				t.Fatalf("calls = %d, want %d", len(a.calls), len(tc.wantTabIDs))
			}
			for i, wantID := range tc.wantTabIDs {
				if got := a.calls[i].TabID; got != wantID {
					t.Errorf("tab %d tab_id = %q, want %q", i, got, wantID)
				}
				if got := a.calls[i].TabLabel; got != wantLabels[i] {
					t.Errorf("tab %d tab_label = %q, want %q", i, got, wantLabels[i])
				}
				if got := a.calls[i].WorkspaceID; got != tc.wantWorkspaceIDs[i] {
					t.Errorf("tab %d workspace_id = %q, want %q", i, got, tc.wantWorkspaceIDs[i])
				}
				if got := a.sockets[i]; got != "/tmp/herdr.sock" {
					t.Errorf("tab %d socket = %q, want %q", i, got, "/tmp/herdr.sock")
				}
			}
		})
	}
}

// TestApply_ExactlyOneTargetIdentity proves that every ApplyLayout call specifies
// either TabID or WorkspaceID, never both and never neither. Herdr's daemon rejects
// calls that set both with "invalid_target: use either tab_id or workspace_id, not both".
func TestApply_ExactlyOneTargetIdentity(t *testing.T) {
	t.Parallel()
	for _, rootTabID := range []string{"w1:t1", ""} {
		a := newFakeApplier()
		target := testTarget()
		target.RootTabID = rootTabID
		if err := Apply(context.Background(), a, target, fourPaneTemplate()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		for i, call := range a.calls {
			hasTab := call.TabID != ""
			hasWorkspace := call.WorkspaceID != ""
			if hasTab == hasWorkspace {
				t.Errorf("call %d: hasTab=%v hasWorkspace=%v (tab_id=%q, workspace_id=%q), want exactly one target identity",
					i, hasTab, hasWorkspace, call.TabID, call.WorkspaceID)
			}
		}
	}
}

// TestApply_FocusTargetsDeclaredTab covers R3.1: exactly the tab named by
// focus receives focus: true, and the others explicitly do not.
func TestApply_FocusTargetsDeclaredTab(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		focus     *config.TemplateFocus
		wantFocus []bool
	}{
		{name: "explicit focus on second tab", focus: &config.TemplateFocus{Tab: "term"}, wantFocus: []bool{false, true}},
		{name: "no focus declared defaults to first tab", focus: nil, wantFocus: []bool{true, false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newFakeApplier()
			tpl := fourPaneTemplate()
			tpl.Focus = tc.focus
			if err := Apply(context.Background(), a, testTarget(), tpl); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(a.calls) != len(tc.wantFocus) {
				t.Fatalf("calls = %d, want %d", len(a.calls), len(tc.wantFocus))
			}
			for i, want := range tc.wantFocus {
				if a.calls[i].Focus != want {
					t.Errorf("tab %d focus = %v, want %v", i, a.calls[i].Focus, want)
				}
			}
		})
	}
}

// TestApply_ApplierErrorStopsRemainingTabs is the fail-closed dispatch rule:
// a rejected tab aborts the loop immediately, so a half-applied template can
// never keep piling tabs onto the workspace. The error names prior progress
// honestly because Herdr has per-tab, not whole-template, atomicity.
func TestApply_ApplierErrorStopsRemainingTabs(t *testing.T) {
	t.Parallel()
	boom := errors.New("daemon rejected layout")
	a := newFakeApplier()
	a.err, a.failAt = boom, 1

	err := Apply(context.Background(), a, testTarget(), fourPaneTemplate())
	if err == nil {
		t.Fatal("Apply must fail when the applier rejects a tab")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Apply error = %v, want it to wrap the applier failure", err)
	}
	if !strings.Contains(err.Error(), "term") || !strings.Contains(err.Error(), "1 tab(s) applied") || !strings.Contains(err.Error(), "partial template") {
		t.Errorf("Apply error = %v, want failing tab and partial-progress context", err)
	}
	if len(a.calls) != 2 {
		t.Errorf("dispatched %d tabs, want exactly two attempts and no tab 3", len(a.calls))
	}
}

// TestApply_CompileFailureWritesNothingToSocket is R8.1: an invalid layout is
// rejected during compilation, so the applier is never invoked at all. The
// zero-call assertion is meaningful precisely because every other test in
// this file proves the same applier does record calls.
func TestApply_CompileFailureWritesNothingToSocket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tpl  config.TemplateConfig
	}{
		{
			name: "invalid split axis",
			tpl: config.TemplateConfig{Tabs: []config.TemplateTab{{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: "diagonal", Children: []string{"a", "b"}},
				{ID: "a"}, {ID: "b"},
			}}}},
		},
		{
			name: "cyclic children",
			tpl: config.TemplateConfig{Tabs: []config.TemplateTab{{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitCols, Children: []string{"a", "main"}},
				{ID: "a"},
			}}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newFakeApplier()
			err := Apply(context.Background(), a, testTarget(), tc.tpl)
			if err == nil {
				t.Fatal("Apply must fail closed on an invalid layout")
			}
			if len(a.calls) != 0 {
				t.Errorf("compile failure still dispatched %d calls, want 0", len(a.calls))
			}
		})
	}
}

// TestApply_CommandOnlyTemplateIsOneFocusedPane covers the common
// "default"/"k8s" shape: no tabs, just a command. It must still travel the
// atomic path as a single focused pane rather than a `pane run` subprocess.
func TestApply_CommandOnlyTemplateIsOneFocusedPane(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		closeOnExit bool
		shell       string
		wantCommand []string
	}{
		{
			name:        "returns to a shell when close_on_exit is false",
			shell:       "/bin/zsh",
			wantCommand: []string{"/bin/zsh", "-l", "-c", "k9s; exec /bin/zsh"},
		},
		{
			name:        "runs directly when close_on_exit is true",
			shell:       "/bin/zsh",
			closeOnExit: true,
			wantCommand: []string{"/bin/zsh", "-l", "-c", "k9s"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newFakeApplier()
			target := testTarget()
			target.Shell = tc.shell
			if err := Apply(context.Background(), a, target, config.TemplateConfig{Command: "k9s", CloseOnExit: tc.closeOnExit}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(a.calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(a.calls))
			}
			p := a.calls[0]
			if p.Root.Type != herdr.NodeTypePane || !p.Focus {
				t.Fatalf("root = %+v focus = %v, want a single focused pane", p.Root, p.Focus)
			}
			if !slices.Equal(p.Root.Command, tc.wantCommand) {
				t.Errorf("pane command = %v, want %v", p.Root.Command, tc.wantCommand)
			}
			if p.Root.Cwd != "/proj" {
				t.Errorf("pane cwd = %q, want the target cwd %q", p.Root.Cwd, "/proj")
			}
		})
	}
}

// TestRunCommand_WrapsCloseOnExit covers the surviving existing-pane path used
// by the --target=tab/pane launches, which open into a container pane the
// caller already created and therefore cannot go through layout.apply (that
// would replace the whole surrounding tab).
func TestRunCommand_WrapsCloseOnExit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tpl    config.TemplateConfig
		binary string
		want   []string
	}{
		{
			name: "close_on_exit chains a self-close",
			tpl:  config.TemplateConfig{Command: "k9s", CloseOnExit: true},
			want: []string{"run:w1:p1:k9s; 'herdr' pane close 'w1:p1'"},
		},
		{
			name:   "custom binary is shell quoted",
			tpl:    config.TemplateConfig{Command: "k9s", CloseOnExit: true},
			binary: "/path with spaces/myherdr",
			want:   []string{"run:w1:p1:k9s; '/path with spaces/myherdr' pane close 'w1:p1'"},
		},
		{
			name: "plain command is untouched",
			tpl:  config.TemplateConfig{Command: "k9s"},
			want: []string{"run:w1:p1:k9s"},
		},
		{
			name: "empty command leaves a plain shell",
			tpl:  config.TemplateConfig{},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{}
			if err := RunCommand(context.Background(), r, "w1:p1", tc.binary, tc.tpl); err != nil {
				t.Fatalf("RunCommand: %v", err)
			}
			if strings.Join(r.ran, "|") != strings.Join(tc.want, "|") {
				t.Errorf("ran = %v, want %v", r.ran, tc.want)
			}
		})
	}
}

// TestRunCommand_ErrorPropagates confirms a pane failure surfaces instead of
// being swallowed, so the caller can roll its container back.
func TestRunCommand_ErrorPropagates(t *testing.T) {
	t.Parallel()
	boom := errors.New("pane gone")
	r := &fakeRunner{err: boom}
	err := RunCommand(context.Background(), r, "w1:p1", "herdr", config.TemplateConfig{Command: "k9s"})
	if !errors.Is(err, boom) {
		t.Fatalf("RunCommand error = %v, want the pane failure wrapped", err)
	}
}

// TestWrapCloseOnExit_Helper is a focused table-driven test for the shared
// wrapCloseOnExit helper, which the compiler and the existing-pane path both
// depend on for identical close-on-exit semantics.
func TestShellCommand_QuotesEveryArg(t *testing.T) {
	t.Parallel()
	got := ShellCommand("/path with spaces/herdr; touch /tmp/pwned", "pane", "close", "p'1; echo injected")
	want := "'/path with spaces/herdr; touch /tmp/pwned' 'pane' 'close' 'p'\\''1; echo injected'"
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
}

func TestWrapCloseOnExit_Helper(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cmd    string
		paneID string
		binary string
		on     bool
		want   string
	}{
		{name: "on wraps", cmd: "nvim", paneID: "p1", binary: "herdr", on: true, want: "nvim; 'herdr' pane close 'p1'"},
		{name: "off no wrap", cmd: "nvim", paneID: "p1", binary: "herdr", on: false, want: "nvim"},
		{name: "custom binary", cmd: "nvim", paneID: "p1", binary: "myherdr", on: true, want: "nvim; 'myherdr' pane close 'p1'"},
		{name: "empty binary defaults to herdr", cmd: "nvim", paneID: "p1", binary: "", on: true, want: "nvim; 'herdr' pane close 'p1'"},
		{name: "paneID empty on=true returns cmd unchanged (defensive no-wrap)", cmd: "nvim", paneID: "", binary: "herdr", on: true, want: "nvim"},
		{name: "off preserves empty cmd", cmd: "", paneID: "p1", binary: "herdr", on: false, want: ""},
		{name: "hostile binary is quoted", cmd: "nvim", paneID: "p1", binary: "herdr; touch /tmp/pwned", on: true, want: "nvim; 'herdr; touch /tmp/pwned' pane close 'p1'"},
		{name: "paneID with shell metacharacters returns cmd unchanged (defensive no-wrap)", cmd: "nvim", paneID: "p1; rm -rf ~ #", binary: "herdr", on: true, want: "nvim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := wrapCloseOnExit(tc.cmd, tc.paneID, tc.binary, tc.on)
			if got != tc.want {
				t.Errorf("wrapCloseOnExit(%q,%q,%q,%v) = %q, want %q", tc.cmd, tc.paneID, tc.binary, tc.on, got, tc.want)
			}
		})
	}
}

// TestApply_EnvPropagation proves target.PathEnv reaches only command-backed
// panes and never commandless panes or split nodes.
func TestApply_EnvPropagation(t *testing.T) {
	t.Parallel()
	a := newFakeApplier()
	target := testTarget()
	target.PathEnv = "/custom/bin:/usr/bin:/bin"

	if err := Apply(context.Background(), a, target, fourPaneTemplate()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(a.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(a.calls))
	}

	// Walk panes and check Env map
	var checkPanes func(n *herdr.LayoutNode)
	checkPanes = func(n *herdr.LayoutNode) {
		if n == nil {
			return
		}
		if n.Type == herdr.NodeTypePane {
			if len(n.Command) > 0 {
				if n.Env == nil || n.Env["PATH"] != target.PathEnv {
					t.Errorf("command pane %s (%v) env = %+v, want PATH=%q", n.PaneID, n.Command, n.Env, target.PathEnv)
				}
				if len(n.Env) != 1 {
					t.Errorf("command pane %s env has extra keys: %+v", n.PaneID, n.Env)
				}
			} else {
				if n.Env != nil {
					t.Errorf("commandless pane %s env = %+v, want nil", n.PaneID, n.Env)
				}
			}
			return
		}
		if n.Env != nil {
			t.Errorf("split node env = %+v, want nil", n.Env)
		}
		checkPanes(n.First)
		checkPanes(n.Second)
	}

	checkPanes(&a.calls[0].Root)
	checkPanes(&a.calls[1].Root)
}
