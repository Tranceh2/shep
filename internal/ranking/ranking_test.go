package ranking

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

func TestIdentitySeparatesPresentationAndActionSemantics(t *testing.T) {
	workspace := source.Candidate{Source: config.SourceHerdr, Label: "old", Meta: map[string]string{"workspace_id": "w1"}, Path: "/same"}
	renamed := workspace.Clone()
	renamed.Label = "new"
	if Identity(workspace) != Identity(renamed) {
		t.Fatal("renaming a candidate changed its identity")
	}
	other := workspace.Clone()
	other.Meta["workspace_id"] = "w2"
	if Identity(workspace) == Identity(other) {
		t.Fatal("different Herdr workspaces share an identity")
	}
	resourceOnly := source.Candidate{Source: config.SourceZoxide, Path: "/same", NormalizedPath: "/same", Label: "same"}
	if Resource(workspace) != Resource(resourceOnly) {
		t.Fatal("equivalent paths did not share resource affinity")
	}
	if Identity(workspace) == Identity(resourceOnly) {
		t.Fatal("different action semantics share an exact identity")
	}
}

func TestSortFrecencyAndCurrentWorkspace(t *testing.T) {
	now := time.Now().Unix()
	snapshot := Snapshot{
		enabled: true,
		exact: map[string]usage{
			"herdr:workspace:w1": {count: 1, lastUsed: now - int64(2*time.Hour/time.Second)},
			"herdr:workspace:w2": {count: 4, lastUsed: now - int64(time.Hour/time.Second)},
		},
		resource:     map[string]usage{},
		capturedAt:   time.Unix(now, 0),
		currentExact: "herdr:workspace:w2",
	}
	candidates := []source.Candidate{
		{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}},
		{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w2"}},
	}
	got := Sort(candidates, "", snapshot)
	if Identity(got[0]) != Identity(candidates[0]) {
		t.Fatalf("previous workspace = %q, want w1", Identity(got[0]))
	}
}

func TestSortUsesLabelQualityBeforeActionCost(t *testing.T) {
	now := time.Now().Unix()
	pathOnly := source.Candidate{Source: config.SourceZoxide, Path: "/tmp/deploy-cache", Label: "cache"}
	openLabel := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy-api", Meta: map[string]string{"workspace_id": "open"}}
	snapshot := Snapshot{
		enabled:    true,
		exact:      map[string]usage{Identity(pathOnly): {count: 100000, lastUsed: now}},
		capturedAt: time.Unix(now, 0),
	}
	got := Sort([]source.Candidate{pathOnly, openLabel}, "deploy", snapshot)
	if Identity(got[0]) != Identity(openLabel) {
		t.Fatalf("label match lost to path-only history: %+v", got)
	}
}

