package tui

import "github.com/tranceh2/shep/internal/source"

// RowKind identifies what a rendered picker row represents. The picker's
// visible list is a flat []Row (see buildRows), not a flat []source.Candidate:
// Row adds the hierarchy/match-provenance information renderList and
// handleKey need without re-deriving it from scratch on every keystroke.
//
// Corrective round: group headers (the old RowGroupHeader divider rows) were
// removed entirely — the list is a single flat sequence, differentiated only
// by each row's icon/color per source (see rowDisplayText/kindPrefix). Every
// row is now selectable by construction; there is no non-actionable row kind
// left to skip.
type RowKind int

const (
	// RowCandidate is a top-level source.Candidate row (an active Herdr
	// workspace, a discovered project, a zoxide directory, or a configured
	// [[workspaces]] entry).
	RowCandidate RowKind = iota
	// RowTab is a synthesized child row for one already-open tab inside a
	// RowCandidate workspace (Source == config.SourceHerdrTab).
	RowTab
	// RowPane is a synthesized grandchild row for one pane inside a RowTab
	// (Source == config.SourceHerdrPane). Enter routes to the same
	// driver.FocusTab call as its parent RowTab — Herdr has no per-pane
	// focus command (see internal/herdr.Driver.FocusTab).
	RowPane
)

// MatchKind records why a row is visible for a non-empty query. It has no
// meaning for an empty query (every row that would ever be shown is shown,
// see buildRows).
type MatchKind int

const (
	// MatchNone applies to any row visible only because the query is empty.
	MatchNone MatchKind = iota
	// MatchDirect means the row's OWN label/path/metadata matched the query.
	MatchDirect
	// MatchDescendant means the row itself did not match, but is shown
	// because one of its descendants did (a workspace shown for a matching
	// tab/pane; a tab shown for a matching pane). Rendered with a distinct,
	// muted "via ..." indicator (see renderRowText) so a descendant-only
	// match is never confused with a direct one.
	MatchDescendant
)

// Row is one entry in the picker's visible, flat, progressively disclosed
// list — the single vocabulary buildRows/renderList/handleKey operate on.
type Row struct {
	Kind      RowKind
	Candidate source.Candidate
	Depth     int // 0 = candidate, 1 = tab, 2 = pane
	Match     MatchKind
	// ID is a stable identity key (see rowIdentity) used for: (1) selection
	// retention across a rebuild, (2) expand/collapse state lookups. It is
	// never derived from slice position, so it survives reordering/filtering.
	ID string
	// Expandable is true for a RowCandidate that can show tab/pane children
	// (Source == config.SourceHerdr) — i.e. Left/Right and Enter toggle its
	// expansion. False for every other row kind (a tab/pane row has no
	// further children in this model; other candidate sources never have
	// descendants).
	Expandable bool
	// Expanded reports whether this row's children are currently rendered
	// beneath it (either because the user manually expanded it, or because
	// the active query matched a descendant — see buildRows).
	Expanded bool
}

// Selectable reports whether Enter should treat r as "open this". Every row
// kind is actionable since group headers were removed — kept as a named
// predicate (rather than inlined `true`) so callers stay self-documenting
// and the invariant ("the cursor can never land on a non-actionable row")
// stays assertable from one place.
func (r Row) Selectable() bool { return true }
