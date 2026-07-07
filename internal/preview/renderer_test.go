package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// fakeRunner is a scriptable CommandRunner for renderer tests.
type fakeRunner struct {
	out map[string]string // argv[0] -> output
	err map[string]error
}

func (f *fakeRunner) Run(_ context.Context, argv []string, _ string, _ int) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	if err, ok := f.err[argv[0]]; ok {
		return "", err
	}
	return f.out[argv[0]], nil
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
	res, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res.Text
}

func cfgWithDefault(names ...string) *config.Config {
	cfg := config.Defaults()
	cfg.Preview.Default = names
	return cfg
}

// TestRender_IdentityLayout confirms the built-in "identity" section shows
// label, path, source and omits template when absent.
func TestRender_IdentityLayout(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity), config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("identity layout:\n got %q\nwant %q", got, want)
	}
}

// TestRender_IdentityWithTemplate includes a matched template line.
func TestRender_IdentityWithTemplate(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity), config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", "dev"))
	want := "foo\npath: /p/foo\nsource: workspaces\ntemplate: dev"
	if got != want {
		t.Errorf("identity+template:\n got %q\nwant %q", got, want)
	}
}

// TestRender_IdentityAndGit confirms identity+git compose with a blank-line
// separator and the git line renders "git: <summary>".
func TestRender_IdentityAndGit(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity, config.PreviewGit), config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main", Dirty: 2}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces\n\ngit: main (2 changes)"
	if got != want {
		t.Errorf("identity+git:\n got %q\nwant %q", got, want)
	}
}

// TestRender_GitBypassed confirms the git section is entirely omitted when
// probes report git missing, or when Summary errors (slow/missing/timeout),
// and that the overall preview falls back to the identity built-in instead
// of a blank success once the only configured section contributes nothing.
func TestRender_GitBypassed(t *testing.T) {
	t.Parallel()

	t.Run("git probe off", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(cfgWithDefault(config.PreviewGit), config.Probes{Git: false},
			&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
		if strings.Contains(got, "git:") {
			t.Errorf("git section must be omitted when probe off: %q", got)
		}
		if !strings.HasPrefix(got, "foo\npath:") {
			t.Errorf("expected identity fallback instead of blank preview, got %q", got)
		}
	})
	t.Run("git summary errors", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(cfgWithDefault(config.PreviewGit), config.Probes{Git: true},
			&fakeGit{err: errors.New("timeout")}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
		if strings.Contains(got, "git:") {
			t.Errorf("git section must be omitted on summary error: %q", got)
		}
		if !strings.HasPrefix(got, "foo\npath:") {
			t.Errorf("expected identity fallback instead of blank preview, got %q", got)
		}
	})
}

// TestRender_NoDefaultFallsBackToIdentity confirms an empty [preview].default
// still renders something (the built-in identity fallback) rather than a
// blank preview.
func TestRender_NoDefaultFallsBackToIdentity(t *testing.T) {
	t.Parallel()
	r := NewRenderer(config.Defaults(), config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if !strings.HasPrefix(got, "foo\npath:") {
		t.Errorf("expected identity fallback, got %q", got)
	}
}

// TestRender_AllConfiguredSectionsFailFallsBackToIdentity is the blocking
// case: every configured section is non-applicable/fails/empty (git probed
// off, a custom command erroring, workspace/active_pane with no driver).
// Render must never come back as a blank success; it must fall back to a
// clean identity preview instead, and any command failure must stay hidden
// (never surfaced as an error/warning).
func TestRender_AllConfiguredSectionsFailFallsBackToIdentity(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewGit, "broken", config.PreviewWorkspace, config.PreviewActivePane)
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"broken": {Command: "false"},
	}
	runner := &fakeRunner{err: map[string]error{"false": errors.New("exit 1")}}
	r := NewRenderer(cfg, config.Probes{Git: false}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("expected clean identity fallback, got %q", got)
	}
}

