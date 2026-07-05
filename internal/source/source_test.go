package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tranceh2/shep/internal/config"
)

// fakeDriver is a controllable HerdrDriver for tests.
type fakeDriver struct {
	detect     bool
	workspaces []Workspace
	listErr    error
}

func (f fakeDriver) Detect(context.Context) bool { return f.detect }
func (f fakeDriver) ListWorkspaces(context.Context) ([]Workspace, error) {
	return f.workspaces, f.listErr
}
func (fakeDriver) FocusOrCreate(context.Context, Candidate) (FocusResult, error) {
	return FocusResult{}, errors.New("fakeDriver does not implement FocusOrCreate")
}
func (fakeDriver) RunStartup(context.Context, string, string) error {
	return errors.New("fakeDriver does not implement RunStartup")
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

// TestCwdProvider_List reports the process working directory as a single
// candidate labelled with its base name.
func TestCwdProvider_List(t *testing.T) {
	t.Parallel()
	p := cwdProvider{}
	cands, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Source != "cwd" {
		t.Errorf("source: got %q", cands[0].Source)
	}
	if cands[0].Label == "" {
		t.Error("cwd candidate has empty label")
	}
}

// TestRootsProvider_EnabledAndList wires a roots source pointing at a temp
// directory and asserts immediate subdirectories become candidates.
func TestRootsProvider_EnabledAndList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// create two project dirs and one file (file must be skipped)
	_ = os.Mkdir(filepath.Join(root, "alpha"), 0o755)
	_ = os.Mkdir(filepath.Join(root, "beta"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "skip-file"), []byte("x"), 0o644)

	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": root}}

	rp := &rootsProvider{cfg: cfg}
	if !rp.enabled(cfg, config.Probes{}) {
		t.Error("roots should be enabled when path is set")
	}
	cands, err := rp.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 dir candidates, got %d: %+v", len(cands), cands)
	}
	labels := map[string]bool{}
	for _, c := range cands {
		labels[c.Label] = true
		if c.Source != "dev" {
			t.Errorf("source: got %q", c.Source)
		}
	}
	// Labels are home-relative display paths (RelativeLabel), not bare base
	// names; root here is outside $HOME (t.TempDir()) so the full path
	// passes through unchanged.
	wantAlpha := filepath.Join(root, "alpha")
	wantBeta := filepath.Join(root, "beta")
	if !labels[wantAlpha] || !labels[wantBeta] {
		t.Errorf("missing expected labels: %v (want %q, %q)", labels, wantAlpha, wantBeta)
	}
}

// TestCwdProvider_ListUsesRelativeLabel proves the cwd candidate's Label is
// home-relative when the process cwd sits under $HOME. Cannot run
// t.Parallel because it mutates HOME and the process cwd.
func TestCwdProvider_ListUsesRelativeLabel(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	cands, err := (cwdProvider{}).List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	want := "~/work/proj"
	if cands[0].Label != want {
		t.Errorf("Label = %q, want %q", cands[0].Label, want)
	}
}

// TestRootsProvider_ListUsesRelativeLabel proves roots candidates get a
// home-relative Label (not a bare base name) when the root sits under $HOME.
// Cannot run t.Parallel because it mutates HOME.
func TestRootsProvider_ListUsesRelativeLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "gamma"), 0o755); err != nil {
		t.Fatalf("mkdir gamma: %v", err)
	}

	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": root}}
	rp := &rootsProvider{cfg: cfg}
	cands, err := rp.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(cands), cands)
	}
	want := "~/projects/gamma"
	if cands[0].Label != want {
		t.Errorf("Label = %q, want %q", cands[0].Label, want)
	}
}

// TestRootsProvider_DisabledWhenNoPath verifies a roots source with an empty
// path keeps the provider disabled.
func TestRootsProvider_DisabledWhenNoPath(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true}
	if (rootsProvider{}).enabled(cfg, config.Probes{}) {
		t.Error("roots should be disabled without a path")
	}
}

