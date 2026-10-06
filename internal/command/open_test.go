package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
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
	detect            bool
	focusErr          error
	lastCand          source.Candidate
	lastWorkspaceName string
	lastAction        source.HerdrAction
	workspaceID       string
	rootTabID         string
	rootPaneID        string
	snapshot          source.Snapshot
	snapshotErr       error
	snapshotCalls     int
	snapshotFn        func(context.Context) (source.Snapshot, error)
	sessions          []source.Session
	sessionsErr       error
	sessionsCalls     int

	renamed   []string
	paneCalls []string // "rename-pane:<pane>:<label>"
	ran       []string
	created   []string // "tab:<ws>:<cwd>:<label>:<focus>" / "split:<pane>:<dir>:<ratio>:<cwd>:<focus>"
	// layouts records atomic layout.apply dispatches as
	// "layout:<tab_id>:<tab_label>:<focus>".
	layouts        []string
	argv           [][]string
	applyLayoutErr error
	focused        []string
	focusTabErr    error
	createTabErr   error
	splitPaneErr   error
	runErr         error
	// runErrOnFirstCall, when non-nil, is returned only for the first RunPane
	// call (the Apply-internal run); every subsequent call succeeds
	// regardless of runErr. Used to script an Apply failure followed by a
	// successful best-effort rollback close.
	runErrOnFirstCall error
	runCallCount      int
}

func (d *openDriver) Detect(context.Context) bool { return d.detect }
func (d *openDriver) Snapshot(ctx context.Context) (source.Snapshot, error) {
	d.snapshotCalls++
	if d.snapshotFn != nil {
		return d.snapshotFn(ctx)
	}
	return d.snapshot, d.snapshotErr
}
func (d *openDriver) ListSessions(context.Context) ([]source.Session, error) {
	d.sessionsCalls++
	return append([]source.Session(nil), d.sessions...), d.sessionsErr
}
func (d *openDriver) FocusOrCreate(_ context.Context, request source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	d.lastCand = request.Candidate
	d.lastWorkspaceName = string(request.WorkspaceName)
	if d.focusErr != nil {
		return source.FocusResult{}, d.focusErr
	}
	action := d.lastAction
	if action == 0 {
		action = source.HerdrActionFocused
	}
	return source.FocusResult{WorkspaceID: d.workspaceID, Action: action, RootTabID: d.rootTabID, RootPaneID: d.rootPaneID}, nil
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
func (d *openDriver) RenamePane(_ context.Context, paneID string, label *string) error {
	value := "<nil>"
	if label != nil {
		value = *label
	}
	d.paneCalls = append(d.paneCalls, "rename-pane:"+paneID+":"+value)
	return errors.New("openDriver RenamePane unexpected")
}
func (d *openDriver) SplitPane(_ context.Context, paneID, direction string, ratio float64, cwd string, focus bool) (source.Pane, error) {
	if d.splitPaneErr != nil {
		return source.Pane{}, d.splitPaneErr
	}
	d.created = append(d.created, "split:"+paneID+":"+direction+":"+strconv.FormatFloat(ratio, 'f', -1, 64)+":"+cwd+":"+openFocusStr(focus))
	return source.Pane{ID: "split-p"}, nil
}
func (d *openDriver) RunPane(ctx context.Context, paneID, command string) error {
	d.ran = append(d.ran, "run:"+paneID+":"+command)
	if strings.Contains(command, "pane close") && ctx.Err() != nil {
		return ctx.Err()
	}
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

// ApplyLayout makes openDriver stand in for the layout.apply socket boundary
// as well as the CLI one, so a test that injects a driver has NO live Herdr
// dependency left anywhere in the launch path. It records each dispatched tab
// as "layout:<tab_id>:<tab_label>:<focus>" plus every pane command the tree
// carries, which is what lets the existing command-level assertions keep
// describing observable behaviour after the dispatcher moved to the socket.
func (d *openDriver) ApplyLayout(_ context.Context, _ string, params herdr.LayoutApplyParams) (*herdr.LayoutApplyResult, error) {
	d.layouts = append(d.layouts,
		"layout:"+params.TabID+":"+params.TabLabel+":"+openFocusStr(params.Focus))
	collectPaneArgv(&params.Root, &d.argv)
	collectPaneCommands(&params.Root, &d.ran)
	if d.applyLayoutErr != nil {
		return nil, d.applyLayoutErr
	}
	return &herdr.LayoutApplyResult{WorkspaceID: params.WorkspaceID, TabID: params.TabID}, nil
}

func collectPaneArgv(n *herdr.LayoutNode, out *[][]string) {
	if n == nil {
		return
	}
	if n.Type == herdr.NodeTypePane {
		if len(n.Command) > 0 {
			*out = append(*out, append([]string(nil), n.Command...))
		}
		return
	}
	collectPaneArgv(n.First, out)
	collectPaneArgv(n.Second, out)
}

// collectPaneCommands appends every non-empty pane command in a dispatched
// layout tree, in left-to-right (declaration) order, as
// "run:<pane_id>:<command>". The pane's own generated id is used because
// under layout.apply a pane's command and its id are decided together in one
// atomic request; there is no prior pane to address.
func collectPaneCommands(n *herdr.LayoutNode, out *[]string) {
	if n == nil {
		return
	}
	if n.Type == herdr.NodeTypePane {
		if len(n.Command) > 0 {
			*out = append(*out, "run:"+n.PaneID+":"+n.Command[len(n.Command)-1])
		}
		return
	}
	collectPaneCommands(n.First, out)
	collectPaneCommands(n.Second, out)
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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
		app.layoutApplier = driver
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
func TestOpen_ResolvesWorktreeWorkspaceName(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{
		Path:           "/trees/shep-feature",
		NormalizedPath: "/trees/shep-feature",
		Label:          "shep (feature/x)",
		Source:         config.SourceProjects,
		Meta: map[string]string{
			"is_worktree": "true",
			"repo":        "shep",
			"branch":      "feature/x",
		},
	}
	app := New()
	app.cfg = config.Defaults()
	request, err := app.workspaceLaunchRequest(cand)
	if err != nil {
		t.Fatalf("workspaceLaunchRequest: %v", err)
	}
	if got := string(request.WorkspaceName); got != "shep@feature/x" {
		t.Fatalf("workspace name = %q, want %q", got, "shep@feature/x")
	}
}

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

// TestOpen_SessionsSourceEndToEnd covers the opt-in production path from
// config source order through collection, resolution, and foreground attach.
func TestOpen_SessionsSourceEndToEnd(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceSessions}
	cfg.Sources.Sessions.Icon = "S"
	driver := &openDriver{detect: true, sessions: []source.Session{{Name: "alpha", Running: true}}}
	var attached string
	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithHerdrDriver(driver),
		WithSessionAttach(func(_ context.Context, binary, name string, _ []string) error {
			if binary != "herdr" {
				t.Errorf("attach binary = %q, want herdr", binary)
			}
			attached = name
			return nil
		}),
	)
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "alpha"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open sessions source: %v (stderr=%q)", err, errOut.String())
	}
	if driver.sessionsCalls != 1 {
		t.Errorf("session list calls = %d, want exactly one", driver.sessionsCalls)
	}
	if attached != "alpha" {
		t.Errorf("attached session = %q, want alpha", attached)
	}
	if driver.lastCand.Path != "" || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("sessions flow used generic workspace behavior: candidate=%+v stdout=%q stderr=%q", driver.lastCand, out.String(), errOut.String())
	}
}

// TestLaunch_SessionAttachDispatchesBeforePathAndDriverChecks verifies a
// sessions row runs the dedicated foreground attach seam even when it has no
// usable path or normal Herdr driver.
func TestLaunch_SessionAttachDispatchesBeforePathAndDriverChecks(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/current.sock")
	t.Setenv("HERDR_SESSION", "current")
	t.Setenv("HERDR_TEST_ONLY", "remove")
	t.Setenv("SHEP_SESSION_UNRELATED", "keep")

	var gotBinary, gotName string
	var gotEnv []string
	app := New(WithSessionAttach(func(_ context.Context, binary, name string, env []string) error {
		gotBinary, gotName = binary, name
		gotEnv = append([]string(nil), env...)
		return nil
	}))
	app.cfg = config.Defaults()
	var out, errOut bytes.Buffer
	_, err := app.launch(context.Background(), source.Candidate{
		Source:  config.SourceSessions,
		Missing: true,
		Meta:    map[string]string{"session_name": "alpha"},
	}, tui.RowActionOpen, "workspace", nil, &out, &errOut)
	if err != nil {
		t.Fatalf("launch session: %v (stderr=%q)", err, errOut.String())
	}
	if gotBinary != "herdr" || gotName != "alpha" {
		t.Errorf("attach = (%q, %q), want (herdr, alpha)", gotBinary, gotName)
	}
	if !containsEnv(gotEnv, "SHEP_SESSION_UNRELATED=keep") {
		t.Errorf("unrelated environment missing from attach child: %v", gotEnv)
	}
	for _, entry := range gotEnv {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "HERDR_") {
			t.Errorf("attach child leaked Herdr environment %q", entry)
		}
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("successful attach output = stdout:%q stderr:%q, want quiet", out.String(), errOut.String())
	}
}

