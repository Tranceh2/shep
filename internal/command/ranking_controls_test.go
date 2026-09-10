package command

import (
	"bytes"
	"context"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

func TestOpenDisabledSkipsRankingOpenAndRecord(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	store := &recordingRankingStore{}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.rankingOpen = func() (rankingStore, error) {
		t.Fatal("disabled open performed ranking I/O")
		return store, nil
	}
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"open", "missing"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected unresolved candidate")
	}
	if len(store.records) != 0 {
		t.Fatalf("disabled open recorded %d events", len(store.records))
	}
}

func TestRankingClearRunsWhileDisabled(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store, err := ranking.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccess(context.Background(), ranking.Keys{Exact: "action"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Ranking.Enabled = false
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"ranking", "clear"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	store, err = ranking.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if snapshot := store.Snapshot(context.Background(), ""); snapshot.HasHistory() {
		t.Fatal("clear left ranking history")
	}
}

func TestDisabledOpenKeepsBaselineOrderAfterClear(t *testing.T) {
	got := ranking.Sort([]source.Candidate{{Label: "b"}, {Label: "a"}}, "", ranking.Snapshot{})
	if got[0].Label != "b" || got[1].Label != "a" {
		t.Fatalf("disabled ranking changed baseline order: %+v", got)
	}
}