// TestResolvePreviewNames_Precedence exercises the documented precedence
// chain end to end: workspace.preview > wildcard.preview >
// sources.<source>.preview > preview.default > built-in fallback.
func TestResolvePreviewNames_Precedence(t *testing.T) {
	t.Parallel()

	t.Run("workspace preview wins over everything", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		cfg.Wildcards = []config.WildcardConfig{{Pattern: "/p/*", Preview: []string{config.PreviewIdentity}}}
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "foo", Path: "/p/foo", Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/p/foo", Source: config.SourceHerdr})
		want := []string{"custom"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("wildcard wins over source and default", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		cfg.Wildcards = []config.WildcardConfig{{Pattern: "/p/*", Preview: []string{config.PreviewIdentity}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/p/foo", Source: config.SourceHerdr})
		if len(got) != 1 || got[0] != config.PreviewIdentity {
			t.Errorf("got %v want [identity]", got)
		}
	})

	t.Run("source preview wins over default", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceHerdr})
		if len(got) != 1 || got[0] != config.PreviewDir {
			t.Errorf("got %v want [dir]", got)
		}
	})

	t.Run("default used when nothing else matches", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != config.PreviewGit {
			t.Errorf("got %v want [git]", got)
		}
	})

	t.Run("built-in fallback when nothing configured at all", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != config.PreviewIdentity {
			t.Errorf("got %v want [identity]", got)
		}
	})
}

// TestResolvePreviewNames_TildeWorkspacePathExpands is a characterization
// test locking down the workspace-preview path-match tier's tilde
// expansion after resolvePreviewNames was switched from a package-local
// expandTildeLocal duplicate to pathutil.ExpandTilde. A workspace configured
// with a leading "~/" must still match a candidate whose path was already
// resolved to the real home directory.
func TestResolvePreviewNames_TildeWorkspacePathExpands(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no resolvable home dir: %v", err)
	}
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "tilde", Path: "~/shep-tilde-test", Preview: []string{"custom"}}}
	cand := source.Candidate{Path: filepath.Join(home, "shep-tilde-test"), Source: config.SourceZoxide}

	got := resolvePreviewNames(cfg, cand)

	if len(got) != 1 || got[0] != "custom" {
		t.Errorf("got %v, want [custom] (tilde-prefixed workspace path should expand and match)", got)
	}
}

// TestResolvePreviewNames_SymlinkAliasMatches is a regression guard: this
// scenario must keep matching after the pathutil.SameDir swap (os.SameFile
// is the source of truth for case-fold and symlink equivalence, so no
// separate Normalize pass is needed at this call site). A workspace
// configured via a symlink must still match a candidate whose path is the
// symlink's real target (and vice versa).
func TestResolvePreviewNames_SymlinkAliasMatches(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	t.Run("workspace is symlink, candidate is real target", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "aliased", Path: link, Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: real, Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != "custom" {
			t.Errorf("got %v, want [custom] (symlink workspace should match its real target)", got)
		}
	})

	t.Run("workspace is real target, candidate is symlink", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "aliased", Path: real, Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: link, Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != "custom" {
			t.Errorf("got %v, want [custom] (real-target workspace should match a symlink candidate)", got)
		}
	})
}

// TestResolvePreviewNames_CaseFoldMatches is a regression guard: this
// scenario must keep matching after the pathutil.SameDir swap (os.SameFile
// is the source of truth for case-fold and symlink equivalence). On a
// case-insensitive filesystem (macOS APFS default, Windows), a workspace
// path differing only in case from the candidate's real directory must
// still match. Gated to darwin/windows because a case-sensitive filesystem
// (most Linux/ext4) genuinely has two distinct directories here.
func TestResolvePreviewNames_CaseFoldMatches(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive match only guaranteed on darwin/windows")
	}
	t.Parallel()
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "ECORP")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(tmp, "ecorp")

	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ecorp", Path: upper, Preview: []string{"custom"}}}
	got := resolvePreviewNames(cfg, source.Candidate{Path: lower, Source: config.SourceZoxide})
	if len(got) != 1 || got[0] != "custom" {
		t.Errorf("got %v, want [custom] (case-insensitive filesystem match)", got)
	}
}

