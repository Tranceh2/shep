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
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// TestNormalize_Delegates proves resolver.Normalize is a 1-line delegate to
// pathutil.Normalize, not a separate implementation that could silently
// diverge. One representative input is enough here — exhaustive coverage of
// Normalize's behavior (tilde expansion, symlink resolution, trailing-slash
// trimming, EvalSymlinks-failure fallback) lives in
// internal/pathutil/normalize_test.go.
func TestMatch_AliasIsSearchableButArbitraryMetaIsNot(t *testing.T) {
	t.Parallel()
	candidates := []source.Candidate{
		{Label: "Kubernetes", Aliases: []string{"k8s"}, Meta: map[string]string{"secret": "k8s"}},
		{Label: "other", Meta: map[string]string{"secret": "k8s"}},
	}
	matches := Match(candidates, "k8s")
	if len(matches) != 1 || matches[0].Label != "Kubernetes" {
		t.Fatalf("matches = %+v, want only alias candidate", matches)
	}
}

func TestNormalize_Delegates(t *testing.T) {
	t.Parallel()
	input := "~/foo/../foo/bar/"
	want, wantErr := pathutil.Normalize(input)
	got, gotErr := Normalize(input)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("resolver.Normalize(%q) err = %v, want err %v", input, gotErr, wantErr)
	}
	if got != want {
		t.Errorf("resolver.Normalize(%q) = %q, want %q (pathutil.Normalize)", input, got, want)
	}
}

// TestDedup_SymlinkCollapse verifies two candidates pointing at the same
// path (one via symlink, one direct) with the same label collapse into a
// single entry. Dedup uses a composite norm+label key so explicitly named
// workspaces targeting the same path are preserved; the label must match for
// a path-only collapse.
func TestDedup_SymlinkCollapse(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	_ = os.Mkdir(real, 0o755)
	link := filepath.Join(tmp, "link")
	_ = os.Symlink(real, link)

	cands := []source.Candidate{
		{Path: link, Label: "same", Source: "a"},
		{Path: real, Label: "same", Source: "b"},
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

// TestDedup_CaseFoldCollapse is the bug-reproduction test: two candidates
// with the SAME label whose paths differ only in case (e.g. a Herdr
// candidate at "ECORP" and a zoxide candidate at "ecorp") must collapse to
// one entry when the filesystem itself considers them the same directory
// (case-insensitive, e.g. macOS APFS default / Windows). This is gated to
// darwin/windows because a case-sensitive filesystem (most Linux/ext4)
// genuinely has two distinct directories here — SameDir correctly reports
// false in that case, so asserting collapse would be wrong off-darwin. Both
// candidates are non-herdr so the herdr exemption (R2) does not apply and the
// collapse this test targets still happens.
func TestDedup_CaseFoldCollapse(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive collapse only guaranteed on darwin/windows")
	}
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "ECORP")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(tmp, "ecorp")

	cands := []source.Candidate{
		{Path: upper, Label: "ECORP", Source: "workspaces"},
		{Path: lower, Label: "ECORP", Source: "zoxide"},
	}
	out := Dedup(cands)
	if len(out) != 1 {
		t.Fatalf("expected 1 after case-fold dedup, got %d: %+v", len(out), out)
	}
	if out[0].Source != "workspaces" {
		t.Errorf("expected first-seen survivor (workspaces), got %q", out[0].Source)
	}
}

// TestDedup_CaseFoldLabelCollapse is the bug-reproduction test: two
// candidates pointing at the exact same literal path (SameDir's byte-equal
// fast path, no filesystem case-insensitivity involved) whose auto-derived
// Labels differ only in case (e.g. a workspaces candidate labeled "ECORP" and a
// zoxide candidate at the same path labeled "ecorp") must collapse to one
// entry. Before the fix, the `kept.Label == c.Label` comparison was
// case-sensitive, so these two visually-duplicate rows for the same real
// directory survived Dedup untouched. Both candidates are non-herdr so the
// herdr exemption (R2) does not apply and the collapse this test targets
// still happens.
func TestDedup_CaseFoldLabelCollapse(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}

	cands := []source.Candidate{
		{Path: real, Label: "ECORP", Source: "workspaces"},
		{Path: real, Label: "ecorp", Source: "zoxide"},
	}
	out := Dedup(cands)
	if len(out) != 1 {
		t.Fatalf("expected 1 after case-insensitive label dedup, got %d: %+v", len(out), out)
	}
	if out[0].Source != "workspaces" {
		t.Errorf("expected first-seen survivor (workspaces), got %q", out[0].Source)
	}
}

