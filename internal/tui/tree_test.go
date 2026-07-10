package tui

import (
	"context"
	"errors"
	"testing"
	"time"

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
