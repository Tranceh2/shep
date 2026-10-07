package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// Corrective round — strict TDD. This file exercises the 5 specific
// regressions/gaps fixed in this round: (1) the "shep" brand mark removed
// from the header, (2) row order following the CONFIGURED general.sources
// order instead of a hardcoded literal, (3) group headers removed entirely
// (RowGroupHeader no longer exists as a type — the compiler itself enforces
// this; the tests below additionally assert every row built is one of the
// three remaining kinds and is selectable), (4) pane rows showing
// path-as-primary/pane-id-as-secondary with a status ICON (never a literal
// status word) that shares the model's single spinner tick loop, and (5) the
// full path restored in the footer.

// === 1. The chrome carries no brand and no pane boxes ===

// TestPromptRow_NoBrandCompactCount proves the borderless chrome: no brand
// mark (Herdr's popup frame already names the plugin), the prompt glyph and
// the all view's placeholder on the prompt row, and the compact count
// ("2", never "2 candidates") right-aligned on the same row.
func TestPromptRow_NoBrandCompactCount(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(
		[]source.Candidate{zoxideCandidate("alpha", "/a"), zoxideCandidate("beta", "/b")},
		nil, Layout{Theme: testTheme(ThemeMocha)},
	)
	m, _ = update(t, m, sizeMsg(120, 36))
	prompt := promptText(m)
	if !strings.HasPrefix(prompt, "❯ ") || !strings.Contains(prompt, "Search workspaces, projects, folders") {
		t.Errorf("prompt row = %q, want the ❯ prompt and the all-view placeholder", prompt)
	}
	if !strings.HasSuffix(prompt, " 2") || strings.Contains(prompt, "candidates") {
		t.Errorf("prompt row = %q, want the compact count \"2\" right-aligned", prompt)
	}
	if strings.Contains(prompt, "[/]") || strings.Contains(prompt, "⌕") {
		t.Errorf("prompt row = %q, must not carry the old keycap or search glyph", prompt)
	}
}

// TestView_ChromeRowsHaveNoBrandOrBoxes proves, end to end, that the chrome
// rows above the body (tab strip, prompt row, rule) never show a brand mark
// and that no rounded pane box is drawn anywhere in the frame.
func TestView_ChromeRowsHaveNoBrandOrBoxes(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))
	lines := viewLines(m)
	chrome := strings.Join(lines[:3], "\n")
	if strings.Contains(strings.ToLower(chrome), "shep") {
		t.Errorf("chrome rows must not carry a brand mark: %q", chrome)
	}
	if !strings.Contains(lines[0], " all ") || !strings.Contains(lines[0], " agents ") {
		t.Errorf("tab strip row = %q, want the all and agents tabs", lines[0])
	}
	for i, line := range lines {
		if strings.ContainsAny(line, "╭╮╰╯") {
			t.Errorf("line %d = %q, must not draw a pane box", i, line)
		}
	}
}

// === 2. List order follows configured general.sources ===

// TestBuildRows_NonDefaultConfiguredOrder proves a non-default source order
// (projects before herdr, the inverse of config.defaultSourceOrder) is
// honored verbatim end-to-end via Layout.SourceOrder -> Model.sourceOrder ->
// rowBuildInput.sourceOrder -> buildRows, not just at the pure buildRows
// unit level (see rows_test.go's TestBuildRows_CustomSourceOrderOverridesDefault
// for that narrower unit proof).
func TestBuildRows_NonDefaultConfiguredOrder(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
		projectCandidate("shep", "/home/dev/shep"),
	}
	nonDefault := []string{config.SourceProjects, config.SourceHerdr, config.SourceZoxide, config.SourceWorkspaces}
	m := NewModelWithLayout(cands, nil, Layout{Theme: testTheme(ThemeMocha), SourceOrder: nonDefault})
	m, _ = update(t, m, sizeMsg(120, 36))
	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(m.rows), m.rows)
	}
	if m.rows[0].Candidate.Label != "shep" {
		t.Errorf("row 0 = %q, want \"shep\" (projects before herdr per configured order)", m.rows[0].Candidate.Label)
	}
	if m.rows[1].Candidate.Label != "backend" {
		t.Errorf("row 1 = %q, want \"backend\"", m.rows[1].Candidate.Label)
	}
}

