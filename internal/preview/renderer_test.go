package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// fakeGit is a deterministic GitProvider for renderer tests; no real processes.
type fakeGit struct {
	summary GitSummary
	err     error
	calls   int
}

func (f *fakeGit) Summary(_ context.Context, _ string) (GitSummary, error) {
	f.calls++
	if f.err != nil {
		return GitSummary{}, f.err
	}
	return f.summary, nil
}

func candidate(label, path, src, template string) source.Candidate {
	c := source.Candidate{
		Path:   path,
		Label:  label,
		Source: src,
	}
	if template != "" {
		c.Meta = map[string]string{"template": template}
	}
	return c
}

func mustRender(t *testing.T, r Renderer, cand source.Candidate) string {
	t.Helper()
	res, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res.Text
}

// TestRender_DefaultLayout (WP-1) confirms the zero-config built-in preview
// shows label, path, source, and omits template/git when absent.
func TestRender_DefaultLayout(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (clean)"
	if got != want {
		t.Errorf("default layout:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultWithTemplate (WP-1) includes a matched template line.
func TestRender_DefaultWithTemplate(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", "go"))
	want := "foo\npath: /p/foo\nsource: roots\ntemplate: go\ngit: main (clean)"
	if got != want {
		t.Errorf("default+template:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultWithGit (WP-1) appends a git summary line when git is fast
// and available, and stays clean when the branch is clean.
func TestRender_DefaultWithGit(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main", Dirty: 2}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (2 changes)"
	if got != want {
		t.Errorf("default+git:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultGitBypassed (WP-1) confirms the git line is omitted when
// probes report git missing, or when Summary returns an error (slow/missing).
func TestRender_DefaultGitBypassed(t *testing.T) {
	t.Parallel()

	t.Run("git probe off", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: false},
			&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
		if gitSummaryLinePresent(got) {
			t.Errorf("git line must be absent when probe off: %q", got)
		}
	})
	t.Run("git summary errors", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true},
			&fakeGit{err: errors.New("timeout")}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
		if gitSummaryLinePresent(got) {
			t.Errorf("git line must be absent on summary error: %q", got)
		}
	})
}

// gitSummaryLinePresent reports whether any line starts with "git:".
func gitSummaryLinePresent(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if len(line) >= 4 && line[:4] == "git:" {
			return true
		}
	}
	return false
}

// TestRender_DeclarativeSections (WP-2) renders [[preview.sections]] in TOML
// declaration order using the declared field names and section types.
func TestRender_DeclarativeSections(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "Identity", Type: config.PreviewSectionBuiltin, Fields: []string{"label", "path", "source"}},
			{Name: "Git", Type: config.PreviewSectionGit},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "Identity\nlabel: foo\npath: /p/foo\nsource: roots\n\nGit\nmain (clean)"
	if got != want {
		t.Errorf("declarative sections:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DeclarativeBuiltinFieldOrder (WP-2) confirms the renderer honours
// the declared field order within a builtin section, not a fixed order.
func TestRender_DeclarativeBuiltinFieldOrder(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "X", Type: config.PreviewSectionBuiltin, Fields: []string{"source", "label"}},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: true}, &fakeGit{}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "X\nsource: roots\nlabel: foo"
	if got != want {
		t.Errorf("builtin field order:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DeclarativeGitUnavailable (WP-2) renders a git section that cannot
// satisfy the summary as an explicit unavailable note under its heading.
func TestRender_DeclarativeGitUnavailable(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "Git", Type: config.PreviewSectionGit},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: false}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "Git\n(git unavailable)"
	if got != want {
		t.Errorf("git unavailable section:\n got %q\nwant %q", got, want)
	}
}

// fakeRunner is a deterministic CommandRunner for renderer routing tests.
type fakeRunner struct {
	out      string
	err      error
	outs     []string
	errs     []error
	calls    int
	lastArgv []string
	lastDir  string
	lastMax  int
}

func (f *fakeRunner) Run(_ context.Context, argv []string, dir string, maxLines int) (string, error) {
	f.calls++
	f.lastArgv = argv
	f.lastDir = dir
	f.lastMax = maxLines
	idx := f.calls - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return "", f.errs[idx]
	}
	if f.err != nil {
		return "", f.err
	}
	if idx < len(f.outs) {
		return f.outs[idx], nil
	}
	return f.out, nil
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ []string, _ string, _ int) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestRender_CommandRouting (WP-3, 2.9) executes the configured command via the
// injected runner, passes the candidate path as a single substituted argument
// and the candidate directory as dir, and surfaces the runner's stdout.
func TestRender_CommandRouting(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Command:  "git -C {path} log -n 5",
		MaxLines: 50,
	}
	runner := &fakeRunner{out: "commit-a\ncommit-b"}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if res.Text != "commit-a\ncommit-b" {
		t.Errorf("text: got %q want %q", res.Text, "commit-a\\ncommit-b")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls: got %d want 1", runner.calls)
	}
	wantArgv := []string{"git", "-C", "/p/foo", "log", "-n", "5"}
	if !stringSliceEqual(runner.lastArgv, wantArgv) {
		t.Errorf("argv: got %v want %v", runner.lastArgv, wantArgv)
	}
	if runner.lastDir != "/p/foo" {
		t.Errorf("dir: got %q want %q", runner.lastDir, "/p/foo")
	}
	if runner.lastMax != 50 {
		t.Errorf("maxlines: got %d want 50", runner.lastMax)
	}
	if res.Warning != "" {
		t.Errorf("unexpected warning %q", res.Warning)
	}
}

// TestRender_CommandFallback (WP-3, 2.9) falls back to the built-in preview and
// records a transient warning when the command fails.
func TestRender_CommandFallback(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "broken-cmd"}
	runner := &fakeRunner{err: errors.New("exit 127")}
	r := NewRenderer(cfg, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	if err != nil {
		t.Fatalf("render error must be nil on fallback: %v", err)
	}
	// Fallback is the built-in default layout (label/path/source, then git).
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (clean)"
	if res.Text != want {
		t.Errorf("fallback text:\n got %q\nwant %q", res.Text, want)
	}
	if res.Warning == "" {
		t.Error("expected a non-empty warning on command failure")
	}
	if res.Warning != "preview command failed" {
		t.Errorf("warning: got %q want generic command failure", res.Warning)
	}
}

// TestRender_CommandTimeoutBounded applies preview.timeout at renderer level so
// even a blocking runner falls back quickly with a safe warning.
func TestRender_CommandTimeoutBounded(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "slow {path}", Timeout: config.Duration(20 * time.Millisecond)}
	r := NewRenderer(cfg, config.Probes{}, nil, blockingRunner{})
	start := time.Now()
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("render error must be nil on timeout fallback: %v", err)
	}
	if res.Warning != "preview command timed out" {
		t.Errorf("warning: got %q want timeout warning", res.Warning)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("timeout fallback took %v, want bounded elapsed", elapsed)
	}
	if res.Text != "foo\npath: /p/foo\nsource: roots" {
		t.Errorf("fallback text: %q", res.Text)
	}
}

// TestRender_CommandFailureIsNotCached retries a failed command on the next
// render for the same path and clears the transient warning after success.
func TestRender_CommandFailureIsNotCached(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "preview {path}", CacheTTL: 0}
	runner := &fakeRunner{outs: []string{"", "fresh output"}, errs: []error{errors.New("boom"), nil}}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	cand := candidate("foo", "/p/foo", "roots", "")
	first, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if first.Warning != "preview command failed" {
		t.Fatalf("first warning: got %q", first.Warning)
	}
	second, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if second.Text != "fresh output" {
		t.Errorf("second text: got %q want fresh command output", second.Text)
	}
	if second.Warning != "" {
		t.Errorf("second warning must be cleared, got %q", second.Warning)
	}
	if second.FromCache {
		t.Error("second render after failure must retry, not come from cache")
	}
	if runner.calls != 2 {
		t.Errorf("runner calls: got %d want 2", runner.calls)
	}
}

