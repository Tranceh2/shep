package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

func TestOpen_RankingProducerAndPinToggleConcurrent(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = true
	app := New()
	app.cfg = cfg
	app.rankingOpen = tempRankingOpen(t)
	producer := app.buildRankingProducer()
	candidate := source.Candidate{Path: "/tmp/project", Label: "project"}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		msg := producer(context.Background())
		if msg.Err != nil {
			t.Errorf("ranking producer error: %v", msg.Err)
		}
	}()
	go func() {
		defer wg.Done()
		result := app.pinToggler()(context.Background(), candidate)
		if result.Err != nil {
			t.Errorf("pin toggle error: %v", result.Err)
		}
	}()
	wg.Wait()
	app.rankingMu.Lock()
	store := app.rankingStore
	app.rankingStore = nil
	app.rankingMu.Unlock()
	if store != nil {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func tempRankingOpen(t *testing.T) func() (rankingStore, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	return func() (rankingStore, error) {
		return ranking.OpenPath(path)
	}
}

// TestOpen_AsyncLoader_InteractiveStartup verifies that running `shep open` with
// no arguments triggers the input-first async TUI path, executing the streaming producers
// and launching the selected candidate.
func TestOpen_AsyncLoader_InteractiveStartup(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	projDir := t.TempDir()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "my-proj", Path: projDir},
	}
	driver := &openDriver{
		detect:      true,
		workspaceID: "w-new",
		lastAction:  source.HerdrActionCreated,
	}

	var producersCount int
	var capturedQuery string

	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithAsyncTUIRunner(func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			producersCount = len(producers)
			capturedQuery = query

			var allCandidates []source.Candidate
			for _, p := range producers {
				msg := p(ctx)
				if msg.Err != nil {
					return source.Candidate{}, tui.RowActionOpen, "", nil, false, msg.Err
				}
				allCandidates = append(allCandidates, msg.Candidates...)
			}
			if len(allCandidates) == 0 {
				return source.Candidate{}, tui.RowActionOpen, "", nil, false, errors.New("no candidates from producers")
			}

			// Select the workspace candidate
			for _, c := range allCandidates {
				if c.Label == "my-proj" {
					return c, tui.RowActionOpen, "", nil, true, nil
				}
			}
			return allCandidates[0], tui.RowActionOpen, "", nil, true, nil
		}),
	)

	var out, errOut bytes.Buffer
	app.out = &out
	app.err = &errOut
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	app.rankingOpen = tempRankingOpen(t)

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	err := app.runOpenWithView(cmd, "", "", "workspace", "")
	if err != nil {
		t.Fatalf("runOpen failed: %v", err)
	}

	if producersCount == 0 {
		t.Fatal("expected streaming producers to be provided for interactive shep open")
	}
	if capturedQuery != "" {
		t.Errorf("capturedQuery = %q, want empty", capturedQuery)
	}
	if driver.lastCand.Label != "my-proj" {
		t.Errorf("launched candidate label = %q, want 'my-proj'", driver.lastCand.Label)
	}
}

