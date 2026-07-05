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

func (fakePreviewRenderer) Render(context.Context, source.Candidate, preview.RenderOptions) (preview.Result, error) {
	return preview.Result{}, nil
}

// --- mocks ---

// openDriver is an open-scoped HerdrDriver mock capturing the last candidate
// and responding with scripted FocusOrCreate / RunStartup / Detect results.
type openDriver struct {
	detect      bool
	focusErr    error
	runErr      error
	lastCand    source.Candidate
	lastStartup string
	lastAction  source.HerdrAction
	workspaceID string
	listErr     error
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
	return source.FocusResult{WorkspaceID: d.workspaceID, Action: action}, nil
}
func (d *openDriver) RunStartup(_ context.Context, workspaceID, command string) error {
	d.lastStartup = command
	d.workspaceID = workspaceID
	return d.runErr
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

// seedCfg creates a temp root with one subdir per name and returns a Config
// whose "seed" roots source scans it. Tests compare against resolved paths
// through resolved() so macOS /var -> /private/var symlinks don't flake.
func seedCfg(t *testing.T, names ...string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
	cfg := config.Defaults()
	cfg.Sources["seed"] = config.Source{
		Kind: config.KindRoots, Enabled: true,
		Options: map[string]string{"path": root},
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

// TestCascadeFor_RoutesBySelector (PL-9) verifies the selector cascade is
// built from [general].selector: builtin skips fzf, fzf and auto include
// fzf, and an unknown/empty value falls back to the builtin shape. Direct
// always runs first regardless of selector value.
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

// TestOpen_DirectMatchBypassesSelector (PL-9) confirms a single exact match
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
		// Sentinel: errors if Select runs, proving Direct short-circuits.
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

// TestOpen_ExactQueryInvokesDriver (S2): a single exact match calls
// FocusOrCreate with the resolved candidate.
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

// TestOpen_NoMatchExitsOne (S3): an unmatched query prints "no match" to
// stderr and exits 1.
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

// TestOpen_HerdrAbsentPrintsPath (HI-5): with no driver, open prints the
// resolved absolute path and exits 0.
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

// TestOpen_HerdrErrorFallsBackToPathPrint (HI-6): a focus error warns to
// stderr and still prints the path, exit 0.
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

// TestOpen_MultipleCandidatesCascade (PL-2): with multiple matches, the
// cascade's pick is forwarded to the driver.
func TestOpen_MultipleCandidatesCascade(t *testing.T) {
	cfg, root := seedCfg(t, "foo", "foobar")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{detect: true, workspaceID: "wA"}
	picked := source.Candidate{Path: foo, NormalizedPath: foo, Label: "foo", Source: "seed"}
	cascade := selector.New(fakeSelector{pick: picked, ok: true})
	_, _, err := runOpen(t, cfg, driver, cascade, "foo")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if driver.lastCand.Path != foo {
		t.Errorf("driver received %q, want %q", driver.lastCand.Path, foo)
	}
}

// TestOpen_AmbiguousNoSelectionPrintsCandidates (PL-8): with multiple matches
// and a cascade that declines, open prints the candidates and exits 1.
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
	// root is outside $HOME here, so the roots provider's home-relative
	// Label is the raw (pre-normalization) path unchanged, not the bare
	// directory name. The path column uses the normalised/resolved path.
	for _, want := range []string{
		foo + "\t" + filepath.Join(root, "foo") + "\n",
		foobar + "\t" + filepath.Join(root, "foobar") + "\n",
	} {
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
// noise to stdout/stderr — cancelling is a normal, quiet outcome, not an
// error to report.
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

// startupCfg builds a config whose roots scan a root containing "foo" and
// attaches a layout matching "foo" with the given startup command.
func startupCfg(t *testing.T, startup string) (*config.Config, string) {
	cfg, root := seedCfg(t, "foo")
	cfg.Layouts["foo"] = config.Layout{Startup: startup}
	return cfg, root
}

func runOpenStartup(t *testing.T, action source.HerdrAction) (string, string) {
	cfg, root := startupCfg(t, "echo hi")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{detect: true, workspaceID: "wA", lastAction: action}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", foo})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return driver.lastStartup, errOut.String()
}

// TestOpen_RunStartupFiresOnCreated: a created workspace runs the matched
// layout's startup command (HI-4).
func TestOpen_RunStartupFiresOnCreated(t *testing.T) {
	last, _ := runOpenStartup(t, source.HerdrActionCreated)
	if last != "echo hi" {
		t.Errorf("startup = %q, want 'echo hi'", last)
	}
}

// TestOpen_RunStartupSkippedOnFocused: focusing an existing workspace does
// not run the startup command (HI-4: startup is for created workspaces).
func TestOpen_RunStartupSkippedOnFocused(t *testing.T) {
	last, _ := runOpenStartup(t, source.HerdrActionFocused)
	if last != "" {
		t.Errorf("startup = %q, want empty for focused workspace", last)
	}
}

// TestOpen_RunStartupErrorIsWarningNotFatal: a failing startup only warns.
func TestOpen_RunStartupErrorIsWarningNotFatal(t *testing.T) {
	cfg, root := startupCfg(t, "echo hi")
	foo := resolved(filepath.Join(root, "foo"))
	driver := &openDriver{
		detect: true, workspaceID: "wA", lastAction: source.HerdrActionCreated,
		runErr: errors.New("boom"),
	}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", foo})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.Contains(errOut.String(), "startup failed") {
		t.Errorf("stderr = %q, want 'startup failed'", errOut.String())
	}
}

// TestNewTUISelector_StoresRenderer (PL-11 wiring): the tui selector built by
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
