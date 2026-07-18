package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

// fakePreviewRenderer is a minimal preview.Renderer stub for command-package
// tests that only need a non-nil renderer identity, not real render output.
type fakePreviewRenderer struct{}

func (fakePreviewRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{}, nil
}

// --- mocks ---

// openDriver is an open-scoped HerdrDriver mock capturing the last candidate
// and responding with scripted FocusOrCreate results plus recording any
// tab/pane mutation calls a template application would issue.
type openDriver struct {
	detect      bool
	focusErr    error
	lastCand    source.Candidate
	lastAction  source.HerdrAction
	workspaceID string
	rootTabID   string
	rootPaneID  string
	listErr     error

	// currentPane/currentPaneErr script CurrentPane: when currentPaneErr is
	// non-nil it is returned (use source.ErrNoFocusedPane to model "shep not
	// inside a herdr pane"); otherwise currentPane is returned as-is.
	currentPane      source.Pane
	currentPaneErr   error
	currentCalled    bool
	currentPaneDelay time.Duration

	renamed      []string
	ran          []string
	created      []string // "tab:<ws>:<cwd>:<label>:<focus>" / "split:<pane>:<dir>:<ratio>:<cwd>:<focus>"
	focused      []string
	focusTabErr  error
	createTabErr error
	splitPaneErr error
	runErr       error
	// runErrOnFirstCall, when non-nil, is returned only for the first RunPane
	// call (the Apply-internal run); every subsequent call succeeds
	// regardless of runErr. Used to script an Apply failure followed by a
	// successful best-effort rollback close.
	runErrOnFirstCall error
	runCallCount      int
}

func (d *openDriver) Detect(context.Context) bool { return d.detect }
func (d *openDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, d.listErr
}
func (d *openDriver) FocusOrCreate(_ context.Context, cand source.Candidate) (source.FocusResult, error) {
	d.lastCand = cand
	if d.focusErr != nil {
		return source.FocusResult{}, d.focusErr
	}
	action := d.lastAction
	if action == 0 {
		action = source.HerdrActionFocused
	}
	return source.FocusResult{WorkspaceID: d.workspaceID, Action: action, RootTabID: d.rootTabID, RootPaneID: d.rootPaneID}, nil
}
func (d *openDriver) ListTabs(context.Context, string) ([]source.Tab, error) {
	return nil, errors.New("openDriver does not implement ListTabs")
}
func (d *openDriver) ListPanes(context.Context, string) ([]source.Pane, error) {
	return nil, errors.New("openDriver does not implement ListPanes")
}
func (d *openDriver) ListAgents(context.Context) ([]source.Agent, error) {
	return nil, errors.New("openDriver does not implement ListAgents")
}
func (d *openDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("openDriver does not implement ReadPane")
}
func (d *openDriver) CreateTab(_ context.Context, workspaceID, cwd, label string, focus bool) (source.Tab, source.Pane, error) {
	if d.createTabErr != nil {
		return source.Tab{}, source.Pane{}, d.createTabErr
	}
	d.created = append(d.created, "tab:"+workspaceID+":"+cwd+":"+label+":"+openFocusStr(focus))
	return source.Tab{ID: "new-t"}, source.Pane{ID: "new-p"}, nil
}
func (d *openDriver) RenameTab(_ context.Context, tabID, label string) error {
	d.renamed = append(d.renamed, "rename:"+tabID+":"+label)
	return nil
}
func (d *openDriver) SplitPane(_ context.Context, paneID, direction string, ratio float64, cwd string, focus bool) (source.Pane, error) {
	if d.splitPaneErr != nil {
		return source.Pane{}, d.splitPaneErr
	}
	d.created = append(d.created, "split:"+paneID+":"+direction+":"+strconv.FormatFloat(ratio, 'f', -1, 64)+":"+cwd+":"+openFocusStr(focus))
	return source.Pane{ID: "split-p"}, nil
}
func (d *openDriver) RunPane(_ context.Context, paneID, command string) error {
	d.ran = append(d.ran, "run:"+paneID+":"+command)
	d.runCallCount++
	if d.runErrOnFirstCall != nil && d.runCallCount == 1 {
		return d.runErrOnFirstCall
	}
	return d.runErr
}
func (d *openDriver) FocusTab(_ context.Context, tabID string) error {
	d.focused = append(d.focused, "focus-tab:"+tabID)
	return d.focusTabErr
}
func (d *openDriver) CurrentPane(ctx context.Context) (source.Pane, error) {
	d.currentCalled = true
	if d.currentPaneDelay > 0 {
		select {
		case <-time.After(d.currentPaneDelay):
		case <-ctx.Done():
			return source.Pane{}, ctx.Err()
		}
	}
	return d.currentPane, d.currentPaneErr
}

// openFocusStr renders a focus bool the same way the templates-package fake
// does, so the recorded CreateTab/SplitPane entries are directly comparable
// across both packages' tests.
func openFocusStr(b bool) string {
	if b {
		return "focus"
	}
	return "nofocus"
}

// fakeSelector lets open tests script the cascade without invoking fzf.
type fakeSelector struct {
	pick source.Candidate
	ok   bool
	err  error
}

func (fakeSelector) Name() string { return "fake" }
func (f fakeSelector) Select(context.Context, []source.Candidate, string) (source.Candidate, bool, error) {
	return f.pick, f.ok, f.err
}

// --- helpers ---

// seedCfg creates a temp root with one [[workspaces]] entry per name and
// returns a Config surfacing them via the workspaces source. Tests compare
// against resolved paths through resolved() so macOS /var -> /private/var
// symlinks don't flake.
func seedCfg(t *testing.T, names ...string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	for _, n := range names {
		dir := filepath.Join(root, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
		cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{Name: n, Path: dir})
	}
	return cfg, root
}

// resolved returns the EvalSymlinks canonisation of a path or the raw path.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// runOpen executes `shep open ...` against a fresh App. driver/cascade are
// optional; nil driver models "Herdr absent", nil cascade uses the default.
func runOpen(t *testing.T, cfg *config.Config, driver *openDriver, cascade *selector.Cascade, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	if driver != nil {
		app.herdrDriver = driver
		app.herdrDriverInjected = true
	}
	app.cfg = cfg
	app.probes = config.Probes{Git: true}
	if driver != nil {
		app.probes.Herdr = true
	}
	if cascade != nil {
		c := cascade
		app.selectorBuilder = func() *selector.Cascade { return c }
	}
	cmd := app.rootCmd()
	cmd.SetArgs(append([]string{"open"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// --- tests ---

// TestCascadeFor_RoutesBySelector verifies the selector cascade is built
// from [general].selector: builtin skips fzf, fzf and auto include fzf, and
// an unknown/empty value falls back to the builtin shape. Direct always runs
// first regardless of selector value.
func TestCascadeFor_RoutesBySelector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sel  string
		want []string
	}{
		{name: "builtin skips fzf", sel: config.SelectorBuiltin, want: []string{"direct", "tui"}},
		{name: "fzf includes fzf", sel: config.SelectorFzf, want: []string{"direct", "fzf", "tui"}},
		{name: "auto includes fzf", sel: config.SelectorAuto, want: []string{"direct", "fzf", "tui"}},
		{name: "empty defaults to builtin", sel: "", want: []string{"direct", "tui"}},
		{name: "unknown treated as builtin", sel: "nope", want: []string{"direct", "tui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cascadeFor(tc.sel, nil, nil, nil, nil, nil, nil).Names()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selector %q: cascade names got %v want %v", tc.sel, got, tc.want)
			}
		})
	}
}

// TestCascadeFor_TreeActiveSkipsFzf (7.1/R6): when tree-expand is active
// (multiple matches, at least one already-open herdr workspace), the
// cascade skips fzf entirely — even when [general].selector requests it —
// and uses the tree-aware tui_tree selector instead of tui, because fzf
// cannot render synthesized child rows.
func TestCascadeFor_TreeActiveSkipsFzf(t *testing.T) {
	t.Parallel()
	matches := []source.Candidate{
		{Source: config.SourceHerdr, Path: "/hw", Label: "open-ws", Meta: map[string]string{"workspace_id": "wA"}},
		{Source: config.SourceZoxide, Path: "/zx", Label: "zx"},
	}
	got := cascadeFor(config.SelectorFzf, nil, nil, nil, nil, nil, matches).Names()
	want := []string{"direct", "tui_tree"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tree-active cascade names = %v, want %v (fzf must be skipped)", got, want)
	}
}

