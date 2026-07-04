package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// fakeRunner is a scriptable CommandRunner. Each entry is matched in order
// against the called command (name + joined args); the matched response is
// returned and consumed. Unmatched calls fall back to errNotScripted so a
// missing expectation fails loudly instead of flaking on the host.
type fakeRunner struct {
	script []fakeCall
	calls  []string
}

type fakeCall struct {
	match string // canonicalised as "name arg arg"
	out   []byte
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.calls = append(f.calls, key)
	for i := range f.script {
		if f.script[i].match == key {
			out, err := f.script[i].out, f.script[i].err
			// consume so repeated identical calls need repeated entries
			f.script = append(f.script[:i], f.script[i+1:]...)
			return out, err
		}
	}
	return nil, errors.New("not scripted: " + key)
}

// workspaceListJSON builds a workspace list envelope with a single workspace.
func workspaceListJSON(id, label string) []byte {
	return []byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
		`{"workspace_id":"` + id + `","label":"` + label + `","active_tab_id":"` + id + `:t1","focused":true,"number":1}]}}`)
}

// paneListJSON builds a pane list envelope from (workspaceID, cwd) pairs. The
// first pane of each workspace is marked focused to mirror a real focused
// workspace.
func paneListJSON(panes ...rawPane) []byte {
	out := `{"id":"cli:pane:list","result":{"panes":[`
	for i, p := range panes {
		if i > 0 {
			out += ","
		}
		out += `{"pane_id":"` + p.PaneID + `","workspace_id":"` + p.WorkspaceID + `","cwd":"` + p.CWD +
			`","foreground_cwd":"` + p.ForegroundCWD + `","focused":` + boolStr(p.Focused) + `}`
	}
	out += `]}}`
	return []byte(out)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// paneCurrentJSON builds the pane current envelope used to discover the
// focused workspace after a create.
func paneCurrentJSON(workspaceID string) []byte {
	return []byte(`{"id":"cli:pane:current","result":{"pane":{"pane_id":"` + workspaceID +
		`:p1","workspace_id":"` + workspaceID + `","cwd":"/x","foreground_cwd":"/x","focused":true}}}`)
}

func TestDetect_BinaryPresent(t *testing.T) {
	d := New("herdr",
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/herdr", nil }))
	if !d.Detect(context.Background()) {
		t.Fatal("Detect should be true when LookPath succeeds")
	}
}

func TestDetect_BinaryAbsent(t *testing.T) {
	d := New("herdr",
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }))
	if d.Detect(context.Background()) {
		t.Fatal("Detect should be false when LookPath fails")
	}
}

// mkDir creates a real directory under t and returns its path so
// EvalSymlinks inside the driver has something to resolve.
func mkDir(t *testing.T, elems ...string) string {
	t.Helper()
	p := filepath.Join(elems...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

// HI-2: workspaces are parsed from JSON and joined with pane cwds. The driver
// stores the raw pane cwd (no normalisation here); normalisation happens only
// during FocusOrCreate matching.
func TestListWorkspaces_JoinsCWDFromPanes(t *testing.T) {
	dir := t.TempDir()
	foo := mkDir(t, dir, "foo")
	bar := mkDir(t, dir, "bar")

	r := &fakeRunner{script: []fakeCall{
		{
			match: "herdr workspace list",
			out: []byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
				`{"workspace_id":"wA","label":"foo","active_tab_id":"wA:t1","focused":true,"number":1},` +
				`{"workspace_id":"wB","label":"bar","active_tab_id":"wB:t1","focused":false,"number":2}]}}`),
		},
		{
			match: "herdr pane list",
			out: paneListJSON(
				rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: foo, ForegroundCWD: foo, Focused: true},
				rawPane{PaneID: "wB:p1", WorkspaceID: "wB", CWD: bar, ForegroundCWD: bar, Focused: false},
			),
		},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 workspaces, got %d: %+v", len(got), got)
	}
	want := map[string]string{"wA": foo, "wB": bar}
	for _, w := range got {
		if w.CWD != want[w.ID] {
			t.Errorf("workspace %s cwd = %q, want %q", w.ID, w.CWD, want[w.ID])
		}
	}
}

// HI-3 / S1: focus an existing workspace whose pane cwd normalises to the
// candidate path; no create call is issued.
func TestFocusOrCreate_FocusesExistingByCWDMatch(t *testing.T) {
	dir := t.TempDir()
	foo := mkDir(t, dir, "foo")
	fooN, err := filepath.EvalSymlinks(foo)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", out: workspaceListJSON("wA", "foo")},
		{match: "herdr pane list", out: paneListJSON(rawPane{
			PaneID: "wA:p1", WorkspaceID: "wA", CWD: foo, ForegroundCWD: foo, Focused: true,
		})},
		{match: "herdr workspace focus wA", out: []byte(`{"id":"cli:workspace:focus","result":{}}`)},
	}}
	d := New("herdr", WithRunner(r))
	res, err := d.FocusOrCreate(context.Background(), source.Candidate{
		Path: foo, NormalizedPath: fooN, Label: "foo",
	})
	if err != nil {
		t.Fatalf("FocusOrCreate: %v", err)
	}
	if res.Action != source.HerdrActionFocused {
		t.Errorf("Action = %v, want HerdrActionFocused", res.Action)
	}
	if res.WorkspaceID != "wA" {
		t.Errorf("WorkspaceID = %q, want wA", res.WorkspaceID)
	}
	// ensure no create was scripted
	for _, c := range r.calls {
		if contains(c, "create") {
			t.Errorf("unexpected create call: %s", c)
		}
	}
}

