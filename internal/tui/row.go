package tui

import "github.com/tranceh2/shep/internal/source"

// RowKind identifies what a rendered picker row represents. The picker's
// visible list is a flat []Row (see buildRows), not a flat []source.Candidate:
// Row adds the hierarchy/match-provenance information listLines and
// handleKey need without re-deriving it from scratch on every keystroke.
//
// The list is a single flat sequence (no header rows), differentiated by each
// row's presentation (see buildRowView); every row is selectable.
type RowKind int

const (
	// RowCandidate is a top-level source.Candidate row (an active Herdr
	// workspace, a discovered project, a zoxide directory, or a configured
	// [[workspaces]] entry).
	RowCandidate RowKind = iota
	// RowTab is a synthesized child row for one already-open tab inside a
	// RowCandidate workspace. Action is RowActionFocusTab.
	RowTab
	// RowPane is a synthesized grandchild row for one pane inside a RowTab.
	// Enter routes to the same driver.FocusTab call as its parent RowTab —
	// Herdr has no per-pane focus command (see internal/herdr.Driver.FocusTab)
	// — so Action is RowActionFocusTab as well.
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
	// muted style (see rowView.descendant) so a descendant-only
	// match is never confused with a direct one.
	MatchDescendant
)

// Row is one entry in the picker's visible, flat, progressively disclosed
// list — the single vocabulary buildRows/listLines/handleKey operate on.
type Row struct {
	Kind      RowKind
	Candidate source.Candidate
	Depth     int // 0 = candidate, 1 = tab, 2 = pane
	Match     MatchKind
	// MatchedIndexes are codepoint indexes into candidateHaystack(Candidate)
	// selected by fuzzy scoring. They are populated only for MatchDirect rows.
	MatchedIndexes []int
	// Action is the typed launch semantics for Enter on this row (see
	// RowAction). It is what the command layer dispatches on — never the
	// candidate's Source string. RowActionOpen (zero) for a RowCandidate;
	// RowActionFocusTab for a synthesized RowTab/RowPane.
	Action RowAction
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
	// IsLast reports whether this synthesized RowTab or RowPane is the final
	// child in its parent's displayed sibling order.
	IsLast bool
	// AncestorIsLast reports whether a RowPane's parent RowTab is its final
	// sibling. This is a two-level-tree simplification; revisit it as a
	// per-depth representation if the hierarchy gains a third level.
	AncestorIsLast bool
}
