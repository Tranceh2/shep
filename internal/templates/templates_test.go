package templates

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// fakeDriver records every tab/pane mutation call so tests can assert the
// exact sequence of Herdr operations issued by Apply, without a real daemon.
// focus flags on CreateTab/SplitPane are captured in created entries so the
// new focus-at-creation-time behaviour is observable.
type fakeDriver struct {
	tabSeq   int
	paneSeq  int
	created  []string // "tab:<workspace>:<cwd>:<label>:<focus>" / "split:<pane>:<dir>:<ratio>:<cwd>:<focus>"
	renamed  []string // "rename:<tab>:<label>"
	ran      []string // "run:<pane>:<command>"
	focused  []string // "focus-tab:<id>"
	splitErr error
}

func (f *fakeDriver) Detect(context.Context) bool { return true }
func (f *fakeDriver) Snapshot(context.Context) (source.Snapshot, error) {
	return source.Snapshot{}, errors.New("not implemented")
}
func (f *fakeDriver) ListSessions(context.Context) ([]source.Session, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDriver) FocusOrCreate(context.Context, source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("not implemented")
}
func (f *fakeDriver) ReadPane(context.Context, string, int) (string, error) { return "", nil }

func (f *fakeDriver) CreateTab(_ context.Context, workspaceID, cwd, label string, focus bool) (source.Tab, source.Pane, error) {
	f.tabSeq++
	f.paneSeq++
	tabID := seqID("t", f.tabSeq)
	paneID := seqID("p", f.paneSeq)
	f.created = append(f.created, "tab:"+workspaceID+":"+cwd+":"+label+":"+focusStr(focus))
	return source.Tab{ID: tabID, WorkspaceID: workspaceID}, source.Pane{ID: paneID, WorkspaceID: workspaceID, TabID: tabID}, nil
}

func (f *fakeDriver) RenameTab(_ context.Context, tabID, label string) error {
	f.renamed = append(f.renamed, "rename:"+tabID+":"+label)
	return nil
}

func (f *fakeDriver) RenamePane(context.Context, string, *string) error {
	return errors.New("not implemented")
}

func (f *fakeDriver) SplitPane(_ context.Context, paneID, direction string, ratio float64, cwd string, focus bool) (source.Pane, error) {
	if f.splitErr != nil {
		return source.Pane{}, f.splitErr
	}
	f.paneSeq++
	newID := seqID("p", f.paneSeq)
	f.created = append(f.created, "split:"+paneID+":"+direction+":"+ratioStr(ratio)+":"+cwd+":"+focusStr(focus))
	return source.Pane{ID: newID}, nil
}

func (f *fakeDriver) RunPane(_ context.Context, paneID, command string) error {
	f.ran = append(f.ran, "run:"+paneID+":"+command)
	return nil
}

func (f *fakeDriver) FocusTab(_ context.Context, tabID string) error {
	f.focused = append(f.focused, "focus-tab:"+tabID)
	return nil
}

func seqID(prefix string, n int) string {
	return prefix + "-" + string(rune('0'+n))
}

func ratioStr(r float64) string {
	// Minimal fixed-precision formatting sufficient for test assertions.
	switch r {
	case 0.5:
		return "0.5"
	case 0.8:
		return "0.8"
	case 0.2:
		return "0.2"
	case 0.3333333333333333:
		return "0.3333333333333333"
	default:
		return "?"
	}
}

func focusStr(b bool) string {
	if b {
		return "focus"
	}
	return "nofocus"
}

// TestApply_FlatCommandRunsInRootPane confirms a Command-only template (no
// tabs) runs directly in the root pane of the freshly created workspace and
// issues no tab/pane mutation calls (the default tab is exactly what we
// want — nothing to fix).
func TestApply_FlatCommandRunsInRootPane(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{Command: "k9s"}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.ran) != 1 || d.ran[0] != "run:w1:p1:k9s" {
		t.Errorf("ran = %v, want single run in root pane", d.ran)
	}
	if len(d.renamed) != 0 || len(d.created) != 0 {
		t.Errorf("flat command template must not create/rename tabs: renamed=%v created=%v", d.renamed, d.created)
	}
}

// TestApply_EmptyCommandLeavesPlainShell confirms an empty command (the
// canonical "default" template) does not call RunPane at all.
func TestApply_EmptyCommandLeavesPlainShell(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, config.TemplateConfig{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.ran) != 0 {
		t.Errorf("expected no RunPane calls for empty command, got %v", d.ran)
	}
}