// TestDedup_SamePathDifferentLabelPreserved (requirement: explicitly named
// workspaces targeting the same path must survive as distinct candidates)
// confirms two candidates with the SAME normalised path but DIFFERENT labels
// are both kept, so defining "ECORP" and "fsociety" at the same path does
// not drop the second entry.
func TestDedup_SamePathDifferentLabelPreserved(t *testing.T) {
	cands := []source.Candidate{
		{Path: "/srv/ecorp", Label: "ECORP", Source: "workspaces"},
		{Path: "/srv/ecorp", Label: "fsociety", Source: "workspaces"},
	}
	out := Dedup(cands)
	if len(out) != 2 {
		t.Fatalf("expected 2 distinct candidates (same path, different labels), got %d: %+v", len(out), out)
	}
	labels := map[string]bool{}
	for _, c := range out {
		labels[c.Label] = true
	}
	if !labels["ECORP"] || !labels["fsociety"] {
		t.Errorf("expected both labels preserved, got %v", labels)
	}
}

func integrationCandidate(label, path, id, command string) source.Candidate {
	return source.Candidate{
		Path:   path,
		Label:  label,
		Source: "kube-contexts",
		Meta: map[string]string{
			"integration":    "true",
			"integration_id": id,
			"command":        command,
		},
	}
}

// TestDedup_IntegrationIdentityPreservesRoutes proves integrations do not lose
// actionable routes merely because their display label and cwd are equal.
func TestDedup_IntegrationIdentityPreservesRoutes(t *testing.T) {
	t.Parallel()
	candidates := []source.Candidate{
		integrationCandidate("cluster-a", "/repo", "direct:cluster-a", "kubectl --context direct:cluster-a"),
		integrationCandidate("cluster-a", "/repo", "connect:cluster-a", "kubectl --context connect:cluster-a"),
	}

	got := Dedup(candidates)
	if len(got) != 2 {
		t.Fatalf("Dedup collapsed distinct integration routes: got %d candidates: %+v", len(got), got)
	}
	if got[0].Meta["integration_id"] != "direct:cluster-a" || got[1].Meta["integration_id"] != "connect:cluster-a" {
		t.Fatalf("integration order or identity changed: %+v", got)
	}
}

// TestDedup_IntegrationIdentityDuplicateKeepsFirst proves identical stable
// identities collapse deterministically without using slice position as an id.
func TestDedup_IntegrationIdentityDuplicateKeepsFirst(t *testing.T) {
	t.Parallel()
	first := integrationCandidate("cluster-a", "/first", "cluster-a", "kubectl --context cluster-a")
	second := integrationCandidate("renamed", "/second", "cluster-a", "kubectl --context cluster-a")

	got := Dedup([]source.Candidate{first, second})
	if len(got) != 1 {
		t.Fatalf("identical integration identity must collapse: %+v", got)
	}
	if got[0].Path != first.Path {
		t.Fatalf("duplicate survivor = %q, want first-seen path %q", got[0].Path, first.Path)
	}
}

// TestDedup_PathBackedIntegrationUsesExactIdentityButSharesResource proves
// candidate dedup and pin affinity intentionally use different keys.
func TestDedup_PathBackedIntegrationUsesExactIdentityButSharesResource(t *testing.T) {
	t.Parallel()
	first := integrationCandidate("cluster-a", "/repo", "direct:cluster-a", "kubectl --context direct:cluster-a")
	second := integrationCandidate("cluster-a", "/repo", "connect:cluster-a", "kubectl --context connect:cluster-a")

	got := Dedup([]source.Candidate{first, second})
	if len(got) != 2 {
		t.Fatalf("path-backed integration routes must both survive: %+v", got)
	}
	if ranking.Identity(got[0]) == ranking.Identity(got[1]) {
		t.Fatal("distinct path-backed integration routes share exact identity")
	}
	if ranking.Resource(got[0]) != ranking.Resource(got[1]) {
		t.Fatal("path-backed integration routes must share resource affinity")
	}
	if ranking.PinKey(got[0]) != ranking.PinKey(got[1]) {
		t.Fatal("path-backed integration pins must share the resource key")
	}
}