// TestRender_CommandCache (WP-3, 2.9) serves the second render for the same
// candidate from cache without invoking the runner again.
func TestRender_CommandCache(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "echo hi", CacheTTL: 0} // 0 -> no expiry by time
	runner := &fakeRunner{out: "hi"}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	cand := candidate("foo", "/p/foo", "roots", "")
	if _, err := r.Render(context.Background(), cand, RenderOptions{}); err != nil {
		t.Fatalf("first render: %v", err)
	}
	res, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !res.FromCache {
		t.Error("second render must come from cache (FromCache=true)")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls after cache hit: got %d want 1", runner.calls)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- PR4: herdr-backed workspace / active_pane preview sections ---

// fakePreviewDriver is a controllable source.HerdrDriver for renderer tests. It
// records the queries and returns scripted tab/pane/agent/read results. The
// action methods (FocusOrCreate, RunStartup) are irrelevant to previews and
// return errors so any accidental call fails loudly.
type fakePreviewDriver struct {
	detect     bool
	tabs       []source.Tab
	panes      []source.Pane
	agents     []source.Agent
	readOut    string
	tabsErr    error
	panesErr   error
	readErr    error
	listCalls  []string
	readCalls  int
	lastLines  int
	lastPaneID string
	// block switches each herdr query into a ctx-bound blocker that returns
	// ctx.Err() — used for the timeout tests.
	block bool
}

func (f *fakePreviewDriver) Detect(context.Context) bool { return f.detect }
func (f *fakePreviewDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, nil
}
func (fakePreviewDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakePreviewDriver.FocusOrCreate not used in previews")
}
func (fakePreviewDriver) RunStartup(context.Context, string, string) error {
	return errors.New("fakePreviewDriver.RunStartup not used in previews")
}
func (f *fakePreviewDriver) ListAgents(context.Context) ([]source.Agent, error) {
	return f.agents, nil
}

func (f *fakePreviewDriver) ListTabs(ctx context.Context, workspaceID string) ([]source.Tab, error) {
	f.listCalls = append(f.listCalls, "tabs:"+workspaceID)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.tabs, f.tabsErr
}
func (f *fakePreviewDriver) ListPanes(ctx context.Context, workspaceID string) ([]source.Pane, error) {
	f.listCalls = append(f.listCalls, "panes:"+workspaceID)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.panes, f.panesErr
}
func (f *fakePreviewDriver) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	f.readCalls++
	f.lastPaneID = paneID
	f.lastLines = lines
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.readOut, f.readErr
}