// TestCascadeFor_SingleMatchIsNotTreeActive (7.1 triangulation): a single
// herdr match is never ambiguous (direct already short-circuits it before a
// cascade is even needed in practice), so tree-expand must not activate and
// the normal cascade — including fzf, if configured — still applies.
func TestCascadeFor_SingleMatchIsNotTreeActive(t *testing.T) {
	t.Parallel()
	matches := []source.Candidate{
		{Source: config.SourceHerdr, Path: "/hw", Label: "open-ws", Meta: map[string]string{"workspace_id": "wA"}},
	}
	got := cascadeFor(config.SelectorFzf, nil, nil, nil, nil, nil, matches).Names()
	want := []string{"direct", "fzf", "tui"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single-match cascade names = %v, want %v (tree-expand must not activate)", got, want)
	}
}

// TestOpen_DirectMatchBypassesSelector confirms a single exact match
// auto-opens via Direct without invoking the interactive cascade, for every
// selector value. The sentinel cascade raises a hard error if Select is ever
// called, so a non-nil error proves the interactive picker leaked through.
func TestOpen_DirectMatchBypassesSelector(t *testing.T) {
	t.Parallel()
	for _, sel := range []string{config.SelectorBuiltin, config.SelectorFzf, config.SelectorAuto} {
		cfg, root := seedCfg(t, "foo")
		cfg.General.Selector = sel
		foo := resolved(filepath.Join(root, "foo"))
		driver := &openDriver{detect: true, workspaceID: "wA"}
		sentinel := fakeSelector{err: errors.New("interactive selector must not run on a single match")}
		c := selector.New(sentinel)
		_, _, err := runOpen(t, cfg, driver, c, "foo")
		if err != nil {
			t.Fatalf("selector %q: open: %v", sel, err)
		}
		if driver.lastCand.NormalizedPath != foo {
			t.Errorf("selector %q: driver got %q want %q", sel, driver.lastCand.NormalizedPath, foo)
		}
	}
}

// TestOpen_ExactQueryInvokesDriver: a single exact match calls FocusOrCreate
// with the resolved candidate.
func TestOpen_ExactQueryInvokesDriver(t *testing.T) {
	cfg, root := seedCfg(t, "foo")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{detect: true, workspaceID: "wA"}
	_, _, err := runOpen(t, cfg, driver, nil, "foo")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if driver.lastCand.Path == "" {
		t.Fatal("driver was not invoked")
	}
	if driver.lastCand.NormalizedPath != foo {
		t.Errorf("driver candidate normalized = %q, want %q", driver.lastCand.NormalizedPath, foo)
	}
}

// TestOpen_NoMatchExitsOne: an unmatched query prints "no match" to stderr
// and exits 1.
func TestOpen_NoMatchExitsOne(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true}
	_, errOut, err := runOpen(t, cfg, driver, nil, "does-not-exist")
	if err == nil {
		t.Fatal("expected exit 1 on no match")
	}
	if !strings.Contains(errOut, "no match: does-not-exist") {
		t.Errorf("stderr = %q, want 'no match'", errOut)
	}
}

// TestOpen_HerdrAbsentPrintsPath: with no driver, open prints the resolved
// absolute path and exits 0.
func TestOpen_HerdrAbsentPrintsPath(t *testing.T) {
	cfg, root := seedCfg(t, "foo")
	foo := resolved(filepath.Join(root, "foo"))
	out, _, err := runOpen(t, cfg, nil, nil, "foo")
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if !strings.Contains(out, foo) {
		t.Errorf("stdout = %q, want it to contain %q", out, foo)
	}
}

// TestOpen_HerdrErrorFallsBackToPathPrint: a focus error warns to stderr and
// still prints the path, exit 0.
func TestOpen_HerdrErrorFallsBackToPathPrint(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true, focusErr: errors.New("daemon down")}
	out, errOut, err := runOpen(t, cfg, driver, nil, "foo")
	if err != nil {
		t.Fatalf("expected exit 0 on herdr error, got %v", err)
	}
	if !strings.Contains(errOut, "herdr unavailable") {
		t.Errorf("stderr = %q, want 'herdr unavailable'", errOut)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("expected path output on stdout, got empty")
	}
}

// TestOpen_MultipleCandidatesCascade: with multiple matches, the cascade's
// pick is forwarded to the driver.
func TestOpen_MultipleCandidatesCascade(t *testing.T) {
	cfg, root := seedCfg(t, "foo", "foobar")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{detect: true, workspaceID: "wA"}
	picked := source.Candidate{Path: foo, NormalizedPath: foo, Label: "foo", Source: "workspaces"}
	cascade := selector.New(fakeSelector{pick: picked, ok: true})
	_, _, err := runOpen(t, cfg, driver, cascade, "foo")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if driver.lastCand.Path != foo {
		t.Errorf("driver received %q, want %q", driver.lastCand.Path, foo)
	}
}

// TestOpen_AmbiguousNoSelectionPrintsCandidates: with multiple matches and a
// cascade that declines, open prints the candidates and exits 1.
func TestOpen_AmbiguousNoSelectionPrintsCandidates(t *testing.T) {
	cfg, _ := seedCfg(t, "foo", "foobar")
	driver := &openDriver{detect: true}
	cascade := selector.New(fakeSelector{ok: false})
	out, errOut, err := runOpen(t, cfg, driver, cascade, "foo")
	if err == nil {
		t.Fatal("expected exit 1 on ambiguous with no selection")
	}
	if !strings.Contains(errOut, "ambiguous") {
		t.Errorf("stderr = %q, want 'ambiguous'", errOut)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("expected candidates on stdout, got empty")
	}
}

// TestOpen_SelectorErrorFallsBackToAmbiguousList keeps non-interactive or
// otherwise unavailable selectors deterministic: show the candidate list and
// exit 1 instead of relying on a TTY-specific failure message only.
func TestOpen_SelectorErrorFallsBackToAmbiguousList(t *testing.T) {
	cfg, root := seedCfg(t, "foo", "foobar")
	foo := resolved(filepath.Join(root, "foo"))
	foobar := resolved(filepath.Join(root, "foobar"))
	driver := &openDriver{detect: true}
	cascade := selector.New(fakeSelector{err: errors.New("not a tty")})
	out, errOut, err := runOpen(t, cfg, driver, cascade, "foo")
	if err == nil {
		t.Fatal("expected exit 1 on selector error")
	}
	for _, want := range []string{foo + "\tfoo\n", foobar + "\tfoobar\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want candidate line %q", out, want)
		}
	}
	if !strings.Contains(errOut, "ambiguous") {
		t.Errorf("stderr = %q, want 'ambiguous'", errOut)
	}
	if !strings.Contains(errOut, "selector unavailable") {
		t.Errorf("stderr = %q, want selector unavailable warning", errOut)
	}
}

// TestOpen_CancelledSelectorExitsQuietly: when the selector cascade returns
// tui.ErrCancelled (the user pressed esc/ctrl+c/ctrl+g), open must exit 1
// without printing the candidate list or any ambiguous/selector-unavailable
// noise to stdout/stderr.
func TestOpen_CancelledSelectorExitsQuietly(t *testing.T) {
	cfg, _ := seedCfg(t, "foo", "foobar")
	driver := &openDriver{detect: true}
	cascade := selector.New(fakeSelector{err: tui.ErrCancelled})
	out, errOut, err := runOpen(t, cfg, driver, cascade, "foo")
	if err == nil {
		t.Fatal("expected exit 1 on cancellation")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want empty on cancellation", out)
	}
	if strings.TrimSpace(errOut) != "" {
		t.Errorf("stderr = %q, want empty on cancellation", errOut)
	}
}

// TestOpen_PathFlagBypassesResolution: --path opens the given path directly.
func TestOpen_PathFlagBypassesResolution(t *testing.T) {
	cfg, root := seedCfg(t, "foo")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{detect: true, workspaceID: "wA"}
	_, _, err := runOpen(t, cfg, driver, nil, "--path", foo)
	if err != nil {
		t.Fatalf("open --path: %v", err)
	}
	if driver.lastCand.Path != foo {
		t.Errorf("driver received %q, want %q", driver.lastCand.Path, foo)
	}
}

// TestOpen_PathFlagWithDot confirms `shep open --path .` (or `shep open .`
// via shell expansion) still works: cwd is not a picker source, but the
// direct path override always resolves.
func TestOpen_PathFlagWithDot(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true, workspaceID: "wA"}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runOpen(t, cfg, driver, nil, "--path", ".")
	if err != nil {
		t.Fatalf("open --path .: %v", err)
	}
	if driver.lastCand.Path != wd {
		t.Errorf("driver received %q, want cwd %q", driver.lastCand.Path, wd)
	}
}

