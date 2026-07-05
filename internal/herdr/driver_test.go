package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

// tabListJSON builds a tab list envelope for a workspace from rawTab entries.
func tabListJSON(tabs ...rawTab) []byte {
	out := `{"id":"cli:tab:list","result":{"type":"tab_list","tabs":[`
	for i, t := range tabs {
		if i > 0 {
			out += ","
		}
		out += `{"tab_id":"` + t.TabID + `","workspace_id":"` + t.WorkspaceID +
			`","label":"` + t.Label + `","focused":` + boolStr(t.Focused) +
			`,"number":` + strconv.Itoa(t.Number) +
			`,"pane_count":` + strconv.Itoa(t.PaneCount) + `}`
	}
	out += `]}}`
	return []byte(out)
}

// agentListJSON builds an agent list envelope from rawAgent entries.
func agentListJSON(agents ...rawAgent) []byte {
	out := `{"id":"cli:agent:list","result":{"agents":[`
	for i, a := range agents {
		if i > 0 {
			out += ","
		}
		out += `{"agent_id":"` + a.AgentID + `","label":"` + a.Label +
			`","agent_status":"` + a.AgentStatus + `"}`
	}
	out += `]}}`
	return []byte(out)
}

// blockingHerdrRunner blocks until ctx is cancelled, mirroring a daemon that
// never responds. Used to exercise context-cancellation behaviour.
type blockingHerdrRunner struct{ calls []string }

func (b *blockingHerdrRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	b.calls = append(b.calls, key)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRawWorkspace_DeserializesCounts (4.1) confirms number, tab_count,
// pane_count, and agent_status from `herdr workspace list` are now captured by
// rawWorkspace (previously discarded). White-box: rawWorkspace is unexported.
func TestRawWorkspace_DeserializesCounts(t *testing.T) {
	in := []byte(`{"workspace_id":"wA","label":"foo","active_tab_id":"wA:t1",` +
		`"focused":true,"number":3,"tab_count":2,"pane_count":4,"agent_status":"running"}`)
	var w rawWorkspace
	if err := json.Unmarshal(in, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.Number != 3 || w.TabCount != 2 || w.PaneCount != 4 || w.AgentStatus != "running" {
		t.Errorf("counts not deserialised: %+v", w)
	}
}

// TestListTabs_ParsesEnvelope (4.2/4.5) parses a tab list envelope into
// source.Tab values honouring id/label/focused/number/pane_count.
func TestListTabs_ParsesEnvelope(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{
			match: "herdr tab list --workspace wA",
			out: tabListJSON(
				rawTab{TabID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 2},
				rawTab{TabID: "wA:t2", WorkspaceID: "wA", Label: "term", Focused: false, Number: 2, PaneCount: 1},
			),
		},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListTabs(context.Background(), "wA")
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tabs, got %d: %+v", len(got), got)
	}
	want := []source.Tab{
		{ID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 2},
		{ID: "wA:t2", WorkspaceID: "wA", Label: "term", Focused: false, Number: 2, PaneCount: 1},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("tab[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestListTabs_EmptyListIsNotError (4.5): an empty tabs array is a normal
// nil/empty result, not an error.
func TestListTabs_EmptyListIsNotError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr tab list --workspace wA", out: tabListJSON()},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListTabs(context.Background(), "wA")
	if err != nil {
		t.Fatalf("ListTabs empty: %v", err)
	}
	if got != nil && len(got) != 0 {
		t.Errorf("expected nil/empty, got %+v", got)
	}
}

// TestListTabs_MalformedJSONReturnsError (4.5): a malformed envelope surfaces
// as an error so the preview layer can degrade gracefully.
func TestListTabs_MalformedJSONReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr tab list --workspace wA", out: []byte(`{not-json`)},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ListTabs(context.Background(), "wA"); err == nil {
		t.Fatal("expected error on malformed tab JSON")
	}
}

// TestListTabs_CommandErrorReturnsError (4.5): a tab-list command failure
// (daemon down) surfaces as an error.
func TestListTabs_CommandErrorReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr tab list --workspace wA", err: errors.New("exit status 1")},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ListTabs(context.Background(), "wA"); err == nil {
		t.Fatal("expected error when tab list command fails")
	}
}