// TestLaunch_SessionAttachRejectsEmptyNameAndSurfacesChildFailure verifies an
// invalid row never invokes the child and an attach error follows the existing
// command failure contract.
func TestLaunch_SessionAttachRejectsEmptyNameAndSurfacesChildFailure(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		called := false
		app := New(WithSessionAttach(func(context.Context, string, string, []string) error {
			called = true
			return nil
		}))
		var errOut bytes.Buffer
		_, err := app.launch(context.Background(), source.Candidate{Source: config.SourceSessions}, tui.RowActionOpen, "workspace", nil, io.Discard, &errOut)
		if !errors.Is(err, errExitOne) || called || !strings.Contains(errOut.String(), "missing session name") {
			t.Errorf("empty session launch = err:%v called:%t stderr:%q", err, called, errOut.String())
		}
	})
	t.Run("child failure", func(t *testing.T) {
		app := New(WithSessionAttach(func(context.Context, string, string, []string) error {
			return errors.New("attach exited 7")
		}))
		var errOut bytes.Buffer
		_, err := app.launch(context.Background(), source.Candidate{Source: config.SourceSessions, Meta: map[string]string{"session_name": "beta"}}, tui.RowActionOpen, "workspace", nil, io.Discard, &errOut)
		if !errors.Is(err, errExitOne) || !strings.Contains(errOut.String(), "attach exited 7") {
			t.Errorf("failed session attach = err:%v stderr:%q", err, errOut.String())
		}
	})
}

func TestLaunch_SessionsSourceTargetGuard(t *testing.T) {
	for _, tt := range []struct {
		name, target string
		inside       bool
	}{
		{"tab inside Herdr", "tab", true},
		{"pane inside Herdr", "pane", true},
		{"tab outside Herdr", "tab", false},
		{"pane outside Herdr", "pane", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			app := New(WithSessionAttach(func(context.Context, string, string, []string) error {
				calls++
				return nil
			}))
			cand := source.Candidate{Source: config.SourceSessions, Label: "alpha", Meta: map[string]string{"session_name": "alpha"}}
			var pane *source.Pane
			want := "--target=" + tt.target + " requires shep to be running inside a herdr workspace pane\n"
			if tt.inside {
				pane = &source.Pane{}
				want = disallowTarget(cand, tt.target) + "\n"
			}
			var errOut bytes.Buffer
			_, err := app.launch(context.Background(), cand, tui.RowActionOpen, tt.target, pane, io.Discard, &errOut)
			if !errors.Is(err, errExitOne) || calls != 0 || errOut.String() != want {
				t.Errorf("launch = err:%v calls:%d stderr:%q, want exit 1, zero calls, %q", err, calls, errOut.String(), want)
			}
		})
	}
}

func TestLaunch_SessionsSourceDefaultTargetAttachesUnchanged(t *testing.T) {
	called, name := 0, ""
	app := New(WithSessionAttach(func(_ context.Context, _ string, got string, _ []string) error {
		called++
		name = got
		return nil
	}))
	var errOut bytes.Buffer
	_, err := app.launch(context.Background(), source.Candidate{Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}}, tui.RowActionOpen, "", nil, io.Discard, &errOut)
	if err != nil || called != 1 || name != "alpha" || errOut.Len() != 0 {
		t.Errorf("default target = err:%v calls:%d name:%q stderr:%q", err, called, name, errOut.String())
	}
}

// TestStripHerdrEnv removes every Herdr-prefixed setting without changing the
// order or values of unrelated environment entries.
func TestStripHerdrEnv(t *testing.T) {
	input := []string{"PATH=/bin", "HERDR_SOCKET_PATH=/tmp/socket", "KEEP=1", "HERDR_FLAG", "NOT_HERDR=value"}
	want := []string{"PATH=/bin", "KEEP=1", "NOT_HERDR=value"}
	if got := stripHerdrEnv(input); !reflect.DeepEqual(got, want) {
		t.Errorf("stripHerdrEnv(%v) = %v, want %v", input, got, want)
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
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
	cfg.General.SourceOrder = nil // no sources at all would still resolve "."
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	driver := &openDriver{detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1", lastAction: source.HerdrActionCreated}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
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
// end-to-end): a freshly created workspace applies [defaults].template in one
// atomic layout.apply that reuses the workspace's root tab, rather than
// leaving that tab unused alongside a new one. The rename is now carried by
// the dispatched tab_label instead of a separate `tab rename` subprocess.
func TestOpen_TemplateAppliesOnCreatedWorkspace(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{{ID: "main", Command: "nvim"}}},
		},
	}
	driver := runOpenTemplate(t, cfg)
	if len(driver.layouts) != 1 || driver.layouts[0] != "layout:wA:t1:code:focus" {
		t.Errorf("expected one atomic layout reusing root tab wA:t1 as %q, got %v", "code", driver.layouts)
	}
	if len(driver.created) != 0 || len(driver.renamed) != 0 {
		t.Errorf("layout.apply must replace tab create/rename subprocesses; created=%v renamed=%v",
			driver.created, driver.renamed)
	}
	if !reflect.DeepEqual(driver.argv, [][]string{{os.Getenv("SHELL"), "-l", "-c", "nvim; exec " + os.Getenv("SHELL")}}) {
		t.Errorf("pane argv = %v, want login-shell argv", driver.argv)
	}
}

// TestOpen_SourcePaneLabelFormatNeverRenamesOmittedLeaf is a boundary
// characterization: source pane formats render picker rows, while template
// leaves without an explicit label preserve the Herdr pane label.
func TestOpen_SourcePaneLabelFormatNeverRenamesOmittedLeaf(t *testing.T) {
	cfg := config.Defaults()
	cfg.Sources.Herdr.PaneLabelFormat = "display={{.Label}}"
	cfg.Templates["default"] = config.TemplateConfig{
		Tabs: []config.TemplateTab{
			{Name: "code", Root: "main", Nodes: []config.TemplateNode{{ID: "main", Command: "nvim"}}},
		},
	}

	driver := runOpenTemplate(t, cfg)
	if !reflect.DeepEqual(driver.argv, [][]string{{os.Getenv("SHELL"), "-l", "-c", "nvim; exec " + os.Getenv("SHELL")}}) {
		t.Fatalf("pane argv = %v, want login-shell argv", driver.argv)
	}
	if len(driver.paneCalls) != 0 {
		t.Fatalf("source pane label format caused Herdr rename calls: %v", driver.paneCalls)
	}
}

// TestOpen_WorkspaceCommandCloseOnExit_RunsDirectly is the end-to-end wiring
// test for native layout.apply semantics: a [[workspaces]] entry with a
// top-level command + close_on_exit flows config -> workspacesProvider.List
// (which forwards Meta["close_on_exit"]="true") -> resolveTemplate (synthetic
// template) -> templates.Apply -> a dispatched pane whose process exits with
// the command. No synthetic pane-close command is needed.
func TestOpen_WorkspaceCommandCloseOnExit_RunsDirectly(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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
	want := [][]string{{os.Getenv("SHELL"), "-l", "-c", "k9s"}}
	if !reflect.DeepEqual(driver.argv, want) {
		t.Errorf("pane argv = %v, want %v", driver.argv, want)
	}
}

