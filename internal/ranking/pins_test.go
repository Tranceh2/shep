package ranking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

func TestPinsMigratePreserveRankingDataAndPersistAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := source.Candidate{Source: config.SourceProjects, Path: "/private/repo", Label: "repo"}
	if err := store.Record(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	pinned, err := store.TogglePin(context.Background(), PinKey(candidate))
	if err != nil || !pinned {
		t.Fatalf("TogglePin() = %v, %v; want true, nil", pinned, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot := reopened.Snapshot(context.Background(), "")
	if !snapshot.IsPinned(candidate) {
		t.Fatal("pin did not survive reopening the database")
	}
	if snapshot.UsageFor(candidate) == 0 {
		t.Fatal("migration did not preserve existing ranking data")
	}
	var table int
	if err := reopened.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='candidate_pins'").Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != 1 {
		t.Fatal("candidate_pins table was not migrated")
	}
}

func TestTogglePinAndUnpinAreIdempotentAndNilSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "v1:resource:" + strings.Repeat("a", 64)
	if pinned, err := store.SetPinned(context.Background(), key, true); err != nil || !pinned {
		t.Fatalf("SetPinned(true) = %v, %v", pinned, err)
	}
	if pinned, err := store.SetPinned(context.Background(), key, true); err != nil || !pinned {
		t.Fatalf("idempotent SetPinned(true) = %v, %v", pinned, err)
	}
	if pinned, err := store.SetPinned(context.Background(), key, false); err != nil || pinned {
		t.Fatalf("SetPinned(false) = %v, %v", pinned, err)
	}
	var nilStore *Store
	if pinned, err := nilStore.TogglePin(context.Background(), key); err != nil || pinned {
		t.Fatalf("nil TogglePin() = %v, %v", pinned, err)
	}
}

// TestPinAppliesToThePinnedRowOnly proves rows sharing a directory keep
// separate pins: pinning a project leaves the zoxide entry, the open Herdr
// workspace, the group and the command entry rooted at the same directory
// unpinned.
func TestPinAppliesToThePinnedRowOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := source.Candidate{Source: config.SourceProjects, Path: "/repo", NormalizedPath: "/repo", Label: "repo"}
	others := []source.Candidate{
		{Source: config.SourceZoxide, Path: "/repo", NormalizedPath: "/repo", Label: "repo"},
		{Source: config.SourceHerdr, Path: "/repo", NormalizedPath: "/repo", Label: "repo", Meta: map[string]string{"workspace_id": "w1"}},
		{Source: config.SourceWorkspaces, Path: "/repo", NormalizedPath: "/repo", Label: "Fsociety", Meta: map[string]string{"entry_id": "fsociety", "group": "true"}},
		{Source: config.SourceWorkspaces, Path: "/repo", NormalizedPath: "/repo", Label: "Logs", Meta: map[string]string{"entry_id": "logs", "command": "tail -f log"}},
	}
	if _, err := store.TogglePin(context.Background(), PinKey(projects)); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if !snapshot.IsPinned(projects) {
		t.Fatal("the pinned row is not pinned")
	}
	for _, other := range others {
		if snapshot.IsPinned(other) {
			t.Errorf("%s row %q shares the directory and got the pin", other.Source, other.Label)
		}
	}
}

func TestPinnedOrderingPreservesBlocksAndComparatorContract(t *testing.T) {
	open := source.Candidate{Source: config.SourceHerdr, Label: "deploy", Meta: map[string]string{"workspace_id": "open"}}
	closed := source.Candidate{Source: config.SourceProjects, Label: "deploy", Path: "/deploy"}
	weakPinned := source.Candidate{Source: config.SourceProjects, Label: "dpl", Path: "/weak"}
	snapshot := Snapshot{enabled: true, pins: map[string]struct{}{PinKey(closed): {}, PinKey(weakPinned): {}}}
	got := SortBySourceOrder([]source.Candidate{weakPinned, closed, open}, "deploy", []string{config.SourceHerdr, config.SourceProjects}, snapshot)
	if Identity(got[0]) != Identity(open) {
		t.Fatalf("exact open Herdr candidate lost precedence: %+v", got)
	}
	if Identity(got[1]) != Identity(closed) {
		t.Fatalf("exact pinned project ordering changed unexpectedly: %+v", got)
	}
	if Identity(got[2]) != Identity(weakPinned) {
		t.Fatalf("lower-layer pin beat exact match: %+v", got)
	}

	first := source.Candidate{Source: config.SourceZoxide, Label: "first", Path: "/first"}
	second := source.Candidate{Source: config.SourceZoxide, Label: "second", Path: "/second"}
	empty := SortBySourceOrder([]source.Candidate{first, second}, "", []string{config.SourceZoxide}, Snapshot{pins: map[string]struct{}{PinKey(second): {}}})
	if Identity(empty[0]) != Identity(second) {
		t.Fatalf("empty-query pin did not move first within its source block: %+v", empty)
	}
}
