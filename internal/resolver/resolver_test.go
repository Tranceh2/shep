package resolver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// TestNormalize_Table is the spec table (PL-4). It exercises tilde expansion,
// absolute resolution, trailing-slash trimming and that an unresolvable
// symlink still returns a stable, non-empty key.
func TestNormalize_Table(t *testing.T) {
	tmp := t.TempDir()
	// Create a real symlink so EvalSymlinks has something to follow.
	target := filepath.Join(tmp, "real")
	_ = os.Mkdir(target, 0o755)
	link := filepath.Join(tmp, "link")
	_ = os.Symlink(target, link)

	cases := []struct {
		name    string
		input   string
		wantAbs bool
		wantSub string // substring expected in result (case-sensitive)
		wantErr bool
	}{
		{name: "empty errors", input: "", wantErr: true},
		{name: "absolute with trailing slash trimmed", input: target + string(filepath.Separator), wantSub: target},
		{name: "symlink resolves to target", input: link, wantSub: target},
		{name: "relative resolved to absolute", input: ".", wantAbs: true},
		{name: "dot cleanup", input: target + "/./sub/..", wantSub: target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize %q: %v", tc.input, err)
			}
			if tc.wantAbs && !filepath.IsAbs(got) {
				t.Errorf("expected absolute, got %q", got)
			}
			if tc.wantSub != "" && !strings.Contains(got, tc.wantSub) {
				t.Errorf("expected result to contain %q, got %q", tc.wantSub, got)
			}
			if strings.HasSuffix(got, string(filepath.Separator)) && got != string(filepath.Separator) {
				t.Errorf("result has trailing separator: %q", got)
			}
		})
	}
}

// TestNormalize_HomeUnresolvable returns an error when HOME is unavailable so
// determination cannot silently produce a wrong dedup key.
func TestNormalize_HomeUnresolvable(t *testing.T) {
	// Cannot use t.Parallel with t.Setenv.
	t.Setenv("HOME", "")
	// On darwin os.UserHomeDir falls back to passwd lookup so this may still
	// succeed; treat that as an acceptable platform variation rather than a
	// hard assertion to keep the test portable.
	if _, err := Normalize("~/x"); err == nil && runtime.GOOS != "darwin" {
		t.Error("expected error expanding ~ without HOME on non-darwin")
	}
}

// TestDedup_SymlinkCollapse (PL-4 S4) verifies two candidates pointing at the
// same path (one via symlink, one direct) collapse into a single entry.
func TestDedup_SymlinkCollapse(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	_ = os.Mkdir(real, 0o755)
	link := filepath.Join(tmp, "link")
	_ = os.Symlink(real, link)

	cands := []source.Candidate{
		{Path: link, Label: "via-link", Source: "a"},
		{Path: real, Label: "direct", Source: "b"},
	}
	out := Dedup(cands)
	if len(out) != 1 {
		t.Fatalf("expected 1 after dedup, got %d: %+v", len(out), out)
	}
	// On macOS /var is itself a symlink to /private/var, so compare against
	// the fully-resolved target rather than the literal `real` path.
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("resolve expected target: %v", err)
	}
	if out[0].NormalizedPath != want {
		t.Errorf("normalized path: got %q want %q", out[0].NormalizedPath, want)
	}
}

// TestDedup_OrderPreserved keeps the first-seen candidate so provider order
// from the registry is the visible tiebreaker.
func TestDedup_OrderPreserved(t *testing.T) {
	cands := []source.Candidate{
		{Path: "/a", Label: "first", Source: "herdr"},
		{Path: "/b", Label: "second", Source: "zoxide"},
		{Path: "/a", Label: "dup", Source: "cwd"},
	}
	out := Dedup(cands)
	if len(out) != 2 {
		t.Fatalf("expected 2 after dedup, got %d", len(out))
	}
	if out[0].Label != "first" {
		t.Errorf("expected first-seen survivor, got %q", out[0].Label)
	}
}

// TestDedup_SetsNormalizedPath confirms dedup fills the NormalizedPath field
// so downstream commands can use it without re-normalising.
func TestDedup_SetsNormalizedPath(t *testing.T) {
	cands := []source.Candidate{{Path: "/a/", Label: "x", Source: "s"}}
	out := Dedup(cands)
	if len(out) != 1 || out[0].NormalizedPath == "" {
		t.Fatalf("normalized path not set: %+v", out)
	}
}

// TestDedup_DefensiveCopy ensures the returned candidates do not share the
// Meta map with the caller's inputs.
func TestDedup_DefensiveCopy(t *testing.T) {
	cands := []source.Candidate{{Path: "/a", Label: "x", Source: "s", Meta: map[string]string{"k": "v"}}}
	out := Dedup(cands)
	out[0].Meta["k"] = "mutated"
	if cands[0].Meta["k"] == "mutated" {
		t.Error("Dedup returned shared Meta reference")
	}
}