// TestOpen_BareDotOpensCWD (requirement: cwd is not a picker source, but
// `shep open .` must still work) confirms a bare "." positional query opens
// the current directory directly, bypassing candidate resolution entirely
// (no source needs to produce a cwd candidate).
func TestOpen_BareDotOpensCWD(t *testing.T) {
	cfg := config.Defaults()
	cfg.General.Sources = nil // no sources at all would still resolve "."
	driver := &openDriver{detect: true, workspaceID: "wA"}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runOpen(t, cfg, driver, nil, ".")
	if err != nil {
		t.Fatalf("open .: %v", err)
	}
	if driver.lastCand.Path != wd {
		t.Errorf("driver received %q, want cwd %q", driver.lastCand.Path, wd)
	}
}

// TestOpen_PathFlagEmptyErrors: an empty --path reports an error, exit 1.
func TestOpen_PathFlagEmptyErrors(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true}
	_, errOut, err := runOpen(t, cfg, driver, nil, "--path", "")
	if err == nil {
		t.Fatal("expected exit 1 on empty --path")
	}
	if !strings.Contains(errOut, "resolve --path") {
		t.Errorf("stderr = %q, want 'resolve --path'", errOut)
	}
}

// TestOpen_MissingWorkspaceFailsCleanly (requirement 11): selecting a
// configured workspace whose path does not exist on disk fails clearly for
// that selection, never falling back to "/", $HOME, or cwd, and shep must
// not create the directory.
func TestOpen_MissingWorkspaceFailsCleanly(t *testing.T) {
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	missing := filepath.Join(t.TempDir(), "does-not-exist-yet")
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ghost", Path: missing}}
	driver := &openDriver{detect: true, workspaceID: "wA"}
	out, errOut, err := runOpen(t, cfg, driver, nil, "ghost")
	if err == nil {
		t.Fatal("expected exit 1 for a missing configured workspace")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want empty (no path fallback)", out)
	}
	if !strings.Contains(errOut, "does not exist") {
		t.Errorf("stderr = %q, want a clear 'does not exist' message", errOut)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("shep must never create the missing directory automatically")
	}
	if driver.lastCand.Path != "" {
		t.Error("herdr must not be invoked for a missing workspace")
	}
}

// TestOpen_PathFlagMissingFailsCleanly (requirement: direct --path flow)
// confirms a --path target that does not exist on disk fails clearly instead
// of a false success (printing the path) or a herdr-unavailable fallback:
// candidateFromPath must stat and mark it Missing so launch()'s existing
// Missing check rejects it.
func TestOpen_PathFlagMissingFailsCleanly(t *testing.T) {
	cfg := config.Defaults()
	missing := filepath.Join(t.TempDir(), "does-not-exist-yet")
	driver := &openDriver{detect: true, workspaceID: "wA"}
	out, errOut, err := runOpen(t, cfg, driver, nil, "--path", missing)
	if err == nil {
		t.Fatal("expected exit 1 for a missing --path target")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want empty (no false-success path print)", out)
	}
	if !strings.Contains(errOut, "does not exist") {
		t.Errorf("stderr = %q, want a clear 'does not exist' message", errOut)
	}
	if driver.lastCand.Path != "" {
		t.Error("herdr must not be invoked for a missing --path target")
	}
}

// TestResolveTemplate_Precedence exercises the full precedence chain: exact
// workspace template > exact workspace command > first matching wildcard's
// template > parent group template > defaults.template > empty.
func TestResolveTemplate_Precedence(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{
			"ws-tpl":     {Command: "ws"},
			"wc-tpl":     {Command: "wc"},
			"parent-tpl": {Command: "parent"},
			"def-tpl":    {Command: "def"},
		},
		Wildcards: []config.WildcardConfig{{Pattern: "foo", Template: "wc-tpl"}},
		Defaults:  config.DefaultsConfig{Template: "def-tpl"},
	}
	base := source.Candidate{Path: "/projects/foo", NormalizedPath: "/projects/foo"}

	t.Run("workspace template wins", func(t *testing.T) {
		t.Parallel()
		cand := base
		cand.Meta = map[string]string{"template": "ws-tpl", "parent_template": "parent-tpl"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "ws" {
			t.Errorf("got %+v want ws-tpl", got)
		}
	})

	t.Run("workspace command wins over wildcard", func(t *testing.T) {
		t.Parallel()
		cand := base
		cand.Meta = map[string]string{"command": "adhoc"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "adhoc" || len(got.Tabs) != 0 {
			t.Errorf("got %+v want ad-hoc command template", got)
		}
	})

	t.Run("workspace command forwards close_on_exit to synthetic template", func(t *testing.T) {
		t.Parallel()
		cand := base
		cand.Meta = map[string]string{"command": "nvim", "close_on_exit": "true"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "nvim" || !got.CloseOnExit {
			t.Errorf("got %+v want {Command: nvim, CloseOnExit: true}", got)
		}
	})

	t.Run("workspace command with close_on_exit false stays plain", func(t *testing.T) {
		t.Parallel()
		cand := base
		cand.Meta = map[string]string{"command": "nvim", "close_on_exit": "false"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "nvim" || got.CloseOnExit {
			t.Errorf("got %+v want {Command: nvim, CloseOnExit: false}", got)
		}
	})

	t.Run("workspace command close_on_exit strict true only rejects 1/T", func(t *testing.T) {
		t.Parallel()
		cand := base
		// The only writer (workspacesProvider.List) emits the literal "true";
		// resolveTemplate must treat any other value (here "1", which
		// strconv.ParseBool would otherwise accept) as false so a future
		// provider writing "1" cannot silently flip close-on-exit on.
		cand.Meta = map[string]string{"command": "nvim", "close_on_exit": "1"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "nvim" || got.CloseOnExit {
			t.Errorf("got %+v want {Command: nvim, CloseOnExit: false} (strict == %q contract)", got, "true")
		}
	})

	t.Run("wildcard wins over parent and defaults", func(t *testing.T) {
		t.Parallel()
		cand := base
		cand.Meta = map[string]string{"parent_template": "parent-tpl"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "wc" {
			t.Errorf("got %+v want wc-tpl", got)
		}
	})

	t.Run("parent group template wins over defaults", func(t *testing.T) {
		t.Parallel()
		cand := source.Candidate{Path: "/other/bar", NormalizedPath: "/other/bar"}
		cand.Meta = map[string]string{"parent_template": "parent-tpl"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "parent" {
			t.Errorf("got %+v want parent-tpl", got)
		}
	})

	t.Run("defaults.template is the final fallback", func(t *testing.T) {
		t.Parallel()
		cand := source.Candidate{Path: "/other/bar", NormalizedPath: "/other/bar"}
		got := resolveTemplate(cand, cfg)
		if got.Command != "def" {
			t.Errorf("got %+v want def-tpl", got)
		}
	})

	t.Run("no config yields empty template", func(t *testing.T) {
		t.Parallel()
		got := resolveTemplate(source.Candidate{Path: "/x"}, nil)
		if got.Command != "" || len(got.Tabs) != 0 {
			t.Errorf("got %+v want empty", got)
		}
	})
}

// TestMatchWildcardTemplate_MalformedPatternNoMatch confirms a bad glob in
// the config is treated as "no match" (never crashes open).
func TestMatchWildcardTemplate_MalformedPatternNoMatch(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Path: "/p/foo", NormalizedPath: "/p/foo"}
	cfg := &config.Config{Wildcards: []config.WildcardConfig{{Pattern: "[", Template: "boom"}}}
	if got := matchWildcardTemplate(cand, cfg); got != "" {
		t.Errorf("malformed pattern: got %q, want empty", got)
	}
}

// TestMatchWildcardTemplate_DoubleStarMatchesDescendant confirms the
// documented "~/projects/kubernetes/**" pattern resolves against a nested
// candidate path, matching runtime behaviour to the documented example.
func TestMatchWildcardTemplate_DoubleStarMatchesDescendant(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory")
	}
	nested := filepath.Join(home, "projects", "kubernetes", "myrepo")
	cand := source.Candidate{Path: nested, NormalizedPath: nested}
	cfg := &config.Config{Wildcards: []config.WildcardConfig{
		{Pattern: "~/projects/kubernetes/**", Template: "k8s"},
	}}
	if got := matchWildcardTemplate(cand, cfg); got != "k8s" {
		t.Errorf("got %q, want k8s for a descendant of the documented pattern", got)
	}
}

// runOpenTemplate wires an openDriver with a HerdrActionCreated response and
// returns it after `shep open --path <root>/foo` runs against cfg.
func runOpenTemplate(t *testing.T, cfg *config.Config) *openDriver {
	t.Helper()
	dir := t.TempDir()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	driver := &openDriver{detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1", lastAction: source.HerdrActionCreated}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return driver
}

// TestOpen_TemplateAppliesOnCreatedWorkspace (top-priority bug fix,
// end-to-end): a freshly created workspace applies [defaults].template,
// reusing the root tab rather than leaving it unused alongside a new one.
func TestOpen_TemplateAppliesOnCreatedWorkspace(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{{ID: "main", Command: "nvim"}}},
		},
	}
	driver := runOpenTemplate(t, cfg)
	if len(driver.created) != 0 {
		t.Errorf("first tab must reuse the root tab, got created=%v", driver.created)
	}
	if len(driver.renamed) != 1 || driver.renamed[0] != "rename:wA:t1:code" {
		t.Errorf("expected root tab renamed to code, got %v", driver.renamed)
	}
	if len(driver.ran) != 1 || driver.ran[0] != "run:wA:p1:nvim" {
		t.Errorf("expected nvim run in root pane, got %v", driver.ran)
	}
}

// TestOpen_WorkspaceCommandCloseOnExit_WrapsRootPane is the end-to-end wiring
// test for the whole chain this feature added: a [[workspaces]] entry with a
// top-level command + close_on_exit flows config -> workspacesProvider.List
// (which forwards Meta["close_on_exit"]="true") -> resolveTemplate (synthetic
// template) -> templates.Apply -> driver.RunPane receiving the shell-chained
// command. It proves no link in the chain silently drops the wrap.
func TestOpen_WorkspaceCommandCloseOnExit_WrapsRootPane(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{
		Name: "ops", Path: dir, Command: "k9s", CloseOnExit: true,
	}}
	driver := &openDriver{
		detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1",
		lastAction: source.HerdrActionCreated,
	}
	_, _, err := runOpen(t, cfg, driver, nil, "ops")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	want := "run:wA:p1:k9s; herdr pane close wA:p1"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected root pane to receive wrapped command %q, got %v", want, driver.ran)
	}
}