// TestOpen_AsyncLoader_PickerStartsBeforeTheRankingStore proves the picker's
// first frame does not wait on the ranking store: its SQLite open (and the
// Herdr focus history read with it) runs in the background, and the command
// still closes the store once the picker has returned.
func TestOpen_AsyncLoader_PickerStartsBeforeTheRankingStore(t *testing.T) {
	t.Parallel()

	pickerStarted := make(chan struct{})
	var releasedByPicker atomic.Bool
	open := tempRankingOpen(t)
	app := New(
		WithHerdrDriver(&openDriver{detect: true}),
		WithAsyncTUIRunner(func(context.Context, []tui.SourceProducer, string, tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			close(pickerStarted)
			return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
		}),
	)
	var out, errOut bytes.Buffer
	app.out = &out
	app.err = &errOut
	app.cfg = config.Defaults()
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = func() (rankingStore, error) {
		select {
		case <-pickerStarted:
			releasedByPicker.Store(true)
		case <-time.After(2 * time.Second):
		}
		return open()
	}

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	if err := app.runOpenWithView(cmd, "", "", "workspace", ""); err != nil {
		t.Fatalf("runOpen failed: %v", err)
	}
	if !releasedByPicker.Load() {
		t.Fatal("the picker started only after the ranking store opened")
	}
	app.rankingMu.Lock()
	store := app.rankingStore
	app.rankingMu.Unlock()
	if store != nil {
		t.Fatal("ranking store left open after the picker returned")
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

// TestOpen_AsyncLoader_CancelReturnsQuiet proves that interactive cancellation
// (Esc / ctrl+c in TUI) cleanly returns nil without printing errors or candidate dumps.
func TestOpen_AsyncLoader_CancelReturnsQuiet(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	driver := &openDriver{detect: true}

	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithAsyncTUIRunner(func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			return source.Candidate{}, tui.RowActionOpen, "", nil, false, tui.ErrCancelled
		}),
	)

	var out, errOut bytes.Buffer
	app.out = &out
	app.err = &errOut
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	app.rankingOpen = tempRankingOpen(t)

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	err := app.runOpenWithView(cmd, "", "", "workspace", "")
	if err != nil {
		t.Fatalf("runOpen on cancel = %v, want nil", err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("expected zero output on cancel, got stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

// TestOpen_AsyncLoader_GroupWorkspaceRecursion proves that selecting a group
// workspace in the async TUI drills into nested registry resolution.
func TestOpen_AsyncLoader_GroupWorkspaceRecursion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	svc := filepath.Join(root, "svc")
	if err := os.MkdirAll(filepath.Join(svc, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "group", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}},
	}

	driver := &openDriver{
		detect:      true,
		workspaceID: "wA",
		lastAction:  source.HerdrActionFocused,
	}

	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithAsyncTUIRunner(func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			for _, p := range producers {
				msg := p(ctx)
				if msg.Err != nil {
					return source.Candidate{}, tui.RowActionOpen, "", nil, false, msg.Err
				}
				for _, c := range msg.Candidates {
					if c.Meta["group"] == "true" {
						return c, tui.RowActionOpen, "", nil, true, nil
					}
				}
			}
			return source.Candidate{}, tui.RowActionOpen, "", nil, false, errors.New("group candidate not found")
		}),
	)

	var out, errOut bytes.Buffer
	app.out = &out
	app.err = &errOut
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	app.rankingOpen = tempRankingOpen(t)

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	err := app.runOpenWithView(cmd, "", "", "workspace", "")
	if err != nil {
		t.Fatalf("runOpen: %v", err)
	}

	svcResolved := resolved(svc)
	if driver.lastCand.NormalizedPath != svcResolved {
		t.Errorf("driver got %q, want the drilled-down project %q", driver.lastCand.NormalizedPath, svcResolved)
	}
}

// TestOpen_AsyncLoader_SamePathGroupWorkspaceRecursion proves that selecting a specific
// group among multiple groups sharing the same path drills into that exact group's scoped registry.
func TestOpen_AsyncLoader_SamePathGroupWorkspaceRecursion(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "repo-arcade")
	if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	counter := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(t.TempDir(), "list-contexts")
	const scriptBody = "#!/bin/sh\ncount=0\nif [ -f \"$COUNT_FILE\" ]; then count=$(cat \"$COUNT_FILE\"); fi\nprintf '%s' $((count + 1)) > \"$COUNT_FILE\"\nprintf '[{\"label\":\"k8s-cluster\",\"path\":\"%s\"}]' \"$GROUP_ROOT\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNT_FILE", counter)
	t.Setenv("GROUP_ROOT", root)

	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Sources.Projects = config.ProjectsSourceConfig{Markers: []string{".git"}}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name: "kube-contexts", Command: []string{script}, Timeout: config.Duration(time.Second), Presentation: config.Presentation{LabelFormat: strPtr("context={{.Label}}")},
	}}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "Kubernetes", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{"kube-contexts"}},
		{Name: "fsociety", Type: config.WorkspaceTypeGroup, Path: root, SourceOrder: []string{config.SourceProjects}},
	}

	driver := &openDriver{
		detect:      true,
		workspaceID: "wA",
		lastAction:  source.HerdrActionFocused,
	}

	// Select the "fsociety" group specifically from the candidates in the async TUI runner.
	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithAsyncTUIRunner(func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			for _, p := range producers {
				msg := p(ctx)
				if msg.Err != nil {
					return source.Candidate{}, tui.RowActionOpen, "", nil, false, msg.Err
				}
				for _, c := range msg.Candidates {
					if c.Meta["group"] == "true" && c.Label == "fsociety" {
						return c, tui.RowActionOpen, "", nil, true, nil
					}
				}
			}
			return source.Candidate{}, tui.RowActionOpen, "", nil, false, errors.New("fsociety group candidate not found")
		}),
	)

	var out, errOut bytes.Buffer
	app.out = &out
	app.err = &errOut
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true, Git: true}
	app.rankingOpen = tempRankingOpen(t)

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	err := app.runOpenWithView(cmd, "", "", "workspace", "")
	if err != nil {
		t.Fatalf("runOpen: %v\nstderr: %s", err, errOut.String())
	}

	if driver.lastCand.Source != config.SourceProjects || driver.lastCand.Path != projDir {
		t.Fatalf("driver got candidate %+v, want projects source in %s", driver.lastCand, projDir)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatalf("kube-contexts custom source was executed when fsociety group was selected")
	}
}

