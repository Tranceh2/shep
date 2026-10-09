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

type ackRecord struct {
	paneID string
	status string
}

type recordingRankingStore struct {
	records []source.Candidate
	acks    []ackRecord
}

func (s *recordingRankingStore) Snapshot(context.Context, string) ranking.Snapshot {
	return ranking.Snapshot{}
}

func (s *recordingRankingStore) RecordSuccess(_ context.Context, _ ranking.Keys) error {
	s.records = append(s.records, source.Candidate{})
	return nil
}

func (s *recordingRankingStore) RecordAcknowledgement(_ context.Context, paneID, status string) error {
	s.acks = append(s.acks, ackRecord{paneID: paneID, status: status})
	return nil
}

func (s *recordingRankingStore) ClearAcknowledgement(context.Context, string) error {
	return nil
}

func (s *recordingRankingStore) TogglePin(context.Context, string) (bool, error) { return true, nil }

func (s *recordingRankingStore) Clear(context.Context) error {
	s.records = nil
	s.acks = nil
	return nil
}
func (s *recordingRankingStore) Close() error { return nil }

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
func (s *failingRankingStore) RecordAcknowledgement(context.Context, string, string) error {
	return errors.New("database unavailable")
}
func (s *failingRankingStore) ClearAcknowledgement(context.Context, string) error {
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
func (s *blockingRankingStore) RecordAcknowledgement(context.Context, string, string) error {
	return nil
}
func (s *blockingRankingStore) ClearAcknowledgement(context.Context, string) error {
	return nil
}
func (s *blockingRankingStore) TogglePin(context.Context, string) (bool, error) { return false, nil }

func (s *blockingRankingStore) Clear(context.Context) error { return nil }
func (s *blockingRankingStore) Close() error                { return nil }

func TestRunOpenRecordsSuccessfulLaunchWithoutBlockingOnRecordFailure(t *testing.T) {
	cfg, _ := seedCfg(t, "foo")
	// Only a genuinely completed launch records, so drive a real
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
	// Recording only happens on a genuinely completed launch, so this
	// test must use a real driver that completes FocusOrCreate (not the nil
	// path-print fallback, which records nothing).
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

func TestRunOpen_RecordsAcknowledgementOnlyOnFocusTabSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		action       tui.RowAction
		cand         source.Candidate
		focusErr     error
		wantAckCount int
		wantPaneID   string
		wantStatus   string
	}{
		{
			name:   "successful_focus_tab_blocked_pane",
			action: tui.RowActionFocusTab,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"pane_id":      "p_blocked",
					"tab_id":       "t1",
					"agent_status": "blocked",
				},
			},
			wantAckCount: 1,
			wantPaneID:   "p_blocked",
			wantStatus:   "blocked",
		},
		{
			name:   "successful_focus_tab_done_pane",
			action: tui.RowActionFocusTab,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"pane_id":      "p_done",
					"tab_id":       "t1",
					"agent_status": "done",
				},
			},
			wantAckCount: 1,
			wantPaneID:   "p_done",
			wantStatus:   "done",
		},
		{
			name:   "failed_focus_tab_does_not_record_ack",
			action: tui.RowActionFocusTab,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"pane_id":      "p_blocked",
					"tab_id":       "t1",
					"agent_status": "blocked",
				},
			},
			focusErr:     errors.New("focus failed"),
			wantAckCount: 0,
		},
		{
			name:   "row_action_open_does_not_record_ack",
			action: tui.RowActionOpen,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"pane_id":      "p_blocked",
					"tab_id":       "t1",
					"agent_status": "blocked",
				},
			},
			wantAckCount: 0,
		},
		{
			name:   "working_status_does_not_record_ack",
			action: tui.RowActionFocusTab,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"pane_id":      "p_working",
					"tab_id":       "t1",
					"agent_status": "working",
				},
			},
			wantAckCount: 0,
		},
		{
			name:   "missing_pane_id_does_not_record_ack",
			action: tui.RowActionFocusTab,
			cand: source.Candidate{
				Source: config.SourceHerdr,
				Meta: map[string]string{
					"tab_id":       "t1",
					"agent_status": "blocked",
				},
			},
			wantAckCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := seedCfg(t, "foo")
			driver := &openDriver{
				detect:      true,
				workspaceID: "wA",
				focusTabErr: tt.focusErr,
				lastAction:  source.HerdrActionFocused,
			}
			store := &recordingRankingStore{}
			var out, errOut bytes.Buffer
			app := New(WithStreams(&out, &errOut), WithHerdrDriver(driver))
			app.cfg = cfg
			app.probes = config.Probes{Herdr: true}
			app.rankingOpen = func() (rankingStore, error) { return store, nil }
			app.asyncTUIRun = func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
				return tt.cand, tt.action, "", nil, true, nil
			}

			cmd := app.rootCmd()
			cmd.SetArgs([]string{"open"})
			_ = cmd.Execute()

			if len(store.acks) != tt.wantAckCount {
				t.Fatalf("acks recorded = %d, want %d", len(store.acks), tt.wantAckCount)
			}
			if tt.wantAckCount > 0 {
				if store.acks[0].paneID != tt.wantPaneID || store.acks[0].status != tt.wantStatus {
					t.Errorf("recorded ack = %+v, want {paneID: %q, status: %q}", store.acks[0], tt.wantPaneID, tt.wantStatus)
				}
			}
		})
	}
}
