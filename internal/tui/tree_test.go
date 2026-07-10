package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// fakeTreeDriver is a controllable source.HerdrDriver scoped to
// TreeExpander's tests. Only ListTabs/ListPanes are exercised by Fetch;
// every other method returns an error if ever called, so a test would fail
// loudly instead of silently succeeding on an unintended code path.
type fakeTreeDriver struct {
	tabs     []source.Tab
	panes    []source.Pane
	tabsErr  error
	panesErr error

	listTabsN  int
	listPanesN int
}

func (f *fakeTreeDriver) Detect(context.Context) bool { return true }
func (f *fakeTreeDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, errors.New("fakeTreeDriver does not implement ListWorkspaces")
}
func (f *fakeTreeDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakeTreeDriver does not implement FocusOrCreate")
}

func (f *fakeTreeDriver) ListTabs(_ context.Context, _ string) ([]source.Tab, error) {
	f.listTabsN++
	if f.tabsErr != nil {
		return nil, f.tabsErr
	}
	return f.tabs, nil
}

func (f *fakeTreeDriver) ListPanes(_ context.Context, _ string) ([]source.Pane, error) {
	f.listPanesN++
	if f.panesErr != nil {
		return nil, f.panesErr
	}
	return f.panes, nil
}

func (f *fakeTreeDriver) ListAgents(context.Context) ([]source.Agent, error) {
	return nil, errors.New("fakeTreeDriver does not implement ListAgents")
}
func (f *fakeTreeDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("fakeTreeDriver does not implement ReadPane")
}
func (f *fakeTreeDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("fakeTreeDriver does not implement CreateTab")
}
func (f *fakeTreeDriver) RenameTab(context.Context, string, string) error {
	return errors.New("fakeTreeDriver does not implement RenameTab")
}
func (f *fakeTreeDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeTreeDriver does not implement SplitPane")
}
func (f *fakeTreeDriver) RunPane(context.Context, string, string) error {
	return errors.New("fakeTreeDriver does not implement RunPane")
}
func (f *fakeTreeDriver) FocusTab(context.Context, string) error {
	return errors.New("fakeTreeDriver does not implement FocusTab")
}
func (f *fakeTreeDriver) CurrentPane(context.Context) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeTreeDriver does not implement CurrentPane")
}

// TestTreeExpander_CacheHitAvoidsDriver (3.1, R6): two Fetch calls for the
// same workspace within the TTL window must hit the cache on the second
// call — the driver sees exactly one ListTabs/ListPanes pair, not two.
func TestTreeExpander_CacheHitAvoidsDriver(t *testing.T) {
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	exp := NewTreeExpander(driver, time.Minute)

	first, ok := exp.Fetch(context.Background(), "w1")
	if !ok {
		t.Fatal("first Fetch: expected ok=true")
	}
	if len(first.Tabs) != 1 || first.Tabs[0].ID != "t1" {
		t.Fatalf("first Fetch: unexpected tabs %+v", first.Tabs)
	}

	second, ok := exp.Fetch(context.Background(), "w1")
	if !ok {
		t.Fatal("second Fetch: expected ok=true")
	}
	if len(second.Tabs) != 1 || second.Tabs[0].ID != "t1" {
		t.Fatalf("second Fetch: unexpected tabs %+v", second.Tabs)
	}

	if driver.listTabsN != 1 {
		t.Errorf("ListTabs calls: got %d, want 1 (cache hit expected on second Fetch)", driver.listTabsN)
	}
	if driver.listPanesN != 1 {
		t.Errorf("ListPanes calls: got %d, want 1 (cache hit expected on second Fetch)", driver.listPanesN)
	}
}

