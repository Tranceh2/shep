package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/workspacename"
)

// fakeDriver is a controllable HerdrDriver for tests.
type fakeDriver struct {
	detect     bool
	workspaces []Workspace
	listErr    error
}

// sessionDriver composes the existing fake with an observable session-list
// boundary so session provider tests never shell out to Herdr.
type sessionDriver struct {
	fakeDriver
	sessions     []Session
	sessionsErr  error
	sessionsCall int
	deadline     time.Time
}

func (d *sessionDriver) ListSessions(ctx context.Context) ([]Session, error) {
	d.sessionsCall++
	d.deadline, _ = ctx.Deadline()
	if d.sessionsErr != nil {
		return nil, d.sessionsErr
	}
	return append([]Session(nil), d.sessions...), nil
}

func (f fakeDriver) Detect(context.Context) bool { return f.detect }
func (f fakeDriver) Snapshot(context.Context) (Snapshot, error) {
	if f.listErr != nil {
		return Snapshot{}, f.listErr
	}
	snapshot := Snapshot{Workspaces: make([]Workspace, 0, len(f.workspaces))}
	for _, workspace := range f.workspaces {
		snapshot.Workspaces = append(snapshot.Workspaces, workspace)
		if workspace.CWD != "" {
			snapshot.Panes = append(snapshot.Panes, Pane{ID: workspace.ID + ":p1", WorkspaceID: workspace.ID, CWD: workspace.CWD})
		}
	}
	return snapshot, nil
}
func (fakeDriver) ListSessions(context.Context) ([]Session, error) { return nil, nil }
func (fakeDriver) FocusOrCreate(context.Context, WorkspaceLaunchRequest) (FocusResult, error) {
	return FocusResult{}, errors.New("fakeDriver does not implement FocusOrCreate")
}

func TestWorkspaceLaunchRequestCarriesNameSeparately(t *testing.T) {
	t.Parallel()
	request := WorkspaceLaunchRequest{
		Candidate:     Candidate{Path: "/srv/platform-api", Label: "display-label"},
		WorkspaceName: workspacename.Name("launch-label"),
	}
	if request.Candidate.Label == string(request.WorkspaceName) {
		t.Fatal("launch name must remain separate from candidate label")
	}
	if got, want := string(request.WorkspaceName), "launch-label"; got != want {
		t.Fatalf("workspace name = %q, want %q", got, want)
	}
}
func (fakeDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("fakeDriver does not implement ReadPane")
}
func (fakeDriver) CreateTab(context.Context, string, string, string, bool) (Tab, Pane, error) {
	return Tab{}, Pane{}, errors.New("fakeDriver does not implement CreateTab")
}
func (fakeDriver) RenameTab(context.Context, string, string) error {
	return errors.New("fakeDriver does not implement RenameTab")
}
func (fakeDriver) RenamePane(context.Context, string, *string) error {
	return errors.New("fakeDriver does not implement RenamePane")
}
func (fakeDriver) SplitPane(context.Context, string, string, float64, string, bool) (Pane, error) {
	return Pane{}, errors.New("fakeDriver does not implement SplitPane")
}
func (fakeDriver) RunPane(context.Context, string, string) error {
	return errors.New("fakeDriver does not implement RunPane")
}
func (fakeDriver) FocusTab(context.Context, string) error {
	return errors.New("fakeDriver does not implement FocusTab")
}

// TestCandidate_Clone ensures Meta is deep-copied so callers cannot mutate a
// provider's internal map through a returned candidate.
func TestCandidate_Clone(t *testing.T) {
	t.Parallel()
	c := Candidate{Path: "/x", Meta: map[string]string{"a": "1"}}
	clone := c.Clone()
	clone.Meta["a"] = "mutated"
	if c.Meta["a"] == "mutated" {
		t.Error("Clone shared Meta map with original")
	}
}