// TestListPanes_ParsesEnvelope (4.2/4.5) parses a workspace-scoped pane list.
func TestListPanes_ParsesEnvelope(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{
			match: "herdr pane list --workspace wA",
			out: paneListJSON(
				rawPane{PaneID: "wA:p1", WorkspaceID: "wA", CWD: "/x", ForegroundCWD: "/x", Focused: true},
				rawPane{PaneID: "wA:p2", WorkspaceID: "wA", CWD: "/y", ForegroundCWD: "", Focused: false},
			),
		},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListPanes(context.Background(), "wA")
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	want := []source.Pane{
		{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", ForegroundCWD: "/x", Focused: true},
		{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", ForegroundCWD: "", Focused: false},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d panes, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("pane[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestListPanes_CommandErrorReturnsError (4.5).
func TestListPanes_CommandErrorReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr pane list --workspace wA", err: errors.New("exit status 1")},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ListPanes(context.Background(), "wA"); err == nil {
		t.Fatal("expected error when pane list command fails")
	}
}

// TestListAgents_ParsesEnvelope (4.2/4.5) parses an agent list envelope into
// source.Agent values honouring id/label/agent_status.
func TestListAgents_ParsesEnvelope(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{
			match: "herdr agent list",
			out: agentListJSON(
				rawAgent{AgentID: "a1", Label: "coder", AgentStatus: "running"},
				rawAgent{AgentID: "a2", Label: "planner", AgentStatus: "idle"},
			),
		},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	want := []source.Agent{
		{ID: "a1", Label: "coder", Status: "running"},
		{ID: "a2", Label: "planner", Status: "idle"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d agents, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("agent[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestListAgents_EmptyListIsNotError (4.5): no agents is normal.
func TestListAgents_EmptyListIsNotError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr agent list", out: agentListJSON()},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents empty: %v", err)
	}
	if got != nil && len(got) != 0 {
		t.Errorf("expected nil/empty, got %+v", got)
	}
}

// TestListTabs_ContextDeadlineSurfaces (4.5): a blocked daemon honours a
// context deadline and returns the underlying ctx error.
func TestListTabs_ContextDeadlineSurfaces(t *testing.T) {
	runner := &blockingHerdrRunner{}
	d := New("herdr", WithRunner(runner))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := d.ListTabs(ctx, "wA"); err == nil {
		t.Fatal("expected error when context deadline exceeded")
	}
}

// readPaneOut wraps a raw stdout string for ReadPane calls. ReadPane does not
// parse a JSON envelope; `herdr pane read --format ansi` returns the captured
// terminal buffer directly.
func readPaneOut(content string) []byte { return []byte(content) }

// TestReadPane_ReturnsBuffer (4.2/4.5) runs `herdr pane read <pane_id>
// --lines <n> --format ansi` and returns the raw stdout buffer.
func TestReadPane_ReturnsBuffer(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr pane read wA:p1 --lines 50 --format ansi", out: readPaneOut("$ ls\nfile.go")},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ReadPane(context.Background(), "wA:p1", 50)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if got != "$ ls\nfile.go" {
		t.Errorf("ReadPane buffer: got %q want %q", got, "$ ls\\nfile.go")
	}
}

// TestReadPane_AnonlinesZeroDefaults (4.5): lines <= 0 omits the flag so the
// daemon applies its own default cap.
func TestReadPane_AnonlinesZeroDefaults(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr pane read wA:p1 --format ansi", out: readPaneOut("ok")},
	}}
	d := New("herdr", WithRunner(r))
	got, err := d.ReadPane(context.Background(), "wA:p1", 0)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if got != "ok" {
		t.Errorf("ReadPane buffer: got %q want %q", got, "ok")
	}
}

// TestReadPane_EmptyPaneIDReturnsError (4.5): an empty pane id is rejected
// before shelling out.
func TestReadPane_EmptyPaneIDReturnsError(t *testing.T) {
	d := New("herdr", WithRunner(&fakeRunner{}))
	if _, err := d.ReadPane(context.Background(), "", 50); err == nil {
		t.Fatal("expected error for empty pane id")
	}
}

// TestReadPane_CommandErrorReturnsError (4.5): a read command failure
// surfaces as an error.
func TestReadPane_CommandErrorReturnsError(t *testing.T) {
	r := &fakeRunner{script: []fakeCall{
		{match: "herdr pane read wA:p1 --lines 10 --format ansi", err: errors.New("exit status 1")},
	}}
	d := New("herdr", WithRunner(r))
	if _, err := d.ReadPane(context.Background(), "wA:p1", 10); err == nil {
		t.Fatal("expected error when pane read command fails")
	}
}

// TestReadPane_ContextDeadlineSurfaces (4.5).
func TestReadPane_ContextDeadlineSurfaces(t *testing.T) {
	runner := &blockingHerdrRunner{}
	d := New("herdr", WithRunner(runner))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := d.ReadPane(ctx, "wA:p1", 50); err == nil {
		t.Fatal("expected error when context deadline exceeded")
	}
}