// TestBuildStreamingProducers_IncludesCustomSourceProviderCandidates proves an
// custom source named in general.source_order gets a streaming producer via
// buildProviderProducer (the same producer builder [[workspaces]]/zoxide/
// projects already use), and that its candidates reach the picker.
func TestBuildStreamingProducers_IncludesCustomSourceProviderCandidates(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name:    "prs",
		Command: []string{"printf", `[{"label":"PR 42","command":"gh pr view 42"}]`},
	}}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{}

	producers := app.streamingProducersForView(context.Background(), "")
	var got []source.Candidate
	var sawSource bool
	for _, p := range producers {
		msg := p(context.Background())
		if msg.Source == "prs" && !msg.Cached {
			sawSource = true
			if msg.Err != nil {
				t.Fatalf("custom source producer Err = %v, want nil", msg.Err)
			}
			got = append(got, msg.Candidates...)
		}
	}
	if !sawSource {
		t.Fatal("expected a streaming producer for the custom source source")
	}
	if len(got) != 1 || got[0].Label != "PR 42" {
		t.Fatalf("custom source candidates = %+v, want one PR 42 row", got)
	}
}

// TestBuildStreamingProducers_CustomSourceFailureSurfacesVisibleError proves a
// failing/timing-out custom source command reports its error on the producer's
// SourceResultMsg.Err (the existing partial-failure contract) instead of
// silently returning an empty candidate list.
func TestBuildStreamingProducers_CustomSourceFailureSurfacesVisibleError(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name:    "prs",
		Command: []string{"false"},
	}}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{}

	producers := app.streamingProducersForView(context.Background(), "")
	var sawErr bool
	for _, p := range producers {
		msg := p(context.Background())
		if msg.Source == "prs" && !msg.Cached {
			if msg.Err == nil {
				t.Fatal("expected a visible error from the failing custom source producer")
			}
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected the custom source producer to run and report an error")
	}
}

// agentsSourceSnapshot is one coherent Herdr generation holding a single agent
// pane plus its containing workspace, so producer tests can assert both the
// herdr and the agents candidate families derive from the same state.
func agentsSourceSnapshot() source.Snapshot {
	return source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "ws1", ActiveTabID: "w1:t1"},
		},
		Tabs: []source.Tab{
			{ID: "w1:t1", WorkspaceID: "w1", Label: "editor", Number: 1, PaneCount: 1},
		},
		Panes: []source.Pane{
			{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: "/srv/ws1", Agent: "claude", AgentStatus: "working", Focused: true},
		},
		FocusedWorkspaceID: "w1",
		FocusedTabID:       "w1:t1",
		FocusedPaneID:      "w1:p1",
	}
}

// TestBuildStreamingProducers_AgentsShareOneSnapshotGeneration proves herdr
// workspaces and agent panes stream from ONE shared Snapshot generation: a
// single producer delivers both candidate families (each row keeping its own
// source identity) together with the snapshot/tree/renderer infra, and the
// daemon is asked for the state exactly once.
func TestBuildStreamingProducers_AgentsShareOneSnapshotGeneration(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceHerdr, config.SourceAgents}
	driver := &openDriver{detect: true, snapshot: agentsSourceSnapshot()}
	app := New(WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}

	producers := app.streamingProducersForView(context.Background(), "")
	var snapshotMsgs []tui.SourceResultMsg
	for _, p := range producers {
		msg := p(context.Background())
		if msg.Err != nil {
			t.Fatalf("producer Err = %v, want nil", msg.Err)
		}
		if msg.Snapshot != nil {
			snapshotMsgs = append(snapshotMsgs, msg)
		}
	}
	if driver.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want exactly one shared generation", driver.snapshotCalls)
	}
	if len(snapshotMsgs) != 1 {
		t.Fatalf("snapshot-carrying messages = %d, want exactly one shared producer", len(snapshotMsgs))
	}
	msg := snapshotMsgs[0]
	if msg.Tree == nil {
		t.Fatal("shared producer did not carry the tree expander")
	}
	if msg.RendererForSnapshot == nil || msg.Renderer == nil {
		t.Fatal("shared producer did not carry the snapshot preview renderer")
	}
	if msg.CurrentPane == nil || msg.CurrentPane.ID != "w1:p1" {
		t.Errorf("CurrentPane = %+v, want focused pane w1:p1", msg.CurrentPane)
	}
	var herdrRows, agentRows int
	for _, c := range msg.Candidates {
		switch c.Source {
		case config.SourceHerdr:
			herdrRows++
		case config.SourceAgents:
			agentRows++
			if c.Meta["pane_id"] != "w1:p1" {
				t.Errorf("agents candidate meta = %+v, want pane w1:p1", c.Meta)
			}
		default:
			t.Errorf("unexpected candidate source %q", c.Source)
		}
	}
	if herdrRows != 1 || agentRows != 1 {
		t.Fatalf("herdr/agent rows = %d/%d, want 1/1 from the shared snapshot generation", herdrRows, agentRows)
	}
}