// TestSupportsCurrentWorkspaceTarget is the shared predicate backing the
// ctrl+t/ctrl+p TUI bindings and the command-layer disallowTarget gate: it
// reports whether a candidate can be opened as a new tab/pane inside the
// Herdr workspace shep is running in. zoxide/projects always can; a
// [[workspaces]] entry can only when it carries a command and is neither a
// group nor a template; herdr (already-open), plain paths, and unknown
// sources never can.
func TestSupportsCurrentWorkspaceTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cand Candidate
		want bool
	}{
		{name: "command-only workspace", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": "nvim"}}, want: true},
		{name: "zoxide", cand: Candidate{Source: config.SourceZoxide}, want: true},
		{name: "projects", cand: Candidate{Source: config.SourceProjects}, want: true},
		{name: "group workspace excluded", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}, want: false},
		{name: "template workspace excluded", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"template": "k8s"}}, want: false},
		{name: "plain workspace no command excluded", cand: Candidate{Source: config.SourceWorkspaces}, want: false},
		{name: "empty-command workspace excluded", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": ""}}, want: false},
		{name: "herdr already-open excluded", cand: Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wA"}}, want: false},
		{name: "command plus group edge excluded", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": "nvim", "group": "true"}}, want: false},
		{name: "command plus template edge excluded", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": "nvim", "template": "k8s"}}, want: false},
		{name: "unknown source excluded", cand: Candidate{Source: "path"}, want: false},
		{name: "synthesized tree child excluded", cand: Candidate{Meta: map[string]string{"workspace_id": "wA", "tab_id": "t1"}}, want: false},
		{name: "nil meta command-only workspace", cand: Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": "yazi", "close_on_exit": "true"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SupportsCurrentWorkspaceTarget(tt.cand); got != tt.want {
				t.Errorf("SupportsCurrentWorkspaceTarget(%+v) = %v, want %v", tt.cand, got, tt.want)
			}
		})
	}
}

// TestHerdrProvider_NilDriverEmpty exercises the "driver absent" path: the
// provider lists no candidates and never panics.
func TestHerdrProvider_NilDriverEmpty(t *testing.T) {
	t.Parallel()
	p := &herdrProvider{driver: nil, probes: config.Probes{Herdr: true}, cfg: config.Defaults()}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates with nil driver, got %d", len(cands))
	}
}

// TestHerdrProvider_WithDriver turns a snapshot into candidates while keeping
// a workspace with no derived CWD visible and explicitly marked missing.
func TestHerdrProvider_WithDriver(t *testing.T) {
	t.Parallel()
	driver := fakeDriver{detect: true, workspaces: []Workspace{
		{ID: "w1", Label: "foo", CWD: "/tmp/foo"},
		{ID: "w2", Label: "", CWD: "/tmp/bar"},
		{ID: "w3", Label: "empty", CWD: ""},
	}}
	p := &herdrProvider{driver: driver, probes: config.Probes{Herdr: true}, cfg: config.Defaults()}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 workspaces including the missing-CWD entry, got %d", len(cands))
	}
	if cands[0].Label != "foo" {
		t.Errorf("human label: got %q, want %q", cands[0].Label, "foo")
	}
	if cands[1].Label != "" {
		t.Errorf("missing human label must stay empty instead of deriving from path or ID: got %q", cands[1].Label)
	}
	if got := cands[1].Meta["workspace_id"]; got != "w2" {
		t.Errorf("stable workspace ID must remain metadata: got %q, want %q", got, "w2")
	}
	if cands[0].Meta["workspace_id"] != "w1" {
		t.Errorf("workspace_id meta not propagated: %v", cands[0].Meta)
	}
	if cands[0].Source != config.SourceHerdr {
		t.Errorf("source: got %q want %q", cands[0].Source, config.SourceHerdr)
	}
	if got := cands[2]; got.Meta["workspace_id"] != "w3" || !got.Missing || got.Path != "" {
		t.Errorf("missing-CWD candidate = %+v, want visible missing workspace w3", got)
	}
}

// TestHerdrProvider_DisabledWhenBinaryMissing ensures gating by probes.Herdr.
func TestHerdrProvider_DisabledWhenBinaryMissing(t *testing.T) {
	t.Parallel()
	p := &herdrProvider{driver: fakeDriver{}, probes: config.Probes{Herdr: false}, cfg: config.Defaults()}
	if p.enabled(config.Defaults(), config.Probes{Herdr: false}) {
		t.Error("herdr should be disabled when binary probe is false")
	}
}

// TestHerdrProvider_ListError surfaces the driver error instead of swallowing it.
func TestHerdrProvider_ListError(t *testing.T) {
	t.Parallel()
	want := errors.New("daemon down")
	p := &herdrProvider{driver: fakeDriver{listErr: want}, probes: config.Probes{Herdr: true}, cfg: config.Defaults()}
	if _, err := p.List(context.Background()); err == nil {
		t.Fatal("expected driver error to propagate")
	}
}