// TestDedup_OrderPreserved keeps the first-seen candidate so provider order
// from the registry is the visible tiebreaker. A true duplicate (same path
// AND same label) is collapsed; the first-seen survivor wins.
func TestDedup_OrderPreserved(t *testing.T) {
	// All three are non-herdr so the herdr exemption (R2) does not apply and
	// the first-seen duplicate collapse this test targets still happens.
	cands := []source.Candidate{
		{Path: "/a", Label: "first", Source: "zoxide"},
		{Path: "/b", Label: "second", Source: "zoxide"},
		{Path: "/a", Label: "first", Source: "workspaces"},
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

// TestDedup_HerdrExempt (R2) proves Dedup never collapses a pair when either
// candidate is herdr-sourced, while ordinary non-herdr duplicates still
// collapse first-seen. herdr candidates model already-open Herdr workspaces:
// two of them may legitimately share a label+path (two open workspaces at the
// same repo), so collapsing either against anything would hide a real
// "resume" option from the picker.
func TestDedup_HerdrExempt(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	foo := filepath.Join(tmp, "foo")
	if err := os.Mkdir(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   []source.Candidate
		want []string // surviving Sources, in order
	}{
		{
			name: "two herdr at same label+path kept both ordered",
			in: []source.Candidate{
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wA"}},
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wB"}},
			},
			want: []string{config.SourceHerdr, config.SourceHerdr},
		},
		{
			name: "herdr plus zoxide at same label+path kept both",
			in: []source.Candidate{
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wA"}},
				{Path: foo, Label: "foo", Source: config.SourceZoxide},
			},
			want: []string{config.SourceHerdr, config.SourceZoxide},
		},
		{
			name: "zoxide plus zoxide still first-wins",
			in: []source.Candidate{
				{Path: foo, Label: "foo", Source: config.SourceZoxide},
				{Path: foo, Label: "foo", Source: config.SourceZoxide},
			},
			want: []string{config.SourceZoxide},
		},
		{
			name: "herdr-only preserves all in order",
			in: []source.Candidate{
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wA"}},
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wB"}},
				{Path: foo, Label: "foo", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "wC"}},
			},
			want: []string{config.SourceHerdr, config.SourceHerdr, config.SourceHerdr},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := Dedup(tt.in)
			if len(out) != len(tt.want) {
				t.Fatalf("Dedup produced %d candidates, want %d: %+v", len(out), len(tt.want), out)
			}
			for i, c := range out {
				if c.Source != tt.want[i] {
					t.Errorf("Dedup[%d].Source = %q, want %q", i, c.Source, tt.want[i])
				}
			}
		})
	}
}

// TestDedup_SessionsExempt preserves independently attachable session targets
// even when their optional session_dir metadata is the same as another source.
func TestDedup_SessionsExempt(t *testing.T) {
	t.Parallel()
	shared := t.TempDir()
	candidates := []source.Candidate{
		{Path: shared, Label: "shared", Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}},
		{Path: shared, Label: "shared", Source: config.SourceSessions, Meta: map[string]string{"session_name": "beta"}},
		{Path: shared, Label: "shared", Source: config.SourceWorkspaces},
	}

	got := Dedup(candidates)
	if len(got) != 3 {
		t.Fatalf("Dedup = %+v, want all independently actionable session and workspace rows", got)
	}
	for i, want := range []string{"alpha", "beta", ""} {
		if got[i].Meta["session_name"] != want {
			t.Errorf("candidate %d session name = %q, want %q", i, got[i].Meta["session_name"], want)
		}
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

// TestResolve drives Resolve's ambiguity/none/exact contract.
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

// TestResolveFromSources_PipelineEndToEnd wires a tiny registry (a
// workspaces source) and confirms collect -> dedup -> match produces the
// expected single match.
func TestResolveFromSources_PipelineEndToEnd(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	alpha := filepath.Join(tmp, "alpha")
	if err := os.Mkdir(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "alpha-ws", Path: alpha}}

	r := source.NewRegistry(cfg, config.Probes{}, nil)
	all, matches, err := ResolveFromSources(context.Background(), r, "alpha")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d (all=%d)", len(matches), len(all))
	}
	if matches[0].Label != "alpha-ws" {
		t.Errorf("match label: got %q, want %q", matches[0].Label, "alpha-ws")
	}
}