// TestOpen_TemplateSkippedOnFocusedWorkspace: focusing an existing workspace
// never applies a template (templates are a "freshly created" concept only).
func TestOpen_TemplateSkippedOnFocusedWorkspace(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{Command: "k9s"}
	cfg.General.Sources = []string{config.SourceWorkspaces}
	driver := &openDriver{detect: true, workspaceID: "wA", lastAction: source.HerdrActionFocused}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(driver.ran) != 0 {
		t.Errorf("expected no template application on focused workspace, got %v", driver.ran)
	}
}

// TestOpen_TemplateFailureIsWarningNotFatal: a failing template application
// only warns, never fails the whole open call.
func TestOpen_TemplateFailureIsWarningNotFatal(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{Command: "boom"}
	dir := t.TempDir()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	driver := &openDriver{
		detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1",
		lastAction: source.HerdrActionCreated, runErr: errors.New("boom"),
	}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.Contains(errOut.String(), "template failed") {
		t.Errorf("stderr = %q, want 'template failed'", errOut.String())
	}
}

// TestOpen_GroupWorkspaceDrillsIntoNestedPicker (group workspace precedence):
// selecting a type=group workspace re-enters resolution scoped to the
// group's own sources/path instead of launching the group entry itself.
func TestOpen_GroupWorkspaceDrillsIntoNestedPicker(t *testing.T) {
	root := t.TempDir()
	svc := filepath.Join(root, "svc")
	if err := os.MkdirAll(filepath.Join(svc, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "group", Type: config.WorkspaceTypeGroup, Path: root, Sources: []string{config.SourceProjects}},
	}
	driver := &openDriver{detect: true, workspaceID: "wA"}
	_, _, err := runOpen(t, cfg, driver, nil, "group")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	svcResolved := resolved(svc)
	if driver.lastCand.NormalizedPath != svcResolved {
		t.Errorf("driver got %q, want the drilled-down project %q", driver.lastCand.NormalizedPath, svcResolved)
	}
}

func runOpenStartupTemplate(t *testing.T, action source.HerdrAction) []string {
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Templates["default"] = config.TemplateConfig{Command: "echo hi"}
	dir := t.TempDir()
	driver := &openDriver{detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1", lastAction: action}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return driver.ran
}

// TestOpen_TemplateFiresOnCreated: a created workspace applies the resolved
// template's command in the root pane.
func TestOpen_TemplateFiresOnCreated(t *testing.T) {
	ran := runOpenStartupTemplate(t, source.HerdrActionCreated)
	if len(ran) != 1 || ran[0] != "run:wA:p1:echo hi" {
		t.Errorf("ran = %v, want a single 'echo hi' run in the root pane", ran)
	}
}

// TestOpen_TemplateSkippedOnFocused: focusing an existing workspace does not
// apply any template.
func TestOpen_TemplateSkippedOnFocused(t *testing.T) {
	ran := runOpenStartupTemplate(t, source.HerdrActionFocused)
	if len(ran) != 0 {
		t.Errorf("ran = %v, want none for focused workspace", ran)
	}
}

// TestLayoutFromConfig_ThreadsOrientationAndWidths (pure function) proves
// layoutFromConfig carries cfg.TUI.Layout into tui.Layout.Orientation
// alongside the existing ListWidth/PreviewWidth wiring, so `shep open`'s
// live ctrl+l toggle starts from the user's configured default orientation.
func TestLayoutFromConfig_ThreadsOrientationAndWidths(t *testing.T) {
	t.Parallel()
	got := layoutFromConfig(config.TUIConfig{ListWidth: "70%", PreviewWidth: "auto", Layout: config.TUILayoutLandscape}, nil)
	want := tui.Layout{ListWidth: "70%", PreviewWidth: "auto", Orientation: tui.LayoutLandscape}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("layoutFromConfig = %+v, want %+v", got, want)
	}
}

// TestLayoutFromConfig_ThreadsIcons proves layoutFromConfig carries
// cfg.TUI.Icons into tui.Layout.Icons verbatim, so the picker's configured
// icon fallback tier (Phase 8) reaches the resolved Model.
func TestLayoutFromConfig_ThreadsIcons(t *testing.T) {
	t.Parallel()
	got := layoutFromConfig(config.TUIConfig{Icons: config.TUIIconsASCII}, nil)
	want := tui.Layout{Icons: tui.IconsASCII}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("layoutFromConfig = %+v, want %+v", got, want)
	}
}

// TestLayoutFromConfig_ThreadsSourceOrder proves layoutFromConfig carries
// [general].sources into tui.Layout.SourceOrder verbatim, so the picker's
// row order matches the configured provider order instead of a hardcoded
// literal.
func TestLayoutFromConfig_ThreadsSourceOrder(t *testing.T) {
	t.Parallel()
	sources := []string{config.SourceProjects, config.SourceHerdr}
	got := layoutFromConfig(config.TUIConfig{}, sources)
	if !reflect.DeepEqual(got.SourceOrder, sources) {
		t.Errorf("layoutFromConfig SourceOrder = %v, want %v", got.SourceOrder, sources)
	}
}

// TestLayoutFromConfig_EmptyLayoutDefaultsToZeroOrientation triangulates the
// unset case: an empty cfg.TUI.Layout must produce a zero-value Orientation
// (landscape default), not an arbitrary string.
func TestLayoutFromConfig_EmptyLayoutDefaultsToZeroOrientation(t *testing.T) {
	t.Parallel()
	got := layoutFromConfig(config.TUIConfig{}, nil)
	if got.Orientation != "" {
		t.Errorf("layoutFromConfig empty layout: Orientation = %q, want empty", got.Orientation)
	}
}

// TestNewTUISelector_StoresRenderer: the tui selector built by
// cascadeFor/newTUISelector carries the injected Renderer through, so `shep
// open`'s Bubble Tea fallback gets the real preview.Renderer instead of
// silently defaulting to nil.
func TestNewTUISelector_StoresRenderer(t *testing.T) {
	r := fakePreviewRenderer{}
	s := newTUISelector(r, nil, nil, nil)
	if s.renderer == nil {
		t.Fatal("expected tuiSelector to carry a non-nil renderer")
	}
}