// fakePreviewDriver is a controllable HerdrDriver for workspace/active_pane
// section tests.
type fakePreviewDriver struct {
	tabs       []source.Tab
	panes      []source.Pane
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

func (f *fakePreviewDriver) Detect(context.Context) bool { return true }
func (f *fakePreviewDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, nil
}
func (fakePreviewDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakePreviewDriver.FocusOrCreate not used in previews")
}
func (f *fakePreviewDriver) ListAgents(context.Context) ([]source.Agent, error) { return nil, nil }

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
func (f *fakePreviewDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("not used in previews")
}
func (f *fakePreviewDriver) RenameTab(context.Context, string, string) error {
	return errors.New("not used in previews")
}
func (f *fakePreviewDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("not used in previews")
}
func (f *fakePreviewDriver) RunPane(context.Context, string, string) error {
	return errors.New("not used in previews")
}
func (fakePreviewDriver) FocusTab(context.Context, string) error {
	return errors.New("not used in previews")
}
func (fakePreviewDriver) CurrentPane(context.Context) (source.Pane, error) {
	return source.Pane{}, errors.New("not used in previews")
}

// herdrCandidate builds a candidate carrying a workspace_id meta key, mirroring
// the herdr source provider's output.
func herdrCandidate(label, path, workspaceID string) source.Candidate {
	return source.Candidate{
		Path:   path,
		Label:  label,
		Source: config.SourceHerdr,
		Meta:   map[string]string{"workspace_id": workspaceID},
	}
}

// TestRender_WorkspaceSection renders an indented tabs/panes tree for a
// candidate that carries a workspace_id meta key.
func TestRender_WorkspaceSection(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace)
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
	if !strings.Contains(got, "workspace") {
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

// TestRender_ActivePaneSection renders the focused pane's captured terminal
// buffer, capped at cfg.Preview.MaxLines.
func TestRender_ActivePaneSection(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
	cfg.Preview.MaxLines = 42
	driver := &fakePreviewDriver{
		panes: []source.Pane{
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true},
		},
		readOut: "$ echo hi\nhi\n$ ",
	}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "active pane") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "$ echo hi") {
		t.Errorf("missing pane buffer: %q", got)
	}
	if driver.lastPaneID != "wA:p1" {
		t.Errorf("ReadPane called on %q, want the focused pane wA:p1", driver.lastPaneID)
	}
	if driver.lastLines != 42 {
		t.Errorf("ReadPane lines = %d, want cfg.Preview.MaxLines=42", driver.lastLines)
	}
}

// TestRender_ActivePaneSection_FallsBackToFirstPane reads the first pane
// when none is marked focused.
func TestRender_ActivePaneSection_FallsBackToFirstPane(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
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

// TestRender_HerdrSections_SkipOnNonHerdrCandidate skips workspace and
// active_pane sections when the candidate has no workspace_id meta key.
func TestRender_HerdrSections_SkipOnNonHerdrCandidate(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace, config.PreviewActivePane, config.PreviewIdentity)
	driver := &fakePreviewDriver{tabs: []source.Tab{{ID: "wA:t1"}}}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("non-herdr preview must skip herdr sections:\n got %q\nwant %q", got, want)
	}
	if len(driver.listCalls) != 0 || driver.readCalls != 0 {
		t.Errorf("herdr driver must not be queried for non-herdr candidate: calls=%v read=%d",
			driver.listCalls, driver.readCalls)
	}
}

// TestRender_WorkspaceSection_TimesOutGracefully confirms a slow daemon is
// bounded by the herdr preview timeout and the section degrades to an
// unavailable note instead of hanging the selector.
func TestRender_WorkspaceSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace)
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	start := time.Now()
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("workspace preview took %v, want bounded by 100ms herdr timeout", elapsed)
	}
	if !strings.Contains(got, "workspace") {
		t.Errorf("section heading should still render on timeout: %q", got)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("missing unavailable note on timeout: %q", got)
	}
}

// TestRender_ActivePaneSection_TimesOutGracefully mirrors the workspace
// timeout test for the active_pane section.
func TestRender_ActivePaneSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, config.Probes{}, nil, nil, WithHerdrDriver(driver))
	start := time.Now()
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	elapsed := time.Since(start)
	if elapsed > 400*time.Millisecond {
		t.Fatalf("active_pane preview took %v, want bounded by herdr timeouts", elapsed)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("missing unavailable note on timeout: %q", got)
	}
}

