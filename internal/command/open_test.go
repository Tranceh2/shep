package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

	renamed []string
	ran     []string
	created []string
	focused []string
	runErr  error
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
func (d *openDriver) CreateTab(_ context.Context, workspaceID, cwd, label string, _ bool) (source.Tab, source.Pane, error) {
	d.created = append(d.created, "tab:"+workspaceID+":"+cwd+":"+label)
	return source.Tab{ID: "new-t"}, source.Pane{ID: "new-p"}, nil
}
func (d *openDriver) RenameTab(_ context.Context, tabID, label string) error {
	d.renamed = append(d.renamed, "rename:"+tabID+":"+label)
	return nil
}
func (d *openDriver) SplitPane(_ context.Context, paneID, direction string, ratio float64, cwd string, _ bool) (source.Pane, error) {
	return source.Pane{ID: "split-p"}, nil
}
func (d *openDriver) RunPane(_ context.Context, paneID, command string) error {
	d.ran = append(d.ran, "run:"+paneID+":"+command)
	return d.runErr
}
func (d *openDriver) FocusTab(_ context.Context, tabID string) error {
	d.focused = append(d.focused, "focus-tab:"+tabID)
	return nil
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
			got := cascadeFor(tc.sel, nil).Names()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selector %q: cascade names got %v want %v", tc.sel, got, tc.want)
			}
		})
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

// TestNewTUISelector_StoresRenderer: the tui selector built by
// cascadeFor/newTUISelector carries the injected Renderer through, so `shep
// open`'s Bubble Tea fallback gets the real preview.Renderer instead of
// silently defaulting to nil.
func TestNewTUISelector_StoresRenderer(t *testing.T) {
	r := fakePreviewRenderer{}
	s := newTUISelector(r)
	if s.renderer == nil {
		t.Fatal("expected tuiSelector to carry a non-nil renderer")
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
