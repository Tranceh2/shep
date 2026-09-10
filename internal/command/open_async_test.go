package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
	err := app.runOpen(cmd, "", "", "workspace")
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
	err := app.runOpen(cmd, "", "", "workspace")
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
	err := app.runOpen(cmd, "", "", "workspace")
	if err != nil {
		t.Fatalf("runOpen: %v", err)
	}

	svcResolved := resolved(svc)
	if driver.lastCand.NormalizedPath != svcResolved {
		t.Errorf("driver got %q, want the drilled-down project %q", driver.lastCand.NormalizedPath, svcResolved)
	}
}

// TestBuildStreamingProducers_IncludesIntegrationProviderCandidates proves an
// integration named in general.source_order gets a streaming producer via
// buildProviderProducer (the same producer builder [[workspaces]]/zoxide/
// projects already use), and that its candidates reach the picker.
func TestBuildStreamingProducers_IncludesIntegrationProviderCandidates(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Integrations = []config.IntegrationConfig{{
		Name:    "prs",
		Command: []string{"printf", `[{"label":"PR 42","command":"gh pr view 42"}]`},
	}}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{}

	producers := app.buildStreamingProducers(context.Background())
	var got []source.Candidate
	var sawSource bool
	for _, p := range producers {
		msg := p(context.Background())
		if msg.Source == "prs" {
			sawSource = true
			if msg.Err != nil {
				t.Fatalf("integration producer Err = %v, want nil", msg.Err)
			}
			got = append(got, msg.Candidates...)
		}
	}
	if !sawSource {
		t.Fatal("expected a streaming producer for the integration source")
	}
	if len(got) != 1 || got[0].Label != "PR 42" {
		t.Fatalf("integration candidates = %+v, want one PR 42 row", got)
	}
}

// TestBuildStreamingProducers_IntegrationFailureSurfacesVisibleError proves a
// failing/timing-out integration command reports its error on the producer's
// SourceResultMsg.Err (the existing partial-failure contract) instead of
// silently returning an empty candidate list.
func TestBuildStreamingProducers_IntegrationFailureSurfacesVisibleError(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{"prs"}
	cfg.Integrations = []config.IntegrationConfig{{
		Name:    "prs",
		Command: []string{"false"},
	}}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{}

	producers := app.buildStreamingProducers(context.Background())
	var sawErr bool
	for _, p := range producers {
		msg := p(context.Background())
		if msg.Source == "prs" {
			if msg.Err == nil {
				t.Fatal("expected a visible error from the failing integration producer")
			}
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected the integration producer to run and report an error")
	}
}
