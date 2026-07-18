package tui

// RowAction is the typed, TUI-owned launch semantics for one picker row: what
// pressing Enter on the row does at the command layer. It replaces the old
// stringly-typed dispatch that keyed off config.SourceHerdrTab /
// configHerdrPane candidate Source values — the launch decision is now a
// property of the Row (owned by the TUI), not a fake Source string carried by
// a generic source.Candidate.
//
// The zero value (RowActionOpen) is the default normal-launch path: the
// candidate opens via the FocusOrCreate / current-workspace target flow.
// RowActionFocusTab applies to a synthesized RowTab or RowPane: Enter focuses
// the already-open Herdr tab identified by the candidate's Meta["tab_id"]
// (Herdr has no per-pane focus command, so a pane row focuses its containing
// tab — the truthful action).
type RowAction int

const (
	// RowActionOpen is the normal launch path: open the candidate's workspace
	// via FocusOrCreate, or into the current workspace when a target override
	// (ctrl+t / ctrl+p) was chosen. It is the zero value so a plain
	// RowCandidate row needs no explicit Action assignment.
	RowActionOpen RowAction = iota
	// RowActionFocusTab routes Enter to driver.FocusTab with the row
	// candidate's Meta["tab_id"]. Used by synthesized RowTab and RowPane rows
	// that identify an already-open tab inside an already-open workspace.
	RowActionFocusTab
)