// TestApply_FirstTabReusesRootTab is the top-priority bug fix: when a
// template declares tabs, the FIRST tab must reuse/rename the workspace's
// existing root tab (created by `herdr workspace create`) instead of
// leaving it as an unused default tab alongside a new one.
func TestApply_FirstTabReusesRootTab(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Command: "nvim"},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 0 {
		t.Fatalf("first tab must reuse the root tab, not create a new one; created=%v", d.created)
	}
	if len(d.renamed) != 1 || d.renamed[0] != "rename:w1:t1:code" {
		t.Errorf("expected root tab renamed to 'code', got %v", d.renamed)
	}
	if len(d.ran) != 1 || d.ran[0] != "run:w1:p1:nvim" {
		t.Errorf("expected nvim run in root pane, got %v", d.ran)
	}
}

// TestApply_SecondTabCreatesNewTab confirms tabs after the first are created
// fresh (not reusing the root tab again).
func TestApply_SecondTabCreatesNewTab(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "a", Nodes: []config.TemplateNode{{ID: "a", Command: "nvim"}}},
			{Name: "term", Root: "b", Nodes: []config.TemplateNode{{ID: "b", Command: ""}}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 1 || d.created[0] != "tab:w1:/proj:term:nofocus" {
		t.Fatalf("expected exactly one new tab created for 'term' without focus (no Focus set, default stays on tab 0), got %v", d.created)
	}
}

// TestApply_BranchNodeSplitsWithRatios (top-priority bug fix continued):
// confirms a branch node with two children and sizes [80,20] issues exactly
// one split with ratio 0.8 (the ORIGINAL/kept pane retains 80%), and each
// leaf's command runs in the correct resulting pane.
func TestApply_BranchNodeSplitsWithRatios(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitRows, Children: []string{"editor", "terminal"}, Sizes: []int{80, 20}},
				{ID: "editor", Command: "nvim"},
				{ID: "terminal", Command: ""},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 1 || d.created[0] != "split:w1:p1:down:0.8:/proj:nofocus" {
		t.Fatalf("expected one 80%% down split of the root pane without focus (no Focus set), got %v", d.created)
	}
	if len(d.ran) != 1 || d.ran[0] != "run:w1:p1:nvim" {
		t.Errorf("expected nvim to run in the KEPT (80%%) pane, got %v", d.ran)
	}
}

// TestApply_ThreeWayEqualSplitCascades (triangulation for N>2 children):
// three children with no explicit sizes split evenly via cascading splits
// (1/3 then 1/2 of the remainder).
func TestApply_ThreeWayEqualSplitCascades(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitCols, Children: []string{"a", "b", "c"}},
				{ID: "a", Command: "one"},
				{ID: "b", Command: "two"},
				{ID: "c", Command: "three"},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 2 {
		t.Fatalf("expected 2 cascading splits for 3 children, got %v", d.created)
	}
	if len(d.ran) != 3 {
		t.Fatalf("expected all 3 leaves to run their command, got %v", d.ran)
	}
}

// TestApply_SplitErrorPropagates confirms a driver failure surfaces instead
// of being silently swallowed.
func TestApply_SplitErrorPropagates(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{splitErr: errors.New("boom")}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitRows, Children: []string{"a", "b"}},
				{ID: "a", Command: ""},
				{ID: "b", Command: ""},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err == nil {
		t.Fatal("expected split error to propagate")
	}
}

// TestApply_DefaultFocus_FirstTabStaysFocused is the core default behaviour:
// when no top-level Focus is declared, the FIRST tab (which reuses the
// workspace's root tab, already focused by `workspace create --focus`) stays
// focused. Subsequent tabs are created with focus=false (--no-focus) so they
// never steal focus from the first. No post-hoc FocusTab call is issued.
func TestApply_DefaultFocus_FirstTabStaysFocused(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "a", Nodes: []config.TemplateNode{{ID: "a", Command: ""}}},
			{Name: "term", Root: "b", Nodes: []config.TemplateNode{{ID: "b", Command: ""}}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Second tab created with --no-focus so the first (root) tab keeps focus.
	if len(d.created) != 1 || d.created[0] != "tab:w1:/proj:term:nofocus" {
		t.Errorf("expected second tab created with nofocus, got %v", d.created)
	}
	// No post-hoc FocusTab calls: focus is set at creation time only.
	if len(d.focused) != 0 {
		t.Errorf("expected no FocusTab calls for default focus, got %v", d.focused)
	}
}