// TestHerdrProvider_MissingDerivedCWDMarkedMissing confirms a workspace with
// no pane-derived CWD stays visible but is marked Missing for clear selection
// feedback instead of being silently dropped.
func TestHerdrProvider_MissingDerivedCWDMarkedMissing(t *testing.T) {
	t.Parallel()
	live := t.TempDir()
	driver := fakeDriver{detect: true, workspaces: []Workspace{
		{ID: "w1", Label: "live", CWD: live},
		{ID: "w2", Label: "no-cwd"},
	}}
	p := &herdrProvider{driver: driver, probes: config.Probes{Herdr: true}, cfg: config.Defaults()}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]Candidate{}
	for _, c := range cands {
		byID[c.Meta["workspace_id"]] = c
	}
	if byID["w1"].Missing {
		t.Errorf("live workspace %q must not be marked Missing", live)
	}
	if !byID["w2"].Missing || byID["w2"].Path != "" {
		t.Errorf("missing-CWD workspace must remain visible and Missing, got %+v", byID["w2"])
	}
}

// TestSessionsProvider_MapsActionableNamedSessions verifies the sessions
// provider is a one-shot, flat source: it carries exact metadata, never marks
// a named session Missing, and accepts absent directory/socket metadata.
func TestSessionsProvider_MapsActionableNamedSessions(t *testing.T) {
	t.Parallel()
	driver := &sessionDriver{fakeDriver: fakeDriver{detect: true}, sessions: []Session{
		{Name: "default", Running: true, Default: true, SessionDir: "/shared", SocketPath: "/tmp/default.sock"},
		{Name: "stopped", SessionDir: "/shared"},
	}}
	p := &sessionsProvider{driver: driver, probes: config.Probes{Herdr: true}, getenv: func(string) string { return "" }}

	candidates, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if driver.sessionsCall != 1 {
		t.Fatalf("session list calls = %d, want one", driver.sessionsCall)
	}
	if driver.deadline.IsZero() || time.Until(driver.deadline) > SessionsListTimeout || time.Until(driver.deadline) <= 0 {
		t.Fatalf("session list deadline = %v, want bounded positive %v", driver.deadline, SessionsListTimeout)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v, want two named sessions", candidates)
	}
	for _, candidate := range candidates {
		if candidate.Source != config.SourceSessions || candidate.Missing {
			t.Errorf("candidate = %+v, sessions must remain selectable and non-missing", candidate)
		}
	}
	if got, want := candidates[0].Meta, map[string]string{
		"session_name": "default", "running": "true", "default": "true", "session_dir": "/shared", "socket_path": "/tmp/default.sock",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("default metadata = %#v, want %#v", got, want)
	}
	if got, want := candidates[1].Meta, map[string]string{
		"session_name": "stopped", "running": "false", "default": "false", "session_dir": "/shared", "socket_path": "",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("partial metadata = %#v, want %#v", got, want)
	}
}

// TestSessionsProvider_ExcludesOnlyProvenSelfAttach checks both exact identity
// proofs and confirms that unmatched or absent environment values retain rows.
func TestSessionsProvider_ExcludesOnlyProvenSelfAttach(t *testing.T) {
	t.Parallel()
	sessions := []Session{
		{Name: "alpha", SocketPath: "/tmp/alpha.sock"},
		{Name: "beta", SocketPath: "/tmp/beta.sock"},
	}
	for _, tt := range []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "no proof keeps all", env: map[string]string{}, want: []string{"alpha", "beta"}},
		{name: "socket exact match excludes alpha", env: map[string]string{"HERDR_SOCKET_PATH": "/tmp/alpha.sock"}, want: []string{"beta"}},
		{name: "session exact match excludes beta", env: map[string]string{"HERDR_SESSION": "beta"}, want: []string{"alpha"}},
		{name: "near match is not proof", env: map[string]string{"HERDR_SESSION": "Beta", "HERDR_SOCKET_PATH": "/tmp/alpha.sock/"}, want: []string{"alpha", "beta"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			driver := &sessionDriver{fakeDriver: fakeDriver{detect: true}, sessions: sessions}
			p := &sessionsProvider{driver: driver, probes: config.Probes{Herdr: true}, getenv: func(key string) string { return tt.env[key] }}
			got, err := p.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			names := make([]string, 0, len(got))
			for _, candidate := range got {
				names = append(names, candidate.Meta["session_name"])
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("visible names = %v, want %v", names, tt.want)
			}
		})
	}
}