// TestRootsProvider_MissingRootSkipped confirms a non-existent root directory
// is skipped rather than reporting an error.
func TestRootsProvider_MissingRootSkipped(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": "/definitely/not/here/sorep"}}
	cands, err := ListRoots(context.Background(), cfg)
	if err != nil {
		t.Fatalf("missing root should be skipped, got err: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for missing root, got %d", len(cands))
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

// TestHerdrProvider_WithDriver turns a fake driver into candidates, filtering
// out entries with an empty CWD.
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
	if len(cands) != 2 {
		t.Fatalf("expected 2 workspaces (empty CWD filtered), got %d", len(cands))
	}
	if cands[1].Label != "bar" {
		t.Errorf("missing-label fallback: got %q", cands[1].Label)
	}
	if cands[0].Meta["workspace_id"] != "w1" {
		t.Errorf("workspace_id meta not propagated: %v", cands[0].Meta)
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

// TestHerdrProvider_DisabledByOverride confirms a config source override wins.
func TestHerdrProvider_DisabledByOverride(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources["herdr"] = config.Source{Kind: config.KindHerdr, Enabled: false}
	p := &herdrProvider{driver: fakeDriver{detect: true}, probes: config.Probes{Herdr: true}, cfg: cfg}
	if p.enabled(cfg, config.Probes{Herdr: true}) {
		t.Error("herdr should be disabled by config override")
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

// TestZoxideProvider_Enabled gates on the binary probe.
func TestZoxideProvider_Enabled(t *testing.T) {
	t.Parallel()
	p := zoxideProvider{}
	if p.enabled(config.Defaults(), config.Probes{Zoxide: false}) {
		t.Error("zoxide should be disabled when binary probe is false")
	}
	if !p.enabled(config.Defaults(), config.Probes{Zoxide: true}) {
		t.Error("zoxide should be enabled when binary probe is true")
	}
	cfg := config.Defaults()
	cfg.Sources["zoxide"] = config.Source{Kind: config.KindZoxide, Enabled: false}
	if p.enabled(cfg, config.Probes{Zoxide: true}) {
		t.Error("zoxide should respect config override disable")
	}
}

// TestRegistry_EnabledOrder honours General.ProviderOrder.
func TestRegistry_EnabledOrder(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.ProviderOrder = []string{"zoxide", "cwd"}
	r := NewRegistry(cfg, config.Probes{Zoxide: true}, nil)
	got := r.Enabled()
	if len(got) < 2 {
		t.Fatalf("expected >=2 enabled, got %d", len(got))
	}
	if got[0].Name() != "zoxide" || got[1].Name() != "cwd" {
		t.Errorf("order: got %s,%s want zoxide,cwd", got[0].Name(), got[1].Name())
	}
}

// TestRegistry_CollectPreservesResultsOnPartialError verifies one failing
// provider does not blank the candidate list.
func TestRegistry_CollectPreservesResultsOnPartialError(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "proj"), 0o755)
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": root}}

	// herdr with a driver that errors but is gated on by probes.Herdr=true.
	// Using a fake driver whose ListWorkspaces errors surfaces the partial-fail.
	driver := fakeDriver{listErr: errors.New("boom")}
	r := NewRegistry(cfg, config.Probes{Herdr: true}, driver)
	got, err := r.Collect(context.Background())
	if err == nil {
		t.Fatal("expected partial error from herdr, got nil")
	}
	// cwd + roots should still appear.
	found := map[string]int{}
	for _, c := range got {
		found[c.Source]++
	}
	if found["cwd"] == 0 {
		t.Error("cwd missing from partial-fail collect")
	}
	if found["dev"] == 0 {
		t.Error("roots candidates missing from partial-fail collect")
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

// TestExpandTilde covers the developer-shorthand expansion used by roots.
// Cannot run t.Parallel because it mutates HOME.
func TestExpandTilde(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := expandTilde("~/code"); !filepath.IsAbs(got) {
		t.Errorf("expected absolute expansion, got %q", got)
	}
	if got := expandTilde("relative"); got != "relative" {
		t.Errorf("non-tilde input should pass through, got %q", got)
	}
}