func TestSortUsesPathOnlyAsLowestTextualTier(t *testing.T) {
	pathOnly := source.Candidate{Source: config.SourceZoxide, Path: "/srv/deploy-api", Label: "service"}
	unmatched := source.Candidate{Source: config.SourceZoxide, Path: "/srv/other", Label: "service"}
	got := Sort([]source.Candidate{unmatched, pathOnly}, "deploy", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(pathOnly) {
		t.Fatalf("path-only match did not outrank an unmatched candidate: %+v", got)
	}
}

func TestSortUsesCheapOpenActionWithinLabelQualityTie(t *testing.T) {
	open := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy", Meta: map[string]string{"workspace_id": "open"}}
	create := source.Candidate{Source: config.SourceZoxide, Path: "/repo", Label: "deploy"}
	got := Sort([]source.Candidate{create, open}, "deploy", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(open) {
		t.Fatalf("open workspace did not win exact label tie: %+v", got)
	}
}

func TestSortOpenHerdrWorkspaceOutranksIntegrationAtSameMatchLayer(t *testing.T) {
	open := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy", Meta: map[string]string{"workspace_id": "open"}}
	integration := source.Candidate{Source: "prs", Path: "/repo", Label: "deploy", Meta: map[string]string{"command": "gh pr view 1"}}
	got := Sort([]source.Candidate{integration, open}, "deploy", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(open) {
		t.Fatalf("open workspace did not outrank integration at same match layer: %+v", got)
	}
}

func TestSortPromotesCurrentFocusedWorkspaceWithinItsTextualLayer(t *testing.T) {
	current := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy", Meta: map[string]string{"workspace_id": "current"}}
	create := source.Candidate{Source: config.SourceZoxide, Path: "/repo", Label: "deploy"}
	snapshot := Snapshot{enabled: true, currentExact: Identity(current)}
	got := Sort([]source.Candidate{create, current}, "deploy", snapshot)
	if Identity(got[0]) != Identity(current) {
		t.Fatalf("open focused workspace did not win its textual-layer tie: %+v", got)
	}
}

func TestSortDoesNotLetCheapActionBeatClearlyBetterLabel(t *testing.T) {
	open := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy service", Meta: map[string]string{"workspace_id": "open"}}
	exact := source.Candidate{Source: config.SourceZoxide, Path: "/repo", Label: "deploy"}
	got := Sort([]source.Candidate{open, exact}, "deploy", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(exact) {
		t.Fatalf("exact label lost to weaker open action: %+v", got)
	}
}

func TestSortKeepsExactLabelAboveNearTieOpenPrefix(t *testing.T) {
	openPrefix := source.Candidate{Source: config.SourceHerdr, Path: "/repo", Label: "deploy-api", Meta: map[string]string{"workspace_id": "open"}}
	exact := source.Candidate{Source: config.SourceZoxide, Path: "/repo", Label: "deploy"}
	got := Sort([]source.Candidate{openPrefix, exact}, "deploy", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(exact) {
		t.Fatalf("exact label lost to lower-tier open action: %+v", got)
	}
}

func TestPathDerivedKeysAreDigestBacked(t *testing.T) {
	candidate := source.Candidate{Source: config.SourceZoxide, Path: "/private/project", NormalizedPath: "/private/project"}
	identity, resource := CandidateKeyParts(candidate)
	if strings.Contains(identity, candidate.Path) || strings.Contains(resource, candidate.Path) {
		t.Fatalf("path-derived keys expose raw path: %q %q", identity, resource)
	}
	if !strings.HasPrefix(identity, "v1:") || !strings.HasPrefix(resource, "v1:") {
		t.Fatalf("path-derived keys lack version prefix: %q %q", identity, resource)
	}
}

func TestSortNearTieUsesHistoryThenBaseline(t *testing.T) {
	now := time.Now().Unix()
	older := source.Candidate{Source: config.SourceZoxide, Path: "/a", NormalizedPath: "/a", Label: "a"}
	newer := source.Candidate{Source: config.SourceZoxide, Path: "/b", NormalizedPath: "/b", Label: "ab"}
	if left, right := fuzzyScore("a", older), fuzzyScore("a", newer); left-right > nearTieWindow || right-left > nearTieWindow {
		t.Fatalf("test inputs are not a near tie: %d vs %d", left, right)
	}
	snapshot := Snapshot{enabled: true, exact: map[string]usage{Identity(older): {count: 2, lastUsed: now}}, capturedAt: time.Unix(now, 0)}
	got := Sort([]source.Candidate{newer, older}, "a", snapshot)
	if Identity(got[0]) != Identity(older) {
		t.Fatalf("near-tie history did not win: %+v", got)
	}
}

func fuzzyScore(query string, candidate source.Candidate) int {
	score, _ := fuzzy.Score(query, candidate.Label+" "+candidate.Path)
	return score
}

func TestSortFuzzyDominatesWeakHistory(t *testing.T) {
	now := time.Now().Unix()
	strong := source.Candidate{Source: config.SourceZoxide, Path: "/repo/omp", Label: "omp"}
	weak := source.Candidate{Source: config.SourceZoxide, Path: "/tmp/cache/xxomp", Label: "scattered"}
	snapshot := Snapshot{enabled: true, exact: map[string]usage{Identity(weak): {count: 100000, lastUsed: now}}, capturedAt: time.Unix(now, 0)}
	got := Sort([]source.Candidate{weak, strong}, "omp", snapshot)
	if Identity(got[0]) != Identity(strong) {
		t.Fatalf("strong fuzzy match lost to weak history: %+v", got)
	}
}

func TestSnapshotDisablesRankingWhenDatabaseUnavailable(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot := store.Snapshot(context.Background(), "herdr:workspace:current"); snapshot.Active() {
		t.Fatal("closed database must disable ranking")
	}
}

func TestSnapshotDisablesRankingWhenScanFails(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO exact_usage(exact_id, count, last_used) VALUES ('broken', 'not-a-count', ?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if snapshot := store.Snapshot(context.Background(), ""); snapshot.Active() {
		t.Fatal("scan failure must disable ranking")
	}
}

func TestRecentSelectionPromotesPreviousDistinctTarget(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := func(id string) source.Candidate {
		return source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": id}, Path: "/" + id}
	}
	frequent := candidate("c")
	if err := store.Record(context.Background(), frequent); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), frequent); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "a"} {
		if err := store.Record(context.Background(), candidate(id)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := store.Snapshot(context.Background(), Identity(candidate("a")))
	got := Sort([]source.Candidate{candidate("a"), frequent, candidate("b")}, "", snapshot)
	if got[0].Meta["workspace_id"] != "b" {
		t.Fatalf("previous distinct target = %q, want b", got[0].Meta["workspace_id"])
	}
}

func TestStoreRecordsAtomicallyAndUsesStatePath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := source.Candidate{Source: config.SourceZoxide, Path: "/repo", NormalizedPath: "/repo", Label: "one"}
	if err := store.Record(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if snapshot.exact[exactStorageKey(Identity(candidate))].count != 2 || snapshot.resource[resourceStorageKey(Resource(candidate))].count != 2 {
		t.Fatalf("record counts = %+v %+v", snapshot.exact, snapshot.resource)
	}
	path := filepath.Join(state, "shep", "ranking.sqlite3")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database path: %v", err)
	}
}

func TestSortDisabledPreservesInputOrder(t *testing.T) {
	candidates := []source.Candidate{{Label: "b"}, {Label: "a"}}
	got := Sort(candidates, "", Snapshot{})
	if got[0].Label != "b" || got[1].Label != "a" {
		t.Fatalf("disabled ranking reordered candidates: %+v", got)
	}
}

func TestSortBySourceOrderKeepsEmptyQueryBlocksContiguous(t *testing.T) {
	now := time.Now().Unix()
	previous := source.Candidate{Source: config.SourceHerdr, Label: "previous", Meta: map[string]string{"workspace_id": "previous"}}
	current := source.Candidate{Source: config.SourceHerdr, Label: "current", Meta: map[string]string{"workspace_id": "current"}}
	workspace := source.Candidate{Source: config.SourceWorkspaces, Label: "configured"}
	zoxide := source.Candidate{Source: config.SourceZoxide, Label: "history"}
	projectA := source.Candidate{Source: config.SourceProjects, Label: "a", Path: "/a"}
	projectB := source.Candidate{Source: config.SourceProjects, Label: "b", Path: "/b"}
	snapshot := Snapshot{
		enabled:      true,
		recent:       []string{exactStorageKey(Identity(previous))},
		currentExact: Identity(current),
		exact:        map[string]usage{Identity(projectB): {count: 2, lastUsed: now}},
		capturedAt:   time.Unix(now, 0),
	}
	input := []source.Candidate{zoxide, current, projectA, workspace, projectB, previous}
	got := SortBySourceOrder(input, "", []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects}, snapshot)
	want := []string{"previous", "current", "configured", "history", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestSortBySourceOrderLeavesUnrankedSourcesInProviderOrder(t *testing.T) {
	first := source.Candidate{Source: config.SourceZoxide, Label: "first"}
	second := source.Candidate{Source: config.SourceZoxide, Label: "second"}
	got := SortBySourceOrder([]source.Candidate{second, first}, "", []string{config.SourceZoxide}, Snapshot{enabled: true})
	if got[0].Label != "second" || got[1].Label != "first" {
		t.Fatalf("zoxide provider order changed: %+v", got)
	}
}

func TestSnapshot_PaneRecentRankAndRecentRank(t *testing.T) {
	t.Parallel()

	paneCand1 := source.Candidate{
		Source: config.SourceAgents,
		Meta:   map[string]string{"pane_id": "p1"},
	}
	paneCand2 := source.Candidate{
		Source: config.SourceAgents,
		Meta:   map[string]string{"pane_id": "p2"},
	}
	paneCandUnknown := source.Candidate{
		Source: config.SourceAgents,
		Meta:   map[string]string{"pane_id": "p_unknown"},
	}

	snap := Snapshot{}.WithRecent([]string{
		Identity(paneCand1),
		Identity(paneCand2),
	})

	// Rank for p1 is 0 (most recent)
	if got, want := snap.PaneRecentRank("p1"), 0; got != want {
		t.Errorf("PaneRecentRank(p1) = %d, want %d", got, want)
	}
	if got, want := snap.RecentRank(paneCand1), 0; got != want {
		t.Errorf("RecentRank(paneCand1) = %d, want %d", got, want)
	}

	// Rank for p2 is 1 (second most recent)
	if got, want := snap.PaneRecentRank("p2"), 1; got != want {
		t.Errorf("PaneRecentRank(p2) = %d, want %d", got, want)
	}
	if got, want := snap.RecentRank(paneCand2), 1; got != want {
		t.Errorf("RecentRank(paneCand2) = %d, want %d", got, want)
	}

	// Rank for unranked pane is len(recent)+1 = 3
	if got, want := snap.PaneRecentRank("p_unknown"), 3; got != want {
		t.Errorf("PaneRecentRank(p_unknown) = %d, want %d", got, want)
	}
	if got, want := snap.RecentRank(paneCandUnknown), 3; got != want {
		t.Errorf("RecentRank(paneCandUnknown) = %d, want %d", got, want)
	}

	// Empty pane ID returns len(recent)+1
	if got, want := snap.PaneRecentRank(""), 3; got != want {
		t.Errorf("PaneRecentRank(\"\") = %d, want %d", got, want)
	}
}

func TestClassifyTextAliasesStayBelowLabelsAndAbovePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		cand  source.Candidate
		want  int
	}{
		{name: "exact alias", query: "k8s", cand: source.Candidate{Label: "cluster", Aliases: []string{"k8s"}}, want: LayerAlias},
		{name: "prefix alias", query: "kub", cand: source.Candidate{Label: "cluster", Aliases: []string{"kubernetes"}}, want: LayerAlias},
		{name: "fuzzy alias", query: "kbs", cand: source.Candidate{Label: "cluster", Aliases: []string{"kubernetes"}}, want: LayerAlias},
		{name: "path below alias", query: "k8s", cand: source.Candidate{Label: "cluster", Path: "/work/k8s", Aliases: []string{"unrelated"}}, want: LayerPathOrMeta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyText(tc.query, tc.cand).layer; got != tc.want {
				t.Fatalf("layer = %d, want %d", got, tc.want)
			}
		})
	}
	label := source.Candidate{Label: "kubernetes", Aliases: []string{"kube"}}
	if got := classifyText("kbs", label).layer; got != LayerFuzzyLabel {
		t.Fatalf("label fuzzy layer = %d, want %d", got, LayerFuzzyLabel)
	}
}

func TestSortAliasMatchKeepsOpenAndPinOrderingWithinLayer(t *testing.T) {
	t.Parallel()
	open := source.Candidate{Source: config.SourceHerdr, Label: "cluster", Aliases: []string{"k8s"}, Meta: map[string]string{"workspace_id": "open"}}
	other := source.Candidate{Source: config.SourceProjects, Label: "other", Aliases: []string{"k8s"}}
	got := Sort([]source.Candidate{other, open}, "k8s", Snapshot{enabled: true})
	if Identity(got[0]) != Identity(open) {
		t.Fatalf("open alias match did not win within alias layer: %+v", got)
	}

	pinned := other.Clone()
	unpinned := source.Candidate{Source: config.SourceProjects, Label: "third", Aliases: []string{"k8s"}}
	snapshot := Snapshot{enabled: true, pins: map[string]struct{}{PinKey(pinned): {}}}
	got = Sort([]source.Candidate{unpinned, pinned}, "k8s", snapshot)
	if Identity(got[0]) != Identity(pinned) {
		t.Fatalf("pinned alias match did not win within alias layer: %+v", got)
	}
}

func TestClassifyTextUsesRefinedLabelLayers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		label string
		path  string
		want  int
	}{
		{name: "exact label", query: "Shep", label: "shep", want: LayerExact},
		{name: "word prefix label", query: "fso", label: "FSOCIETY/arcade", want: LayerPrefix},
		{name: "fuzzy label", query: "dpy", label: "deploy-open", want: LayerFuzzyLabel},
		{name: "path fallback", query: "fso", label: "repository", path: "/work/fsociety-repo", want: LayerPathOrMeta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyText(tc.query, source.Candidate{Label: tc.label, Path: tc.path})
			if got.layer != tc.want {
				t.Fatalf("classifyText() layer = %d, want %d (quality=%+v)", got.layer, tc.want, got)
			}
		})
	}
}

