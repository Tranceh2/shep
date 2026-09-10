package command

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

type recordingRankingStore struct {
	records []source.Candidate
}

func (s *recordingRankingStore) Snapshot(context.Context, string) ranking.Snapshot {
	return ranking.Snapshot{}
}

func (s *recordingRankingStore) RecordSuccess(_ context.Context, _ ranking.Keys) error {
	s.records = append(s.records, source.Candidate{})
	return nil
}

func (s *recordingRankingStore) TogglePin(context.Context, string) (bool, error) { return true, nil }

func (s *recordingRankingStore) Clear(context.Context) error { s.records = nil; return nil }
func (s *recordingRankingStore) Close() error                { return nil }

type failingRankingStore struct {
	records int
}

type blockingRankingStore struct {
	recordStarted chan struct{}
}

func (s *failingRankingStore) Snapshot(context.Context, string) ranking.Snapshot {
	return ranking.Snapshot{}
}

func (s *failingRankingStore) RecordSuccess(context.Context, ranking.Keys) error {
	s.records++
	return errors.New("database unavailable")
}
func (s *failingRankingStore) TogglePin(context.Context, string) (bool, error) {
	return false, errors.New("database unavailable")
}

func (s *failingRankingStore) Clear(context.Context) error { return nil }
func (s *failingRankingStore) Close() error                { return nil }

func (s *blockingRankingStore) Snapshot(context.Context, string) ranking.Snapshot {
	return ranking.Snapshot{}
}

func (s *blockingRankingStore) RecordSuccess(ctx context.Context, _ ranking.Keys) error {
	close(s.recordStarted)
	<-ctx.Done()
	return ctx.Err()
}
func (s *blockingRankingStore) TogglePin(context.Context, string) (bool, error) { return false, nil }

func (s *blockingRankingStore) Clear(context.Context) error { return nil }
func (s *blockingRankingStore) Close() error                { return nil }

func TestRunOpenRecordsSuccessfulLaunchWithoutBlockingOnRecordFailure(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	// R3-2: only a genuinely completed launch records, so drive a real
	// FocusOrCreate success rather than the nil path-print fallback.
	driver := &openDriver{detect: true, workspaceID: "wA", lastAction: source.HerdrActionFocused}
	store := &failingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut), WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "foo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if store.records != 1 {
		t.Fatalf("record calls = %d, want one successful launch record", store.records)
	}
}

func TestRunOpenRecordFailureIsBounded(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	// R3-2: recording only happens on a genuinely completed launch, so this
	// test must use a real driver that completes FocusOrCreate (not the nil
	// path-print fallback, which correctly records nothing now).
	driver := &openDriver{detect: true, workspaceID: "wA", lastAction: source.HerdrActionFocused}
	store := &blockingRankingStore{recordStarted: make(chan struct{})}
	app := New(WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "foo"})

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	select {
	case <-store.recordStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("recording did not start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ranking recording exceeded its explicit timeout")
	}
}

func TestRunOpenDoesNotRecordFailedLaunch(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	driver := insidePaneDriver(source.Pane{ID: "current", WorkspaceID: "w1", TabID: "t1", CWD: "/tmp"})
	driver.createTabErr = errors.New("create failed")
	store := &recordingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut), WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "--target", "tab", "foo"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected failed launch")
	}
	if len(store.records) != 0 {
		t.Fatalf("record calls = %d, want none after failed launch", len(store.records))
	}
}

func TestRunOpenDoesNotRecordCancelledSelection(t *testing.T) {
	cfg, _ := seedCfg(t, "foo", "foobar")
	store := &recordingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	app.selectorBuilder = func() *selector.Cascade { return selector.New(fakeSelector{err: tui.ErrCancelled}) }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "foo"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected cancelled open")
	}
	if len(store.records) != 0 {
		t.Fatalf("cancelled selection recorded %d events", len(store.records))
	}
}

func TestRunOpenDegradesRankingFailuresToBaselineLaunch(t *testing.T) {
	for _, name := range []string{"open", "version", "migration", "recovery", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := seedCfg(t, "foo")
			driver := &openDriver{detect: true, workspaceID: "w1"}
			app := New(WithHerdrDriver(driver))
			app.cfg, app.probes = cfg, config.Probes{Herdr: true}
			app.rankingOpen = func() (rankingStore, error) {
				if name == "snapshot" {
					return &recordingRankingStore{}, nil
				}
				return nil, errors.New(name + " failure")
			}
			cmd := app.rootCmd()
			cmd.SetArgs([]string{"open", "foo"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("baseline after %s failure: %v", name, err)
			}
		})
	}
}

func TestRunOpenDoesNotRecordFailedTemplateApplication(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
	cfg.Templates["default"] = config.TemplateConfig{Command: "boom"}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "foo", Path: dir}}
	driver := &openDriver{
		detect: true, workspaceID: "wA", rootTabID: "wA:t1", rootPaneID: "wA:p1",
		lastAction: source.HerdrActionCreated, runErr: errors.New("boom"),
	}
	store := &recordingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut), WithHerdrDriver(driver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.rankingOpen = func() (rankingStore, error) { return store, nil }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "foo"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected template application failure")
	}
	if len(store.records) != 0 {
		t.Fatalf("record calls = %d, want none after template failure", len(store.records))
	}
	if !bytes.Contains(errOut.Bytes(), []byte("warning: template failed")) {
		t.Fatalf("stderr = %q, want template warning", errOut.String())
	}
}