// TestTUISelector_Select_ForwardsChosenTargetToApp proves the TUI→App
// bridge end-to-end: when the (fake, real-Bubble-Tea-free) picker resolves a
// ctrl+t pick — a candidate plus target "tab" — tuiSelector.Select invokes
// onTarget with it, exactly as selectorFactory wires onTarget to
// App.setChosenTarget, so a.chosenTarget carries the override after Select
// returns.
func TestTUISelector_Select_ForwardsChosenTargetToApp(t *testing.T) {
	t.Parallel()
	app := New()
	sel := newTUISelector(nil, nil, app.setChosenTarget, app.setChosenAction)
	sel.run = func(_ context.Context, candidates []source.Candidate, _ string, _ preview.Renderer, _ *source.Pane, _ ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error) {
		return candidates[0], tui.RowActionOpen, "tab", true, nil
	}

	cand := source.Candidate{Path: "/x", Label: "x"}
	pick, ok, err := sel.Select(context.Background(), []source.Candidate{cand}, "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if !ok || pick.Label != "x" {
		t.Fatalf("Select() = %+v, ok=%v, want the candidate selected", pick, ok)
	}
	if app.chosenTarget != "tab" {
		t.Errorf("app.chosenTarget = %q, want %q after a simulated ctrl+t pick", app.chosenTarget, "tab")
	}
	if app.chosenAction != tui.RowActionOpen {
		t.Errorf("app.chosenAction = %v, want RowActionOpen forwarded from the pick", app.chosenAction)
	}
}

// TestTUISelector_Select_ForwardsChosenActionToApp proves the TUI→App action
// bridge: when the (fake) tree-aware picker resolves a RowActionFocusTab pick
// (Enter on a synthesized tab/pane row), tuiSelector.Select invokes onAction
// with it, exactly as selectorFactory wires onAction to App.setChosenAction,
// so a.chosenAction carries RowActionFocusTab after Select returns — the
// typed signal launch dispatches on instead of the candidate's Source string.
func TestTUISelector_Select_ForwardsChosenActionToApp(t *testing.T) {
	t.Parallel()
	app := New()
	sel := newTUISelector(nil, nil, app.setChosenTarget, app.setChosenAction)
	sel.run = func(_ context.Context, candidates []source.Candidate, _ string, _ preview.Renderer, _ *source.Pane, _ ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error) {
		return candidates[0], tui.RowActionFocusTab, "", true, nil
	}

	pick, ok, err := sel.Select(context.Background(), []source.Candidate{{Path: "/x", Label: "x"}}, "")
	if err != nil || !ok {
		t.Fatalf("Select() = ok=%v err=%v, want ok=true nil error", ok, err)
	}
	if pick.Label != "x" {
		t.Fatalf("Select() = %+v, want the candidate selected", pick)
	}
	if app.chosenAction != tui.RowActionFocusTab {
		t.Errorf("app.chosenAction = %v, want RowActionFocusTab forwarded from a synthesized-row pick", app.chosenAction)
	}
}

// TestTUISelector_Select_EnterLeavesChosenTargetEmpty proves a plain enter
// pick (target "") also reaches onTarget, so any earlier chosenTarget from a
// prior batch is cleared rather than left stale — a fake picker still ran
// and returned "" (the default), so App.chosenTarget must reflect that.
func TestTUISelector_Select_EnterLeavesChosenTargetEmpty(t *testing.T) {
	t.Parallel()
	app := New()
	app.chosenTarget = "pane" // simulate stale state from a prior invocation
	sel := newTUISelector(nil, nil, app.setChosenTarget, app.setChosenAction)
	sel.run = func(_ context.Context, candidates []source.Candidate, _ string, _ preview.Renderer, _ *source.Pane, _ ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error) {
		return candidates[0], tui.RowActionOpen, "", true, nil
	}

	if _, ok, err := sel.Select(context.Background(), []source.Candidate{{Path: "/x", Label: "x"}}, ""); err != nil || !ok {
		t.Fatalf("Select() = ok=%v, err=%v, want ok=true nil error", ok, err)
	}
	if app.chosenTarget != "" {
		t.Errorf("app.chosenTarget = %q, want empty after a plain enter pick", app.chosenTarget)
	}
}

// TestTUISelector_Select_CancelledNeverInvokesOnTarget proves a cancelled or
// unsuccessful pick (ok=false) never calls onTarget, so App.chosenTarget is
// left untouched instead of being overwritten with a meaningless target.
func TestTUISelector_Select_CancelledNeverInvokesOnTarget(t *testing.T) {
	t.Parallel()
	app := New()
	app.chosenTarget = "tab"
	sel := newTUISelector(nil, nil, app.setChosenTarget, app.setChosenAction)
	sel.run = func(_ context.Context, _ []source.Candidate, _ string, _ preview.Renderer, _ *source.Pane, _ ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error) {
		return source.Candidate{}, tui.RowActionOpen, "pane", false, tui.ErrCancelled
	}

	if _, ok, err := sel.Select(context.Background(), []source.Candidate{{Path: "/x", Label: "x"}}, ""); ok || !errors.Is(err, tui.ErrCancelled) {
		t.Fatalf("Select() = ok=%v, err=%v, want ok=false ErrCancelled", ok, err)
	}
	if app.chosenTarget != "tab" {
		t.Errorf("app.chosenTarget = %q, want unchanged %q after a cancelled pick", app.chosenTarget, "tab")
	}
}

// TestApp_BuildPreviewRenderer_ReturnsNonNil confirms the App wires a usable
// preview.Renderer from config + probes even with no [preview] customisation,
// so `shep open`'s TUI selector never falls back to a nil renderer silently.
func TestApp_BuildPreviewRenderer_ReturnsNonNil(t *testing.T) {
	app := New()
	app.cfg = config.Defaults()
	app.probes = config.Probes{}
	r := app.buildPreviewRenderer()
	if r == nil {
		t.Fatal("expected buildPreviewRenderer to return a non-nil Renderer")
	}
}

// recordingDriver is an open-scoped HerdrDriver that records the preview
// queries (ListTabs/ListPanes/ReadPane) so the CLI wiring test can prove the
// injected driver reaches the preview renderer.
type recordingDriver struct {
	tabsQueried  int
	panesQueried int
	readQueried  int
}

func (*recordingDriver) Detect(context.Context) bool { return true }
func (*recordingDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, nil
}
func (*recordingDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("not used")
}
func (*recordingDriver) ListAgents(context.Context) ([]source.Agent, error) { return nil, nil }
func (d *recordingDriver) ListTabs(_ context.Context, _ string) ([]source.Tab, error) {
	d.tabsQueried++
	return []source.Tab{{ID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 2}}, nil
}
func (d *recordingDriver) ListPanes(_ context.Context, _ string) ([]source.Pane, error) {
	d.panesQueried++
	return []source.Pane{{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true}}, nil
}
func (d *recordingDriver) ReadPane(_ context.Context, _ string, _ int) (string, error) {
	d.readQueried++
	return "$ echo hi", nil
}
func (*recordingDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("not used")
}
func (*recordingDriver) RenameTab(context.Context, string, string) error {
	return errors.New("not used")
}
func (*recordingDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("not used")
}
func (*recordingDriver) RunPane(context.Context, string, string) error { return errors.New("not used") }
func (*recordingDriver) FocusTab(context.Context, string) error        { return errors.New("not used") }
func (*recordingDriver) CurrentPane(context.Context) (source.Pane, error) {
	return source.Pane{}, nil
}

// TestApp_BuildPreviewRenderer_ThreadsHerdrDriver proves an injected
// HerdrDriver is wired into the preview renderer so workspace/active_pane
// sections render against it instead of being skipped.
func TestApp_BuildPreviewRenderer_ThreadsHerdrDriver(t *testing.T) {
	driver := &recordingDriver{}
	app := New(WithHerdrDriver(driver))
	app.cfg = config.Defaults()
	app.cfg.Preview.Default = []string{config.PreviewWorkspace, config.PreviewActivePane}
	app.probes = config.Probes{}
	r := app.buildPreviewRenderer()

	cand := source.Candidate{
		Path: "/x", Label: "foo", Source: "herdr",
		Meta: map[string]string{"workspace_id": "wA"},
	}
	res, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if driver.tabsQueried == 0 || driver.panesQueried == 0 || driver.readQueried == 0 {
		t.Errorf("driver not threaded into renderer: tabs=%d panes=%d read=%d",
			driver.tabsQueried, driver.panesQueried, driver.readQueried)
	}
	if !strings.Contains(res.Text, "edit") {
		t.Errorf("workspace section did not render tabs from driver: %q", res.Text)
	}
	if !strings.Contains(res.Text, "$ echo hi") {
		t.Errorf("active_pane section did not render buffer from driver: %q", res.Text)
	}
}