// TestRegistry_SessionsFailureIsIsolated verifies a failed bounded sessions
// lookup yields no session rows while unrelated configured sources remain.
func TestRegistry_SessionsFailureIsIsolated(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceSessions, config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "still-here", Path: t.TempDir()}}
	driver := &sessionDriver{fakeDriver: fakeDriver{detect: true}, sessionsErr: context.DeadlineExceeded}

	candidates, err := NewRegistry(cfg, config.Probes{Herdr: true}, driver).Collect(context.Background())
	if err == nil {
		t.Fatal("expected isolated sessions error")
	}
	if len(candidates) != 1 || candidates[0].Source != config.SourceWorkspaces {
		t.Errorf("candidates = %+v, want only unaffected workspace", candidates)
	}
}

// TestZoxideProvider_Enabled gates purely on the binary probe: general.sources
// gating is applied by the registry, not the provider's own enabled().
func TestZoxideProvider_Enabled(t *testing.T) {
	t.Parallel()
	p := zoxideProvider{}
	if p.enabled(config.Defaults(), config.Probes{Zoxide: false}) {
		t.Error("zoxide should be disabled when binary probe is false")
	}
	if !p.enabled(config.Defaults(), config.Probes{Zoxide: true}) {
		t.Error("zoxide should be enabled when binary probe is true")
	}
}

// TestParseZoxideOutput_UnscopedReturnsEverything confirms the top-level
// registry (root == "") surfaces the full zoxide history unfiltered.
func TestParseZoxideOutput_UnscopedReturnsEverything(t *testing.T) {
	t.Parallel()
	raw := "10\t/home/x/projects/foo\n5\t/home/x/other/bar\n"
	got := parseZoxideOutput(raw, "")
	if len(got) != 2 {
		t.Fatalf("expected 2 unscoped candidates, got %d: %v", len(got), got)
	}
}

// TestParseZoxideOutput_ScopedToGroupRoot (requirement: group-scoped zoxide)
// confirms a non-empty root filters results to root's own descendants
// (including root itself), keeping the user's entire zoxide history out of
// a group workspace's nested picker.
func TestParseZoxideOutput_ScopedToGroupRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inScope := filepath.Join(root, "svc-a")
	nested := filepath.Join(root, "svc-a", "sub")
	outOfScope := filepath.Join(t.TempDir(), "other-project")
	raw := "9\t" + root + "\n8\t" + inScope + "\n7\t" + nested + "\n6\t" + outOfScope + "\n"

	got := parseZoxideOutput(raw, root)

	want := map[string]bool{root: true, inScope: true, nested: true}
	if len(got) != len(want) {
		t.Fatalf("expected %d scoped candidates, got %d: %v", len(want), len(got), got)
	}
	for _, c := range got {
		if !want[c.Path] {
			t.Errorf("candidate %q must not be surfaced outside root %q", c.Path, root)
		}
	}
}

// TestParseZoxideOutput_SkipsMalformedLines confirms empty lines and blank
// path columns are skipped rather than producing empty-path candidates.
func TestParseZoxideOutput_SkipsMalformedLines(t *testing.T) {
	t.Parallel()
	raw := "\n5\t\n3\t/x/y\n"
	got := parseZoxideOutput(raw, "")
	if len(got) != 1 || got[0].Path != "/x/y" {
		t.Errorf("got %v, want a single candidate /x/y", got)
	}
}

// TestParseZoxideOutput_StalePathMarkedMissing (requirement: a candidate
// whose path no longer exists on disk must fail clearly on selection, never
// fall back to "/", $HOME, or cwd) confirms zoxide's directory history,
// which can go stale once a visited directory is deleted, is stat'd so
// launch()'s existing Missing check actually has something to reject.
func TestParseZoxideOutput_StalePathMarkedMissing(t *testing.T) {
	t.Parallel()
	live := t.TempDir()
	stale := filepath.Join(t.TempDir(), "deleted-project")
	raw := "10\t" + live + "\n5\t" + stale + "\n"

	got := parseZoxideOutput(raw, "")

	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %v", len(got), got)
	}
	byPath := map[string]Candidate{}
	for _, c := range got {
		byPath[c.Path] = c
	}
	if byPath[live].Missing {
		t.Errorf("live path %q must not be marked Missing", live)
	}
	if !byPath[stale].Missing {
		t.Errorf("stale path %q must be marked Missing", stale)
	}
}