// TestOpen_TemplateSkippedOnFocusedWorkspace: focusing an existing workspace
// never applies a template (templates are a "freshly created" concept only).
func TestOpen_TemplateSkippedOnFocusedWorkspace(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{Command: "k9s"}
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	driver := &openDriver{detect: true, workspaceID: "wA", lastAction: source.HerdrActionFocused}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
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

// TestOpen_TemplateFailureReturnsErrorAfterWarning: a failing template
// application preserves its warning and non-transactional side effects while
// returning a failure so the selection is not recorded as successful.
//
// This is also the regression guard for the reportedExitError plain-error
// bug: templates.Apply's failure here is a plain, non-ExitCoder error (from
// applyLayoutErr, wrapped only by fmt.Errorf in templates.Apply), so
// markReported(applyErr) in launchWorkspace exercises exactly the path that
// used to silently coerce to exit code 0. ExitCode must report the package's
// ordinary failure code (1), and cmd.Execute()'s own return value —
// asserted directly via ExitCoder, matching how cmd/shep/main.go derives the
// process exit status — must agree.
func TestOpen_TemplateFailureReturnsErrorAfterWarning(t *testing.T) {
	cfg := config.Defaults()
	cfg.Templates["default"] = config.TemplateConfig{Command: "boom"}
	dir := t.TempDir()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	driver := &openDriver{
		detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1",
		lastAction: source.HerdrActionCreated, applyLayoutErr: errors.New("boom"),
	}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("open should fail when template application fails")
	}
	if !strings.Contains(errOut.String(), "template failed") {
		t.Errorf("stderr = %q, want 'template failed'", errOut.String())
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(err) = %d, want 1 (plain error wrapped by markReported must never report 0)", got)
	}
	ec, ok := err.(ExitCoder)
	if !ok {
		t.Fatal("markReported(applyErr) must satisfy ExitCoder directly (cmd/shep/main.go asserts this via ExitCode)")
	}
	if got := ec.ExitCode(); got != 1 {
		t.Fatalf("direct err.(ExitCoder).ExitCode() = %d, want 1", got)
	}
	app.reportUnhandledError(err)
	lines := strings.Split(strings.TrimRight(errOut.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr = %q, want exactly one line after reportUnhandledError, got %d", errOut.String(), len(lines))
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "group", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}},
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

func TestOpen_GroupWorkspaceLazilyRunsCustomSourceAndDirectSelectsSingleRow(t *testing.T) {
	root := t.TempDir()
	counter := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(t.TempDir(), "list-contexts")
	const scriptBody = "#!/bin/sh\ncount=0\nif [ -f \"$COUNT_FILE\" ]; then count=$(cat \"$COUNT_FILE\"); fi\nprintf '%s' $((count + 1)) > \"$COUNT_FILE\"\nprintf '[{\"label\":\"context\",\"path\":\"%s\"}]' \"$GROUP_ROOT\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNT_FILE", counter)
	t.Setenv("GROUP_ROOT", root)

	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name: "kube-contexts", Command: []string{script}, Timeout: config.Duration(time.Second), LabelFormat: "context={{.Label}}",
	}}
	cfg.Workspaces = []config.WorkspaceConfig{{
		Name: "Kubernetes", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{"kube-contexts"},
	}}
	driver := &openDriver{detect: true, workspaceID: "wA"}
	_, _, err := runOpen(t, cfg, driver, nil, "Kubernetes")
	if err != nil {
		t.Fatalf("open Kubernetes: %v", err)
	}
	if driver.lastCand.Source != "kube-contexts" || driver.lastCand.Label != "context" {
		t.Fatalf("launched candidate = %+v, want the nested custom source row", driver.lastCand)
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "1" {
		t.Fatalf("custom source count = %q, read error = %v, want exactly one lazy run", data, err)
	}
}