// TestOpenLayoutToggle_ConfigUnchangedAfterCtrlL is an integration test
// proving the FULL production chain end-to-end — a config.Config with a
// [tui] layout set, run through layoutFromConfig, feeding a constructed
// tui.Model, driven through the live ctrl+l toggle — never mutates the
// original config.Config/config.TUIConfig value the config was loaded into.
// model.go's cycleOrientationOverride only flips Model's own in-memory Layout
// copy (unit-tested in isolation by TestCtrlL_TogglesAutoLandscapeAuto
// and TestLayoutFromConfig_ThreadsOrientationAndWidths), but neither of those
// proves the two links actually compose correctly in the real wiring
// `shep open` uses; this test proves that "session-only, never persisted"
// guarantee holds across the whole chain, not just at each unit-tested link.
func TestOpenLayoutToggle_ConfigUnchangedAfterCtrlL(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.TUI = config.TUIConfig{ListWidth: "70%", PreviewWidth: "auto", Layout: config.TUILayoutLandscape}
	originalTUI := cfg.TUI

	layout := layoutFromConfig(cfg.TUI, cfg.General.Sources)
	cands := []source.Candidate{{Path: "/a", Label: "a"}}
	m := tui.NewModelWithLayout(cands, nil, layout)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model from Update, got %T", updated)
	}

	// Sanity: the toggle DID flip the model's own session-only orientation
	// (proving this test actually exercises the mutation path) — starting
	// from landscape, a single ctrl+l toggles back to auto (the stacked/
	// portrait third state was removed along with the stacked layout) —
	// while...
	toggled := layoutFromConfig(config.TUIConfig{Layout: ""}, nil)
	if mm.Layout().Orientation != toggled.Orientation {
		t.Fatalf("setup: expected ctrl+l to flip Model's orientation to auto, got %+v", mm.Layout())
	}
	// ...the original config.TUIConfig value stays completely unchanged.
	if cfg.TUI != originalTUI {
		t.Errorf("cfg.TUI mutated by ctrl+l toggle: got %+v, want unchanged %+v", cfg.TUI, originalTUI)
	}
}

// targetFlagValue builds `shep open`'s cobra command, parses the given flag
// args against it (without running the command body), and returns the resolved
// --target value. Used by the flag-parsing tests below so they exercise real
// cobra flag plumbing instead of poking struct fields directly.
func targetFlagValue(t *testing.T, parseArgs ...string) string {
	t.Helper()
	app := New()
	cmd := app.openCmd()
	if err := cmd.ParseFlags(parseArgs); err != nil {
		t.Fatalf("ParseFlags(%v): %v", parseArgs, err)
	}
	return cmd.Flags().Lookup("target").Value.String()
}

// TestOpenCmd_TargetFlag_Parses confirms `--target=tab` is accepted and stored.
func TestOpenCmd_TargetFlag_Parses(t *testing.T) {
	t.Parallel()
	if got := targetFlagValue(t, "--target", "tab"); got != "tab" {
		t.Errorf("--target tab = %q, want %q", got, "tab")
	}
	if got := targetFlagValue(t, "--target=pane"); got != "pane" {
		t.Errorf("--target=pane = %q, want %q", got, "pane")
	}
}

// TestOpenCmd_TargetFlag_DefaultsToWorkspace confirms the zero value of
// --target is "workspace", preserving the pre-flag launch behaviour.
func TestOpenCmd_TargetFlag_DefaultsToWorkspace(t *testing.T) {
	t.Parallel()
	if got := targetFlagValue(t); got != "workspace" {
		t.Errorf("default --target = %q, want %q", got, "workspace")
	}
}

// TestOpenCmd_TargetFlag_RejectsInvalid confirms an unknown --target value is
// rejected (via PreRunE validation) with a clear error rather than silently
// falling through to the workspace path.
func TestOpenCmd_TargetFlag_RejectsInvalid(t *testing.T) {
	t.Parallel()
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true, workspaceID: "wA"}
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "bogus", "foo")
	if err == nil {
		t.Fatal("expected an error for an invalid --target value")
	}
	if !strings.Contains(errOut, "invalid --target") {
		t.Errorf("stderr = %q, want it to mention 'invalid --target'", errOut)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("driver must not be invoked for an invalid --target; got candidate %q", driver.lastCand.Path)
	}
}

// commandWorkspaceCfg builds a config with a single Command-only [[workspaces]]
// entry (command + optional close_on_exit) rooted at a temp dir, so the
// target=tab/pane tests get a candidate whose Meta carries the command.
func commandWorkspaceCfg(t *testing.T, name, command string, closeOnExit bool) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{
		Name: name, Path: dir, Command: command, CloseOnExit: closeOnExit,
	}}
	return cfg, dir
}

// insidePaneDriver returns an openDriver configured to look like shep is
// running inside the given Herdr pane (CurrentPane succeeds). workspaceID is
// only used by the workspace-target path; the tab/pane paths read from
// currentPane instead.
func insidePaneDriver(pane source.Pane) *openDriver {
	return &openDriver{
		detect:      true,
		currentPane: pane,
	}
}

// TestOpen_TargetTab_OpensInCurrentWorkspace (end-to-end): a Command-only
// workspace entry opened with --target=tab creates a new tab in the workspace
// shep is running inside (focus=true so the new tab gets keyboard focus) and
// runs the command in that tab's root pane, wrapped with close_on_exit.
func TestOpen_TargetTab_OpensInCurrentWorkspace(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", true)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	_, _, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err != nil {
		t.Fatalf("open --target=tab: %v", err)
	}
	if len(driver.created) != 1 || driver.created[0] != "tab:wA:/cur:ops:focus" {
		t.Errorf("expected one focused CreateTab in the current workspace, got %v", driver.created)
	}
	want := "run:new-p:k9s; herdr pane close new-p"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected wrapped command %q in the new pane, got %v", want, driver.ran)
	}
	// The standalone-workspace path must NOT have run.
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called for target=tab; got candidate %q", driver.lastCand.Path)
	}
}

// TestOpen_TargetPane_OpensInCurrentWorkspace (end-to-end): --target=pane
// splits a new pane off the current one (right, 0.5, focus=true) and runs the
// wrapped command there.
func TestOpen_TargetPane_OpensInCurrentWorkspace(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", true)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	_, _, err := runOpen(t, cfg, driver, nil, "--target", "pane", "ops")
	if err != nil {
		t.Fatalf("open --target=pane: %v", err)
	}
	if len(driver.created) != 1 || driver.created[0] != "split:cur-p:right:0.5:/cur:focus" {
		t.Errorf("expected one focused right split of the current pane, got %v", driver.created)
	}
	want := "run:split-p:k9s; herdr pane close split-p"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected wrapped command %q in the split pane, got %v", want, driver.ran)
	}
}

// TestOpen_TargetTab_NoCurrentPane_Errors (end-to-end): --target=tab when
// shep is NOT running inside a Herdr pane (CurrentPane returns
// source.ErrNoFocusedPane) surfaces a clear, specific error and never reaches
// CreateTab/RunPane.
func TestOpen_TargetTab_NoCurrentPane_Errors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", true)
	driver := &openDriver{
		detect:         true,
		currentPaneErr: source.ErrNoFocusedPane,
	}
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err == nil {
		t.Fatal("expected an error when --target=tab has no current pane")
	}
	if !strings.Contains(errOut, "requires shep to be running inside a herdr workspace pane") {
		t.Errorf("stderr = %q, want the 'inside a herdr workspace pane' message", errOut)
	}
	if len(driver.created) != 0 || len(driver.ran) != 0 {
		t.Errorf("no tab/pane mutation must occur without a current pane; created=%v ran=%v", driver.created, driver.ran)
	}
}

// TestOpen_TargetTab_TemplateEntry_Errors (end-to-end): --target=tab on a
// workspace that declares a template (not a bare command) is rejected with a
// message naming the template, because tab/pane only support Command-only
// entries.
func TestOpen_TargetTab_TemplateEntry_Errors(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ops")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Templates["code"] = config.TemplateConfig{Command: "nvim"}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ops", Path: dir, Template: "code"}}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err == nil {
		t.Fatal("expected an error for --target=tab on a template entry")
	}
	if !strings.Contains(errOut, "only supports command-only entries") || !strings.Contains(errOut, "template") {
		t.Errorf("stderr = %q, want the command-only/template message", errOut)
	}
	if len(driver.created) != 0 || len(driver.ran) != 0 {
		t.Errorf("no tab/pane mutation must occur for a template entry; created=%v ran=%v", driver.created, driver.ran)
	}
}