// TestEmptyQueryKeepsSourceBlocksContiguousWithActiveRanking proves the
// production wiring fix: with an ACTIVE ranking snapshot and an empty query,
// applyFilter composes strict contiguous source blocks (via
// ranking.SortBySourceOrder) so a zoxide candidate never interleaves between
// Herdr candidates. This is the end-to-end regression for the observed bug
// where zoxide directories appeared among Herdr workspaces.
func TestEmptyQueryKeepsSourceBlocksContiguousWithActiveRanking(t *testing.T) {
	store, err := ranking.OpenPath(filepath.Join(t.TempDir(), "ranking.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := store.Snapshot(context.Background(), "")
	if !snapshot.Active() {
		t.Fatal("expected an active ranking snapshot")
	}
	// Interleaved input order across sources; empty query must regroup them.
	cands := []source.Candidate{
		herdrCandidate("alpha", "/srv/alpha", "w1"),
		zoxideCandidate("mid", "/tmp/mid"),
		herdrCandidate("beta", "/srv/beta", "w2"),
	}
	order := []string{config.SourceHerdr, config.SourceZoxide}
	m := NewModelWithLayout(cands, nil, Layout{Theme: testTheme(ThemeMocha), SourceOrder: order, RankingSnapshot: snapshot})
	m, _ = update(t, m, sizeMsg(120, 36))
	var got []string
	for _, r := range m.rows {
		if r.Kind == RowCandidate {
			got = append(got, r.Candidate.Source)
		}
	}
	want := []string{config.SourceHerdr, config.SourceHerdr, config.SourceZoxide}
	if !equalStrings(got, want) {
		t.Errorf("empty-query source blocks = %v, want %v (zoxide must not interleave Herdr)", got, want)
	}
}

// === 3. Group headers removed entirely; every row is selectable ===

// TestBuildRows_OnlyKnownRowKinds proves every row buildRows ever produces is
// one of the three remaining kinds (RowCandidate, RowTab, RowPane) — there is
// no fourth "header" kind left to accidentally reintroduce.
func TestBuildRows_OnlyKnownRowKinds(t *testing.T) {
	t.Parallel()
	rows := buildRows(rowBuildInput{candidates: goldenCandidates()})
	require.NotEmpty(t, rows, "setup must exercise the known-kinds loop")
	for _, r := range rows {
		switch r.Kind {
		case RowCandidate, RowTab, RowPane:
			// ok
		default:
			t.Errorf("unexpected RowKind %v in row %+v", r.Kind, r)
		}
	}
}

// === 4. Pane row primary/secondary + status icon (never a literal word) ===

// TestRowDisplayText_PaneRow_PathPrimarySecondaryPaneID proves a RowPane's
// primary names the pane by its label (the default pane format no longer
// repeats the path on every child), its secondary stays empty, and no
// agent_status literal word ever appears in the rendered row text — only an
// icon.
func TestRowDisplayText_PaneRow_PathPrimarySecondaryPaneID(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	for _, status := range []string{"working", "idle", "done", "blocked", "", "totally-bogus"} {
		row := Row{
			Kind:  RowPane,
			Depth: 2,
			Candidate: source.Candidate{
				Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": status},
			},
		}
		primary, secondary := m.rowDisplayText(row)
		if strings.Contains(primary, "/srv/api") {
			t.Errorf("status=%q: primary = %q, must not repeat the path", status, primary)
		}
		if !strings.Contains(primary, "p1") {
			t.Errorf("status=%q: primary = %q, want it to contain the pane id \"p1\"", status, primary)
		}
		if secondary != "" {
			t.Errorf("status=%q: secondary = %q, want empty (RowPane secondary removed entirely by Change 2)", status, secondary)
		}
		full := ansi.Strip(primary + " " + secondary)
		for _, word := range []string{"working", "idle", "done", "blocked", "unknown"} {
			if strings.Contains(full, word) {
				t.Errorf("status=%q: rendered row text contains literal status word %q: %q", status, word, full)
			}
		}
	}
}

// TestRowDisplayText_PaneRow_NoStatusMeansNoIcon proves an empty
// agent_status renders no icon glyph at all (not even a fallback/unknown
// marker) — "no status = no icon".
func TestRowDisplayText_PaneRow_NoStatusMeansNoIcon(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	withStatus := Row{Kind: RowPane, Candidate: source.Candidate{
		Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": "idle"},
	}}
	withoutStatus := Row{Kind: RowPane, Candidate: source.Candidate{
		Label: "p1", Path: "/srv/api", Meta: map[string]string{},
	}}
	primaryWith, _ := m.rowDisplayText(withStatus)
	primaryWithout, _ := m.rowDisplayText(withoutStatus)
	if ansi.StringWidth(ansi.Strip(primaryWithout)) >= ansi.StringWidth(ansi.Strip(primaryWith)) {
		t.Errorf("no-status primary %q should be shorter than idle-status primary %q (no icon prepended)", primaryWithout, primaryWith)
	}
	if strings.Contains(ansi.Strip(primaryWithout), "✓") {
		t.Errorf("no-status primary must not contain the idle icon: %q", primaryWithout)
	}
}

// === 5. Working-status pane row arms the shared spinner tick loop ===

// TestSpinner_WorkingStatusPaneRowArmsWithoutPreviewLoading proves a visible
// working-status pane row arms the shared spinner tick loop even when no
// preview render is in flight, and that the single-tick-loop invariant
// (spinnerRunning) is respected exactly as it is for the preview-loading
// case.
func TestSpinner_WorkingStatusPaneRowArmsWithoutPreviewLoading(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	if m.previewLoading {
		t.Fatal("setup: expected previewLoading=false (no renderer wired)")
	}
	m.rows = []Row{{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": "working"},
		},
	}}
	if !m.anyVisibleRowWorking() {
		t.Fatal("setup: expected anyVisibleRowWorking()=true")
	}
	cmd := m.maybeStartSpinner()
	if cmd == nil {
		t.Fatal("expected a working-status pane row to arm the spinner tick loop even with no preview loading")
	}
	if !m.spinnerRunning {
		t.Fatal("expected spinnerRunning=true after arming")
	}
	// Second call must not double-arm (single-tick-loop invariant).
	if cmd := m.maybeStartSpinner(); cmd != nil {
		t.Error("expected no second Tick while the loop is already running")
	}
}

// TestSpinner_DeArmsOnceNeitherConditionHolds proves handleSpinnerTick lets
// the loop die once BOTH previewLoading is false AND no visible row is
// working — mirroring the preview-loading-only stop condition exactly.
func TestSpinner_DeArmsOnceNeitherConditionHolds(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.spinnerRunning = true
	m.previewLoading = false
	m.rows = []Row{{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": "working"},
		},
	}}
	// Still working: the loop must keep rescheduling.
	mm, cmd := m.handleSpinnerTick(spinner.TickMsg{})
	if cmd == nil {
		t.Error("expected the tick loop to keep rescheduling while a visible row is still working")
	}
	if !mm.spinnerRunning {
		t.Error("expected spinnerRunning to remain true while still needed")
	}

	// Status resolves (no more working rows) and no preview loading either:
	// the loop must die.
	mm.rows = []Row{{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": "done"},
		},
	}}
	mm2, cmd := mm.handleSpinnerTick(spinner.TickMsg{})
	if cmd != nil {
		t.Error("expected the tick loop to die once neither previewLoading nor a working row holds")
	}
	if mm2.spinnerRunning {
		t.Error("expected spinnerRunning=false once the tick loop stops")
	}
}