func TestOpen_SamePathGroups_ResolveDistinctGroupConfigs(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "repo-arcade")
	if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	counter := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(t.TempDir(), "list-contexts")
	const scriptBody = "#!/bin/sh\ncount=0\nif [ -f \"$COUNT_FILE\" ]; then count=$(cat \"$COUNT_FILE\"); fi\nprintf '%s' $((count + 1)) > \"$COUNT_FILE\"\nprintf '[{\"label\":\"k8s-cluster\",\"path\":\"%s\"}]' \"$GROUP_ROOT\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNT_FILE", counter)
	t.Setenv("GROUP_ROOT", root)

	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name: "kube-contexts", Command: []string{script}, Timeout: config.Duration(time.Second), LabelFormat: "context={{.Label}}",
	}}
	// Two groups sharing the exact same Path, but different names and source orders.
	cfg.Workspaces = []config.WorkspaceConfig{
		{
			Name: "Kubernetes", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{"kube-contexts"},
		},
		{
			Name: "fsociety", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{"projects"},
		},
	}

	// 1. Opening "fsociety" must resolve the fsociety group (projects source), NOT the Kubernetes group.
	driverFsociety := &openDriver{detect: true, workspaceID: "wA"}
	out, errOut, err := runOpen(t, cfg, driverFsociety, nil, "fsociety")
	if err != nil {
		t.Fatalf("open fsociety: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	if driverFsociety.lastCand.Source != config.SourceProjects || driverFsociety.lastCand.Path != projDir {
		t.Fatalf("launched candidate for fsociety = %+v, want project candidate in %s", driverFsociety.lastCand, projDir)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatalf("kube-contexts custom source ran when opening fsociety, want 0 runs")
	}

	// 2. Opening "Kubernetes" must resolve the Kubernetes group (kube-contexts source).
	driverKube := &openDriver{detect: true, workspaceID: "wB"}
	_, _, err = runOpen(t, cfg, driverKube, nil, "Kubernetes")
	if err != nil {
		t.Fatalf("open Kubernetes: %v", err)
	}
	if driverKube.lastCand.Source != "kube-contexts" || driverKube.lastCand.Label != "k8s-cluster" {
		t.Fatalf("launched candidate for Kubernetes = %+v, want kube-contexts candidate", driverKube.lastCand)
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "1" {
		t.Fatalf("kube-contexts count = %q, want exactly 1 run", data)
	}
}

func runOpenStartupTemplate(t *testing.T, action source.HerdrAction) *openDriver {
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Templates["default"] = config.TemplateConfig{Command: "echo hi"}
	dir := t.TempDir()
	driver := &openDriver{detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1", lastAction: action}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--path", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return driver
}

// TestOpen_TemplateFiresOnCreated: a created workspace applies the resolved
// template's command in the dispatched root pane.
func TestOpen_TemplateFiresOnCreated(t *testing.T) {
	driver := runOpenStartupTemplate(t, source.HerdrActionCreated)
	want := [][]string{{os.Getenv("SHELL"), "-l", "-c", "echo hi; exec " + os.Getenv("SHELL")}}
	if !reflect.DeepEqual(driver.argv, want) {
		t.Errorf("pane argv = %v, want %v", driver.argv, want)
	}
}

// TestOpen_TemplateSkippedOnFocused: focusing an existing workspace does not
// apply any template.
func TestOpen_TemplateSkippedOnFocused(t *testing.T) {
	driver := runOpenStartupTemplate(t, source.HerdrActionFocused)
	if len(driver.argv) != 0 {
		t.Errorf("pane argv = %v, want none for focused workspace", driver.argv)
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

// TestLayoutFromConfigWithCustomSources_ThreadsPerCustomSourceLabelFormat
// proves layoutFromConfigWithCustomSources resolves each declared
// [[sources.custom]] entry's label_format into Layout.LabelFormats.CustomSources,
// keyed by name, alongside the five fixed built-in fields layoutFromConfig
// already threads.
func TestLayoutFromConfigWithCustomSources_ThreadsPerCustomSourceLabelFormat(t *testing.T) {
	t.Parallel()
	customSources := []config.CustomSourceConfig{
		{Name: "prs", LabelFormat: "PR {{.Label}}"},
		{Name: "issues", LabelFormat: "#{{.Label}}"},
	}
	order := []string{"issues", "prs"}
	got := layoutFromConfigWithCustomSources(config.TUIConfig{}, order, customSources)
	want := map[string]string{"prs": "PR {{.Label}}", "issues": "#{{.Label}}"}
	if !reflect.DeepEqual(got.LabelFormats.CustomSources, want) {
		t.Errorf("LabelFormats.CustomSources = %v, want %v", got.LabelFormats.CustomSources, want)
	}
	if !reflect.DeepEqual(got.SourceOrder, order) {
		t.Errorf("nested custom source SourceOrder = %v, want %v", got.SourceOrder, order)
	}
}

func TestLayoutFromConfigWithCustomSources_ThreadsAgentsLabelFormat(t *testing.T) {
	t.Parallel()
	sources := config.SourcesConfig{
		Agents: config.AgentsSourceConfig{LabelFormat: "agent={{.Label}}"},
	}
	got := layoutFromConfigWithCustomSources(config.TUIConfig{}, nil, nil, sources)
	if want := "agent={{.Label}}"; got.LabelFormats.Agents != want {
		t.Errorf("LabelFormats.Agents = %q, want %q", got.LabelFormats.Agents, want)
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

// recordingDriver records the preserved live pane-read preview operation.
type recordingDriver struct {
	readQueried int
}

func (*recordingDriver) Detect(context.Context) bool { return true }
func (*recordingDriver) Snapshot(context.Context) (source.Snapshot, error) {
	return source.Snapshot{}, nil
}
func (*recordingDriver) ListSessions(context.Context) ([]source.Session, error) { return nil, nil }
func (*recordingDriver) FocusOrCreate(context.Context, source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("not used")
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
func (*recordingDriver) RenamePane(context.Context, string, *string) error {
	return errors.New("not used")
}
func (*recordingDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("not used")
}
func (*recordingDriver) RunPane(context.Context, string, string) error { return errors.New("not used") }
func (*recordingDriver) FocusTab(context.Context, string) error        { return errors.New("not used") }

// TestApp_BuildPreviewRenderer_UsesSnapshotAndLivePaneRead proves workspace
// state is rendered from the startup generation while active-pane content
// retains its dedicated live pane-read command.
func TestApp_BuildPreviewRenderer_UsesSnapshotAndLivePaneRead(t *testing.T) {
	driver := &recordingDriver{}
	app := New(WithHerdrDriver(driver))
	app.cfg = config.Defaults()
	app.cfg.Preview.Default = []string{config.PreviewWorkspace, config.PreviewActivePane}
	app.probes = config.Probes{}
	app.startupSnapshot = &source.Snapshot{
		Workspaces:    []source.Workspace{{ID: "wA"}},
		Tabs:          []source.Tab{{ID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 1}},
		Panes:         []source.Pane{{ID: "wA:p1", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/x", Focused: true}},
		FocusedPaneID: "wA:p1",
	}
	r := app.buildPreviewRenderer()

	cand := source.Candidate{
		Path: "/x", Label: "foo", Source: "herdr",
		Meta: map[string]string{"workspace_id": "wA"},
	}
	res, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if driver.readQueried == 0 {
		t.Errorf("renderer must retain the dedicated live pane read")
	}
	if !strings.Contains(res.Text, "edit") {
		t.Errorf("workspace section did not render tabs from snapshot: %q", res.Text)
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

	layout := layoutFromConfig(cfg.TUI, cfg.General.SourceOrder)
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
	if !reflect.DeepEqual(cfg.TUI, originalTUI) {
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{
		Name: name, Path: dir, Command: command, CloseOnExit: closeOnExit,
	}}
	return cfg, dir
}

// insidePaneDriver returns an openDriver with the given pane resolved by the
// startup snapshot.
func insidePaneDriver(pane source.Pane) *openDriver {
	pane.Focused = true
	return &openDriver{
		detect: true,
		snapshot: source.Snapshot{
			Panes:         []source.Pane{pane},
			FocusedPaneID: pane.ID,
		},
	}
}

// TestOpen_TargetTab_OpensInCurrentWorkspace (end-to-end): a Command-only
// workspace entry opened with --target=tab creates a new tab in the workspace
// shep is running inside (focus=true so the new tab gets keyboard focus) and
// runs the command in that tab's root pane, wrapped with close_on_exit.
func TestOpen_TargetTab_OpensInCurrentWorkspace(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
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
	want := "run:new-p:k9s; 'herdr' pane close 'new-p'"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected wrapped command %q in the new pane, got %v", want, driver.ran)
	}
	// The standalone-workspace path must NOT have run.
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called for target=tab; got candidate %q", driver.lastCand.Path)
	}
}

func TestOpen_UsesOneStartupSnapshotForTargeting(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	driver := &openDriver{
		detect: true,
		snapshot: source.Snapshot{
			Workspaces:         []source.Workspace{{ID: "w-snapshot", ActiveTabID: "w-snapshot:t1", Focused: true}},
			Tabs:               []source.Tab{{ID: "w-snapshot:t1", WorkspaceID: "w-snapshot", Focused: true}},
			Panes:              []source.Pane{{ID: "w-snapshot:p1", WorkspaceID: "w-snapshot", TabID: "w-snapshot:t1", CWD: "/snapshot-cwd", Focused: true}},
			FocusedWorkspaceID: "w-snapshot",
			FocusedTabID:       "w-snapshot:t1",
			FocusedPaneID:      "w-snapshot:p1",
		},
	}

	_, _, err := runOpen(t, cfg, driver, nil, "--target", "tab", "ops")
	if err != nil {
		t.Fatalf("open --target=tab: %v", err)
	}
	if driver.snapshotCalls != 1 {
		t.Fatalf("Snapshot calls = %d, want exactly one startup snapshot", driver.snapshotCalls)
	}
	if len(driver.created) != 1 || driver.created[0] != "tab:w-snapshot:/snapshot-cwd:ops:focus" {
		t.Errorf("CreateTab target = %v, want snapshot workspace/tab/pane context", driver.created)
	}
}

func TestOpen_InitialSnapshotFailure_IsolatesHerdrSource(t *testing.T) {
	cfg, root := seedCfg(t, "fallback")
	cfg.General.SourceOrder = []string{config.SourceHerdr, config.SourceWorkspaces}
	driver := &openDriver{
		detect:      true,
		snapshotErr: errors.New("snapshot unavailable"),
		workspaceID: "created",
	}

	_, errOut, err := runOpen(t, cfg, driver, nil, "fallback")
	if err != nil {
		t.Fatalf("open with initial snapshot failure: %v", err)
	}
	if driver.snapshotCalls != 1 {
		t.Errorf("Snapshot calls = %d, want exactly one initial attempt", driver.snapshotCalls)
	}
	if !strings.Contains(errOut, "warning: herdr snapshot unavailable: snapshot unavailable") {
		t.Errorf("stderr = %q, want snapshot-unavailable diagnostic", errOut)
	}
	if got, want := driver.lastCand.Source, config.SourceWorkspaces; got != want {
		t.Errorf("resolved source = %q, want unrelated configured source %q", got, want)
	}
	if got, want := driver.lastCand.NormalizedPath, resolved(filepath.Join(root, "fallback")); got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
}

func TestApp_HydrateStartupSnapshot_TimesOut(t *testing.T) {
	driver := &openDriver{
		detect: true,
		snapshotFn: func(ctx context.Context) (source.Snapshot, error) {
			<-ctx.Done()
			return source.Snapshot{}, ctx.Err()
		},
	}
	app := New(WithHerdrDriver(driver))

	started := time.Now()
	err := app.hydrateStartupSnapshot(context.Background())
	elapsed := time.Since(started)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hydrateStartupSnapshot error = %v, want deadline exceeded", err)
	}
	if elapsed > source.SnapshotTimeout+500*time.Millisecond {
		t.Fatalf("hydrateStartupSnapshot took %v, want bounded by %v", elapsed, source.SnapshotTimeout)
	}
}

// TestOpen_TargetPane_OpensInCurrentWorkspace (end-to-end): --target=pane
// splits a new pane off the current one (right, 0.5, focus=true) and runs the
// wrapped command there.
func TestOpen_TargetPane_OpensInCurrentWorkspace(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
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
	want := "run:split-p:k9s; 'herdr' pane close 'split-p'"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected wrapped command %q in the split pane, got %v", want, driver.ran)
	}
}

// TestOpen_TargetTab_NoCurrentPane_Errors (end-to-end): --target=tab when the
// startup snapshot has no focused pane surfaces a clear, specific error and
// never reaches CreateTab/RunPane.
func TestOpen_TargetTab_NoCurrentPane_Errors(t *testing.T) {
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", true)
	driver := &openDriver{
		detect: true,
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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
	app.layoutApplier = driver
	app.cfg = config.Defaults()
	app.probes = config.Probes{Herdr: true, Git: true}
	_, err := app.launch(context.Background(), cand, action, target, currentPane, &out, &errOut)
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
	t.Setenv("HERDR_BIN_PATH", "")
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
	wantClose := "run:new-p:'herdr' 'pane' 'close' 'new-p'"
	if driver.ran[1] != wantClose {
		t.Errorf("expected rollback close %q, got %q", wantClose, driver.ran[1])
	}
}

// TestOpen_TargetPane_ApplyFailureRollsBackAndErrors mirrors the tab test for
// --target=pane, confirming the rollback close targets the split pane.
func TestLaunchInCurrentWorkspaceRollbackUsesIndependentContext(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
	cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	driver.runErrOnFirstCall = errors.New("boom")
	app := New(WithHerdrDriver(driver))
	app.cfg = cfg
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var errOut bytes.Buffer
	_, err := app.launchInCurrentWorkspace(ctx, driver, source.Candidate{Source: config.SourceWorkspaces, Path: "/ops", Label: "ops", Meta: map[string]string{"command": "k9s"}}, "tab", &pane, &errOut)
	if !errors.Is(err, errExitOne) {
		t.Fatalf("launch error = %v, want errExitOne", err)
	}
	if len(driver.ran) != 2 || driver.ran[1] != "run:new-p:'herdr' 'pane' 'close' 'new-p'" {
		t.Fatalf("rollback did not run independently: %v", driver.ran)
	}
}

func TestOpen_TargetPane_ApplyFailureRollsBackAndErrors(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "")
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
	wantClose := "run:split-p:'herdr' 'pane' 'close' 'split-p'"
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

// TestOpen_TargetTab_CustomSourceCandidate_Opens proves an custom source row
// carrying a command supports --target=tab through the exact same
// SupportsCurrentWorkspaceTarget/launchInCurrentWorkspace path as a
// [[workspaces]] command entry, with no parallel launch code.
func TestOpen_TargetTab_CustomSourceCandidate_Opens(t *testing.T) {
	cand := source.Candidate{Source: "prs", Path: "/repo", Label: "PR 42", Meta: map[string]string{"command": "gh pr view 42"}}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "tab", &pane, driver)
	if err != nil {
		t.Fatalf("launch --target=tab custom source: %v (stderr=%q)", err, errOut)
	}
	if len(driver.created) != 1 || driver.created[0] != "tab:wA:/cur:PR 42:focus" {
		t.Errorf("expected one focused CreateTab for the custom source candidate, got %v", driver.created)
	}
	want := "run:new-p:gh pr view 42"
	if len(driver.ran) != 1 || driver.ran[0] != want {
		t.Errorf("expected command run in the new pane, got %v", driver.ran)
	}
}

// TestOpen_TargetTab_CustomSourceCandidateWithoutCommand_Errors proves an
// custom source row with no command is rejected the same way a plain path is:
// disallowTarget's default branch, not a special custom source-only message.
func TestOpen_TargetTab_CustomSourceCandidateWithoutCommand_Errors(t *testing.T) {
	cand := source.Candidate{Source: "prs", Path: "/repo", Label: "PR 42"}
	pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
	driver := insidePaneDriver(pane)
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "tab", &pane, driver)
	if err == nil {
		t.Fatal("expected an error for --target=tab on a commandless custom source row")
	}
	if !strings.Contains(errOut, "requires an entry with a command") {
		t.Errorf("stderr = %q, want the no-command message", errOut)
	}
}

// TestOpen_CustomSourceRowWithPathOpensAsWorkspace proves an custom source row
// carrying a path opens through the ordinary FocusOrCreate workspace path —
// no special-cased custom source launch branch exists.
func TestOpen_CustomSourceRowWithPathOpensAsWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "prs", Command: []string{"printf", "[]"}}}
	driver := &openDriver{detect: true, workspaceID: "w-new", lastAction: source.HerdrActionFocused}
	app := New(WithStreams(&bytes.Buffer{}, &bytes.Buffer{}))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	cand := source.Candidate{Source: "prs", Path: dir, Label: "PR 42", Meta: map[string]string{"command": "gh pr view 42"}}
	var out, errOut bytes.Buffer
	_, err := app.launch(context.Background(), cand, tui.RowActionOpen, "workspace", nil, &out, &errOut)
	if err != nil {
		t.Fatalf("launch workspace target for custom source row: %v (stderr=%q)", err, errOut.String())
	}
	if driver.lastCand.Path != dir {
		t.Errorf("FocusOrCreate candidate path = %q, want %q", driver.lastCand.Path, dir)
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

// TestOpen_LaunchSourceAgentsRoutesToFocusTab proves that any SourceAgents
// candidate routes to driver.FocusTab using Meta["tab_id"] even when selected
// with RowActionOpen (e.g. from CLI or nested picker), and never creates a
// workspace or calls FocusOrCreate.
func TestOpen_LaunchSourceAgentsRoutesToFocusTab(t *testing.T) {
	cand := source.Candidate{
		Source: config.SourceAgents,
		Label:  "agent-p1",
		Path:   "/svc/api",
		Meta:   map[string]string{"workspace_id": "wA", "tab_id": "t1", "pane_id": "p1"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "workspace", nil, driver)
	if err != nil {
		t.Fatalf("launch sourceAgents: %v (stderr=%q)", err, errOut)
	}
	if len(driver.focused) != 1 || driver.focused[0] != "focus-tab:t1" {
		t.Errorf("expected FocusTab(t1), got %v", driver.focused)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called for sourceAgents candidate; got %+v", driver.lastCand)
	}
	if len(driver.created) != 0 {
		t.Errorf("no workspace or tab should be created; got %v", driver.created)
	}
}

// TestOpen_LaunchSourceAgentsMissingTabIDFailsCleanly proves that a SourceAgents
// candidate missing Meta["tab_id"] surfaces a clean warning and errExitOne,
// never creates a workspace, and never calls FocusTab with empty id.
func TestOpen_LaunchSourceAgentsMissingTabIDFailsCleanly(t *testing.T) {
	cand := source.Candidate{
		Source: config.SourceAgents,
		Label:  "agent-missing-tab",
		Path:   "/svc/api",
		Meta:   map[string]string{"workspace_id": "wA", "pane_id": "p1"},
	}
	driver := insidePaneDriver(source.Pane{ID: "cur-p", WorkspaceID: "wA", CWD: "/cur"})
	errOut, err := runLaunchDirect(t, cand, tui.RowActionOpen, "workspace", nil, driver)
	if err == nil {
		t.Fatal("expected error for sourceAgents candidate missing tab_id")
	}
	if !errors.Is(err, errExitOne) {
		t.Errorf("expected errExitOne, got %v", err)
	}
	if !strings.Contains(errOut, "missing tab id") {
		t.Errorf("stderr = %q, want 'missing tab id'", errOut)
	}
	if len(driver.focused) != 0 {
		t.Errorf("FocusTab must not be called; got %v", driver.focused)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called; got candidate %+v", driver.lastCand)
	}
	if len(driver.created) != 0 {
		t.Errorf("no workspace or tab should be created; got %v", driver.created)
	}
}

// TestOpen_RunOpenCLISelectionSourceAgentsFocusesTabAndNeverCreates proves
// that when a SourceAgents candidate is resolved and selected through the CLI
// path (runOpen with query), launch focuses the containing tab and never
// creates a workspace.
func TestOpen_RunOpenCLISelectionSourceAgentsFocusesTabAndNeverCreates(t *testing.T) {
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceAgents}

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "wA", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "wA", Label: "arcade"},
		},
		Panes: []source.Pane{
			{
				ID:            "p1",
				WorkspaceID:   "wA",
				TabID:         "t1",
				Agent:         "pi",
				AgentStatus:   "working",
				TerminalTitle: "security-audit",
				ForegroundCWD: "/srv/fsociety/sub",
			},
		},
	}

	driver := insidePaneDriver(snapshot.Panes[0])
	driver.snapshot = snapshot

	out, errOut, err := runOpen(t, cfg, driver, nil, "security-audit")
	if err != nil {
		t.Fatalf("runOpen failed: %v (stderr=%q, stdout=%q)", err, errOut, out)
	}

	if len(driver.focused) != 1 || driver.focused[0] != "focus-tab:t1" {
		t.Errorf("expected FocusTab(t1), got %v", driver.focused)
	}
	if driver.lastCand.Path != "" {
		t.Errorf("FocusOrCreate must not be called; got %+v", driver.lastCand)
	}
	if len(driver.created) != 0 {
		t.Errorf("no workspace should be created; got %v", driver.created)
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

// --- R3-1: nested group effective source order ---

// TestEffectiveGroupSourceOrder is the pure-function table test for the single
// helper that resolves a nested group picker's iteration order (R3-1).
// Precedence: explicit ws.SourceOrder when hasWorkspace && non-empty; else the
// parsed group_sources list; else cfg.General.SourceOrder.
func TestEffectiveGroupSourceOrder(t *testing.T) {
	t.Parallel()
	global := []string{config.SourceHerdr, config.SourceWorkspaces}
	groupSources := []string{config.SourceProjects, config.SourceSessions}

	cases := []struct {
		name         string
		cfg          *config.Config
		ws           config.WorkspaceConfig
		hasWorkspace bool
		groupSources []string
		want         []string
	}{
		{
			name:         "explicit workspace order wins",
			cfg:          config.Defaults(),
			ws:           config.WorkspaceConfig{SourceOrder: []string{config.SourceProjects, config.SourceWorkspaces}},
			hasWorkspace: true,
			groupSources: groupSources,
			want:         []string{config.SourceProjects, config.SourceWorkspaces},
		},
		{
			name:         "group_sources used when workspace bound but order empty",
			cfg:          config.Defaults(),
			ws:           config.WorkspaceConfig{SourceOrder: nil},
			hasWorkspace: true,
			groupSources: []string{config.SourceSessions, config.SourceHerdr},
			want:         []string{config.SourceSessions, config.SourceHerdr},
		},
		{
			name:         "group_sources used when no workspace binding",
			cfg:          config.Defaults(),
			ws:           config.WorkspaceConfig{},
			hasWorkspace: false,
			groupSources: []string{config.SourceZoxide, config.SourceProjects},
			want:         []string{config.SourceZoxide, config.SourceProjects},
		},
		{
			name:         "global fallback when both workspace order and group_sources empty",
			cfg:          &config.Config{General: config.General{SourceOrder: append([]string(nil), global...)}},
			ws:           config.WorkspaceConfig{SourceOrder: nil},
			hasWorkspace: true,
			groupSources: nil,
			want:         global,
		},
		{
			name:         "global fallback when no workspace and no group_sources",
			cfg:          &config.Config{General: config.General{SourceOrder: append([]string(nil), global...)}},
			ws:           config.WorkspaceConfig{},
			hasWorkspace: false,
			groupSources: nil,
			want:         global,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := effectiveGroupSourceOrder(tc.cfg, tc.ws, tc.hasWorkspace, tc.groupSources)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("effectiveGroupSourceOrder = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNestedPicker_OmittedOrderUsesGlobalEffectiveOrder (R3-1) proves the
// nested group picker computes one effective order before registry construction:
// when a group omits SourceOrder, the scoped registry still runs the global
// sources and the nested cascade receives candidates in that same global order.
// A captureSelector records the ranked candidate order so the end-to-end path
// catches the former empty-registry bug.
func TestNestedPicker_RankingAndLayoutShareEffectiveOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A project marker directory so the projects provider yields a candidate
	// distinct from the sessions candidate.
	projDir := filepath.Join(root, "svc")
	if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Global order lists workspaces first (so the group [[workspaces]] entry is
	// discoverable at the top level), then projects, then sessions. The group
	// REVERSES projects/sessions to [sessions, projects]. The nested picker
	// MUST rank sessions first — proving the group's effective order, not the
	// global one, drove ranking.
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces, config.SourceProjects, config.SourceSessions}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Workspaces = []config.WorkspaceConfig{
		{
			Name: "group", Type: config.WorkspaceTypeGroup, Path: root,
			SourceOrder: []string{config.SourceSessions, config.SourceProjects},
		},
	}

	driver := &openDriver{
		detect: true, workspaceID: "wA",
		sessions: []source.Session{{Name: "run-svc", Running: true}},
	}

	// captureSelector records the ordered candidate Sources it received, so
	// the test asserts the ranking order matches the group's effective order.
	// It always picks the sessions candidate (the group-declared first source).
	var capturedSources []string
	sessCand := source.Candidate{Source: config.SourceSessions, Label: "run-svc", Meta: map[string]string{"session_name": "run-svc"}}
	captureSel := captureSelector{
		pick: sessCand,
		ok:   true,
		onSelect: func(cands []source.Candidate) {
			capturedSources = make([]string, 0, len(cands))
			for _, c := range cands {
				capturedSources = append(capturedSources, c.Source)
			}
		},
	}
	cascade := selector.New(captureSel)

	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.layoutApplier = driver
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	app.selectorBuilder = func() *selector.Cascade { return cascade }
	// The session attach is a no-op so the launch completes without shelling out.
	app.sessionAttach = func(context.Context, string, string, []string) error { return nil }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "group"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open group: %v (stderr=%q)", err, errOut.String())
	}

	if len(capturedSources) < 2 {
		t.Fatalf("nested cascade received %d candidates, want at least 2 (sessions + projects); captureSelector may not have run", len(capturedSources))
	}
	// Group effective order is [sessions, projects]; sessions MUST rank first.
	// Under the OLD global-order bug, projects would rank first.
	wantFirst := config.SourceSessions
	if capturedSources[0] != wantFirst {
		t.Errorf("nested picker first source = %q, want %q (group effective order [sessions, projects], not global [projects, sessions])", capturedSources[0], wantFirst)
	}
}

// captureSelector is a selector.Selector that records the candidate order it
// received via onSelect before returning its scripted pick.
type captureSelector struct {
	pick     source.Candidate
	ok       bool
	err      error
	onSelect func([]source.Candidate)
}

func (captureSelector) Name() string { return "capture" }
func (s captureSelector) Select(_ context.Context, candidates []source.Candidate, _ string) (source.Candidate, bool, error) {
	if s.onSelect != nil {
		s.onSelect(append([]source.Candidate(nil), candidates...))
	}
	return s.pick, s.ok, s.err
}

// --- R3-2: truthful launch outcome recording ---

// runOpenWithRecordingStore wires a fresh App with a recordingRankingStore and
// the given driver/cfg, runs `shep open <args>`, and returns the store (whose
// records slice is the assertion target) plus stdout/stderr/err.
func runOpenWithRecordingStore(t *testing.T, cfg *config.Config, driver *openDriver, args ...string) (*recordingRankingStore, string, string, error) {
	t.Helper()
	store := &recordingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	if driver != nil {
		app.herdrDriver = driver
		app.herdrDriverInjected = true
		app.layoutApplier = driver
	}
	app.cfg = cfg
	if driver != nil {
		app.probes = config.Probes{Herdr: true, Git: true}
	} else {
		app.probes = config.Probes{Git: true}
	}
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	app.sessionAttach = func(context.Context, string, string, []string) error { return nil }
	cmd := app.rootCmd()
	cmd.SetArgs(append([]string{"open"}, args...))
	err := cmd.Execute()
	return store, out.String(), errOut.String(), err
}

// TestLaunchOutcome_CompletedRecordsExactlyOnce (R3-2) proves each genuinely
// completed launch path — workspace focus, workspace create, current-workspace
// tab, session attach, and child-tab focus — records exactly one history entry.
// The degraded path-print fallbacks are covered separately (must record zero).
func TestLaunchOutcome_CompletedRecordsExactlyOnce(t *testing.T) {
	t.Parallel()

	t.Run("workspace focus records once", func(t *testing.T) {
		t.Parallel()
		cfg, _ := seedCfg(t, "foo")
		driver := &openDriver{detect: true, workspaceID: "wA", lastAction: source.HerdrActionFocused}
		store, _, _, err := runOpenWithRecordingStore(t, cfg, driver, "foo")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if len(store.records) != 1 {
			t.Errorf("focus records = %d, want exactly 1", len(store.records))
		}
	})

	t.Run("workspace create records once", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.General.SourceOrder = []string{config.SourceWorkspaces}
		driver := &openDriver{detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1", lastAction: source.HerdrActionCreated}
		store, _, _, err := runOpenWithRecordingStore(t, cfg, driver, "--path", t.TempDir())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if len(store.records) != 1 {
			t.Errorf("create records = %d, want exactly 1", len(store.records))
		}
	})

	t.Run("current-workspace tab records once", func(t *testing.T) {
		t.Parallel()
		cfg, _ := commandWorkspaceCfg(t, "ops", "k9s", false)
		pane := source.Pane{ID: "cur-p", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/cur"}
		driver := insidePaneDriver(pane)
		store, _, _, err := runOpenWithRecordingStore(t, cfg, driver, "--target", "tab", "ops")
		if err != nil {
			t.Fatalf("open --target=tab: %v", err)
		}
		if len(store.records) != 1 {
			t.Errorf("tab records = %d, want exactly 1", len(store.records))
		}
	})

	t.Run("session attach records once", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.General.SourceOrder = []string{config.SourceSessions}
		cfg.Sources.Sessions.Icon = "S"
		driver := &openDriver{detect: true, sessions: []source.Session{{Name: "alpha", Running: true}}}
		store, _, _, err := runOpenWithRecordingStore(t, cfg, driver, "alpha")
		if err != nil {
			t.Fatalf("open sessions: %v", err)
		}
		if len(store.records) != 1 {
			t.Errorf("session attach records = %d, want exactly 1", len(store.records))
		}
	})

	t.Run("child tab focus records once", func(t *testing.T) {
		t.Parallel()
		// RowActionFocusTab is only reachable through the TUI picker, so this
		// case drives runOpen with a fake TUI selector that returns a
		// focus-tab pick, proving the completed child-tab launch records
		// exactly once through the runOpen → launch → launchChildTab path.
		root := t.TempDir()
		svc := filepath.Join(root, "svc")
		if err := os.MkdirAll(filepath.Join(svc, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := config.Defaults()
		cfg.General.SourceOrder = []string{config.SourceProjects}
		cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}, Roots: []string{root}}
		driver := &openDriver{detect: true}
		store := &recordingRankingStore{}
		var out, errOut bytes.Buffer
		app := New(WithStreams(&out, &errOut))
		app.herdrDriver = driver
		app.herdrDriverInjected = true
		app.layoutApplier = driver
		app.cfg = cfg
		app.probes = config.Probes{Herdr: true, Git: true}
		app.rankingOpen = func() (rankingStore, error) { return store, nil }
		// Build a TUI selector whose fake run returns a focus-tab pick; wire
		// it through selectorFactory's cascade so onAction reaches the App.
		focusCand := source.Candidate{Label: "tab1", Path: svc, Meta: map[string]string{"tab_id": "t1"}}
		tuiSel := newTUISelector(nil, nil, app.setChosenTarget, app.setChosenAction)
		tuiSel.run = func(_ context.Context, _ []source.Candidate, _ string, _ preview.Renderer, _ *source.Pane, _ ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error) {
			return focusCand, tui.RowActionFocusTab, "", true, nil
		}
		cascade := selector.New(selector.Direct{}, tuiSel)
		app.selectorBuilder = func() *selector.Cascade { return cascade }
		// Two sibling projects force the cascade (not Direct) to select.
		if err := os.MkdirAll(filepath.Join(root, "other", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"open", ""})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("open child tab: %v (stderr=%q)", err, errOut.String())
		}
		if len(store.records) != 1 {
			t.Errorf("child tab focus records = %d, want exactly 1", len(store.records))
		}
	})
}

// TestLaunchOutcome_PathOnlyRecordsZero (R3-2) proves the two degraded
// path-print fallbacks — Herdr unavailable (driver nil / Detect false) and
// FocusOrCreate failure — both print the resolved path to stdout AND record
// zero history entries. These branches must return non-fatally without being
// mistaken for a completed launch.
func TestLaunchOutcome_PathOnlyRecordsZero(t *testing.T) {
	t.Parallel()

	t.Run("herdr unavailable prints path and records zero", func(t *testing.T) {
		t.Parallel()
		cfg, root := seedCfg(t, "foo")
		foo := resolved(filepath.Join(root, "foo"))
		// nil driver models "Herdr absent" — runOpen's hydrateStartupSnapshot
		// leaves Driver() nil, so launch prints the path.
		store, out, _, err := runOpenWithRecordingStore(t, cfg, nil, "foo")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if len(store.records) != 0 {
			t.Errorf("herdr-unavailable records = %d, want 0", len(store.records))
		}
		if !strings.Contains(out, foo) {
			t.Errorf("stdout = %q, want it to contain the printed path %q", out, foo)
		}
	})

	t.Run("focusOrCreate failure prints path and records zero", func(t *testing.T) {
		t.Parallel()
		cfg, _ := seedCfg(t, "foo")
		driver := &openDriver{detect: true, focusErr: errors.New("daemon down")}
		store, out, _, err := runOpenWithRecordingStore(t, cfg, driver, "foo")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if len(store.records) != 0 {
			t.Errorf("focusOrCreate-failure records = %d, want 0", len(store.records))
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("expected path printed to stdout on focusOrCreate failure, got empty")
		}
	})
}

// TestLaunchOutcome_CancellationAndFailureRecordZero (R3-2) proves cancellation
// and generic launch failures record zero history entries — they never
// represent a completed navigation.
func TestLaunchOutcome_CancellationAndFailureRecordZero(t *testing.T) {
	t.Parallel()

	t.Run("cancellation records zero", func(t *testing.T) {
		t.Parallel()
		cfg, _ := seedCfg(t, "foo", "foobar")
		store := &recordingRankingStore{}
		var out, errOut bytes.Buffer
		app := New(WithStreams(&out, &errOut))
		app.cfg = cfg
		app.probes = config.Probes{Git: true}
		app.rankingOpen = func() (rankingStore, error) { return store, nil }
		app.selectorBuilder = func() *selector.Cascade { return selector.New(fakeSelector{err: tui.ErrCancelled}) }
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"open", "foo"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected exit 1 on cancellation")
		}
		if len(store.records) != 0 {
			t.Errorf("cancellation records = %d, want 0", len(store.records))
		}
	})

	t.Run("missing workspace failure records zero", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.General.SourceOrder = []string{config.SourceWorkspaces}
		missing := filepath.Join(t.TempDir(), "ghost")
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "ghost", Path: missing}}
		driver := &openDriver{detect: true, workspaceID: "wA"}
		store, _, _, err := runOpenWithRecordingStore(t, cfg, driver, "ghost")
		if err == nil {
			t.Fatal("expected exit 1 for a missing workspace")
		}
		if len(store.records) != 0 {
			t.Errorf("missing-workspace records = %d, want 0", len(store.records))
		}
	})
}