// TestNewScopedRegistry_ThreadsRootIntoZoxideProvider confirms a group
// workspace's nested registry wires its root into the zoxide provider (not
// just the projects provider), so the scoping in parseZoxideOutput actually
// takes effect end-to-end instead of the provider defaulting to unscoped.
func TestNewScopedRegistry_ThreadsRootIntoZoxideProvider(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	r := NewScopedRegistry(config.Defaults(), config.Probes{Zoxide: true}, nil, []string{config.SourceZoxide}, root)
	p, ok := r.providers[config.SourceZoxide].(*zoxideProvider)
	if !ok {
		t.Fatalf("expected *zoxideProvider, got %T", r.providers[config.SourceZoxide])
	}
	if p.root != root {
		t.Errorf("zoxide provider root = %q, want %q", p.root, root)
	}
}

// TestRegistry_EnabledHonoursGeneralSources confirms only the sources listed
// in general.sources run, in that declared order.
func TestRegistry_EnabledHonoursGeneralSources(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceZoxide, config.SourceHerdr}
	r := NewRegistry(cfg, config.Probes{Zoxide: true, Herdr: true}, fakeDriver{detect: true})
	got := r.Enabled()
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled, got %d: %v", len(got), got)
	}
	if got[0].Name() != config.SourceZoxide || got[1].Name() != config.SourceHerdr {
		t.Errorf("order: got %s,%s want zoxide,herdr", got[0].Name(), got[1].Name())
	}
}

// TestRegistry_EnabledExcludesUnlistedSources confirms a source not named in
// general.sources never runs even if its binary is present.
func TestRegistry_EnabledExcludesUnlistedSources(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceHerdr}
	r := NewRegistry(cfg, config.Probes{Zoxide: true, Herdr: true}, fakeDriver{detect: true})
	for _, p := range r.Enabled() {
		if p.Name() == config.SourceZoxide {
			t.Fatal("zoxide should not run when absent from general.sources")
		}
	}
}

// TestRegistry_CollectPreservesResultsOnPartialError verifies one failing
// provider does not blank the candidate list.
func TestRegistry_CollectPreservesResultsOnPartialError(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ok", Path: t.TempDir()}}

	driver := fakeDriver{listErr: errors.New("boom")}
	r := NewRegistry(cfg, config.Probes{Herdr: true}, driver)
	got, err := r.Collect(context.Background())
	if err == nil {
		t.Fatal("expected partial error from herdr, got nil")
	}
	found := map[string]int{}
	for _, c := range got {
		found[c.Source]++
	}
	if found[config.SourceWorkspaces] == 0 {
		t.Error("workspaces candidates missing from partial-fail collect")
	}
}

// TestRegistry_CollectAttachesConfiguredIcon (requirement: source icons from
// [sources.*].icon must be shown) confirms Collect looks up the configured
// icon for each candidate's source and attaches it to the candidate.
func TestRegistry_CollectAttachesConfiguredIcon(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Workspaces.Icon = "★"
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "proj", Path: t.TempDir()}}
	r := NewRegistry(cfg, config.Probes{}, nil)
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].Icon != "★" {
		t.Errorf("expected icon %q on workspaces candidate, got %q", "★", got[0].Icon)
	}
}

// TestRelativeLabel_PathUnderHome converts an absolute path under $HOME to
// "~/..." form. Cannot run t.Parallel because it mutates HOME.
func TestRelativeLabel_PathUnderHome(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	got := RelativeLabel("/home/user/projects/foo")
	want := "~/projects/foo"
	if got != want {
		t.Errorf("RelativeLabel = %q, want %q", got, want)
	}
}

// TestRelativeLabel_PathOutsideHome leaves a path outside $HOME unchanged.
func TestRelativeLabel_PathOutsideHome(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	got := RelativeLabel("/opt/bar")
	want := "/opt/bar"
	if got != want {
		t.Errorf("RelativeLabel = %q, want %q", got, want)
	}
}

// TestRelativeLabel_HomeUnresolvable falls back to the path's base name when
// the home directory cannot be resolved.
func TestRelativeLabel_HomeUnresolvable(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	defer func() { userHomeDir = orig }()

	got := RelativeLabel("/home/user/x")
	want := "x"
	if got != want {
		t.Errorf("RelativeLabel = %q, want %q", got, want)
	}
}