// TestOpen_TargetTab_PlainPathHasNoCommand_Errors (end-to-end via --path):
// --target=tab on a plain path with no command/template/group is rejected,
// because there is no command to run (and no zoxide/projects path) in the
// new tab.
func TestOpen_TargetTab_PlainPathHasNoCommand_Errors(t *testing.T) {
	cfg := config.Defaults()
	dir := t.TempDir()
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "tab", "--path", dir)
	if err == nil {
		t.Fatal("expected an error for --target=tab on a plain path")
	}
	if !strings.Contains(errOut, "requires an entry with a command") || !strings.Contains(errOut, "or a zoxide/projects path") {
		t.Errorf("stderr = %q, want the broadened 'requires an entry with a command (or a zoxide/projects path)' message", errOut)
	}
	if len(driver.created) != 0 || len(driver.ran) != 0 {
		t.Errorf("no tab/pane mutation must occur for a plain path; created=%v ran=%v", driver.created, driver.ran)
	}
}

// runLaunchDirect calls App.launch directly (bypassing runOpen's candidate
// resolution) so a target-validation test can feed a crafted candidate the
// normal resolution flow would intercept — e.g. a group workspace, which
// resolveFromRegistry always drills into and therefore never forwards to
// launch. It returns stderr and the launch error.
func runLaunchDirect(t *testing.T, cand source.Candidate, action tui.RowAction, target string, currentPane *source.Pane, driver *openDriver) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = config.Defaults()
	app.probes = config.Probes{Herdr: true, Git: true}
	err := app.launch(context.Background(), cand, action, target, currentPane, &out, &errOut)
	return errOut.String(), err
}

// TestLaunch_TargetTab_GroupWorkspace_Errors (direct launch): a group
// workspace candidate (Meta["group"]="true") is rejected for --target=tab.
// This shape is unreachable through `shep open` (resolveFromRegistry drills
// into groups before launch ever sees them), so the guard is exercised here
// against launch directly to keep the disallowTarget branch covered.
func TestLaunch_TargetTab_GroupWorkspace_Errors(t *testing.T) {
	cand := source.Candidate{Path: "/g", Label: "groupentry", Meta: map[string]string{"group": "true"}}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "tab", &pane, driver)
	if err == nil {
		t.Fatal("expected an error for --target=tab on a group workspace")
	}
	if !strings.Contains(errOut, "requires an entry with a command") || !strings.Contains(errOut, "group workspace") {
		t.Errorf("stderr = %q, want the group-workspace message", errOut)
	}
	if len(driver.created) != 0 || len(driver.ran) != 0 {
		t.Errorf("no tab/pane mutation must occur for a group entry; created=%v ran=%v", driver.created, driver.ran)
	}
}

// TestRunOpen_CurrentPaneTimeout confirms runOpen bounds its CurrentPane
// probe with a short timeout: a hung Herdr daemon (here, a CurrentPane stub
// that sleeps 5s) must not block the whole invocation — runOpen degrades
// gracefully (currentPane stays nil) and returns well within the 2s budget
// plus test slack.
func TestRunOpen_CurrentPaneTimeout(t *testing.T) {
	t.Parallel()
	cfg, _ := seedCfg(t, "foo")
	driver := &openDriver{detect: true, currentPaneDelay: 5 * time.Second}
	start := time.Now()
	_, _, err := runOpen(t, cfg, driver, nil, "foo")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runOpen: %v", err)
	}
	if !driver.currentCalled {
		t.Fatal("expected CurrentPane to have been called")
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("runOpen took %v, want it to return within ~2.5s despite a hung CurrentPane", elapsed)
	}
}

// TestLaunch_TargetPane_NoCurrentPane_Errors triangulates the pane target
// against the same no-current-pane guard the tab target uses, confirming both
// targets share the precondition.
func TestLaunch_TargetPane_NoCurrentPane_Errors(t *testing.T) {
	cand := source.Candidate{Path: "/x", Label: "ops", Meta: map[string]string{"command": "k9s"}}
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "pane", nil, &openDriver{detect: true})
	if err == nil {
		t.Fatal("expected an error for --target=pane with no current pane")
	}
	if !strings.Contains(errOut, "requires shep to be running inside a herdr workspace pane") {
		t.Errorf("stderr = %q, want the inside-a-pane message", errOut)
	}
}

// TestOpen_TargetTab_ApplyFailureRollsBackAndErrors (end-to-end): when
// templates.Apply fails after --target=tab has already created the new tab
// (RunPane's Apply-internal call errors), launchInCurrentWorkspace must
// best-effort close the now-ghost pane/tab and surface a real error (exit 1)
// instead of silently returning nil.
func TestOpen_TargetTab_ApplyFailureRollsBackAndErrors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	driver.runErrOnFirstCall = errors.New("boom")
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err == nil {
		t.Fatal("expected an error when Apply fails after tab creation")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "warning: launch failed") {
		t.Errorf("stderr = %q, want it to mention launch failed", errOut)
	}
	// Two RunPane calls expected: the failed Apply-internal run, then the
	// best-effort close attempt on the ghost pane.
	if len(driver.ran) != 2 {
		t.Fatalf("expected 2 RunPane calls (failed run + rollback close), got %v", driver.ran)
	}
	wantClose := "run:new-p:herdr pane close new-p"
	if driver.ran[1] != wantClose {
		t.Errorf("expected rollback close %q, got %q", wantClose, driver.ran[1])
	}
}

// TestOpen_TargetPane_ApplyFailureRollsBackAndErrors mirrors the tab test for
// --target=pane, confirming the rollback close targets the split pane.
func TestOpen_TargetPane_ApplyFailureRollsBackAndErrors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	driver.runErrOnFirstCall = errors.New("boom")
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "pane", "ops")
	if err == nil {
		t.Fatal("expected an error when Apply fails after pane split")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "warning: launch failed") {
		t.Errorf("stderr = %q, want it to mention launch failed", errOut)
	}
	if len(driver.ran) != 2 {
		t.Fatalf("expected 2 RunPane calls (failed run + rollback close), got %v", driver.ran)
	}
	wantClose := "run:split-p:herdr pane close split-p"
	if driver.ran[1] != wantClose {
		t.Errorf("expected rollback close %q, got %q", wantClose, driver.ran[1])
	}
}

// TestOpen_TargetTab_CreateTabFails_Errors (R3-003 regression): a
// driver.CreateTab failure must surface a non-nil errExitOne so the process
// exits 1, instead of silently swallowing the error and exiting 0. No
// RunPane/Apply call must follow a CreateTab failure since there is no
// container tab/pane to apply the template into.
func TestOpen_TargetTab_CreateTabFails_Errors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	driver.createTabErr = errors.New("herdr daemon unreachable")
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err == nil {
		t.Fatal("expected an error when CreateTab fails")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "warning: herdr tab create failed") {
		t.Errorf("stderr = %q, want it to mention tab create failed", errOut)
	}
	if len(driver.ran) != 0 {
		t.Errorf("no RunPane call must occur after a CreateTab failure, got %v", driver.ran)
	}
}

// TestOpen_TargetPane_SplitPaneFails_Errors mirrors the tab test for
// --target=pane, confirming a driver.SplitPane failure also surfaces
// errExitOne instead of a silent nil (R3-003 regression).
func TestOpen_TargetPane_SplitPaneFails_Errors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	driver.splitPaneErr = errors.New("herdr daemon unreachable")
	_, errOut, err := runOpen(t, cfg, driver, nil, "--target", "pane", "ops")
	if err == nil {
		t.Fatal("expected an error when SplitPane fails")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "warning: herdr pane split failed") {
		t.Errorf("stderr = %q, want it to mention pane split failed", errOut)
	}
	if len(driver.ran) != 0 {
		t.Errorf("no RunPane call must occur after a SplitPane failure, got %v", driver.ran)
	}
}

// TestOpen_TargetTab_HerdrCandidate_Errors (R4): an already-open herdr
// workspace candidate is rejected for --target=tab with an "already open"
// message and never reaches CreateTab/SplitPane — it must be resumed via
// --target=workspace, not opened-new inside the current workspace.
func TestOpen_TargetTab_HerdrCandidate_Errors(t *testing.T) {
	cand := source.Candidate{Source: config.SourceHerdr, Path: "/hw", Label: "open-ws", Meta: map[string]string{"workspace_id": "wA"}}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "tab", &pane, driver)
	if err == nil {
		t.Fatal("expected an error for --target=tab on an already-open herdr workspace")
	}
	if !strings.Contains(errOut, "already open") {
		t.Errorf("stderr = %q, want an 'already open' message", errOut)
	}
	if len(driver.created) != 0 || len(driver.ran) != 0 {
		t.Errorf("no tab/pane mutation must occur for an already-open herdr workspace; created=%v ran=%v", driver.created, driver.ran)
	}
}