// herdrCandidate builds a candidate carrying a workspace_id meta key, mirroring
// the herdr source provider's output.
func herdrCandidate(label, path, workspaceID string) source.Candidate {
	return source.Candidate{
		Path:   path,
		Label:  label,
		Source: "herdr",
		Meta:   map[string]string{"workspace_id": workspaceID},
	}
}

// TestRender_WorkspaceSection (PR4) renders an indented tabs/panes tree for a
// candidate that carries a workspace_id meta key.
func TestRender_WorkspaceSection(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Workspace", Type: config.PreviewSectionWorkspace},
		},
	}
	driver := &fakePreviewDriver{
		tabs: []source.Tab{
			{ID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 2},
			{ID: "wA:t2", WorkspaceID: "wA", Label: "term", Focused: false, Number: 2, PaneCount: 1},
		},
		panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true},
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
		},
	}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "Workspace") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "edit") || !strings.Contains(got, "term") {
		t.Errorf("missing tab labels: %q", got)
	}
	if !strings.Contains(got, "*") {
		t.Errorf("missing focused marker for active tab: %q", got)
	}
	if !strings.Contains(got, "/x") || !strings.Contains(got, "/y") {
		t.Errorf("missing pane cwds: %q", got)
	}
	if len(driver.listCalls) != 2 {
		t.Errorf("expected 2 herdr list calls, got %d: %v", len(driver.listCalls), driver.listCalls)
	}
}