func TestSelectorFactory_StatusDialerWiredWhenSocketPresent(t *testing.T) {
	t.Run("HERDR_SOCKET_PATH set wires UnixStatusDialer", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr-test.sock")
		app := New()
		dialer := app.resolveStatusDialer()
		if dialer == nil {
			t.Fatal("expected non-nil StatusDialer when HERDR_SOCKET_PATH is set")
		}
	})

	t.Run("HERDR_SOCKET_PATH unset yields nil dialer", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "")
		app := New()
		dialer := app.resolveStatusDialer()
		if dialer != nil {
			t.Errorf("expected nil StatusDialer when HERDR_SOCKET_PATH is unset, got %+v", dialer)
		}
	})

	t.Run("injected StatusDialer overrides environment", func(t *testing.T) {
		t.Setenv("HERDR_SOCKET_PATH", "/tmp/env.sock")
		injected := tui.FuncStatusDialer(func(_ context.Context) (tui.StatusStream, error) {
			return nil, nil
		})
		app := New(WithStatusDialer(injected))
		dialer := app.resolveStatusDialer()
		if dialer == nil {
			t.Fatal("expected injected dialer, got nil")
		}
	})
}

func TestOpen_AmbiguousQueryTabOnlySourcesAndGroup(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "team-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "team-project", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.TUI.Tabs = []string{"all", "projects", "review", "team"}
	cfg.Sources.Projects.Markers = []string{".git"}
	cfg.Sources.Projects.Roots = []string{root}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "team one", Path: filepath.Join(root, "one")},
		{Name: "team two", Path: filepath.Join(root, "two")},
		{ID: "team", Name: "Team", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}},
	}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "review", Command: []string{"echo", `[{"label":"team review","path":"/review"}]`}}}
	app := New()
	app.cfg = cfg
	all := []source.Candidate{{Source: config.SourceWorkspaces, Label: "team one", Path: filepath.Join(root, "one")}, {Source: config.SourceWorkspaces, Label: "team two", Path: filepath.Join(root, "two")}}
	layout := app.pickerLayout(cfg.General.SourceOrder, all)
	if len(layout.Tabs) != 4 || layout.Tabs[1].Load == nil || layout.Tabs[2].Load == nil || layout.Tabs[3].Load == nil {
		t.Fatalf("ambiguous picker tabs = %+v", layout.Tabs)
	}
	m := tui.NewModelWithLayout(all, nil, layout)
	if got := m.ActiveTab(); got != "all" {
		t.Fatalf("default tab = %q, want all", got)
	}
	for _, tab := range layout.Tabs[1:] {
		rows, err := tab.Load(context.Background(), nil)
		if err != nil {
			t.Fatalf("load %s: %v", tab.ID, err)
		}
		if len(rows) != 1 || rows[0].Source == config.SourceWorkspaces || rows[0].Label == "" {
			t.Errorf("tab %s candidates = %+v", tab.ID, rows)
		}
	}
	if !reflect.DeepEqual(cfg.General.SourceOrder, []string{config.SourceWorkspaces}) {
		t.Fatalf("tab loading mutated all source order: %v", cfg.General.SourceOrder)
	}
}

