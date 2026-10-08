package queryhistory

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// TestRecord_NewestFirstWithoutDuplicates proves recorded queries come back
// newest first, a repeated query moves to the front instead of repeating,
// blank ones are skipped, and a missing file is an empty history.
func TestRecord_NewestFirstWithoutDuplicates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "queries")
	if got, err := Load(path); err != nil || got != nil {
		t.Fatalf("Load(missing) = %v, %v; want an empty history", got, err)
	}
	for _, q := range []string{"allsafe", "e corp", "  ", "allsafe", "dark army"} {
		if err := Record(path, q); err != nil {
			t.Fatalf("Record(%q): %v", q, err)
		}
	}
	got, err := Load(path)
	if want := []string{"dark army", "allsafe", "e corp"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("Load = %q, %v; want %q", got, err, want)
	}
}

// TestRecord_KeepsTheNewestLimit proves the history keeps only the newest
// Limit queries.
func TestRecord_KeepsTheNewestLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queries")
	for i := range Limit + 5 {
		if err := Record(path, fmt.Sprintf("q%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Load(path)
	if len(got) != Limit || got[0] != fmt.Sprintf("q%d", Limit+4) || got[Limit-1] != "q5" {
		t.Errorf("history = %d entries from %q to %q, want the newest %d", len(got), got[0], got[len(got)-1], Limit)
	}
}