// TestResolveFromSources_PreservePartialError ensures a Collect error is
// surfaced while still returning the candidates other providers produced.
func TestResolveFromSources_PreservePartialError(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceHerdr, config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ok", Path: t.TempDir()}}

	// herdr driver errors but is gated on; workspaces must still come through.
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
func (fakeErrDriver) Snapshot(context.Context) (source.Snapshot, error) {
	return source.Snapshot{}, errors.New("daemon down")
}
func (fakeErrDriver) ListSessions(context.Context) ([]source.Session, error) {
	return nil, errors.New("daemon down")
}
func (fakeErrDriver) FocusOrCreate(context.Context, source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakeErrDriver does not implement FocusOrCreate")
}
func (fakeErrDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("fakeErrDriver does not implement ReadPane")
}
func (fakeErrDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("fakeErrDriver does not implement CreateTab")
}
func (fakeErrDriver) RenameTab(context.Context, string, string) error {
	return errors.New("fakeErrDriver does not implement RenameTab")
}
func (fakeErrDriver) RenamePane(context.Context, string, *string) error {
	return errors.New("fakeErrDriver does not implement RenamePane")
}
func (fakeErrDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeErrDriver does not implement SplitPane")
}
func (fakeErrDriver) RunPane(context.Context, string, string) error {
	return errors.New("fakeErrDriver does not implement RunPane")
}
func (fakeErrDriver) FocusTab(context.Context, string) error {
	return errors.New("fakeErrDriver does not implement FocusTab")
}

// fakeWorkspacesDriver is a HerdrDriver that returns a fixed workspace set,
// used by the priority-dedup contract test.
type fakeWorkspacesDriver struct {
	workspaces []source.Workspace
}

func (fakeWorkspacesDriver) Detect(context.Context) bool { return true }
func (d fakeWorkspacesDriver) Snapshot(context.Context) (source.Snapshot, error) {
	snapshot := source.Snapshot{Workspaces: make([]source.Workspace, 0, len(d.workspaces))}
	for _, workspace := range d.workspaces {
		snapshot.Workspaces = append(snapshot.Workspaces, workspace)
		if workspace.CWD != "" {
			snapshot.Panes = append(snapshot.Panes, source.Pane{ID: workspace.ID + ":p1", WorkspaceID: workspace.ID, CWD: workspace.CWD})
		}
	}
	return snapshot, nil
}
func (fakeWorkspacesDriver) ListSessions(context.Context) ([]source.Session, error) { return nil, nil }
func (fakeWorkspacesDriver) FocusOrCreate(context.Context, source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakeWorkspacesDriver does not implement FocusOrCreate")
}
func (fakeWorkspacesDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("fakeWorkspacesDriver does not implement ReadPane")
}
func (fakeWorkspacesDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("fakeWorkspacesDriver does not implement CreateTab")
}
func (fakeWorkspacesDriver) RenameTab(context.Context, string, string) error {
	return errors.New("fakeWorkspacesDriver does not implement RenameTab")
}
func (fakeWorkspacesDriver) RenamePane(context.Context, string, *string) error {
	return errors.New("fakeWorkspacesDriver does not implement RenamePane")
}
func (fakeWorkspacesDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeWorkspacesDriver does not implement SplitPane")
}
func (fakeWorkspacesDriver) RunPane(context.Context, string, string) error {
	return errors.New("fakeWorkspacesDriver does not implement RunPane")
}
func (fakeWorkspacesDriver) FocusTab(context.Context, string) error {
	return errors.New("fakeWorkspacesDriver does not implement FocusTab")
}

// TestDedup_HerdrExempt_AcrossRegistry (R2, end-to-end through the real
// collect→dedup pipeline) proves a herdr-sourced candidate and a
// [[workspaces]] candidate at the same path+label survive as TWO distinct
// candidates regardless of general.sources order, instead of the pre-R2
// collapse-to-one. This is the picker-level guarantee that "resume an
// already-open workspace" and "open a new one" stay unambiguous.
func TestDedup_HerdrExempt_AcrossRegistry(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	foo := filepath.Join(tmp, "foo")
	if err := os.Mkdir(foo, 0o755); err != nil {
		t.Fatal(err)
	}
	fooResolved, err := filepath.EvalSymlinks(foo)
	if err != nil {
		t.Fatalf("resolve foo: %v", err)
	}
	// Both providers surface the same path with the same label "foo" so the
	// pair WOULD have collapsed pre-R2; R2's herdr exemption keeps both.
	herdrCand := source.Workspace{ID: "wfoo", Label: "foo", CWD: foo}

	for _, order := range [][]string{
		{config.SourceHerdr, config.SourceWorkspaces},
		{config.SourceWorkspaces, config.SourceHerdr},
	} {
		cfg := config.Defaults()
		cfg.General.SourceOrder = order
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "foo", Path: foo}}
		r := source.NewRegistry(cfg, config.Probes{Herdr: true},
			fakeWorkspacesDriver{workspaces: []source.Workspace{herdrCand}})
		raw, err := r.Collect(context.Background())
		if err != nil {
			t.Fatalf("collect (order=%v): %v", order, err)
		}
		out := Dedup(raw)

		herdrCount, wsCount := 0, 0
		for _, c := range out {
			if c.NormalizedPath != fooResolved {
				continue
			}
			switch c.Source {
			case config.SourceHerdr:
				herdrCount++
			case config.SourceWorkspaces:
				wsCount++
			}
		}
		if herdrCount != 1 || wsCount != 1 {
			t.Errorf("order=%v: expected 1 herdr + 1 workspaces candidate at foo, got herdr=%d workspaces=%d (out=%+v)",
				order, herdrCount, wsCount, out)
		}
	}
}