func TestSortOpenHerdrWinsWithinEveryTextualLayer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		open  string
		other string
	}{
		{name: "exact", query: "shep", open: "shep", other: "shep"},
		{name: "prefix", query: "fso", open: "FSOCIETY/arcade", other: "fsociety-repo"},
		{name: "fuzzy label", query: "dpy", open: "deploy-open", other: "directory-py"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open := source.Candidate{Source: config.SourceHerdr, Label: tc.open, Path: "/open", Meta: map[string]string{"workspace_id": "open"}}
			other := source.Candidate{Source: config.SourceProjects, Label: tc.other, Path: "/other"}
			got := SortBySourceOrder([]source.Candidate{other, open}, tc.query, []string{config.SourceProjects, config.SourceHerdr}, Snapshot{})
			if Identity(got[0]) != Identity(open) {
				t.Fatalf("open Herdr candidate ranked after unopened candidate: %+v", got)
			}
		})
	}
}

func TestSort_FocusMRU_OrdersOpenWorkspacesByFocusHistory(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	// launch history in ranking.sqlite3 had A first, then B
	snapshot := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		recent:       []string{exactStorageKey(Identity(wsA)), exactStorageKey(Identity(wsB))},
		workspaceMRU: []string{"ws-c", "ws-b", "ws-a"},
	}

	// Current=C and focus MRU=[C, B, A] must yield B, A, C (C demoted, B before A from focus MRU)
	got := SortBySourceOrder([]source.Candidate{wsC, wsA, wsB}, "", []string{config.SourceHerdr}, snapshot)
	want := []string{"ws-b", "ws-a", "ws-c"}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_TransitionsWithoutDuplicates(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	// Transition 1: A -> B -> C (focused C)
	snapshotC := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-b", "ws-a"},
	}
	gotC := SortBySourceOrder([]source.Candidate{wsA, wsB, wsC}, "", []string{config.SourceHerdr}, snapshotC)
	wantC := []string{"ws-b", "ws-a", "ws-c"}
	for i, label := range wantC {
		if gotC[i].Label != label {
			t.Errorf("at C: candidate[%d] = %q, want %q", i, gotC[i].Label, label)
		}
	}

	// Transition 2: Focus A (now chain is A -> C -> B, focused A)
	snapshotA := Snapshot{
		enabled:      true,
		currentExact: Identity(wsA),
		workspaceMRU: []string{"ws-a", "ws-c", "ws-b"},
	}
	gotA := SortBySourceOrder([]source.Candidate{wsA, wsB, wsC}, "", []string{config.SourceHerdr}, snapshotA)
	wantA := []string{"ws-c", "ws-b", "ws-a"}
	for i, label := range wantA {
		if gotA[i].Label != label {
			t.Errorf("at A: candidate[%d] = %q, want %q", i, gotA[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_StaleClosedIDsIgnored(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	snapshot := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-closed-1", "ws-b", "ws-closed-2", "ws-a"},
	}
	got := SortBySourceOrder([]source.Candidate{wsC, wsA, wsB}, "", []string{config.SourceHerdr}, snapshot)
	want := []string{"ws-b", "ws-a", "ws-c"}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_FallbackWhenHistoryUnavailable(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	// No workspaceMRU; recent launch history has A then B
	snapshot := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		recent:       []string{exactStorageKey(Identity(wsA)), exactStorageKey(Identity(wsB))},
	}
	got := SortBySourceOrder([]source.Candidate{wsC, wsB, wsA}, "", []string{config.SourceHerdr}, snapshot)
	want := []string{"ws-a", "ws-b", "ws-c"}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_PinnedPriorityPreserved(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	// A is pinned, C is current, MRU is [C, B, A]
	snapshot := Snapshot{
		enabled:      true,
		pins:         map[string]struct{}{PinKey(wsA): {}},
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-b", "ws-a"},
	}
	got := SortBySourceOrder([]source.Candidate{wsC, wsB, wsA}, "", []string{config.SourceHerdr}, snapshot)
	want := []string{"ws-a", "ws-b", "ws-c"}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}

	// C is pinned AND current -> pinned takes priority over demotion
	snapshotC := Snapshot{
		enabled:      true,
		pins:         map[string]struct{}{PinKey(wsC): {}},
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-b", "ws-a"},
	}
	gotC := SortBySourceOrder([]source.Candidate{wsA, wsB, wsC}, "", []string{config.SourceHerdr}, snapshotC)
	wantC := []string{"ws-c", "ws-b", "ws-a"}
	for i, label := range wantC {
		if gotC[i].Label != label {
			t.Errorf("pinned current candidate[%d] = %q, want %q", i, gotC[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_SourceBlocksRemainContiguous(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}
	proj := source.Candidate{Source: config.SourceProjects, Label: "proj-1"}
	zox := source.Candidate{Source: config.SourceZoxide, Label: "zox-1"}

	snapshot := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-b", "ws-a"},
	}
	input := []source.Candidate{proj, wsC, zox, wsA, wsB}
	got := SortBySourceOrder(input, "", []string{config.SourceHerdr, config.SourceProjects, config.SourceZoxide}, snapshot)
	want := []string{"ws-b", "ws-a", "ws-c", "proj-1", "zox-1"}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].Label, label)
		}
	}
}

func TestSort_FocusMRU_NonEmptyQueryUnchanged(t *testing.T) {
	wsA := source.Candidate{Source: config.SourceHerdr, Label: "project-alpha", Meta: map[string]string{"workspace_id": "ws-a"}}
	wsB := source.Candidate{Source: config.SourceHerdr, Label: "project-beta", Meta: map[string]string{"workspace_id": "ws-b"}}
	wsC := source.Candidate{Source: config.SourceHerdr, Label: "project-gamma", Meta: map[string]string{"workspace_id": "ws-c"}}

	// With query "beta", wsB matches exact prefix
	snapshot := Snapshot{
		enabled:      true,
		currentExact: Identity(wsC),
		workspaceMRU: []string{"ws-c", "ws-a", "ws-b"}, // MRU has ws-a before ws-b
	}
	got := SortBySourceOrder([]source.Candidate{wsA, wsC, wsB}, "beta", []string{config.SourceHerdr}, snapshot)
	if got[0].Label != "project-beta" {
		t.Fatalf("query did not rank beta first: got %q", got[0].Label)
	}
}