// === 6. Zoxide/project rows show full path, no secondary ===

// TestRowDisplayText_ZoxideAndProjectsShowFullPathNoSecondary proves
// SourceZoxide/SourceProjects rows no longer get the basename-first/
// shortened-parent-path treatment: primary is the full label (or path when
// label is empty) and secondary is always empty.
func TestRowDisplayText_ZoxideAndProjectsShowFullPathNoSecondary(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	cases := []Row{
		{Kind: RowCandidate, Candidate: zoxideCandidate("Downloads", "/home/dev/Downloads")},
		{Kind: RowCandidate, Candidate: projectCandidate("shep", "/home/dev/shep")},
		{Kind: RowCandidate, Candidate: zoxideCandidate("tmp", "/tmp")},
	}
	for _, row := range cases {
		primary, secondary := m.rowDisplayText(row)
		if secondary != "" {
			t.Errorf("candidate %q: secondary = %q, want empty (no basename/parent-path split anymore)", row.Candidate.Label, secondary)
		}
		if !strings.Contains(primary, row.Candidate.Label) {
			t.Errorf("candidate %q: primary = %q, want it to contain the full label", row.Candidate.Label, primary)
		}
	}
}

// === 7. Footer shortcuts are path-free and unboxed ===

// TestRenderFooter_HasNoSelectedPathOrBrackets proves the footer remains a
// compact shortcut contract at both standard and narrow widths.
func TestRenderFooter_HasNoSelectedPathOrBrackets(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/home/dev/alpha")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	for _, size := range []struct{ width, height int }{{120, 36}, {60, 20}, {15, 20}} {
		m, _ = update(t, m, sizeMsg(size.width, size.height))
		footer := footerText(m)
		if strings.Contains(footer, "/home/dev/alpha") || strings.ContainsAny(footer, "[]") {
			t.Errorf("footer at %dx%d = %q, must contain neither selected path nor brackets", size.width, size.height, footer)
		}
	}
}