// TestWorkspacesProvider_List turns predefined [[workspaces]] entries into
// candidates labelled by Name, with tilde-expanded paths, under Source
// "workspaces". Cannot run t.Parallel because it mutates HOME for tilde
// expansion.
func TestWorkspacesProvider_List(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	abs := filepath.Join(home, "code", "shep")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "docs", Path: "~/docs"},
		{Name: "shep", Path: abs},
	}
	p := &workspacesProvider{cfg: cfg}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got, want := len(cands), 2; got != want {
		t.Fatalf("expected 2 candidates, got %d: %+v", got, cands)
	}
	wantDocs := filepath.Join(home, "docs")
	if cands[0].Path != wantDocs {
		t.Errorf("docs path: got %q want %q", cands[0].Path, wantDocs)
	}
	if cands[0].Label != "docs" {
		t.Errorf("docs label: got %q want docs", cands[0].Label)
	}
	if cands[0].Source != config.SourceWorkspaces {
		t.Errorf("docs source: got %q", cands[0].Source)
	}
	if !cands[0].Missing {
		t.Error("docs path does not exist on disk and should be Missing")
	}
	if cands[1].Missing {
		t.Error("shep path exists on disk and should not be Missing")
	}
}

// TestWorkspacesProvider_StalePathMarkedMissing (requirement: a candidate
// whose path no longer exists on disk must fail clearly on selection, never
// fall back to "/", $HOME, or cwd) confirms a predefined [[workspaces]] path,
// which can point at a directory deleted after the config was written, is
// stat'd so launch()'s existing Missing check actually has something to
// reject instead of silently printing the path.
func TestWorkspacesProvider_StalePathMarkedMissing(t *testing.T) {
	t.Parallel()
	live := t.TempDir()
	stale := filepath.Join(t.TempDir(), "deleted-workspace")
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "live", Path: live},
		{Name: "stale", Path: stale},
	}
	p := &workspacesProvider{cfg: cfg}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byPath := map[string]Candidate{}
	for _, c := range cands {
		byPath[c.Path] = c
	}
	if byPath[live].Missing {
		t.Errorf("live workspace %q must not be marked Missing", live)
	}
	if !byPath[stale].Missing {
		t.Errorf("stale workspace %q must be marked Missing", stale)
	}
}

// TestWorkspacesProvider_GroupEntry confirms a type=group workspace yields
// one candidate marked as a group with its sources list encoded in Meta,
// instead of being treated as a normal launchable candidate.
func TestWorkspacesProvider_GroupEntry(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	root := t.TempDir()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "projects", Type: config.WorkspaceTypeGroup, Path: root, Sources: []string{config.SourceProjects, config.SourceZoxide}},
	}
	p := &workspacesProvider{cfg: cfg}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Meta["group"] != "true" {
		t.Errorf("expected group meta marker, got %v", cands[0].Meta)
	}
	if got, want := cands[0].Meta["group_sources"], "projects,zoxide"; got != want {
		t.Errorf("group_sources: got %q want %q", got, want)
	}
}

// TestWorkspacesProvider_CloseOnExitForwardsMeta confirms a workspace with
// CloseOnExit=true and a command surfaces that flag via Meta["close_on_exit"]
// = "true", mirroring how Meta["command"] forwards the workspace command to
// the command layer. Mirroring the existing command-forwarding pattern keeps
// the synthetic-template path and the real one in sync.
func TestWorkspacesProvider_CloseOnExitForwardsMeta(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	root := t.TempDir()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "yazi", Path: root, Command: "yazi", CloseOnExit: true},
		{Name: "shell", Path: root, Command: "bash"},
	}
	p := &workspacesProvider{cfg: cfg}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if got := cands[0].Meta["close_on_exit"]; got != "true" {
		t.Errorf("yazi close_on_exit: got %q want %q", got, "true")
	}
	if got := cands[0].Meta["command"]; got != "yazi" {
		t.Errorf("yazi command: got %q want %q", got, "yazi")
	}
	if _, ok := cands[1].Meta["close_on_exit"]; ok {
		t.Errorf("shell (CloseOnExit=false) must not set Meta[close_on_exit], got %q", cands[1].Meta["close_on_exit"])
	}
}

// TestWorkspacesProvider_Enabled gates on non-empty Workspaces.
func TestWorkspacesProvider_Enabled(t *testing.T) {
	t.Parallel()
	empty := config.Defaults()
	if (workspacesProvider{cfg: empty}).enabled(empty, config.Probes{}) {
		t.Error("workspacesProvider should be disabled when Workspaces is empty")
	}
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "x", Path: "/x"}}
	if !(workspacesProvider{cfg: cfg}).enabled(cfg, config.Probes{}) {
		t.Error("workspacesProvider should be enabled when Workspaces non-empty")
	}
}

// TestWorkspacesProvider_ListHonoursContextCancellation ensures the provider
// aborts when its context is already cancelled.
func TestWorkspacesProvider_ListHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "a", Path: "/a"},
		{Name: "b", Path: "/b"},
	}
	p := &workspacesProvider{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cands, err := p.List(ctx)
	if err != context.Canceled {
		t.Errorf("err: got %v want context.Canceled", err)
	}
	if cands != nil {
		t.Errorf("expected nil candidates on cancelled ctx, got %v", cands)
	}
}