// TestBuildStreamingProducers_AgentsOnlyCarriesSnapshotInfra proves that with
// general.source_order = ["agents"] the agents-only flow still receives the
// full Herdr infra (snapshot/tree/renderer/current pane — the source of the
// Agents view's counts, previews, and focus action) while the normal all view
// keeps showing only the configured agents rows, all from one snapshot call.
func TestBuildStreamingProducers_AgentsOnlyCarriesSnapshotInfra(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceAgents}
	driver := &openDriver{detect: true, snapshot: agentsSourceSnapshot()}
	app := New(WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}

	producers := app.streamingProducersForView(context.Background(), "")
	var msg tui.SourceResultMsg
	sawSnapshot := false
	for _, p := range producers {
		candidate := p(context.Background())
		if candidate.Err != nil {
			t.Fatalf("producer Err = %v, want nil", candidate.Err)
		}
		if candidate.Snapshot != nil {
			if sawSnapshot {
				t.Fatal("more than one producer carried a snapshot generation")
			}
			sawSnapshot = true
			msg = candidate
		}
	}
	if driver.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want exactly one", driver.snapshotCalls)
	}
	if !sawSnapshot {
		t.Fatal("agents-only flow did not carry the snapshot generation")
	}
	if msg.Tree == nil {
		t.Fatal("agents-only producer did not carry the tree expander")
	}
	if msg.RendererForSnapshot == nil || msg.Renderer == nil {
		t.Fatal("agents-only producer did not carry the snapshot preview renderer")
	}
	if msg.CurrentPane == nil || msg.CurrentPane.ID != "w1:p1" {
		t.Errorf("CurrentPane = %+v, want focused pane w1:p1", msg.CurrentPane)
	}
	var herdrRows, agentRows int
	for _, c := range msg.Candidates {
		switch c.Source {
		case config.SourceHerdr:
			herdrRows++
		case config.SourceAgents:
			agentRows++
		}
	}
	if herdrRows != 0 {
		t.Errorf("herdr workspace rows = %d, want 0 when herdr is not in source_order", herdrRows)
	}
	if agentRows != 1 {
		t.Errorf("agent rows = %d, want 1", agentRows)
	}
}

func TestBuildRankingProducer_LoadsWorkspaceMRU(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Ranking.Enabled = true
	app := New(
		WithHistoryMRUReader(func(context.Context) ([]string, error) {
			return []string{"ws-c", "ws-b", "ws-a"}, nil
		}),
	)
	app.cfg = cfg
	app.rankingOpen = tempRankingOpen(t)

	producer := app.buildRankingProducer()
	msg := producer(context.Background())
	if msg.Err != nil {
		t.Fatalf("ranking producer Err = %v, want nil", msg.Err)
	}
	if msg.RankingSnapshot == nil {
		t.Fatal("expected non-nil RankingSnapshot")
	}
	mru := msg.RankingSnapshot.WorkspaceMRU()
	if len(mru) != 3 || mru[0] != "ws-c" || mru[1] != "ws-b" || mru[2] != "ws-a" {
		t.Fatalf("msg.RankingSnapshot.WorkspaceMRU = %v, want [ws-c ws-b ws-a]", mru)
	}
}

