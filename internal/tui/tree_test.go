package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/source"
)

// TestTreeExpander_CacheHitAvoidsDriver proves two Fetch calls for the same
// workspace within the TTL window hit the cache on the second call.
func TestTreeExpander_CacheHitAvoidsDriver(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)

	if _, ok := tree.Fetch(context.Background(), "w1"); !ok {
		t.Fatal("first Fetch: expected ok=true")
	}
	if _, ok := tree.Fetch(context.Background(), "w1"); !ok {
		t.Fatal("second Fetch: expected ok=true")
	}
	if driver.listTabsN != 1 || driver.listPanesN != 1 {
		t.Errorf("expected exactly 1 ListTabs/ListPanes pair, got listTabsN=%d listPanesN=%d", driver.listTabsN, driver.listPanesN)
	}
}

// TestTreeExpander_ListTabsErrorDegradesGracefully proves a ListTabs
// failure degrades to ok=false, never a panic, and stores nothing.
func TestTreeExpander_ListTabsErrorDegradesGracefully(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabsErr: errors.New("herdr tab list: boom")}
	tree := NewTreeExpander(driver, time.Minute)
	if _, ok := tree.Fetch(context.Background(), "w1"); ok {
		t.Error("expected ok=false on ListTabs error")
	}
}

// TestTreeExpander_ListPanesErrorDegradesGracefully mirrors the above for
// ListPanes.
func TestTreeExpander_ListPanesErrorDegradesGracefully(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:     []source.Tab{{ID: "t1", WorkspaceID: "w1"}},
		panesErr: errors.New("herdr pane list: boom"),
	}
	tree := NewTreeExpander(driver, time.Minute)
	if _, ok := tree.Fetch(context.Background(), "w1"); ok {
		t.Error("expected ok=false on ListPanes error")
	}
}

// TestTreeExpander_ReadPane_CachelessPassthrough proves ReadPane forwards
// straight to the driver (no caching layer of its own — a pane's live
// buffer must never be served stale).
func TestTreeExpander_ReadPane_CachelessPassthrough(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{readText: "hello from pane"}
	tree := NewTreeExpander(driver, time.Minute)

	text, err := tree.ReadPane(context.Background(), "p1", 100)
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if text != "hello from pane" {
		t.Errorf("ReadPane text = %q, want %q", text, "hello from pane")
	}
	if _, err := tree.ReadPane(context.Background(), "p1", 100); err != nil {
		t.Fatalf("second ReadPane: %v", err)
	}
	if driver.readPaneN != 2 {
		t.Errorf("expected 2 driver ReadPane calls (no caching), got %d", driver.readPaneN)
	}
}

// TestTreeExpander_ReadPane_ErrorPropagates proves a driver error surfaces
// through ReadPane rather than being swallowed.
func TestTreeExpander_ReadPane_ErrorPropagates(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{readErr: errors.New("herdr pane read: boom")}
	tree := NewTreeExpander(driver, time.Minute)
	if _, err := tree.ReadPane(context.Background(), "p1", 100); err == nil {
		t.Error("expected ReadPane to propagate the driver error")
	}
}

