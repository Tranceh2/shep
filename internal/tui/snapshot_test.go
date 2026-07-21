package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

type scriptedSnapshotDriver struct {
	responses  []snapshotDriverResponse
	calls      int
	snapshotFn func(context.Context) (source.Snapshot, error)
}

type snapshotDriverResponse struct {
	snapshot source.Snapshot
	err      error
}

func (d *scriptedSnapshotDriver) Snapshot(ctx context.Context) (source.Snapshot, error) {
	d.calls++
	if d.snapshotFn != nil {
		return d.snapshotFn(ctx)
	}
	if len(d.responses) == 0 {
		return source.Snapshot{}, errors.New("unexpected snapshot")
	}
	response := d.responses[0]
	d.responses = d.responses[1:]
	return response.snapshot, response.err
}

func (d *scriptedSnapshotDriver) ReadPane(context.Context, string, int) (string, error) {
	return "", errors.New("not used")
}

type snapshotRenderer struct{ text string }

func (r snapshotRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: r.text}, nil
}

func snapshotGeneration(workspaceID, paneID, status string) source.Snapshot {
	return source.Snapshot{
		Workspaces:         []source.Workspace{{ID: workspaceID, Label: workspaceID, ActiveTabID: workspaceID + ":t1", Focused: true}},
		Tabs:               []source.Tab{{ID: workspaceID + ":t1", WorkspaceID: workspaceID, Label: "editor", Focused: true, Number: 1, PaneCount: 1}},
		Panes:              []source.Pane{{ID: paneID, WorkspaceID: workspaceID, TabID: workspaceID + ":t1", CWD: "/" + workspaceID, Focused: true, AgentStatus: status}},
		FocusedWorkspaceID: workspaceID,
		FocusedTabID:       workspaceID + ":t1",
		FocusedPaneID:      paneID,
	}
}

func newSnapshotModel(driver *scriptedSnapshotDriver, initial source.Snapshot, herdrIcon string) Model {
	base := []source.Candidate{
		{Label: "z", Path: "/z", Source: config.SourceZoxide},
		source.HerdrCandidates(initial)[0],
		{Label: "command", Path: "/command", Source: config.SourceWorkspaces},
	}
	if herdrIcon != "" {
		base[1].Icon = herdrIcon
	}
	m := NewModelWithTree(base, snapshotRenderer{text: "initial"}, NewTreeExpanderFromSnapshot(initial), Layout{})
	return m.WithSnapshotRefresh(driver, initial, func(snapshot source.Snapshot) preview.Renderer {
		return snapshotRenderer{text: snapshot.Panes[0].AgentStatus}
	}, herdrIcon)
}

func newEmptySnapshotModel(driver *scriptedSnapshotDriver, herdrIcon string) Model {
	initial := source.Snapshot{}
	base := []source.Candidate{
		{Label: "z", Path: "/z", Source: config.SourceZoxide},
		{Label: "command", Path: "/command", Source: config.SourceWorkspaces},
	}
	return NewModelWithTree(base, snapshotRenderer{text: "initial"}, NewTreeExpanderFromSnapshot(initial), Layout{}).
		WithSnapshotRefresh(driver, initial, nil, herdrIcon)
}

func applySnapshotRefresh(t *testing.T, m Model) Model {
	t.Helper()
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	cmd := m.applyFilter()
	if cmd == nil {
		t.Fatal("expired generation did not schedule a refresh")
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func TestSnapshotRefresh_GatesByTTLAndSingleFlight(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snapshotGeneration("w2", "w2:p1", "idle")}}}
	m := newSnapshotModel(driver, initial, "")

	if cmd := m.applyFilter(); cmd != nil {
		t.Fatal("fresh generation unexpectedly scheduled a refresh")
	}
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	cmd := m.applyFilter()
	if cmd == nil {
		t.Fatal("expired generation did not schedule a refresh")
	}
	if !m.snapshotRefreshing {
		t.Fatal("snapshot refresh was not marked in flight")
	}
	if second := m.applyFilter(); second != nil {
		t.Fatal("eligible activity started a second snapshot refresh while one was in flight")
	}
	if driver.calls != 0 {
		t.Fatalf("Snapshot was called before the returned command ran: %d", driver.calls)
	}
	_ = cmd()
	if driver.calls != 1 {
		t.Fatalf("Snapshot calls = %d, want exactly 1", driver.calls)
	}
}