// TestOpen_TargetTab_ZoxideCandidate_Opens (R4): a zoxide candidate (no
// command) opened with --target=tab creates a new tab in the current
// workspace as a plain shell — disallowTarget allows it, and the empty
// command means no RunPane fires.
func TestOpen_TargetTab_ZoxideCandidate_Opens(t *testing.T) {
	cand := source.Candidate{Source: config.SourceZoxide, Path: "/zx", Label: "zx-proj"}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "tab", &pane, driver)
	if err != nil {
		t.Fatalf("launch --target=tab zoxide: %v (stderr=%q)", err, errOut)
	}
	if len(driver.created) != 1 || driver.created[0] != "tab:wA:/cur:zx-proj:focus" {
		t.Errorf("expected one focused CreateTab for the zoxide candidate, got %v", driver.created)
	}
	if len(driver.ran) != 0 {
		t.Errorf("a zoxide candidate has no command; expected no RunPane, got %v", driver.ran)
	}
}

// TestOpen_TargetPane_ProjectsCandidate_Opens (R4): a projects candidate (no
// command) opened with --target=pane splits a new pane in the current
// workspace as a plain shell.
func TestOpen_TargetPane_ProjectsCandidate_Opens(t *testing.T) {
	cand := source.Candidate{Source: config.SourceProjects, Path: "/proj", Label: "proj"}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "pane", &pane, driver)
	if err != nil {
		t.Fatalf("launch --target=pane projects: %v (stderr=%q)", err, errOut)
	}
	if len(driver.created) != 1 || driver.created[0] != "split:cur-p:right:0.5:/cur:focus" {
		t.Errorf("expected one focused right split for the projects candidate, got %v", driver.created)
	}
	if len(driver.ran) != 0 {
		t.Errorf("a projects candidate has no command; expected no RunPane, got %v", driver.ran)
	}
}

// --- launchChildTab (typed RowAction dispatch) ---

// TestOpen_LaunchFocusTabActionRoutesToFocusTab: a synthesized child row
// (RowActionFocusTab) routes Enter to driver.FocusTab with its Meta["tab_id"],
// bypassing FocusOrCreate entirely — the child row identifies an already-open
// tab inside an already-open workspace, so there is nothing to focus-or-create
// at the workspace level. The dispatch is the TUI-owned typed RowAction, not
// the candidate's Source string (the child carries no Source).
func TestOpen_LaunchFocusTabActionRoutesToFocusTab(t *testing.T) {
	cand := source.Candidate{
		Label: "api",
		Path:  "/svc/api",
		Meta:  map[string]string{"workspace_id": "wA", "tab_id": "t1"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionFocusTab, "workspace", nil, driver)
	if err != nil {
		t.Fatalf("launch child tab: %v (stderr=%q)", err, errOut)
	}
	if len(driver.focused) != 1 || driver.focused[0] != "focus-tab:t1" {
		t.Errorf("expected FocusTab(t1), got %v", driver.focused)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called for a focus-tab action; got candidate %+v", driver.lastCand)
	}
}

// TestOpen_LaunchFocusTabAction_RoutesOnActionNotSource triangulates the
// dispatch contract: a candidate that LOOKS like a normal zoxide entry (a real
// provider Source) still routes to FocusTab when the typed action is
// RowActionFocusTab. This proves launch decides on the RowAction, never on the
// candidate's Source string — the whole point of moving tree-only launch
// semantics out of the config.SourceHerdrTab/SourceHerdrPane constants.
func TestOpen_LaunchFocusTabAction_RoutesOnActionNotSource(t *testing.T) {
	cand := source.Candidate{
		Source: config.SourceZoxide,
		Label:  "looks-like-zoxide",
		Path:   "/svc/api",
		Meta:   map[string]string{"tab_id": "t9"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionFocusTab, "tab", &source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"}, driver)
	if err != nil {
		t.Fatalf("launch focus-tab action: %v (stderr=%q)", err, errOut)
	}
	if len(driver.focused) != 1 || driver.focused[0] != "focus-tab:t9" {
		t.Errorf("expected FocusTab(t9) despite the zoxide Source, got %v", driver.focused)
	}
	if len(driver.created) != 0 {
		t.Errorf("CreateTab must not be called for a focus-tab action even with target=tab; got %v", driver.created)
	}
}

// TestOpen_LaunchFocusTabAction_PaneRowFocusesContainingTab: a pane row
// (RowActionFocusTab with a pane_id) ALSO routes Enter to driver.FocusTab with
// its Meta["tab_id"] — Herdr has no per-pane focus command (see
// internal/herdr.Driver.FocusTab's own doc comment), so focusing the
// containing tab is the safest truthful action for a pane row, and it reuses
// launchChildTab unchanged (only Meta["tab_id"] is read).
func TestOpen_LaunchFocusTabAction_PaneRowFocusesContainingTab(t *testing.T) {
	cand := source.Candidate{
		Label: "p1",
		Path:  "/svc/api",
		Meta:  map[string]string{"workspace_id": "wA", "tab_id": "t1", "pane_id": "p1"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionFocusTab, "workspace", nil, driver)
	if err != nil {
		t.Fatalf("launch child pane: %v (stderr=%q)", err, errOut)
	}
	if len(driver.focused) != 1 || driver.focused[0] != "focus-tab:t1" {
		t.Errorf("expected FocusTab(t1) for a pane row, got %v", driver.focused)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called for a focus-tab action; got candidate %+v", driver.lastCand)
	}
}

// TestOpen_LaunchHerdrWorkspaceStillFocusOrCreate (regression): a normal
// SourceHerdr (parent workspace) candidate selected with RowActionOpen still
// goes through the unchanged FocusOrCreate path — this proves the
// RowActionFocusTab branch in App.launch does not shadow the existing herdr
// workspace contract (shep-resolver-resume-vs-new).
func TestOpen_LaunchHerdrWorkspaceStillFocusOrCreate(t *testing.T) {
	cand := source.Candidate{Source: config.SourceHerdr, Path: "/hw", Label: "open-ws", Meta: map[string]string{"workspace_id": "wA"}}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "workspace", nil, driver)
	if err != nil {
		t.Fatalf("launch parent workspace: %v (stderr=%q)", err, errOut)
	}
	if driver.lastCand.Path != cand.Path {
		t.Errorf("expected FocusOrCreate to be called with the parent candidate, got %+v", driver.lastCand)
	}
	if len(driver.focused) != 0 {
		t.Errorf("FocusTab must not be called for a parent workspace candidate; got %v", driver.focused)
	}
}

// TestOpen_LaunchFocusTabAction_MissingTabIDErrors (triangulation): a
// focus-tab action whose candidate is missing Meta["tab_id"] (should not
// happen in practice, but launchChildTab must not blindly call FocusTab(""))
// surfaces a clear error and exit code 1 instead of calling the driver with an
// empty id.
func TestOpen_LaunchFocusTabAction_MissingTabIDErrors(t *testing.T) {
	cand := source.Candidate{
		Label: "api",
		Path:  "/svc/api",
		Meta:  map[string]string{"workspace_id": "wA"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionFocusTab, "workspace", nil, driver)
	if err == nil {
		t.Fatal("expected an error for a focus-tab action with no tab_id")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if len(driver.focused) != 0 {
		t.Errorf("FocusTab must not be called without a tab_id; got %v", driver.focused)
	}
	if errOut == "" {
		t.Error("expected a warning written to stderr")
	}
}

// TestOpen_LaunchFocusTabAction_FocusTabErrorSurfacesExitOne (triangulation):
// a driver.FocusTab failure surfaces a warning and errExitOne — no resource
// was created, so there is nothing to roll back.
func TestOpen_LaunchFocusTabAction_FocusTabErrorSurfacesExitOne(t *testing.T) {
	cand := source.Candidate{
		Label: "api",
		Path:  "/svc/api",
		Meta:  map[string]string{"workspace_id": "wA", "tab_id": "t1"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	driver.focusTabErr = errors.New("herdr tab focus: boom")
	errOut, err := runLaunchDirect(t, cand, tui.RowActionFocusTab, "workspace", nil, driver)
	if err == nil {
		t.Fatal("expected an error when driver.FocusTab fails")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "herdr tab focus") {
		t.Errorf("stderr = %q, want it to mention the FocusTab failure", errOut)
	}
}
