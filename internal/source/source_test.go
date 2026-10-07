package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
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
	c := Candidate{Path: "/x", Aliases: []string{"k8s"}, Meta: map[string]string{"a": "1"}}
	clone := c.Clone()
	clone.Meta["a"] = "mutated"
	clone.Aliases[0] = "mutated"
	if c.Meta["a"] == "mutated" {
		t.Error("Clone shared Meta map with original")
	}
	if c.Aliases[0] == "mutated" {
		t.Error("Clone shared Aliases slice with original")
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
		{name: "custom source command-only", cand: Candidate{Source: "prs", Meta: map[string]string{"command": "gh pr view"}}, want: true},
		{name: "custom source without command excluded", cand: Candidate{Source: "prs"}, want: false},
		{name: "custom source group excluded", cand: Candidate{Source: "prs", Meta: map[string]string{"command": "gh pr view", "group": "true"}}, want: false},
		{name: "custom source template excluded", cand: Candidate{Source: "prs", Meta: map[string]string{"command": "gh pr view", "template": "dev"}}, want: false},
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
	cfg.General.SourceOrder = []string{config.SourceSessions, config.SourceWorkspaces}
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
func TestListProjectsRoots_DeduplicatesNormalizedRootsAndProjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project := filepath.Join(root, "service")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectsSourceConfig{Markers: []string{"go.mod"}}
	roots := []string{root, filepath.Join(root, "nested", "..")}
	got, err := ListProjectsRoots(context.Background(), cfg, roots)
	if err != nil {
		t.Fatalf("list roots: %v", err)
	}
	normalizedProject, err := pathutil.Normalize(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != normalizedProject {
		t.Fatalf("projects = %+v, want one normalized project %q", got, normalizedProject)
	}
}

func TestNewRegistry_ProjectsUseConfiguredGlobalRootsOnly(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Projects.Roots = []string{t.TempDir()}
	r := NewRegistry(cfg, config.Probes{}, nil)
	p, ok := r.providers[config.SourceProjects].(*projectsProvider)
	if !ok {
		t.Fatalf("projects provider type = %T", r.providers[config.SourceProjects])
	}
	if !reflect.DeepEqual(p.roots, cfg.Sources.Projects.Roots) {
		t.Fatalf("provider roots = %v, want %v", p.roots, cfg.Sources.Projects.Roots)
	}
}

func TestNewScopedRegistryForWorkspaceWithOrderUsesGlobalFallback(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceSessions, config.SourceProjects}
	workspace := config.WorkspaceConfig{SourceOrder: nil}
	r := NewScopedRegistryForWorkspaceWithOrder(cfg, config.Probes{}, nil, workspace, cfg.General.SourceOrder, t.TempDir())
	providers := r.Providers()
	if len(providers) < 2 || providers[0].Name() != config.SourceSessions || providers[1].Name() != config.SourceProjects {
		t.Fatalf("scoped providers = %v, want global fallback order", providerNames(providers))
	}
}

func providerNames(providers []Provider) []string {
	names := make([]string, len(providers))
	for i, provider := range providers {
		names[i] = provider.Name()
	}
	return names
}

func TestNewScopedRegistryForWorkspaceUsesLocalProjectOverride(t *testing.T) {
	t.Parallel()
	recursive := false
	maxDepth := 0
	workspace := config.WorkspaceConfig{
		Name:        "group",
		Type:        config.WorkspaceTypeGroup,
		SourceOrder: []string{config.SourceProjects},
		Sources: config.WorkspaceSourcesConfig{Projects: &config.ProjectsSourceOverride{
			Recursive: &recursive,
			MaxDepth:  &maxDepth,
		}},
	}
	r := NewScopedRegistryForWorkspace(config.Defaults(), config.Probes{}, nil, workspace, t.TempDir())
	p, ok := r.providers[config.SourceProjects].(*projectsProvider)
	if !ok {
		t.Fatalf("projects provider type = %T", r.providers[config.SourceProjects])
	}
	if p.cfg.Sources.Projects.Recursive || p.cfg.Sources.Projects.MaxDepth != 0 {
		t.Fatalf("local override was not merged: %+v", p.cfg.Sources.Projects)
	}
}