// TestSynthesizeWorkspaceChildren_TabsAndPanes proves the full two-level
// tree is built: each tab gets its own nested panes, every synthesized
// candidate carries the ids a consumer needs (workspace_id always; tab_id
// on both; pane_id only on panes) — the contract launchChildTab and
// row.go's Enter routing depend on. Synthesized children carry NO Source
// (semantics live on the Row's Kind/Action, not a fake Source string) and
// carry workspace_label for concise parent-context display.
func TestSynthesizeWorkspaceChildren_TabsAndPanes(t *testing.T) {
	t.Parallel()
	tabs := []source.Tab{
		{ID: "t1", WorkspaceID: "w1", Label: "api"},
		{ID: "t2", WorkspaceID: "w1", Label: "db"},
	}
	panes := []source.Pane{
		{ID: "p1", Label: "worker", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api", ForegroundCWD: "/svc/api/cmd"},
		{ID: "p2", Label: "shell", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api", AgentStatus: "working"},
		{ID: "p3", WorkspaceID: "w1", TabID: "t2", CWD: "/svc/db"},
	}
	wc := synthesizeWorkspaceChildren("w1", "backend", "/svc", tabs, panes)

	if len(wc.Tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(wc.Tabs))
	}
	apiTab := wc.Tabs[0]
	if apiTab.Tab.Source != "" {
		t.Errorf("tab api Source = %q, want empty (synthesized children carry no Source)", apiTab.Tab.Source)
	}
	if apiTab.Tab.Meta["workspace_id"] != "w1" || apiTab.Tab.Meta["tab_id"] != "t1" {
		t.Errorf("tab api Meta = %v, want workspace_id=w1 tab_id=t1", apiTab.Tab.Meta)
	}
	if apiTab.Tab.Meta["workspace_label"] != "backend" {
		t.Errorf("tab api Meta[workspace_label] = %q, want \"backend\" (parent context)", apiTab.Tab.Meta["workspace_label"])
	}
	if len(apiTab.Panes) != 2 {
		t.Fatalf("expected 2 panes under tab api, got %d", len(apiTab.Panes))
	}
	p1 := apiTab.Panes[0]
	if p1.Meta["tab_id"] != "t1" || p1.Meta["pane_id"] != "p1" || p1.Meta["workspace_id"] != "w1" {
		t.Errorf("pane p1 Meta = %v, want workspace_id=w1 tab_id=t1 pane_id=p1", p1.Meta)
	}
	if p1.Path != "/svc/api/cmd" {
		t.Errorf("pane p1 Path = %q, want ForegroundCWD /svc/api/cmd", p1.Path)
	}
	if p1.Label != "worker" {
		t.Errorf("pane p1 Label = %q, want Herdr pane label \"worker\"", p1.Label)
	}
	if p1.Meta["tab_label"] != "api" {
		t.Errorf("pane p1 Meta[tab_label] = %q, want parent tab label \"api\"", p1.Meta["tab_label"])
	}
	if p1.Meta["agent_status"] != "" {
		t.Errorf("pane p1 Meta[agent_status] = %q, want empty (no status reported)", p1.Meta["agent_status"])
	}
	p2 := apiTab.Panes[1]
	if p2.Path != "/svc/api" {
		t.Errorf("pane p2 Path = %q, want CWD fallback /svc/api", p2.Path)
	}
	if p2.Label != "shell" {
		t.Errorf("pane p2 Label = %q, want Herdr pane label \"shell\"", p2.Label)
	}
	if p2.Meta["agent_status"] != "working" {
		t.Errorf("pane p2 Meta[agent_status] = %q, want \"working\" (carried in Meta, not the label — see render.go's agentStatusIcon)", p2.Meta["agent_status"])
	}

	dbTab := wc.Tabs[1]
	if len(dbTab.Panes) != 1 || dbTab.Panes[0].Meta["pane_id"] != "p3" {
		t.Errorf("expected exactly pane p3 under tab db, got %+v", dbTab.Panes)
	}
}

// TestSynthesizeWorkspaceChildren_ThreadsTabNumber proves every synthesized
// tab candidate retains the Herdr tab number needed for inline row rendering.
func TestSynthesizeWorkspaceChildren_ThreadsTabNumber(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		number int
		want   string
	}{
		{name: "single digit", number: 3, want: "3"},
		{name: "multiple digits", number: 12, want: "12"},
		{name: "zero value is omitted", number: 0, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			children := synthesizeWorkspaceChildren(
				"w1",
				"backend",
				"/srv",
				[]source.Tab{{ID: "t1", Label: "deploy", Number: tt.number}},
				nil,
			)

			if got := children.Tabs[0].Tab.Meta["tab_number"]; got != tt.want {
				t.Errorf("tab_number = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPrimaryTabCWD_PrefersForegroundCWD proves a tab's own display path
// prefers a live foreground process's cwd over the pane's own cwd.
func TestPrimaryTabCWD_PrefersForegroundCWD(t *testing.T) {
	t.Parallel()
	tab := source.Tab{ID: "t1"}
	panes := []source.Pane{{TabID: "t1", CWD: "/base", ForegroundCWD: "/base/sub"}}
	if got := primaryTabCWD(tab, panes, "/parent"); got != "/base/sub" {
		t.Errorf("primaryTabCWD = %q, want /base/sub", got)
	}
}

// TestPrimaryTabCWD_FallsBackToCWD proves the pane's own CWD is used when
// there is no live foreground process.
func TestPrimaryTabCWD_FallsBackToCWD(t *testing.T) {
	t.Parallel()
	tab := source.Tab{ID: "t1"}
	panes := []source.Pane{{TabID: "t1", CWD: "/base"}}
	if got := primaryTabCWD(tab, panes, "/parent"); got != "/base" {
		t.Errorf("primaryTabCWD = %q, want /base", got)
	}
}

// TestPrimaryTabCWD_FallsBackToParentPath proves a tab with no matching
// pane in the slice falls back to the parent workspace's path.
func TestPrimaryTabCWD_FallsBackToParentPath(t *testing.T) {
	t.Parallel()
	tab := source.Tab{ID: "t1"}
	if got := primaryTabCWD(tab, nil, "/parent"); got != "/parent" {
		t.Errorf("primaryTabCWD = %q, want /parent", got)
	}
}

func TestSelectTabPaneID(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		panes []source.Pane
		tabID string
		want  string
		ok    bool
	}{
		{
			name: "focused pane in selected tab wins",
			panes: []source.Pane{
				{ID: "p1", TabID: "t1"},
				{ID: "p2", TabID: "t2", Focused: true},
				{ID: "p3", TabID: "t1", Focused: true},
			},
			tabID: "t1",
			want:  "p3",
			ok:    true,
		},
		{
			name: "first pane in selected tab is deterministic fallback",
			panes: []source.Pane{
				{ID: "p1", TabID: "t2"},
				{ID: "p2", TabID: "t1"},
				{ID: "p3", TabID: "t1"},
			},
			tabID: "t1",
			want:  "p2",
			ok:    true,
		},
		{
			name:  "zero panes is unavailable",
			tabID: "t1",
			want:  "",
			ok:    false,
		},
		{
			name: "panes belonging only to another tab are unavailable",
			panes: []source.Pane{
				{ID: "p1", TabID: "t2", Focused: true},
				{ID: "p2", TabID: "t2"},
			},
			tabID: "t1",
			want:  "",
			ok:    false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := selectTabPaneID(tt.panes, tt.tabID)
			if got != tt.want || ok != tt.ok {
				t.Errorf("selectTabPaneID() = (%q, %t), want (%q, %t)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTreeExpanderResolveActivePaneID(t *testing.T) {
	t.Parallel()

	t.Run("uses cached workspace panes", func(t *testing.T) {
		driver := &fakeTreeDriver{
			tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1"}},
			panes: []source.Pane{
				{ID: "p1", WorkspaceID: "w1", TabID: "t1"},
				{ID: "p2", WorkspaceID: "w1", TabID: "t1", Focused: true},
			},
		}
		tree := NewTreeExpander(driver, time.Minute)

		for range 2 {
			got, ok := tree.ResolveActivePaneID(context.Background(), "w1", "t1")
			if got != "p2" || !ok {
				t.Errorf("ResolveActivePaneID() = (%q, %t), want (\"p2\", true)", got, ok)
			}
		}
		if driver.listTabsN != 1 || driver.listPanesN != 1 {
			t.Errorf("expected one cached ListTabs/ListPanes pair, got listTabsN=%d listPanesN=%d", driver.listTabsN, driver.listPanesN)
		}
	})

	t.Run("fetch failure is unavailable", func(t *testing.T) {
		tree := NewTreeExpander(&fakeTreeDriver{panesErr: errors.New("herdr pane list: boom")}, time.Minute)

		got, ok := tree.ResolveActivePaneID(context.Background(), "w1", "t1")
		if got != "" || ok {
			t.Errorf("ResolveActivePaneID() = (%q, %t), want (\"\", false)", got, ok)
		}
	})
}