// TestTreeExpander_ListTabsErrorDegradesGracefully (3.1 error path): a
// driver error must not crash Fetch — it returns ok=false and stores
// nothing, so a later successful Fetch is not shadowed by a poisoned cache
// entry.
func TestTreeExpander_ListTabsErrorDegradesGracefully(t *testing.T) {
	driver := &fakeTreeDriver{tabsErr: errors.New("herdr tab list: boom")}
	exp := NewTreeExpander(driver, time.Minute)

	if _, ok := exp.Fetch(context.Background(), "w1"); ok {
		t.Fatal("expected ok=false on driver error")
	}

	driver.tabsErr = nil
	driver.tabs = []source.Tab{{ID: "t1", WorkspaceID: "w1"}}
	driver.panes = []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1"}}

	got, ok := exp.Fetch(context.Background(), "w1")
	if !ok {
		t.Fatal("expected ok=true once the driver recovers (no poisoned cache entry from the earlier error)")
	}
	if len(got.Tabs) != 1 {
		t.Fatalf("recovered Fetch: unexpected tabs %+v", got.Tabs)
	}
}

// TestTreeExpander_ListPanesErrorDegradesGracefully (3.1 triangulation): the
// ListPanes call is a distinct early-return branch from ListTabs — this
// covers it separately so the branch isn't left implicit.
func TestTreeExpander_ListPanesErrorDegradesGracefully(t *testing.T) {
	driver := &fakeTreeDriver{
		tabs:     []source.Tab{{ID: "t1", WorkspaceID: "w1"}},
		panesErr: errors.New("herdr pane list: boom"),
	}
	exp := NewTreeExpander(driver, time.Minute)

	if _, ok := exp.Fetch(context.Background(), "w1"); ok {
		t.Fatal("expected ok=false when ListPanes errors")
	}
	if driver.listTabsN != 1 {
		t.Errorf("ListTabs calls: got %d, want 1 (called before the failing ListPanes)", driver.listTabsN)
	}
}

// --- Phase 4: pure child synthesis ---
//
// The design's Testing Strategy table names these tests
// TestModelTree_QueryInsertsOnlyMatchingChildTabs, TestModelTree_CWDOnlyTabMatch,
// TestModelTree_WorkspaceOnlyMatchStaysFlat and TestModelTree_ChildCandidateCarriesIDs
// against a wired Model.applyFilter — but that wiring is Phase 5 (PR3, not yet
// implemented). Adapted here to exercise the same behavior directly against
// the pure functions Phase 5 will call: synthesizeChildren (the row builder)
// and matchingChildren (the query filter, reusing model.go's own
// candidateSource+fuzzy.FindFrom contract — no second matching
// implementation). Test names are kept identical to the design so PR3's
// verify phase can trace them back to the same requirement.

// TestModelTree_QueryInsertsOnlyMatchingChildTabs (4.1/R2): a query matching
// one tab's Label inserts only that tab's synthesized child; the sibling
// tab is excluded.
func TestModelTree_QueryInsertsOnlyMatchingChildTabs(t *testing.T) {
	tabs := []source.Tab{
		{ID: "t1", WorkspaceID: "w1", Label: "api"},
		{ID: "t2", WorkspaceID: "w1", Label: "db"},
	}
	panes := []source.Pane{
		{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"},
		{ID: "p2", WorkspaceID: "w1", TabID: "t2", CWD: "/svc/db"},
	}
	children := synthesizeChildren("w1", "/svc", tabs, panes)

	matched := matchingChildren("api", children)

	if len(matched) != 1 {
		t.Fatalf("matched children: got %d, want 1: %+v", len(matched), matched)
	}
	if matched[0].Label != "api" {
		t.Errorf("matched child label: got %q, want %q", matched[0].Label, "api")
	}
}

// TestModelTree_CWDOnlyTabMatch (4.1/R2): a tab with no Label still matches
// via its resolved CWD, using the same "Label path" haystack shape as the
// parent-level matcher (model.go's candidateSource).
func TestModelTree_CWDOnlyTabMatch(t *testing.T) {
	tabs := []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: ""}}
	panes := []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/var/log"}}
	children := synthesizeChildren("w1", "/svc", tabs, panes)

	matched := matchingChildren("log", children)

	if len(matched) != 1 {
		t.Fatalf("expected CWD-only match, got %d children: %+v", len(matched), matched)
	}
	if matched[0].Path != "/var/log" {
		t.Errorf("matched child path: got %q, want %q", matched[0].Path, "/var/log")
	}
}