// HI-3 / S2: no workspace matches -> create with --cwd --label --focus, then
// discover the new workspace id via pane current.
func TestFocusOrCreate_CreatesWhenNoMatch(t *testing.T) {
	dir := t.TempDir()
	mismatch := filepath.Join(dir, "other")
	matching := mkDir(t, dir, "bar")
	// candidate carries no NormalizedPath so the driver normalises on the fly
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", out: workspaceListJSON("wA", "other")},
		{
			match: "herdr pane list",
			out:   paneListJSON(rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: mismatch, ForegroundCWD: mismatch, Focused: true}),
		},
		{match: "herdr workspace create --cwd " + matching + " --label bar --focus", out: []byte(`{"id":"cli:workspace:create","result":{}}`)},
		{match: "herdr pane current", out: paneCurrentJSON("wNEW")},
	}}
	d := New("herdr", WithRunner(r))
	res, err := d.FocusOrCreate(context.Background(), source.Candidate{
		Path: matching, Label: "bar",
	})
	if err != nil {
		t.Fatalf("FocusOrCreate: %v", err)
	}
	if res.Action != source.HerdrActionCreated {
		t.Fatalf("Action = %v, want HerdrActionCreated", res.Action)
	}
	if res.WorkspaceID != "wNEW" {
		t.Errorf("WorkspaceID = %q, want wNEW", res.WorkspaceID)
	}
}

// The candidate normalises its own path when NormalizedPath is empty (defensive
// against callers that skip the resolver).
func TestFocusOrCreate_NormalizesMissingNormalizedPath(t *testing.T) {
	dir := t.TempDir()
	foo := mkDir(t, dir, "foo")
	// pass no NormalizedPath to confirm internal Abs+EvalSymlinks kicks in
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", out: workspaceListJSON("wA", "foo")},
		{match: "herdr pane list", out: paneListJSON(rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: foo, ForegroundCWD: foo, Focused: true})},
		{match: "herdr workspace focus wA", out: []byte(`{}`)},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.FocusOrCreate(context.Background(), source.Candidate{Path: foo, Label: "foo"}); err != nil {
		t.Fatalf("FocusOrCreate: %v", err)
	}
}

// foreground_cwd is also considered when cwd is empty (some Herdr panes only
// populate foreground_cwd while a command is running).
func TestFocusOrCreate_MatchesForegroundCWD(t *testing.T) {
	dir := t.TempDir()
	foo := mkDir(t, dir, "foo")
	fooN, err := filepath.EvalSymlinks(foo)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", out: workspaceListJSON("wA", "foo")},
		{
			match: "herdr pane list",
			out:   paneListJSON(rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: "", ForegroundCWD: foo, Focused: true}),
		},
		{match: "herdr workspace focus wA", out: []byte(`{}`)},
	}}
	d := New("herdr", WithRunner(r))
	res, err := d.FocusOrCreate(context.Background(), source.Candidate{Path: foo, NormalizedPath: fooN, Label: "foo"})
	if err != nil {
		t.Fatalf("FocusOrCreate: %v", err)
	}
	if res.Action != source.HerdrActionFocused {
		t.Fatalf("Action = %v, want HerdrActionFocused", res.Action)
	}
}

// HI-6: malformed workspace list JSON surfaces as an error so the caller can
// fall back to a path-print.
func TestListWorkspaces_MalformedJSONReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", out: []byte(`{not-json`)},
		{match: "herdr pane list", out: []byte(`{"id":"cli:pane:list","result":{"panes":[]}}`)},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ListWorkspaces(context.Background()); err == nil {
		t.Fatal("expected error on malformed workspace JSON")
	}
}

// HI-6: a herdr command failure (daemon down) surfaces as an error.
func TestListWorkspaces_DaemonDownReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace list", err: errors.New("exit status 1: daemon not running")},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ListWorkspaces(context.Background()); err == nil {
		t.Fatal("expected error when workspace list command fails")
	}
}

// HI-4: RunStartup lists the workspace's panes and runs the command in the
// first pane.
func TestRunStartup_RunsInFirstPane(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{
			match: "herdr pane list --workspace wA",
			out:   paneListJSON(rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: "/x", ForegroundCWD: "/x", Focused: true}),
		},
		{match: "herdr pane run wA:p1 echo hi", out: []byte(`{}`)},
	}}
	d := New("herdr", WithRunner(r))
	if err := d.RunStartup(context.Background(), "wA", "echo hi"); err != nil {
		t.Fatalf("RunStartup: %v", err)
	}
}

// HI-4: an empty command is a no-op (callers may have no startup configured).
func TestRunStartup_EmptyCommandIsNoOp(t *testing.T) {
	d := New("herdr", WithRunner(&fakeRunner{}))
	if err := d.RunStartup(context.Background(), "wA", "   "); err != nil {
		t.Fatalf("RunStartup empty: %v", err)
	}
}

// HI-4: a workspace with no panes is a startup error, surfaced not swallowed.
func TestRunStartup_NoPanesReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr pane list --workspace wA", out: paneListJSON()},
	}}
	d := New("herdr", WithRunner(r))
	if err := d.RunStartup(context.Background(), "wA", "echo hi"); err == nil {
		t.Fatal("expected error for empty workspace")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