// TestListProjects_NonRecursiveOneLevel scans only immediate children when
// Recursive is false, matching markers by presence of any configured file or
// directory name.
func TestListProjects_NonRecursiveOneLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkMarkerDir(t, root, "alpha", ".git")
	mkMarkerDir(t, root, "beta", "go.mod")
	_ = os.Mkdir(filepath.Join(root, "not-a-project"), 0o755)

	cfg := config.ProjectsSourceConfig{Markers: []string{".git", "go.mod"}}
	cands, err := ListProjects(context.Background(), cfg, root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 project candidates, got %d: %+v", len(cands), cands)
	}
	for _, c := range cands {
		if c.Source != config.SourceProjects {
			t.Errorf("source: got %q", c.Source)
		}
	}
}

// TestListProjects_RecursiveRespectsMaxDepthAndIgnore confirms recursive
// scanning descends up to max_depth, skips ignored directory names, and does
// not descend past an already-detected project.
func TestListProjects_RecursiveRespectsMaxDepthAndIgnore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// depth 1: client/ (not a project itself)
	mkDirs(t, root, "client")
	// depth 2: client/app (a project, has go.mod) with a NESTED go.mod
	// project beneath it that must NOT be reported (no descending past a hit).
	mkMarkerDir(t, filepath.Join(root, "client"), "app", "go.mod")
	mkMarkerDir(t, filepath.Join(root, "client", "app"), "nested", "go.mod")
	// an ignored directory full of noise that must be skipped entirely.
	noisy := filepath.Join(root, "node_modules")
	mkMarkerDir(t, root, "node_modules", "go.mod")
	_ = noisy

	cfg := config.ProjectsSourceConfig{
		Recursive: true, MaxDepth: 3,
		Markers: []string{"go.mod"},
		Ignore:  []string{"node_modules"},
	}
	cands, err := ListProjects(context.Background(), cfg, root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 project (client/app), got %d: %+v", len(cands), cands)
	}
	want := filepath.Join(root, "client", "app")
	if cands[0].Path != want {
		t.Errorf("path: got %q want %q", cands[0].Path, want)
	}
}

// TestListProjects_MissingRootReturnsEmpty confirms a non-existent root is
// treated as "no candidates", not an error.
func TestListProjects_MissingRootReturnsEmpty(t *testing.T) {
	t.Parallel()
	cfg := config.ProjectsSourceConfig{Markers: []string{".git"}}
	cands, err := ListProjects(context.Background(), cfg, "/definitely/not/here/sorep")
	if err != nil {
		t.Fatalf("missing root should be skipped, got err: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for missing root, got %d", len(cands))
	}
}

// TestProjectsProvider_EnabledOnlyWithRoot confirms the provider is inert
// with no root (top-level general.sources listing without any group
// workspace supplying a scan root).
func TestProjectsProvider_EnabledOnlyWithRoot(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	if (&projectsProvider{cfg: cfg, root: ""}).enabled(cfg, config.Probes{}) {
		t.Error("projects provider should be disabled without a root")
	}
	if !(&projectsProvider{cfg: cfg, root: "/x"}).enabled(cfg, config.Probes{}) {
		t.Error("projects provider should be enabled with a root")
	}
}

// TestNewScopedRegistry_UsesGroupSourcesAndRoot confirms a scoped registry
// only enables the requested sources and feeds the projects provider the
// group's own root.
func TestNewScopedRegistry_UsesGroupSourcesAndRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkMarkerDir(t, root, "svc", ".git")
	cfg := config.Defaults()
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}

	r := NewScopedRegistry(cfg, config.Probes{}, nil, []string{config.SourceProjects}, root)
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate from scoped registry, got %d: %+v", len(got), got)
	}
	if got[0].Source != config.SourceProjects {
		t.Errorf("source: got %q", got[0].Source)
	}
}