func TestOpen_AmbiguousQueryPreservesDirectResolution(t *testing.T) {
	cfg, root := seedCfg(t, "team")
	cfg.Ranking.Enabled = false
	cfg.TUI.Tabs = []string{"all", "projects"}
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	app := New()
	app.cfg = cfg
	var out, errOut bytes.Buffer
	cmd := app.openCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"team"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("direct query: %v, stderr = %q", err, errOut.String())
	}
	if !strings.Contains(out.String(), filepath.Join(root, "team")) {
		t.Fatalf("direct match output = %q", out.String())
	}
}

func TestOpen_ViewValidation(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.TUI.Tabs = []string{"all"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "review"}}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Type: config.WorkspaceTypeGroup, Path: t.TempDir()}}
	for _, id := range []string{"all", "agents", "herdr", "sessions", "workspaces", "zoxide", "projects", "review", "team"} {
		t.Run(id, func(t *testing.T) {
			app := New()
			app.cfg = cfg
			app.asyncTUIRun = func(_ context.Context, _ []tui.SourceProducer, _ string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
				model := tui.NewModelWithLayout(nil, nil, layout)
				if model.ActiveTab() != id {
					t.Errorf("active view = %q, want %q", model.ActiveTab(), id)
				}
				return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
			}
			cmd := app.openCmd()
			cmd.SetArgs([]string{"--view", id})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
	app := New()
	app.cfg = cfg
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "missing"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "team") {
		t.Fatalf("unknown view error = %v", err)
	}
	cmd = app.openCmd()
	cmd.SetArgs([]string{"--view", "agents", "--path", "/tmp"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--path") {
		t.Fatalf("path conflict = %v", err)
	}
	cmd = app.openCmd()
	cmd.SetArgs([]string{"--view", "agents", "."})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("dot conflict = %v", err)
	}
	cmd = app.openCmd()
	cmd.SetArgs([]string{"--view", ""})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty view error = %v", err)
	}
	cmd = app.openCmd()
	cmd.SetArgs([]string{"--agents"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("removed flag = %v", err)
	}
}

func TestOpen_AgentIconViaRegistryInViewAndGroup(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.Sources.Agents.Icon = "X "
	cfg.General.SourceOrder = []string{config.SourceAgents}
	cfg.TUI.Tabs = []string{"all", "agents", "team"}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Name: "Team", Type: config.WorkspaceTypeGroup, Path: t.TempDir(), SourceOrder: []string{config.SourceAgents}}}
	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "Team", CWD: cfg.Workspaces[0].Path}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "work"}},
		Panes:      []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "pi", AgentStatus: "idle", CWD: cfg.Workspaces[0].Path}},
	}
	app := New(WithHerdrDriver(&openDriver{detect: true, snapshot: snapshot}))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}

	for _, view := range []string{"", "agents", "team"} {
		t.Run("view="+view, func(t *testing.T) {
			var found bool
			for _, producer := range app.streamingProducersForView(context.Background(), view) {
				msg := producer(context.Background())
				if msg.Snapshot == nil {
					continue
				}
				if got := msg.SnapshotIcons[config.SourceAgents]; got != "X " {
					t.Errorf("snapshot agents icon = %q, want X space", got)
				}
				for _, candidate := range msg.Candidates {
					if candidate.Source == config.SourceAgents {
						found = true
						if candidate.Icon != "X " {
							t.Errorf("agent candidate icon = %q, want X space", candidate.Icon)
						}
					}
				}
			}
			if !found && view != "team" {
				t.Fatal("agents source was absent from the shared snapshot producer")
			}
		})
	}

	layout := app.pickerLayout(cfg.General.SourceOrder, nil)
	if layout.Tabs[2].Kind != tui.TabGroup || layout.Tabs[2].Load == nil {
		t.Fatalf("group tab has no loader: %+v", layout.Tabs[2])
	}
	group, err := layout.Tabs[2].Load(context.Background(), &snapshot)
	if err != nil || len(group) != 1 || group[0].Source != config.SourceAgents || group[0].Icon != "X " {
		t.Fatalf("group agent rows = %+v, error = %v; want configured icon", group, err)
	}
}