// TestRender_ActivePaneSection (PR4) renders the focused pane's captured
// terminal buffer, capped at MaxLines.
func TestRender_ActivePaneSection(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 42,
		Sections: []config.PreviewSection{
			{Name: "ActivePane", Type: config.PreviewSectionActivePane},
		},
	}
	driver := &fakePreviewDriver{
		panes: []source.Pane{
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true},
		},
		readOut: "$ echo hi\nhi\n$ ",
	}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "ActivePane") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "$ echo hi") {
		t.Errorf("missing pane buffer: %q", got)
	}
	if driver.lastPaneID != "wA:p1" {
		t.Errorf("ReadPane called on %q, want the focused pane wA:p1", driver.lastPaneID)
	}
	if driver.lastLines != 42 {
		t.Errorf("ReadPane lines = %d, want cfg.MaxLines=42", driver.lastLines)
	}
}

// TestRender_ActivePaneSection_FallsBackToFirstPane (PR4) reads the first pane
// when none is marked focused.
func TestRender_ActivePaneSection_FallsBackToFirstPane(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Pane", Type: config.PreviewSectionActivePane},
		},
	}
	driver := &fakePreviewDriver{
		panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: false},
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
		},
		readOut: "buffer",
	}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "buffer") {
		t.Errorf("missing buffer: %q", got)
	}
	if driver.lastPaneID != "wA:p1" {
		t.Errorf("ReadPane called on %q, want first pane wA:p1", driver.lastPaneID)
	}
}

// TestRender_HerdrSections_SkipOnNonHerdrCandidate (PR4) skips workspace and
// active_pane sections when the candidate has no workspace_id meta key.
func TestRender_HerdrSections_SkipOnNonHerdrCandidate(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Workspace", Type: config.PreviewSectionWorkspace},
			{Name: "Pane", Type: config.PreviewSectionActivePane},
			{Name: "Identity", Type: config.PreviewSectionBuiltin, Fields: []string{"label"}},
		},
	}
	driver := &fakePreviewDriver{tabs: []source.Tab{{ID: "wA:t1"}}}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	// Plain roots candidate, no workspace_id meta.
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "Identity\nlabel: foo"
	if got != want {
		t.Errorf("non-herdr preview must skip herdr sections:\n got %q\nwant %q", got, want)
	}
	if len(driver.listCalls) != 0 || driver.readCalls != 0 {
		t.Errorf("herdr driver must not be queried for non-herdr candidate: calls=%v read=%d",
			driver.listCalls, driver.readCalls)
	}
}

// TestRender_WorkspaceSection_TimesOutGracefully (PR4 goal 2) confirms a slow
// daemon is bounded by the 100ms preview timeout and the section degrades to a
// muted unavailable note instead of hanging the selector.
func TestRender_WorkspaceSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Workspace", Type: config.PreviewSectionWorkspace},
		},
	}
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	start := time.Now()
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("workspace preview took %v, want bounded by 100ms herdr timeout", elapsed)
	}
	if !strings.Contains(got, "Workspace") {
		t.Errorf("section heading should still render on timeout: %q", got)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("missing muted unavailable note on timeout: %q", got)
	}
}

// TestRender_ActivePaneSection_TimesOutGracefully (PR4 goal 2).
func TestRender_ActivePaneSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Pane", Type: config.PreviewSectionActivePane},
		},
	}
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	start := time.Now()
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	elapsed := time.Since(start)
	if elapsed > 400*time.Millisecond {
		t.Fatalf("active_pane preview took %v, want bounded by herdr timeouts", elapsed)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("missing muted unavailable note on timeout: %q", got)
	}
}

// TestRender_HerdrSections_WithoutDriver (PR4) degrades gracefully when no
// driver is wired (e.g. herdr not installed): herdr sections are skipped.
func TestRender_HerdrSections_WithoutDriver(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		MaxLines: 50,
		Sections: []config.PreviewSection{
			{Name: "Workspace", Type: config.PreviewSectionWorkspace},
			{Name: "Identity", Type: config.PreviewSectionBuiltin, Fields: []string{"label"}},
		},
	}
	r := NewRenderer(cfg, config.Probes{}, nil, nil)
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	want := "Identity\nlabel: foo"
	if got != want {
		t.Errorf("no driver must skip herdr sections:\n got %q\nwant %q", got, want)
	}
}