func mkDirs(t *testing.T, elems ...string) string {
	t.Helper()
	p := filepath.Join(elems...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

// mkMarkerDir creates parent/name/marker (marker as an empty file, or as a
// directory when it ends with "/") so tests can build project fixtures.
func mkMarkerDir(t *testing.T, parent, name, marker string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	return dir
}

func TestHerdrCandidates_DeriveWorkspaceCWDAndKeepMissingWorkspace(t *testing.T) {
	focused := t.TempDir()
	snapshot := Snapshot{
		Workspaces: []Workspace{
			{ID: "wA", Label: "project", ActiveTabID: "wA:t1"},
			{ID: "wB", Label: "no-cwd"},
		},
		Panes: []Pane{
			{ID: "wA:p1", WorkspaceID: "wA", TabID: "wA:t1", CWD: t.TempDir()},
			{ID: "wA:p2", WorkspaceID: "wA", TabID: "wA:t1", CWD: t.TempDir(), ForegroundCWD: focused, Focused: true, AgentStatus: "working"},
			{ID: "orphan:p1", WorkspaceID: "missing", TabID: "missing:t1", CWD: "/ignored"},
		},
	}

	cands := HerdrCandidates(snapshot)
	if len(cands) != 2 {
		t.Fatalf("HerdrCandidates count = %d, want 2: %+v", len(cands), cands)
	}
	if got, want := cands[0], (Candidate{Path: focused, Label: "project", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wA", "active_tab_id": "wA:t1"}}); !sameCandidate(got, want) {
		t.Errorf("candidate[0] = %+v, want %+v", got, want)
	}
	if got := cands[1]; got.Label != "no-cwd" || got.Path != "" || !got.Missing || got.Meta["workspace_id"] != "wB" {
		t.Errorf("candidate without derived CWD = %+v, want visible missing workspace", got)
	}
}

func TestHerdrCandidates_StaleDerivedCWDMarkedMissing(t *testing.T) {
	stale := filepath.Join(t.TempDir(), "deleted")
	existing := t.TempDir()
	tests := []struct {
		name        string
		cwd         string
		wantMissing bool
	}{
		{
			name:        "deleted derived CWD remains visible and missing",
			cwd:         stale,
			wantMissing: true,
		},
		{
			name:        "existing derived CWD is not missing",
			cwd:         existing,
			wantMissing: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := Snapshot{
				Workspaces: []Workspace{{ID: "w1", Label: "project"}},
				Panes:      []Pane{{ID: "w1:p1", WorkspaceID: "w1", CWD: tt.cwd}},
			}

			cands := HerdrCandidates(snapshot)
			if len(cands) != 1 {
				t.Fatalf("HerdrCandidates count = %d, want 1: %+v", len(cands), cands)
			}
			cand := cands[0]
			if cand.Path != tt.cwd || cand.Label != "project" || cand.Meta["workspace_id"] != "w1" {
				t.Errorf("candidate = %+v, want visible workspace with CWD %q", cand, tt.cwd)
			}
			if cand.Missing != tt.wantMissing {
				t.Errorf("candidate Missing = %v, want %v", cand.Missing, tt.wantMissing)
			}
		})
	}
}

func TestResolveFocusedPane_RequiresValidFocusedPaneID(t *testing.T) {
	fullPane := Pane{ID: "wA:p1", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/project", ForegroundCWD: "/project", AgentStatus: "blocked"}
	tests := []struct {
		name     string
		snapshot Snapshot
		want     *Pane
	}{
		{
			name:     "returns full focused pane record",
			snapshot: Snapshot{FocusedPaneID: "wA:p1", Panes: []Pane{fullPane}},
			want:     &fullPane,
		},
		{
			name:     "empty focus is unavailable",
			snapshot: Snapshot{Panes: []Pane{fullPane}},
		},
		{
			name:     "missing focused pane is unavailable",
			snapshot: Snapshot{FocusedPaneID: "wA:p9", Panes: []Pane{fullPane}},
		},
		{
			name:     "malformed focused pane id is unavailable",
			snapshot: Snapshot{FocusedPaneID: "wA:p1; rm -rf /", Panes: []Pane{{ID: "wA:p1; rm -rf /", WorkspaceID: "wA", TabID: "wA:t1", CWD: "/project"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveFocusedPane(tt.snapshot)
			if tt.want == nil {
				if ok || got != nil {
					t.Errorf("ResolveFocusedPane = (%+v, %v), want unavailable", got, ok)
				}
				return
			}
			if !ok {
				t.Fatal("ResolveFocusedPane reported unavailable")
			}
			if *got != *tt.want {
				t.Errorf("ResolveFocusedPane = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

func sameCandidate(got, want Candidate) bool {
	if got.Path != want.Path || got.Label != want.Label || got.Source != want.Source || got.Missing != want.Missing || len(got.Meta) != len(want.Meta) {
		return false
	}
	for key, value := range want.Meta {
		if got.Meta[key] != value {
			return false
		}
	}
	return true
}