// TestApply_FocusSecondTab_CreatedWithFocus confirms focus = { tab = "term" }
// makes the second tab the focus target: it is created with focus=true
// (--focus) so herdr moves keyboard focus to it at creation time.
func TestApply_FocusSecondTab_CreatedWithFocus(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Focus: &config.TemplateFocus{Tab: "term"},
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "a", Nodes: []config.TemplateNode{{ID: "a", Command: ""}}},
			{Name: "term", Root: "b", Nodes: []config.TemplateNode{{ID: "b", Command: ""}}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 1 || d.created[0] != "tab:w1:/proj:term:focus" {
		t.Errorf("expected second tab created with focus, got %v", d.created)
	}
	if len(d.focused) != 0 {
		t.Errorf("expected no post-hoc FocusTab (focus is at creation), got %v", d.focused)
	}
}

// TestApply_FocusFirstTab_NoExtraFocusCalls confirms focus = { tab = "code" }
// on the FIRST tab (which reuses the already-focused root tab) issues no
// extra CreateTab or FocusTab calls — the root tab is already focused, so
// nothing needs to happen at the tab level.
func TestApply_FocusFirstTab_NoExtraFocusCalls(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Focus: &config.TemplateFocus{Tab: "code"},
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Command: "nvim"},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.created) != 0 {
		t.Errorf("first tab reuses root tab, expected no creates, got %v", d.created)
	}
	if len(d.focused) != 0 {
		t.Errorf("root tab already focused, expected no FocusTab, got %v", d.focused)
	}
}

// TestApply_FocusNodeKeptPane_SplitNoFocus confirms that when the focus node
// maps to the KEPT pane after a split (the first child), the split is issued
// with focus=false (--no-focus) so the kept pane retains keyboard focus.
func TestApply_FocusNodeKeptPane_SplitNoFocus(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Focus: &config.TemplateFocus{Tab: "code", Node: "editor"},
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitRows, Children: []string{"editor", "terminal"}, Sizes: []int{80, 20}},
				{ID: "editor", Command: "nvim"},
				{ID: "terminal", Command: ""},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// editor is child[0] → the KEPT pane. The split creates terminal's pane
	// (the NEW pane), which must NOT steal focus (--no-focus) so editor keeps it.
	if len(d.created) != 1 || d.created[0] != "split:w1:p1:down:0.8:/proj:nofocus" {
		t.Errorf("expected split with nofocus (editor is kept pane), got %v", d.created)
	}
}

// TestApply_FocusNodeNewPane_SplitFocus confirms that when the focus node maps
// to the NEW pane created by a split (the last child), the split is issued
// with focus=true (--focus) so the new pane receives keyboard focus.
func TestApply_FocusNodeNewPane_SplitFocus(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Focus: &config.TemplateFocus{Tab: "code", Node: "terminal"},
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitRows, Children: []string{"editor", "terminal"}, Sizes: []int{80, 20}},
				{ID: "editor", Command: "nvim"},
				{ID: "terminal", Command: ""},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// terminal is child[1] → the NEW pane from the split. The split must
	// pass --focus so terminal's pane receives focus.
	if len(d.created) != 1 || d.created[0] != "split:w1:p1:down:0.8:/proj:focus" {
		t.Errorf("expected split with focus (terminal is new pane), got %v", d.created)
	}
}

// TestApply_FocusTabOnly_AllSplitsNoFocus confirms that when Focus.Node is
// empty (focus just the tab, no specific pane), every split within that tab
// passes --no-focus so the tab's root pane keeps focus.
func TestApply_FocusTabOnly_AllSplitsNoFocus(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Focus: &config.TemplateFocus{Tab: "code"},
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Split: config.SplitCols, Children: []string{"a", "b"}},
				{ID: "a", Command: "one"},
				{ID: "b", Command: "two"},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj"}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, c := range d.created {
		if !strings.HasSuffix(c, ":nofocus") {
			t.Errorf("focus tab only: expected all splits --no-focus, got %q", c)
		}
	}
}

// TestApply_CloseOnExit_WrapsCommand confirms a leaf node with CloseOnExit=true
// and a non-empty Command has its command wrapped so the pane closes itself
// once the command's shell returns control. The wrap appends
// "; <binary> pane close <pane_id>" using the target's binary name.
func TestApply_CloseOnExit_WrapsCommand(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Command: "nvim", CloseOnExit: true},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "herdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "run:w1:p1:nvim; herdr pane close w1:p1"
	if len(d.ran) != 1 || d.ran[0] != want {
		t.Errorf("expected wrapped command %q, got %v", want, d.ran)
	}
}

// TestApply_CloseOnExitFalse_PlainCommand confirms CloseOnExit=false (the
// default) sends the plain command with no close-on-exit chaining.
func TestApply_CloseOnExitFalse_PlainCommand(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Command: "nvim"},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "herdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.ran) != 1 || d.ran[0] != "run:w1:p1:nvim" {
		t.Errorf("expected plain command, got %v", d.ran)
	}
}