func TestOpen_ConfiguredTabsAndHiddenAgents(t *testing.T) {
	app := New()
	cfg := config.Defaults()
	cfg.TUI.Tabs = []string{"projects", "review", "team"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "review"}}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Name: "Team", Type: config.WorkspaceTypeGroup, Path: t.TempDir(), SourceOrder: []string{config.SourceProjects}}}
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.herdrDriver = &openDriver{detect: true, snapshot: source.Snapshot{Panes: []source.Pane{{ID: "p1", Agent: "opencode"}}}}
	app.herdrDriverInjected = true
	var captured tui.Layout
	var capturedProducers []tui.SourceProducer
	app.asyncTUIRun = func(_ context.Context, producers []tui.SourceProducer, _ string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		capturedProducers = producers
		captured = layout
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "agents"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if captured.InitialTab != "agents" {
		t.Errorf("initial tab = %q", captured.InitialTab)
	}
	foundSnapshot := false
	for _, producer := range capturedProducers {
		msg := producer(context.Background())
		for _, name := range msg.SnapshotSources {
			if name == config.SourceAgents {
				foundSnapshot = true
			}
		}
	}
	if !foundSnapshot {
		t.Error("agents view did not schedule the agents snapshot provider")
	}
	if len(captured.Tabs) != 4 || captured.Tabs[1].Kind != tui.TabCustomSource || captured.Tabs[2].Kind != tui.TabGroup || captured.Tabs[2].Load == nil || captured.Tabs[3].ID != "agents" {
		t.Fatalf("configured tabs = %+v", captured.Tabs)
	}
}