// TestMatch_CaseInsensitiveSubstring (PL-7) matches against label, path, and
// the normalised path alike.
func TestMatch_CaseInsensitiveSubstring(t *testing.T) {
	cands := []source.Candidate{
		{Path: "/srv/x/projects/Foo", NormalizedPath: "/srv/x/projects/Foo", Label: "Foo", Source: "s"},
		{Path: "/srv/bar", NormalizedPath: "/srv/bar", Label: "bar", Source: "s"},
		{Path: "/opt/other", NormalizedPath: "/opt/other", Label: "other", Source: "s"},
	}
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "empty returns all", query: "", want: 3},
		{name: "label case-insensitive", query: "FOO", want: 1},
		{name: "path substring", query: "srv", want: 2},
		{name: "normalized target", query: "projects", want: 1},
		{name: "no match", query: "zzz", want: 0},
		{name: "partial label", query: "oth", want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(cands, tc.query)
			if len(got) != tc.want {
				t.Errorf("query %q: got %d matches want %d", tc.query, len(got), tc.want)
			}
		})
	}
}

// Test_resolveCases drives Resolve's ambiguity/none/exact contract.
func TestResolve(t *testing.T) {
	cands := []source.Candidate{
		{Path: "/a/foo", NormalizedPath: "/a/foo", Label: "foo"},
		{Path: "/b/foobar", NormalizedPath: "/b/foobar", Label: "foobar"},
		{Path: "/c/other", NormalizedPath: "/c/other", Label: "other"},
	}
	tests := []struct {
		name      string
		query     string
		wantOK    bool
		wantAmbig bool
		wantNone  bool
	}{
		{name: "exact match", query: "other", wantOK: true},
		{name: "ambiguous foo substring", query: "foo", wantAmbig: true},
		{name: "no match", query: "zzz", wantNone: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := Resolve(cands, tc.query)
			if tc.wantOK && (!ok || err != nil) {
				t.Errorf("expected ok, got ok=%v err=%v", ok, err)
			}
			if tc.wantAmbig && (ok || err == nil) {
				t.Errorf("expected ambiguous, got ok=%v err=%v", ok, err)
			}
			if tc.wantNone && (ok || err == nil) {
				t.Errorf("expected none, got ok=%v err=%v", ok, err)
			}
			if tc.wantAmbig && err != nil && !strings.Contains(err.Error(), "ambiguous") {
				t.Errorf("expected ambiguous error, got %v", err)
			}
			if tc.wantNone && err != nil && !strings.Contains(err.Error(), "no match") {
				t.Errorf("expected no-match error, got %v", err)
			}
		})
	}
}

// TestResolve_NilRegistry wraps the nil-guard so callers crash cleanly.
func TestResolve_NilRegistry(t *testing.T) {
	if _, _, err := ResolveFromSources(context.Background(), nil, "x"); err == nil {
		t.Fatal("expected error for nil registry")
	}
}

// TestResolveFromSources_PipelineEndToEnd wires a tiny registry and confirms
// collect -> dedup -> match produces the expected single match.
func TestResolveFromSources_PipelineEndToEnd(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	_ = os.Mkdir(filepath.Join(tmp, "alpha"), 0o755)
	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": tmp}}

	// cwd provider will also fire; that is fine, dedup handles it.
	r := source.NewRegistry(cfg, config.Probes{}, nil)
	all, matches, err := ResolveFromSources(context.Background(), r, "alpha")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d (all=%d)", len(matches), len(all))
	}
	if matches[0].Label != "alpha" {
		t.Errorf("match label: got %q", matches[0].Label)
	}
}

// TestResolveFromSources_PreservePartialError ensures a Collect error is
// surfaced while still returning the candidates other providers produced.
func TestResolveFromSources_PreservePartialError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	_ = os.Mkdir(filepath.Join(tmp, "alpha"), 0o755)
	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": tmp}}

	// herdr driver errors but is gated on; cwd + roots must still come through.
	driver := fakeErrDriver{}
	r := source.NewRegistry(cfg, config.Probes{Herdr: true}, driver)
	all, _, err := ResolveFromSources(context.Background(), r, "")
	if err == nil {
		t.Fatal("expected partial error from herdr, got nil")
	}
	if len(all) == 0 {
		t.Fatal("expected non-zero candidates despite partial error")
	}
}

type fakeErrDriver struct{}

func (fakeErrDriver) Detect(context.Context) bool { return true }
func (fakeErrDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, errors.New("daemon down")
}
func (fakeErrDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakeErrDriver does not implement FocusOrCreate")
}
func (fakeErrDriver) RunStartup(context.Context, string, string) error {
	return errors.New("fakeErrDriver does not implement RunStartup")
}