func TestSnapshotRefresh_EligibleKeyBatchesRefreshCommand(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snapshotGeneration("w2", "w2:p1", "idle")}}}
	m := newSnapshotModel(driver, initial, "")
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = next.(Model)
	if !m.snapshotRefreshing {
		t.Fatal("eligible key activity did not start a refresh")
	}
	if !containsSnapshotResponse(cmd) {
		t.Fatal("eligible key activity did not return the refresh command for Bubble Tea to run")
	}
	if driver.calls != 1 {
		t.Fatalf("Snapshot calls = %d, want exactly 1", driver.calls)
	}
}

func containsSnapshotResponse(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(snapshotResponseMsg); ok {
		return true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, queued := range batch {
		if _, ok := queued().(snapshotResponseMsg); ok {
			return true
		}
	}
	return false
}

func TestSnapshotRefresh_SuccessAtomicallyReplacesGeneration(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	replacement := snapshotGeneration("w2", "w2:p1", "idle")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: replacement}}}
	m := newSnapshotModel(driver, initial, "")
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	cmd := m.applyFilter()
	if cmd == nil {
		t.Fatal("expired generation did not schedule a refresh")
	}
	msg := cmd()
	next, previewCmd := m.Update(msg)
	m = next.(Model)
	if previewCmd == nil {
		t.Fatal("successful replacement did not dispatch a new current-row preview")
	}

	if got := []string{m.baseCandidates[0].Source, m.baseCandidates[1].Meta["workspace_id"], m.baseCandidates[2].Source}; got[0] != config.SourceZoxide || got[1] != "w2" || got[2] != config.SourceWorkspaces {
		t.Errorf("candidate source order after replacement = %v, want zoxide, w2, workspaces", got)
	}
	if m.currentPane == nil || *m.currentPane != replacement.Panes[0] {
		t.Errorf("currentPane = %+v, want %+v", m.currentPane, replacement.Panes[0])
	}
	tree, ok := m.tree.Fetch(context.Background(), "w2")
	if !ok || len(tree.Panes) != 1 || tree.Panes[0].AgentStatus != "idle" {
		t.Errorf("replacement tree = %+v (ok=%v), want w2 idle pane", tree, ok)
	}
	result, err := m.renderer.Render(context.Background(), m.baseCandidates[1])
	if err != nil || result.Text != "idle" {
		t.Errorf("replacement renderer result = (%+v, %v), want idle", result, err)
	}
	if m.previewSeq == 0 {
		t.Error("successful generation replacement did not bump previewSeq")
	}
}

func TestSnapshotRefresh_PreservesConfiguredHerdrIcon(t *testing.T) {
	for _, tt := range []struct {
		name string
		icon string
	}{
		{name: "custom icon", icon: "󰅩"},
		{name: "empty icon preserves default behavior"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initial := snapshotGeneration("w1", "w1:p1", "working")
			replacement := snapshotGeneration("w2", "w2:p1", "idle")
			driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: replacement}}}
			m := applySnapshotRefresh(t, newSnapshotModel(driver, initial, tt.icon))

			if len(m.baseCandidates) != 3 {
				t.Fatalf("candidate count = %d, want 3: %+v", len(m.baseCandidates), m.baseCandidates)
			}
			got := m.baseCandidates[1]
			want := source.HerdrCandidates(replacement)[0]
			if got.Icon != tt.icon {
				t.Errorf("refreshed Herdr icon = %q, want %q", got.Icon, tt.icon)
			}
			if got.Path != want.Path || got.Label != want.Label || got.Missing != want.Missing || got.Meta["workspace_id"] != want.Meta["workspace_id"] {
				t.Errorf("refreshed Herdr candidate = %+v, want preserved fields from %+v", got, want)
			}
			if got, want := m.baseCandidates[0].Source, config.SourceZoxide; got != want {
				t.Errorf("first unrelated source = %q, want %q", got, want)
			}
			if got, want := m.baseCandidates[2].Source, config.SourceWorkspaces; got != want {
				t.Errorf("last unrelated source = %q, want %q", got, want)
			}
		})
	}
}

func TestSnapshotRefresh_FirstAppearanceGetsConfiguredHerdrIcon(t *testing.T) {
	const herdrIcon = "󰅩"
	replacement := snapshotGeneration("w2", "w2:p1", "idle")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: replacement}}}
	m := applySnapshotRefresh(t, newEmptySnapshotModel(driver, herdrIcon))

	if len(m.baseCandidates) != 3 {
		t.Fatalf("candidate count = %d, want 3: %+v", len(m.baseCandidates), m.baseCandidates)
	}
	if got := []string{m.baseCandidates[0].Source, m.baseCandidates[1].Source, m.baseCandidates[2].Source}; got[0] != config.SourceZoxide || got[1] != config.SourceWorkspaces || got[2] != config.SourceHerdr {
		t.Errorf("candidate source order = %v, want zoxide, workspaces, herdr", got)
	}
	got := m.baseCandidates[2]
	want := source.HerdrCandidates(replacement)[0]
	if got.Icon != herdrIcon {
		t.Errorf("first refreshed Herdr icon = %q, want %q", got.Icon, herdrIcon)
	}
	if got.Path != want.Path || got.Label != want.Label || got.Missing != want.Missing || got.Meta["workspace_id"] != want.Meta["workspace_id"] {
		t.Errorf("first refreshed Herdr candidate = %+v, want preserved fields from %+v", got, want)
	}
}