func TestOpen_AsyncLoader_FocusMRU_EndToEndOrdering(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Ranking.Enabled = true

	snap := source.Snapshot{
		FocusedWorkspaceID: "ws-c",
		Workspaces: []source.Workspace{
			{ID: "ws-a", Label: "ws-a"},
			{ID: "ws-b", Label: "ws-b"},
			{ID: "ws-c", Label: "ws-c"},
		},
	}

	driver := &openDriver{
		detect:   true,
		snapshot: snap,
	}

	var capturedRanking ranking.Snapshot
	var capturedCandidates []source.Candidate
	var capturedOrder []string

	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithHistoryMRUReader(func(context.Context) ([]string, error) {
			return []string{"ws-c", "ws-b", "ws-a"}, nil
		}),
		WithAsyncTUIRunner(func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
			capturedOrder = layout.SourceOrder
			for _, p := range producers {
				msg := p(ctx)
				if msg.RankingSnapshot != nil {
					capturedRanking = *msg.RankingSnapshot
				}
				if len(msg.Candidates) > 0 {
					capturedCandidates = append(capturedCandidates, msg.Candidates...)
				}
			}
			return source.Candidate{Label: "ws-b", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "ws-b"}}, tui.RowActionOpen, "", nil, true, nil
		}),
	)
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = tempRankingOpen(t)

	var stdout, stderr bytes.Buffer
	app.out = &stdout
	app.err = &stderr

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("openCmd Execute err = %v", err)
	}

	capturedRanking = capturedRanking.WithCurrentExact(ranking.Identity(source.Candidate{
		Source: config.SourceHerdr,
		Meta:   map[string]string{"workspace_id": "ws-c"},
	}))
	sorted := ranking.SortBySourceOrder(capturedCandidates, "", capturedOrder, capturedRanking)
	want := []string{"ws-b", "ws-a", "ws-c"}
	if len(sorted) != len(want) {
		t.Fatalf("got %d sorted candidates, want %d", len(sorted), len(want))
	}
	for i, label := range want {
		if sorted[i].Label != label {
			t.Errorf("sorted[%d] = %q, want %q", i, sorted[i].Label, label)
		}
	}
}

func TestOpen_FocusMRU_StaleClosedIDsFilteredInSynchronousPath(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Ranking.Enabled = true

	snap := source.Snapshot{
		FocusedWorkspaceID: "ws-c",
		Workspaces: []source.Workspace{
			{ID: "ws-a", Label: "ws-a"},
			{ID: "ws-b", Label: "ws-b"},
			{ID: "ws-c", Label: "ws-c"},
		},
	}

	driver := &openDriver{
		detect:   true,
		snapshot: snap,
	}

	app := New(
		WithHerdrDriver(driver),
		WithLayoutApplier(driver),
		WithHistoryMRUReader(func(context.Context) ([]string, error) {
			return []string{"ws-c", "ws-closed-x", "ws-b", "ws-closed-y", "ws-a"}, nil
		}),
	)
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = tempRankingOpen(t)

	var stdout, stderr bytes.Buffer
	app.out = &stdout
	app.err = &stderr

	cmd := app.openCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--path", "/tmp"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("openCmd Execute err = %v", err)
	}

	mru := app.rankingData.WorkspaceMRU()
	if len(mru) != 3 || mru[0] != "ws-c" || mru[1] != "ws-b" || mru[2] != "ws-a" {
		t.Fatalf("filtered MRU = %v, want [ws-c ws-b ws-a]", mru)
	}
}

// TestBuildStreamingProducers_SlowSourcesStreamTheirLastResultFirst proves a
// custom source's fresh result is saved and the next picker streams it
// first, marked Cached, while a changed configuration of the source streams
// nothing saved.
func TestBuildStreamingProducers_SlowSourcesStreamTheirLastResultFirst(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name:    "prs",
		Command: []string{"printf", `[{"label":"PR 42","command":"gh pr view 42"}]`},
	}}
	run := func(cfg *config.Config) (cached []source.Candidate, sawCached bool) {
		app := New()
		app.cfg = cfg
		app.probes = config.Probes{}
		for _, p := range app.streamingProducersForView(context.Background(), "") {
			if msg := p(context.Background()); msg.Source == "prs" && msg.Cached {
				sawCached = true
				cached = append(cached, msg.Candidates...)
			}
		}
		return cached, sawCached
	}

	if cached, saw := run(cfg); !saw || len(cached) != 0 {
		t.Fatalf("first run: cached producer %v with %+v, want one with nothing saved yet", saw, cached)
	}
	cached, _ := run(cfg)
	if len(cached) != 1 || cached[0].Label != "PR 42" || cached[0].Presentation == nil {
		t.Fatalf("second run: cached %+v, want the saved PR 42 row with its presentation", cached)
	}
	changed := *cfg
	changed.Sources.Custom = []config.CustomSourceConfig{{Name: "prs", Command: []string{"printf", "[]"}}}
	if cached, _ := run(&changed); len(cached) != 0 {
		t.Errorf("changed configuration: cached %+v, want nothing", cached)
	}
}