// TestApply_CloseOnExit_CustomBinary confirms the close-on-exit wrap uses the
// configured binary name (from Target.Binary), not a hardcoded "herdr".
func TestApply_CloseOnExit_CustomBinary(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{
				{ID: "main", Command: "nvim", CloseOnExit: true},
			}},
		},
	}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "myherdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "run:w1:p1:nvim; myherdr pane close w1:p1"
	if len(d.ran) != 1 || d.ran[0] != want {
		t.Errorf("expected wrap with custom binary %q, got %v", want, d.ran)
	}
}

// TestApply_CloseOnExitTopLevel_WrapsCommand confirms a top-level
// TemplateConfig (no Tabs) with CloseOnExit=true and a non-empty Command has
// its command wrapped so the root pane closes itself once the command
// finishes — the workspace-command path that was previously silently ignored.
// It reuses the same wrap as the leaf-node path (one tested code path).
func TestApply_CloseOnExitTopLevel_WrapsCommand(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{Command: "nvim", CloseOnExit: true}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "herdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "run:w1:p1:nvim; herdr pane close w1:p1"
	if len(d.ran) != 1 || d.ran[0] != want {
		t.Errorf("expected wrapped top-level command %q, got %v", want, d.ran)
	}
}

// TestApply_CloseOnExitTopLevelFalse_PlainCommand confirms CloseOnExit=false
// (the default) on a top-level template sends the plain command with no
// close-on-exit chaining.
func TestApply_CloseOnExitTopLevelFalse_PlainCommand(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{Command: "nvim"}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "herdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(d.ran) != 1 || d.ran[0] != "run:w1:p1:nvim" {
		t.Errorf("expected plain top-level command, got %v", d.ran)
	}
}

// TestApply_CloseOnExitTopLevel_CustomBinary confirms the top-level wrap uses
// the configured binary name (Target.Binary), not a hardcoded "herdr", and
// falls back to "herdr" when Binary is empty.
func TestApply_CloseOnExitTopLevel_CustomBinary(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{Command: "nvim", CloseOnExit: true}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "myherdr",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "run:w1:p1:nvim; myherdr pane close w1:p1"
	if len(d.ran) != 1 || d.ran[0] != want {
		t.Errorf("expected top-level wrap with custom binary %q, got %v", want, d.ran)
	}
}

// TestApply_CloseOnExitTopLevel_EmptyBinary_DefaultsToHerdr confirms the
// simple-Command path with an empty Target.Binary still produces a valid wrap
// ("herdr" substituted by wrapCloseOnExit, the single default owner) rather
// than a malformed "k9s;  pane close <id>" with a missing binary token.
func TestApply_CloseOnExitTopLevel_EmptyBinary_DefaultsToHerdr(t *testing.T) {
	t.Parallel()
	d := &fakeDriver{}
	tpl := config.TemplateConfig{Command: "k9s", CloseOnExit: true}
	err := Apply(context.Background(), d, Target{
		WorkspaceID: "w1", RootTabID: "w1:t1", RootPaneID: "w1:p1", CWD: "/proj", Binary: "",
	}, tpl)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "run:w1:p1:k9s; herdr pane close w1:p1"
	if len(d.ran) != 1 || d.ran[0] != want {
		t.Errorf("expected empty binary to default to herdr in the wrap %q, got %v", want, d.ran)
	}
}

// TestWrapCloseOnExit_Helper is a focused table-driven test for the shared
// wrapCloseOnExit helper used by both the simple-Command Apply branch and the
// leaf applyNode branch. Both code paths must share this one implementation.
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
		{name: "on wraps", cmd: "nvim", paneID: "p1", binary: "herdr", on: true, want: "nvim; herdr pane close p1"},
		{name: "off no wrap", cmd: "nvim", paneID: "p1", binary: "herdr", on: false, want: "nvim"},
		{name: "custom binary", cmd: "nvim", paneID: "p1", binary: "myherdr", on: true, want: "nvim; myherdr pane close p1"},
		{name: "empty binary defaults to herdr", cmd: "nvim", paneID: "p1", binary: "", on: true, want: "nvim; herdr pane close p1"},
		{name: "paneID empty on=true returns cmd unchanged (defensive no-wrap)", cmd: "nvim", paneID: "", binary: "herdr", on: true, want: "nvim"},
		{name: "off preserves empty cmd", cmd: "", paneID: "p1", binary: "herdr", on: false, want: ""},
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