func TestSnapshotRefresh_FailureAndStaleResponseRetainPriorGeneration(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{err: errors.New("daemon unavailable")}}}
	m := newSnapshotModel(driver, initial, "")
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	cmd := m.applyFilter()
	next, _ := m.Update(cmd())
	m = next.(Model)
	if m.baseCandidates[1].Meta["workspace_id"] != "w1" || m.currentPane == nil || m.currentPane.ID != "w1:p1" {
		t.Errorf("failed refresh replaced usable generation: candidates=%+v current=%+v", m.baseCandidates, m.currentPane)
	}
	if m.previewErr != "snapshot refresh failed" {
		t.Errorf("previewErr = %q, want concise refresh diagnostic", m.previewErr)
	}

	stale := snapshotResponseMsg{seq: m.snapshotSeq - 1, snapshot: snapshotGeneration("w9", "w9:p1", "done")}
	next, _ = m.Update(stale)
	m = next.(Model)
	if m.baseCandidates[1].Meta["workspace_id"] != "w1" || m.currentPane == nil || m.currentPane.ID != "w1:p1" {
		t.Errorf("stale refresh response replaced prior generation: candidates=%+v current=%+v", m.baseCandidates, m.currentPane)
	}
}

func TestSnapshotRefresh_FailureBacksOffBeforeRetry(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	retry := snapshotGeneration("w2", "w2:p1", "idle")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{err: errors.New("daemon unavailable")}, {snapshot: retry}}}
	m := newSnapshotModel(driver, initial, "")
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)

	first := m.applyFilter()
	if first == nil {
		t.Fatal("expired generation did not schedule an initial refresh")
	}
	next, _ := m.Update(first())
	m = next.(Model)
	if m.snapshotRefreshing || m.previewErr != "snapshot refresh failed" || m.baseCandidates[1].Meta["workspace_id"] != "w1" {
		t.Fatalf("failed refresh state = refreshing:%t diagnostic:%q candidates:%+v", m.snapshotRefreshing, m.previewErr, m.baseCandidates)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = next.(Model)
	if m.snapshotRefreshing || driver.calls != 1 {
		t.Fatalf("immediate activity state = refreshing:%t calls:%d, want false and 1", m.snapshotRefreshing, driver.calls)
	}

	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	second := m.applyFilter()
	if second == nil {
		t.Fatal("refresh did not become eligible again after the TTL")
	}
	next, _ = m.Update(second())
	m = next.(Model)
	if driver.calls != 2 || m.baseCandidates[1].Meta["workspace_id"] != "w2" {
		t.Errorf("retry state = calls:%d candidates:%+v, want 2 and workspace w2", driver.calls, m.baseCandidates)
	}
}

func TestSnapshotRefresh_TimeoutRetainsPriorGeneration(t *testing.T) {
	initial := snapshotGeneration("w1", "w1:p1", "working")
	driver := &scriptedSnapshotDriver{
		snapshotFn: func(ctx context.Context) (source.Snapshot, error) {
			<-ctx.Done()
			return source.Snapshot{}, ctx.Err()
		},
	}
	m := newSnapshotModel(driver, initial, "")
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)

	cmd := m.applyFilter()
	if cmd == nil {
		t.Fatal("expired generation did not schedule a refresh")
	}
	started := time.Now()
	msg := cmd()
	if elapsed := time.Since(started); elapsed > source.SnapshotTimeout+500*time.Millisecond {
		t.Fatalf("snapshot refresh took %v, want bounded by %v", elapsed, source.SnapshotTimeout)
	}
	next, _ := m.Update(msg)
	m = next.(Model)

	if m.snapshotRefreshing {
		t.Fatal("timed-out refresh left the single-flight guard set")
	}
	if m.baseCandidates[1].Meta["workspace_id"] != "w1" || m.currentPane == nil || m.currentPane.ID != "w1:p1" {
		t.Errorf("timed-out refresh replaced usable generation: candidates=%+v current=%+v", m.baseCandidates, m.currentPane)
	}
	if m.previewErr != "snapshot refresh failed" {
		t.Errorf("previewErr = %q, want concise refresh diagnostic", m.previewErr)
	}
}