func TestOpen_ViewPickerIgnoresFzfAndDirectWithQuery(t *testing.T) {
	cfg, _ := seedCfg(t, "team")
	cfg.Ranking.Enabled = false
	cfg.General.Selector = config.SelectorFzf
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.TUI.Tabs = []string{"all"}
	app := New()
	app.cfg = cfg
	called := false
	app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		called = true
		if query != "team" || layout.InitialTab != "workspaces" {
			t.Fatalf("query = %q, view = %q", query, layout.InitialTab)
		}
		var candidates []source.Candidate
		for _, producer := range producers {
			msg := producer(ctx)
			if msg.Source == config.SourceWorkspaces {
				candidates = append(candidates, msg.Candidates...)
			} else if msg.Source != "ranking" {
				t.Errorf("unrelated producer %q", msg.Source)
			}
		}
		if len(candidates) != 1 {
			t.Fatalf("want one candidate, got %+v", candidates)
		}
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "workspaces", "team"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("built-in picker not called for sole exact match")
	}
}

func TestOpen_ViewGroupOnlyLoadsScopedSources(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.General.SourceOrder = []string{"unrelated"}
	cfg.General.Selector = config.SelectorFzf
	cfg.TUI.Tabs = []string{"all"}
	cfg.Sources.Projects.Markers = []string{".git"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "unrelated", Command: []string{"false"}}}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}}}
	app := New()
	app.cfg = cfg
	app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		if query != "project" || layout.InitialTab != "team" || len(layout.Tabs) != 2 || layout.Tabs[1].Load == nil {
			t.Fatalf("query %q, layout %+v", query, layout)
		}
		for _, producer := range producers {
			msg := producer(ctx)
			if msg.Source == "unrelated" {
				t.Error("unrelated external command scheduled")
			}
		}
		rows, err := layout.Tabs[1].Load(ctx, nil)
		if err != nil || len(rows) != 1 || rows[0].Path != project {
			t.Fatalf("group rows = %+v, error = %v", rows, err)
		}
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "team", "project"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestOpen_ViewBypassesSynchronousSelectorOverride(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.General.Selector = config.SelectorFzf
	app := New()
	app.cfg = cfg
	app.selectorBuilder = func() *selector.Cascade {
		t.Fatal("synchronous cascade was invoked for an explicit view")
		return nil
	}
	called := false
	app.asyncTUIRun = func(_ context.Context, _ []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		called = true
		if query != "find" || layout.InitialTab != "agents" {
			t.Errorf("query = %q, initial view = %q", query, layout.InitialTab)
		}
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "agents", "find"})
	if err := cmd.Execute(); err != nil || !called {
		t.Fatalf("async picker called = %v, error = %v", called, err)
	}
}

func TestOpen_ViewAmbiguousID(t *testing.T) {
	cfg := config.Defaults()
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "team"}}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Type: config.WorkspaceTypeGroup}}
	app := New()
	app.cfg = cfg
	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "team"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous view error = %v", err)
	}
}

func TestOpen_VisibleAgentsTabUsesSingleSnapshotWithoutEnablingAll(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.General.SourceOrder = []string{config.SourceProjects}
	cfg.TUI.Tabs = []string{"all", "agents"}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	driver := &openDriver{detect: true, snapshot: source.Snapshot{Panes: []source.Pane{{ID: "p1", Agent: "opencode"}}}}
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	producers := app.buildStreamingProducers(context.Background())
	agents := 0
	for _, producer := range producers {
		msg := producer(context.Background())
		for _, c := range msg.Candidates {
			if c.Source == config.SourceAgents {
				agents++
			}
		}
	}
	if agents != 1 || driver.snapshotCalls != 1 {
		t.Fatalf("agents = %d, snapshots = %d; want one of each", agents, driver.snapshotCalls)
	}
}

func TestOpen_GroupTabHerdrUsesSharedSnapshotOutsideAll(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	cfg.General.SourceOrder = []string{config.SourceProjects}
	cfg.TUI.Tabs = []string{"team"}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Name: "Team", Type: config.WorkspaceTypeGroup, Path: t.TempDir(), SourceOrder: []string{config.SourceHerdr}}}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	driver := &openDriver{detect: true, snapshot: source.Snapshot{Workspaces: []source.Workspace{{ID: "w1", Label: "one"}}}}
	app.herdrDriver = driver
	app.herdrDriverInjected = true
	app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, _ string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		var snapshot *source.Snapshot
		for _, producer := range producers {
			msg := producer(ctx)
			if msg.Snapshot != nil {
				snapshot = msg.Snapshot
			}
		}
		if snapshot == nil {
			t.Fatal("group tab did not request shared snapshot")
		}
		_, err := layout.Tabs[0].Load(ctx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if driver.snapshotCalls != 1 {
			t.Fatalf("snapshot calls = %d, want 1", driver.snapshotCalls)
		}
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	if err := app.openCmd().Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestOpen_GroupTabScopedCandidates(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "project", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Sources.Projects.Markers = []string{".git"}
	cfg.Ranking.Enabled = false
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.TUI.Tabs = []string{"team"}
	cfg.Workspaces = []config.WorkspaceConfig{{ID: "team", Name: "Team", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}, Template: "default"}}
	app := New()
	app.cfg = cfg
	app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		if len(layout.Tabs) != 1 || layout.Tabs[0].Load == nil {
			t.Fatalf("group tab = %+v", layout.Tabs)
		}
		candidates, err := layout.Tabs[0].Load(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0].Path != filepath.Join(root, "project") {
			t.Fatalf("scoped candidates = %+v", candidates)
		}
		if candidates[0].Meta["parent_template"] != "default" {
			t.Errorf("group template = %+v", candidates[0].Meta)
		}
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}
	cmd := app.openCmd()
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestOpen_ViewAgentsSetsInitialTab(t *testing.T) {
	t.Parallel()
	var capturedLayout tui.Layout
	app := New()
	app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
		capturedLayout = layout
		return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
	}

	cmd := app.openCmd()
	cmd.SetArgs([]string{"--view", "agents"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedLayout.InitialTab != "agents" {
		t.Errorf("captured InitialTab = %q, want agents", capturedLayout.InitialTab)
	}
}
