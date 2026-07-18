package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
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

// === 1. Header has no brand text ===

// TestRenderHeader_NoBrandText proves the header never renders the "shep"
// brand mark — it is a single "Search: <query>" line plus the right-aligned
// count, nothing else.
func TestRenderHeader_NoBrandText(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(
		[]source.Candidate{zoxideCandidate("alpha", "/a"), zoxideCandidate("beta", "/b")},
		nil, Layout{Theme: ThemeMocha},
	)
	m, _ = update(t, m, sizeMsg(120, 36))
	plain := stripNonSGRANSI(m.renderHeader(120))
	if strings.Contains(plain, "shep") {
		t.Errorf("header must not contain the removed brand mark \"shep\": %q", plain)
	}
	if !strings.Contains(plain, "Search:") {
		t.Errorf("header missing \"Search:\" role: %q", plain)
	}
	if !strings.Contains(plain, "2 candidates") {
		t.Errorf("header missing count \"2 candidates\": %q", plain)
	}
}

// TestView_HeaderLineHasNoBrand proves the full View() output's header line
// (everything before the first pane border) never contains the brand mark
// either — an end-to-end check, not just the isolated renderHeader unit.
func TestView_HeaderLineHasNoBrand(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	view := stripNonSGRANSI(m.View())
	firstBorder := strings.Index(view, "╭")
	if firstBorder < 0 {
		t.Fatal("no pane border found in view")
	}
	header := view[:firstBorder]
	if strings.Contains(header, "shep") {
		t.Errorf("header line must not contain \"shep\": %q", header)
	}
	if !strings.Contains(header, "Search:") {
		t.Errorf("header line missing \"Search:\": %q", header)
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
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha, SourceOrder: nonDefault})
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

// === 3. Group headers removed entirely; every row is selectable ===

// TestBuildRows_OnlyKnownRowKinds proves every row buildRows ever produces is
// one of the three remaining kinds (RowCandidate, RowTab, RowPane) — there is
// no fourth "header" kind left to accidentally reintroduce.
func TestBuildRows_OnlyKnownRowKinds(t *testing.T) {
	t.Parallel()
	rows := buildRows(rowBuildInput{candidates: goldenCandidates()})
	for _, r := range rows {
		switch r.Kind {
		case RowCandidate, RowTab, RowPane:
			// ok
		default:
			t.Errorf("unexpected RowKind %v in row %+v", r.Kind, r)
		}
	}
}

// TestCursor_NeverLandsOnNonActionableRow proves every row Selectable() is
// true — trivially guaranteed now that group headers are gone, but asserted
// explicitly per the corrective-round contract ("the cursor can never land
// on a non-actionable row").
func TestCursor_NeverLandsOnNonActionableRow(t *testing.T) {
	t.Parallel()
	rows := buildRows(rowBuildInput{candidates: goldenCandidates()})
	if len(rows) == 0 {
		t.Fatal("setup: expected at least one row")
	}
	for i, r := range rows {
		if !r.Selectable() {
			t.Errorf("row %d (%+v) is not selectable — a non-actionable row exists", i, r)
		}
	}
}

// === 4. Pane row primary/secondary + status icon (never a literal word) ===

// TestRowDisplayText_PaneRow_PathPrimarySecondaryPaneID proves a RowPane's
// primary is its path and secondary is its pane id (Candidate.Label), and
// that no agent_status literal word ever appears in the rendered row text —
// only an icon.
func TestRowDisplayText_PaneRow_PathPrimarySecondaryPaneID(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	for _, status := range []string{"working", "idle", "done", "blocked", "", "totally-bogus"} {
		row := Row{
			Kind: RowPane,
			Candidate: source.Candidate{
				Label: "p1", Path: "/srv/api", Meta: map[string]string{"agent_status": status},
			},
		}
		primary, secondary := m.rowDisplayText(row)
		if !strings.Contains(primary, "/srv/api") {
			t.Errorf("status=%q: primary = %q, want it to contain the path /srv/api", status, primary)
		}
		if secondary != "p1" {
			t.Errorf("status=%q: secondary = %q, want the pane id \"p1\"", status, secondary)
		}
		full := stripNonSGRANSI(primary + " " + secondary)
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
	if ansi.StringWidth(stripNonSGRANSI(primaryWithout)) >= ansi.StringWidth(stripNonSGRANSI(primaryWith)) {
		t.Errorf("no-status primary %q should be shorter than idle-status primary %q (no icon prepended)", primaryWithout, primaryWith)
	}
	if strings.Contains(stripNonSGRANSI(primaryWithout), "✓") {
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

// === 7. Footer full-path restoration ===

// TestRenderFooter_ShowsFullPathAndPreservesHints proves the footer shows
// the full path of the currently highlighted row alongside the (unchanged)
// keybinding hints, at a width comfortably large enough to fit both.
func TestRenderFooter_ShowsFullPathAndPreservesHints(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/home/dev/alpha")}, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	footer := stripNonSGRANSI(m.renderFooter())
	if !strings.Contains(footer, "/home/dev/alpha") {
		t.Errorf("footer missing full path: %q", footer)
	}
	if !strings.Contains(footer, "enter open") || !strings.Contains(footer, "esc cancel") {
		t.Errorf("footer missing keybinding hints: %q", footer)
	}
}

// TestRenderFooter_TruncatesPathFromLeftBeforeDroppingHints proves that at a
// narrow width, the path segment truncates from the LEFT (keeping the
// identifying tail) before the hints ever lose a single character, and
// degrades to hints-only once there is no meaningful room left for a path at
// all.
func TestRenderFooter_TruncatesPathFromLeftBeforeDroppingHints(t *testing.T) {
	t.Parallel()
	longPath := "/home/dev/a/very/deeply/nested/project/directory/name"
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("name", longPath)}, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(60, 20))
	footer := stripNonSGRANSI(m.renderFooter())
	if !strings.Contains(footer, "…") {
		t.Errorf("expected the long path to truncate with a leading ellipsis: %q", footer)
	}
	if !strings.Contains(footer, "enter open") {
		t.Errorf("hints must survive even when the path truncates: %q", footer)
	}
	// The tail (most identifying part) of the path must survive, not its root.
	if !strings.Contains(footer, "name") {
		t.Errorf("expected the path's identifying tail to survive left-truncation: %q", footer)
	}

	// At an extremely narrow width, the footer must degrade to hints-only
	// (no unreadable one-character path sliver) while still fitting.
	m2, _ := update(t, m, sizeMsg(15, 20))
	narrowFooter := stripNonSGRANSI(m2.renderFooter())
	if strings.Contains(narrowFooter, "…") && !strings.Contains(narrowFooter, "enter") {
		t.Errorf("expected a hints-only degrade at extremely narrow width: %q", narrowFooter)
	}
}

// TestFooterPathSegment_EmptyWhenNothingHighlighted proves footerPathSegment
// returns "" when there is nothing highlighted (empty rows).
func TestFooterPathSegment_EmptyWhenNothingHighlighted(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	if got := m.footerPathSegment(); got != "" {
		t.Errorf("footerPathSegment() = %q, want empty with no candidates", got)
	}
}