// TestRender_HerdrSections_WithoutDriver degrades gracefully when no driver
// is wired (e.g. herdr not installed): herdr sections are skipped.
func TestRender_HerdrSections_WithoutDriver(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace, config.PreviewIdentity)
	r := NewRenderer(cfg, config.Probes{}, nil, nil)
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	want := "foo\npath: /x\nsource: herdr"
	if got != want {
		t.Errorf("no driver must skip herdr sections:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DirSection_PicksLsdFirst confirms lsd wins when present.
func TestRender_DirSection_PicksLsdFirst(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "lsd" {
			return "/usr/bin/lsd", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"lsd": "lsd-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "lsd-output" {
		t.Errorf("got %q, want lsd-output", got)
	}
}

// TestRender_DirSection_FallsBackToEza confirms eza is used when lsd is
// absent.
func TestRender_DirSection_FallsBackToEza(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "eza" {
			return "/usr/bin/eza", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"eza": "eza-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "eza-output" {
		t.Errorf("got %q, want eza-output", got)
	}
}

// TestRender_DirSection_FallsBackToLs confirms ls is the last resort when
// neither lsd nor eza is present.
func TestRender_DirSection_FallsBackToLs(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"ls": "ls-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "ls-output" {
		t.Errorf("got %q, want ls-output", got)
	}
}

// TestDirArgv_Lsd_UsesColorAlways confirms dirArgv forces --color=always
// into the lsd invocation so the "dir" preview section shows lsd's real
// colored listing (the whole point of shelling out to lsd instead of
// plain ls). This is safe because internal/tui/model.go's truncateToWidth
// is ANSI-aware (charmbracelet/x/ansi.Truncate) and never cuts mid-escape
// sequence — see TestTruncateToWidth_ANSIStyledInput_* in
// internal/tui/model_internal_test.go.
func TestDirArgv_Lsd_UsesColorAlways(t *testing.T) {
	// No t.Parallel(): this test mutates the package-level dirLookPath seam
	// (see dirLookPath in renderer.go), which races under -race against
	// TestDirArgv_Eza_UsesColorAlways if both run concurrently.
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "lsd" {
			return "/usr/bin/lsd", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	got := dirArgv("/p/foo")
	want := []string{"lsd", "-la", "--icon=always", "--color=always", "/p/foo"}
	if !equalArgv(got, want) {
		t.Errorf("dirArgv(lsd) = %v, want %v", got, want)
	}
}

// TestDirArgv_Eza_UsesColorAlways is the eza counterpart of
// TestDirArgv_Lsd_UsesColorAlways.
func TestDirArgv_Eza_UsesColorAlways(t *testing.T) {
	// No t.Parallel(): this test mutates the package-level dirLookPath seam
	// (see dirLookPath in renderer.go), which races under -race against
	// TestDirArgv_Lsd_UsesColorAlways if both run concurrently.
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "eza" {
			return "/usr/bin/eza", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	got := dirArgv("/p/foo")
	want := []string{"eza", "--all", "--git", "--icons", "--color=always", "/p/foo"}
	if !equalArgv(got, want) {
		t.Errorf("dirArgv(eza) = %v, want %v", got, want)
	}
}

func equalArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRender_DirSection_NoRunnerSkipsSilently confirms a missing runner
// simply omits the section instead of erroring, falling back to the
// identity built-in rather than a blank preview.
func TestRender_DirSection_NoRunnerSkipsSilently(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if !strings.HasPrefix(got, "foo\npath:") {
		t.Errorf("expected identity fallback instead of blank preview, got %q", got)
	}
}

// TestRender_CustomCommand_RunsAndSubstitutesPath confirms a declared
// [preview.commands.<name>] entry runs with {path} substituted.
func TestRender_CustomCommand_RunsAndSubstitutesPath(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault("recent_commits")
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"recent_commits": {Command: "git -C {path} log -n 3"},
	}
	runner := &fakeRunner{out: map[string]string{"git": "commit-log"}}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "commit-log" {
		t.Errorf("got %q, want commit-log", got)
	}
}

// TestRender_CustomCommand_FailureHiddenFromNormalOutput confirms a failing
// custom command is silently omitted, not surfaced as an error/warning, per
// the "hide command errors from normal preview output" rule.
func TestRender_CustomCommand_FailureHiddenFromNormalOutput(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewIdentity, "broken")
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"broken": {Command: "git -C {path} log"},
	}
	runner := &fakeRunner{err: map[string]error{"git": errors.New("exit 1")}}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "workspaces", ""))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "foo\npath: /p/foo\nsource: workspaces"
	if res.Text != want {
		t.Errorf("got %q want %q (broken command must be silently omitted)", res.Text, want)
	}
}

// TestRender_UnknownSectionNameSkipped confirms a name that is neither a
// built-in nor a declared command is simply omitted (config validation
// already prevents this at Load time, but the renderer stays defensive).
func TestRender_UnknownSectionNameSkipped(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewIdentity, "does-not-exist")
	r := NewRenderer(cfg, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestRender_CachesResult confirms a second Render call for the same
// candidate/config is served from cache (FromCache=true), avoiding a
// redundant git call.
func TestRender_CachesResult(t *testing.T) {
	t.Parallel()
	git := &fakeGit{summary: GitSummary{Branch: "main"}}
	r := NewRenderer(cfgWithDefault(config.PreviewGit), config.Probes{Git: true}, git, nil)
	cand := candidate("foo", "/p/foo", "workspaces", "")
	first, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if first.FromCache {
		t.Error("first render should not be from cache")
	}
	second, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !second.FromCache {
		t.Error("second render should be served from cache")
	}
	if git.calls != 1 {
		t.Errorf("git.Summary should only be called once, got %d calls", git.calls)
	}
}