// TestModelTree_WorkspaceOnlyMatchStaysFlat (4.1/R2): a query that would
// match the PARENT workspace row's own Label ("backend") shares no
// characters with either child's haystack ("api /svc/api", "db /svc/db"),
// so matchingChildren returns zero rows — no children are synthesized
// for this filter pass.
func TestModelTree_WorkspaceOnlyMatchStaysFlat(t *testing.T) {
	tabs := []source.Tab{
		{ID: "t1", WorkspaceID: "w1", Label: "api"},
		{ID: "t2", WorkspaceID: "w1", Label: "db"},
	}
	panes := []source.Pane{
		{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"},
		{ID: "p2", WorkspaceID: "w1", TabID: "t2", CWD: "/svc/db"},
	}
	children := synthesizeChildren("w1", "/svc", tabs, panes)

	matched := matchingChildren("backend", children)

	if len(matched) != 0 {
		t.Fatalf("expected zero matching children (workspace-only match), got %d: %+v", len(matched), matched)
	}
}

// TestModelTree_ChildCandidateCarriesIDs (4.1/R3): a synthesized child row
// carries Source=SourceHerdrTab and Meta[workspace_id]/Meta[tab_id] so
// PR3's launchChildTab can route Enter to driver.FocusTab.
func TestModelTree_ChildCandidateCarriesIDs(t *testing.T) {
	tabs := []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}
	panes := []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}}

	children := synthesizeChildren("w1", "/svc", tabs, panes)

	if len(children) != 1 {
		t.Fatalf("expected exactly one synthesized child, got %d", len(children))
	}
	child := children[0]
	if child.Source != config.SourceHerdrTab {
		t.Errorf("child.Source: got %q, want %q", child.Source, config.SourceHerdrTab)
	}
	if child.Meta["workspace_id"] != "w1" {
		t.Errorf("child.Meta[workspace_id]: got %q, want %q", child.Meta["workspace_id"], "w1")
	}
	if child.Meta["tab_id"] != "t1" {
		t.Errorf("child.Meta[tab_id]: got %q, want %q", child.Meta["tab_id"], "t1")
	}
}

// TestPrimaryTabCWD_PrefersForegroundCWD (4.2 triangulation): when the
// matching pane has a non-empty ForegroundCWD (a live foreground process),
// it wins over the pane's own CWD.
func TestPrimaryTabCWD_PrefersForegroundCWD(t *testing.T) {
	tab := source.Tab{ID: "t1", WorkspaceID: "w1"}
	panes := []source.Pane{
		{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api", ForegroundCWD: "/svc/api/cmd"},
	}
	got := primaryTabCWD(tab, panes, "/svc")
	if got != "/svc/api/cmd" {
		t.Errorf("primaryTabCWD: got %q, want ForegroundCWD %q", got, "/svc/api/cmd")
	}
}

// TestPrimaryTabCWD_FallsBackToCWD (4.2 triangulation): an empty
// ForegroundCWD falls back to the matching pane's CWD.
func TestPrimaryTabCWD_FallsBackToCWD(t *testing.T) {
	tab := source.Tab{ID: "t1", WorkspaceID: "w1"}
	panes := []source.Pane{
		{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api", ForegroundCWD: ""},
	}
	got := primaryTabCWD(tab, panes, "/svc")
	if got != "/svc/api" {
		t.Errorf("primaryTabCWD: got %q, want CWD %q", got, "/svc/api")
	}
}

// TestPrimaryTabCWD_FallsBackToParentPath (4.2 triangulation): a tab with
// no matching pane in the slice at all falls back to parentPath, so a
// synthesized row never renders an empty path.
func TestPrimaryTabCWD_FallsBackToParentPath(t *testing.T) {
	tab := source.Tab{ID: "t1", WorkspaceID: "w1"}
	panes := []source.Pane{
		{ID: "p9", WorkspaceID: "w1", TabID: "other-tab", CWD: "/unrelated"},
	}
	got := primaryTabCWD(tab, panes, "/svc")
	if got != "/svc" {
		t.Errorf("primaryTabCWD: got %q, want parentPath fallback %q", got, "/svc")
	}
}