// TestScopedProjectsUseEffectiveGroupMaxDepth proves the merged group setting
// controls the actual filesystem scan and that the group root replaces global
// project roots rather than combining with them.
func TestScopedProjectsUseEffectiveGroupMaxDepth(t *testing.T) {
	t.Parallel()
	groupRoot := t.TempDir()
	globalRoot := t.TempDir()
	mkDirs(t, groupRoot, "level-one", "level-two", "level-three")
	deepProject := filepath.Join(groupRoot, "level-one", "level-two", "level-three")
	if err := os.WriteFile(filepath.Join(deepProject, "go.mod"), []byte("module deep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	globalProject := mkMarkerDir(t, globalRoot, "global", "go.mod")

	global := config.Defaults()
	global.General.SourceOrder = []string{config.SourceProjects}
	global.Sources.Projects = config.ProjectsSourceConfig{
		Roots:     []string{globalRoot},
		Recursive: true,
		MaxDepth:  1,
		Markers:   []string{"go.mod"},
	}
	maxDepth := 3
	workspace := config.WorkspaceConfig{
		Name:        "group",
		Type:        config.WorkspaceTypeGroup,
		SourceOrder: []string{config.SourceProjects},
		Sources:     config.WorkspaceSourcesConfig{Projects: &config.ProjectsSourceOverride{MaxDepth: &maxDepth}},
	}

	r := NewScopedRegistryForWorkspace(global, config.Probes{}, nil, workspace, groupRoot)
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != deepProject {
		t.Fatalf("scoped projects = %+v, want only deep group project %q", got, deepProject)
	}
	if got[0].Path == globalProject {
		t.Fatal("scoped project scan used global roots")
	}
}

func TestCustomSourceProvider_ParseJSONRows(t *testing.T) {
	t.Parallel()
	const payload = `[{"label":"PR 42","path":"/repo","command":"gh pr view 42","icon":"","template":"dev","close_on_exit":true,"aliases":[" review ","PR"],"meta":{"number":"42","context":"feature"}}]`
	got, err := ParseCustomSourceJSON("prs", []byte(payload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	want := Candidate{Path: "/repo", Label: "PR 42", Icon: "", Source: "prs", Aliases: []string{"review", "PR"}, Meta: map[string]string{
		"command": "gh pr view 42", "template": "dev", "close_on_exit": "true", "number": "42", "context": "feature", "custom_source": "true",
	}}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("candidate = %+v, want %+v", got[0], want)
	}
}

func TestCustomSourceProvider_ParseJSONRowsRejectsMalformedPartialAndEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		json string
	}{
		{name: "malformed", json: `[{"label":"broken"}`},
		{name: "missing label", json: `[{"path":"/repo"}]`},
		{name: "empty output", json: ``},
		{name: "null output", json: `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCustomSourceJSON("prs", []byte(tt.json)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
	if got, err := ParseCustomSourceJSON("prs", []byte(`[]`)); err != nil || len(got) != 0 {
		t.Fatalf("empty array = (%v, %v), want empty candidates and nil error", got, err)
	}
}

func TestCustomSourceProvider_ListFailureAndTimeoutAreVisible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command []string
		timeout time.Duration
	}{
		{name: "command failure", command: []string{"false"}, timeout: time.Second},
		{name: "timeout", command: []string{"sleep", "1"}, timeout: 10 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &customSourceProvider{cfg: config.CustomSourceConfig{Name: "prs", Command: tt.command, Timeout: config.Duration(tt.timeout)}}
			_, err := p.List(context.Background())
			if err == nil {
				t.Fatal("expected custom source error")
			}
		})
	}
}

func TestRegistry_CustomSourceProviderRegistrationAndOrder(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "prs", Command: []string{"printf", "[]"}}}
	cfg.General.SourceOrder = []string{"prs"}
	r := NewRegistry(cfg, config.Probes{}, nil)
	got := r.Enabled()
	if len(got) != 1 || got[0].Name() != "prs" {
		t.Fatalf("enabled providers = %v, want custom source prs", got)
	}
}

func TestScopedRegistry_LazyCustomSourceOnlyRunsWhenListed(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	command := []string{"sh", "-c", `n=$((${COUNT:-0}+1)); printf '%s' "$n" > "$COUNT_FILE"; printf '[{"label":"context"}]'`}
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "kube-contexts", Command: command, Timeout: config.Duration(time.Second)}}
	t.Setenv("COUNT_FILE", counter)

	top, err := NewRegistry(cfg, config.Probes{}, nil).Collect(context.Background())
	if err != nil {
		t.Fatalf("top-level collect: %v", err)
	}
	if len(top) != 0 {
		t.Fatalf("top-level candidates = %v, want no custom source candidates", top)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatalf("custom source ran at top level, count file stat = %v", err)
	}

	group := config.WorkspaceConfig{Type: config.WorkspaceTypeGroup, SourceOrder: []string{"kube-contexts"}}
	got, err := NewScopedRegistryForWorkspace(cfg, config.Probes{}, nil, group, t.TempDir()).Collect(context.Background())
	if err != nil {
		t.Fatalf("group collect: %v", err)
	}
	if len(got) != 1 || got[0].Label != "context" {
		t.Fatalf("group candidates = %v, want custom source candidate", got)
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "1" {
		t.Fatalf("custom source count = %q, read error = %v, want exactly one run", data, err)
	}
}

func TestRegistry_EnabledHonoursGeneralSources(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceZoxide, config.SourceHerdr}
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
	cfg.General.SourceOrder = []string{config.SourceHerdr}
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
	star := "★"
	cfg.Sources.Workspaces.Icon = &star
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

	// A template icon depends on the row; only the picker draws it.
	tmplIcon := "{{ if .IsWorktree }}W{{ end }}"
	cfg.Sources.Workspaces.Icon = &tmplIcon
	if icon := NewRegistry(cfg, config.Probes{}, nil).IconFor(config.SourceWorkspaces); icon != "" {
		t.Errorf("IconFor(template icon) = %q, want empty", icon)
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
func TestWorkspacesProvider_IdentityIsOpaqueAndVersioned(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	first := config.WorkspaceConfig{Name: "one", Path: path, Command: "nvim", Template: "dev"}
	identity := WorkspaceEntryIdentity(path, first)
	if strings.Contains(identity, path) || strings.Contains(identity, "nvim") || strings.Contains(identity, "dev") {
		t.Fatalf("workspace identity leaks configured values: %q", identity)
	}
	if !strings.HasPrefix(identity, "v1:") {
		t.Fatalf("workspace identity is not versioned: %q", identity)
	}
	if len(identity) < 19 {
		t.Fatalf("workspace identity is not opaque enough: %q", identity)
	}
}

func TestWorkspacesProvider_IdentityIgnoresPresentationAndOrder(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	first := config.WorkspaceConfig{Name: "one", Path: path, Command: "nvim"}
	second := config.WorkspaceConfig{Name: "renamed", Path: path, Command: "nvim"}
	if got, want := WorkspaceEntryIdentity(path, first), WorkspaceEntryIdentity(path, second); got != want {
		t.Fatalf("presentation-only change altered identity: %q != %q", got, want)
	}
	changed := config.WorkspaceConfig{Name: "one", Path: path, Command: "bash"}
	if WorkspaceEntryIdentity(path, first) == WorkspaceEntryIdentity(path, changed) {
		t.Fatal("different actions share configured workspace identity")
	}
}

func TestWorkspacesProvider_AliasesReachGroupCandidate(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "Kubernetes", Type: config.WorkspaceTypeGroup, Path: t.TempDir(), Aliases: []string{"k8s"}}}
	candidates, err := (&workspacesProvider{cfg: cfg}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || len(candidates[0].Aliases) != 1 || candidates[0].Aliases[0] != "k8s" {
		t.Fatalf("group aliases = %+v, want [k8s]", candidates)
	}
	if got := candidates[0].Meta["entry_id"]; got == "" {
		t.Fatal("group candidate lost its stable entry identity")
	}
}

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
func TestWorkspacesProvider_IdentityCanonicalizesUnorderedGroupSources(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	base := config.WorkspaceConfig{
		Name:        "group",
		Path:        path,
		Type:        config.WorkspaceTypeGroup,
		SourceOrder: []string{config.SourceProjects, config.SourceZoxide},
	}
	reordered := base
	reordered.SourceOrder = []string{config.SourceZoxide, config.SourceProjects}
	if got, want := WorkspaceEntryIdentity(path, base), WorkspaceEntryIdentity(path, reordered); got != want {
		t.Fatalf("reordering group sources changed identity: %q != %q", got, want)
	}
	changedMembership := base
	changedMembership.SourceOrder = []string{config.SourceProjects, config.SourceHerdr}
	if WorkspaceEntryIdentity(path, base) == WorkspaceEntryIdentity(path, changedMembership) {
		t.Fatal("different group source membership shares identity")
	}
	changedAction := base
	changedAction.Template = "alternate"
	if WorkspaceEntryIdentity(path, base) == WorkspaceEntryIdentity(path, changedAction) {
		t.Fatal("different workspace action shares identity")
	}
}

func TestWorkspacesProvider_GroupEntry(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	root := t.TempDir()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "projects", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects, config.SourceZoxide}},
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

func TestWorktreeCandidate_EnrichesAndDeduplicatesProjects(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	repo := filepath.Join(root, "shep")
	linked := filepath.Join(base, "trees", "feature-x")
	missing := filepath.Join(base, "trees", "missing")
	for _, dir := range []string{filepath.Join(repo, ".git", "worktrees"), linked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	discover := func(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
		return DiscoverWorktreesWithRunner(ctx, repoPath, func(context.Context, string, ...string) ([]byte, error) {
			return []byte(strings.Join([]string{
				"worktree " + repo, "HEAD 0123456789abcdef", "branch refs/heads/main", "",
				"worktree " + linked, "HEAD abcdef0123456789", "branch refs/heads/feature/x", "",
				"worktree " + missing, "HEAD deadbeef", "branch refs/heads/stale", "",
			}, "\n")), nil
		})
	}
	got, err := listProjectsWithDiscovery(context.Background(), config.ProjectsSourceConfig{Markers: []string{".git"}}, root, discover)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want main plus one linked worktree", got)
	}
	byPath := map[string]Candidate{got[0].Path: got[0], got[1].Path: got[1]}
	if main := byPath[repo]; main.Meta["main_worktree"] != "true" || main.Meta["branch"] != "main" {
		t.Fatalf("main metadata = %#v", main.Meta)
	}
	worktree := byPath[linked]
	wantMeta := map[string]string{"is_worktree": "true", "branch": "feature/x", "repo": "shep", "worktree_path": linked, "head": "abcdef0123456789", "main_worktree": "false"}
	if worktree.Label != "shep (feature/x)" || worktree.Source != config.SourceProjects || !reflect.DeepEqual(worktree.Meta, wantMeta) {
		t.Fatalf("linked candidate = %+v, want label and metadata %#v", worktree, wantMeta)
	}
	if _, exists := byPath[missing]; exists {
		t.Fatal("non-existent worktree was emitted")
	}
}

func TestWorktreeCandidate_BareRepoAndDetachedHeadTriangulation(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	repo := filepath.Join(root, "bare-repo")
	linked := filepath.Join(base, "trees", "detached-tree")
	for _, dir := range []string{filepath.Join(repo, "worktrees"), linked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	discover := func(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
		return DiscoverWorktreesWithRunner(ctx, repoPath, func(context.Context, string, ...string) ([]byte, error) {
			return []byte(strings.Join([]string{
				"worktree " + repo, "bare", "",
				"worktree " + linked, "HEAD abcdef0123456789", "detached", "",
			}, "\n")), nil
		})
	}
	got, err := listProjectsWithDiscovery(context.Background(), config.ProjectsSourceConfig{Markers: []string{"worktrees"}}, root, discover)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want bare main plus one detached linked worktree", got)
	}
	byPath := map[string]Candidate{got[0].Path: got[0], got[1].Path: got[1]}
	worktree := byPath[linked]
	wantMeta := map[string]string{"is_worktree": "true", "branch": "", "repo": "bare-repo", "worktree_path": linked, "head": "abcdef0123456789", "main_worktree": "false"}
	if worktree.Source != config.SourceProjects || !reflect.DeepEqual(worktree.Meta, wantMeta) {
		t.Fatalf("detached candidate = %+v, want metadata %#v", worktree, wantMeta)
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

func TestAgentCandidates_DerivationAndPrecedence(t *testing.T) {
	snapshot := Snapshot{
		FocusedPaneID: "p2",
		Workspaces: []Workspace{
			{ID: "w1", Label: "ws1", CWD: "/srv/ws1"},
			{ID: "w2", Label: "ws2", CWD: "/srv/ws2"},
		},
		Tabs: []Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "tab1"},
			{ID: "t2", WorkspaceID: "w2", Label: "tab2"},
		},
		Panes: []Pane{
			{
				ID:            "p1",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Agent:         "pi",
				AgentStatus:   "blocked",
				TerminalTitle: "security audit",
				ForegroundCWD: "/srv/ws1/sub",
				Focused:       true,
			},
			{
				ID:          "p2",
				WorkspaceID: "w2",
				TabID:       "t2",
				Agent:       "claude",
				AgentStatus: "working",
				Label:       "codegen",
				CWD:         "/srv/ws2/work",
			},
			{
				ID:          "p3",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "opencode",
				AgentStatus: "idle",
			},
			{
				ID:          "p4",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "agent-runner",
				AgentStatus: "unknown", // unknown with non-empty Agent is included
			},
			{
				ID:          "p_plain",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "",
				AgentStatus: "", // non-agent pane, must be excluded
			},
			{
				ID:          "p_unknown",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "",
				AgentStatus: "unknown", // unknown without agent, must be excluded
			},
		},
	}

	cands := AgentCandidates(snapshot)
	if len(cands) != 4 {
		t.Fatalf("AgentCandidates count = %d, want 4", len(cands))
	}

	// p1: ForegroundCWD preferred, TerminalTitle preferred, Source is config.SourceAgents
	c1 := cands[0]
	if c1.Source != config.SourceAgents {
		t.Errorf("c1.Source = %q, want %q", c1.Source, config.SourceAgents)
	}
	if c1.Path != "/srv/ws1/sub" {
		t.Errorf("c1.Path = %q, want /srv/ws1/sub", c1.Path)
	}
	if c1.Label != "security audit" {
		t.Errorf("c1.Label = %q, want 'security audit'", c1.Label)
	}
	if c1.Meta["focused"] != "true" {
		t.Errorf("c1.Meta[focused] = %q, want 'true'", c1.Meta["focused"])
	}
	if c1.Meta["agent"] != "pi" || c1.Meta["agent_status"] != "blocked" || c1.Meta["kind"] != "agent" {
		t.Errorf("c1.Meta unexpected: %+v", c1.Meta)
	}

	// p2: CWD fallback, Label fallback, FocusedPaneID match
	c2 := cands[1]
	if c2.Path != "/srv/ws2/work" {
		t.Errorf("c2.Path = %q, want /srv/ws2/work", c2.Path)
	}
	if c2.Label != "codegen" {
		t.Errorf("c2.Label = %q, want 'codegen'", c2.Label)
	}
	if c2.Meta["focused"] != "true" {
		t.Errorf("c2.Meta[focused] = %q, want 'true' from FocusedPaneID match", c2.Meta["focused"])
	}

	// p3: workspace CWD fallback, "agent " + ID label fallback
	c3 := cands[2]
	if c3.Path != "/srv/ws1" {
		t.Errorf("c3.Path = %q, want /srv/ws1", c3.Path)
	}
	if c3.Label != "agent p3" {
		t.Errorf("c3.Label = %q, want 'agent p3'", c3.Label)
	}

	// p4: unknown status with agent name is preserved
	c4 := cands[3]
	if c4.Meta["agent"] != "agent-runner" || c4.Meta["agent_status"] != "unknown" {
		t.Errorf("c4 unexpected: %+v", c4.Meta)
	}
}

func TestAgentsProvider_RegistryLifecycle(t *testing.T) {
	cfg := config.Defaults()
	robot := "🤖 "
	cfg.Sources.Agents.Icon = &robot
	probes := config.Probes{Herdr: true}

	reg := NewRegistry(cfg, probes, nil)
	var agentsProv Provider
	for _, p := range reg.Providers() {
		if p.Name() == config.SourceAgents {
			agentsProv = p
			break
		}
	}
	if agentsProv == nil {
		t.Fatal("agents provider not registered in Registry")
	}

	// IconFor
	if got, want := reg.IconFor(config.SourceAgents), "🤖 "; got != want {
		t.Errorf("reg.IconFor(SourceAgents) = %q, want %q", got, want)
	}

	// By default, agents is not in cfg.General.SourceOrder, so Enabled() should not include it
	for _, p := range reg.Enabled() {
		if p.Name() == config.SourceAgents {
			t.Errorf("default Enabled() contains %q, want excluded", config.SourceAgents)
		}
	}

	// When configured in source_order, it runs and Collect() gathers its candidates
	snapshot := Snapshot{
		Workspaces: []Workspace{{ID: "w1", Label: "ws1", CWD: "/srv/ws1"}},
		Panes: []Pane{
			{ID: "p1", WorkspaceID: "w1", Agent: "opencode", AgentStatus: "working"},
		},
	}
	cfg.General.SourceOrder = []string{config.SourceAgents}
	regWithAgents := NewRegistry(cfg, probes, nil).WithHerdrSnapshot(snapshot)

	cands, err := regWithAgents.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("Collect cands count = %d, want 1", len(cands))
	}
	if cands[0].Source != config.SourceAgents {
		t.Errorf("cand.Source = %q, want %q", cands[0].Source, config.SourceAgents)
	}
	if cands[0].Icon != "🤖 " {
		t.Errorf("cand.Icon = %q, want %q", cands[0].Icon, "🤖 ")
	}

	// DisableHerdr disables agentsProvider
	regWithAgents.DisableHerdr()
	disabledCands, _ := regWithAgents.Collect(context.Background())
	if len(disabledCands) != 0 {
		t.Errorf("Collect after DisableHerdr count = %d, want 0", len(disabledCands))
	}
}

// TestScopedRegistry_AgentsRestrictedToGroupRoot proves a group workspace's
// scoped registry confines the agents source to the group root while the
// top-level registry stays unscoped. A pane is kept when its derived candidate
// path (ForegroundCWD first) or its workspace CWD lies at or beneath root —
// the two can disagree and both are membership signals. Panes tied to neither
// (including sibling-prefix paths like /srv/group2) are cross-root and must
// never leak into the nested picker.
func TestScopedRegistry_AgentsRestrictedToGroupRoot(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceAgents}
	probes := config.Probes{Herdr: true}
	snapshot := Snapshot{
		Workspaces: []Workspace{
			{ID: "w_in", Label: "group-ws", CWD: "/srv/group/w"},
			{ID: "w_out", Label: "other-ws", CWD: "/other/w"},
		},
		Tabs: []Tab{
			{ID: "t_in", WorkspaceID: "w_in"},
			{ID: "t_out", WorkspaceID: "w_out"},
		},
		Panes: []Pane{
			// Path tie: foreground CWD under root although the workspace
			// itself lives elsewhere.
			{ID: "p_path", WorkspaceID: "w_out", TabID: "t_out", Agent: "claude", AgentStatus: "working", ForegroundCWD: "/srv/group/app"},
			// Workspace tie: the group's own workspace whose foreground CWD
			// wandered outside root (workspace path vs ForegroundCWD).
			{ID: "p_ws", WorkspaceID: "w_in", TabID: "t_in", Agent: "pi", AgentStatus: "blocked", ForegroundCWD: "/other/wander"},
			// Cross-root: neither path nor workspace under root.
			{ID: "p_out", WorkspaceID: "w_out", TabID: "t_out", Agent: "opencode", AgentStatus: "idle", ForegroundCWD: "/other/x"},
			// Sibling-prefix trap: /srv/group2 is not /srv/group.
			{ID: "p_sibling", WorkspaceID: "w_out", TabID: "t_out", Agent: "runner", AgentStatus: "done", ForegroundCWD: "/srv/group2/app"},
			// Path falls back to the workspace CWD, itself under root.
			{ID: "p_fallback", WorkspaceID: "w_in", TabID: "t_in", Agent: "builder", AgentStatus: "done"},
		},
	}

	scoped := NewScopedRegistry(cfg, probes, nil, []string{config.SourceAgents}, "/srv/group").WithHerdrSnapshot(snapshot)
	got, err := scoped.Collect(context.Background())
	if err != nil {
		t.Fatalf("scoped Collect error: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, c := range got {
		gotIDs[c.Meta["pane_id"]] = true
	}
	for _, want := range []string{"p_path", "p_ws", "p_fallback"} {
		if !gotIDs[want] {
			t.Errorf("scoped agents missing %s (got %v)", want, gotIDs)
		}
	}
	for _, banned := range []string{"p_out", "p_sibling"} {
		if gotIDs[banned] {
			t.Errorf("scoped agents leaked cross-root %s (got %v)", banned, gotIDs)
		}
	}
	if len(got) != 3 {
		t.Errorf("scoped agents count = %d, want 3: %+v", len(got), got)
	}

	// The top-level registry must not gain a global root filter.
	unscoped := NewRegistry(cfg, probes, nil).WithHerdrSnapshot(snapshot)
	all, err := unscoped.Collect(context.Background())
	if err != nil {
		t.Fatalf("unscoped Collect error: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("unscoped agents count = %d, want 5 (no global filter)", len(all))
	}
}

func TestScopedRegistry_AgentsEmptyRootFailsClosed(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceAgents}
	snapshot := Snapshot{
		Workspaces: []Workspace{{ID: "other", CWD: "/other"}},
		Panes:      []Pane{{ID: "p", WorkspaceID: "other", Agent: "pi", AgentStatus: "working", ForegroundCWD: "/other/p"}},
	}
	probes := config.Probes{Herdr: true}
	group := config.WorkspaceConfig{Type: config.WorkspaceTypeGroup, SourceOrder: []string{config.SourceAgents}}
	scoped := NewScopedRegistryForWorkspace(cfg, probes, nil, group, "").WithHerdrSnapshot(snapshot)
	got, err := scoped.Collect(context.Background())
	if err != nil {
		t.Fatalf("scoped Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty-root scoped agents = %+v, want no candidates", got)
	}

	global := NewRegistry(cfg, probes, nil).WithHerdrSnapshot(snapshot)
	got, err = global.Collect(context.Background())
	if err != nil || len(got) != 1 || got[0].Meta["pane_id"] != "p" {
		t.Fatalf("global unscoped agents = (%+v, %v), want pane p", got, err)
	}
}

func TestScopedRegistry_AgentsRootAliasMatchesNormalizedPath(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "real")
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceAgents}
	snapshot := Snapshot{
		Workspaces: []Workspace{{ID: "in", CWD: filepath.Join(root, "app")}, {ID: "out", CWD: filepath.Join(parent, "other")}},
		Panes: []Pane{
			{ID: "in", WorkspaceID: "in", Agent: "pi", ForegroundCWD: filepath.Join(root, "app")},
			{ID: "out", WorkspaceID: "out", Agent: "pi", ForegroundCWD: filepath.Join(parent, "other")},
		},
	}
	got, err := NewScopedRegistry(cfg, config.Probes{Herdr: true}, nil, []string{config.SourceAgents}, alias).
		WithHerdrSnapshot(snapshot).Collect(context.Background())
	if err != nil || len(got) != 1 || got[0].Meta["pane_id"] != "in" {
		t.Fatalf("alias-root agents = (%+v, %v), want only in", got, err)
	}
}

// TestAgentCandidatesInRoot_PureScoping locks the group-root membership rule
// as a pure function: a pane is kept when its derived candidate path or its
// workspace CWD lies at or beneath root (root itself included), sibling-prefix
// paths are not descendants, and an empty scoped root returns no panes.
func TestAgentCandidatesInRoot_PureScoping(t *testing.T) {
	t.Parallel()
	snapshot := Snapshot{
		Workspaces: []Workspace{
			{ID: "w_in", Label: "group-ws", CWD: "/srv/group/w"},
			{ID: "w_out", Label: "other-ws", CWD: "/other/w"},
		},
		Panes: []Pane{
			{ID: "p_root", WorkspaceID: "w_out", Agent: "a", AgentStatus: "working", ForegroundCWD: "/srv/group"},
			{ID: "p_deep", WorkspaceID: "w_out", Agent: "b", AgentStatus: "idle", ForegroundCWD: "/srv/group/deep/app"},
			{ID: "p_ws", WorkspaceID: "w_in", Agent: "c", AgentStatus: "idle", ForegroundCWD: "/other/wander"},
			{ID: "p_sibling", WorkspaceID: "w_out", Agent: "d", AgentStatus: "idle", ForegroundCWD: "/srv/group2/app"},
			{ID: "p_out", WorkspaceID: "w_out", Agent: "e", AgentStatus: "idle", ForegroundCWD: "/other/x"},
		},
	}

	for _, tt := range []struct {
		name string
		root string
		want []string
	}{
		{name: "scoped keeps root, descendants and workspace ties", root: "/srv/group", want: []string{"p_root", "p_deep", "p_ws"}},
		{name: "empty scoped root fails closed", root: "", want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentCandidatesInRoot(snapshot, tt.root)
			gotIDs := make([]string, 0, len(got))
			for _, c := range got {
				gotIDs = append(gotIDs, c.Meta["pane_id"])
			}
			if len(gotIDs) != len(tt.want) || (len(gotIDs) > 0 && !reflect.DeepEqual(gotIDs, tt.want)) {
				t.Errorf("AgentCandidatesInRoot(%q) = %v, want %v", tt.root, gotIDs, tt.want)
			}
		})
	}
}
