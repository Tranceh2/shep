package tui

import "github.com/tranceh2/shep/internal/config"

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

// Footer/help Enter labels: the single source of truth for the per-row-kind
// wording both the footer hint and the "?" help overlay render, so neither
// surface can drift from the other or from the action handleEnter dispatches.
// The wording is deliberately TRUTHFUL and generic: focus-or-create is
// resolved later at the command layer, so a candidate row promises neither;
// a pane row focuses its CONTAINING tab (Herdr has no per-pane focus command).
const (
	// footerLabelOpen is the truthful generic verb for a top-level candidate
	// row: it opens the destination (focus-or-create resolved later), without
	// promising creation or focusing an existing workspace.
	footerLabelOpen = "open"
	// helpLabelOpen matches the pre-existing help-overlay Enter description
	// for a candidate row; kept verbatim so the approved help body is stable.
	helpLabelOpen = "open the highlighted row"
	// footerLabelFocusTab is the tab-row Enter verb; it matches
	// internal/herdr.Driver.FocusTab, the call Enter dispatches for a RowTab.
	footerLabelFocusTab = "Focus tab"
	// helpLabelFocusTab describes the RowTab Enter action for the help overlay.
	helpLabelFocusTab = "focus the already-open tab"
	// footerLabelFocusContainingTab is the pane-row Enter verb. Herdr exposes
	// no per-pane focus command, so a pane row focuses the tab that CONTAINS
	// it — the truthful action, never "focus pane".
	footerLabelFocusContainingTab = "Focus containing tab"
	// helpLabelFocusContainingTab describes the RowPane Enter action.
	helpLabelFocusContainingTab = "focus the tab containing this pane"
	// footerLabelOpenSession is the session-row Enter verb: attach-oriented
	// open wording, never "create".
	footerLabelOpenSession = "open session"
	// helpLabelOpenSession describes the session-row Enter action.
	helpLabelOpenSession = "open the highlighted session"
)

// rowActionDescriptorResult is the single resolved description of what Enter
// does on one row: the typed Action the command layer dispatches, plus the
// truthful FooterLabel/HelpText both display surfaces render. handleEnter,
// footerHints, and helpBodyText all read from this one result, so copy can
// never drift from behavior (SPEC-NAV-1.8 parity).
type rowActionDescriptorResult struct {
	Action      RowAction
	FooterLabel string
	HelpText    string
}

// rowActionDescriptor resolves the truthful Enter action and display copy for
// one row, keyed by its kind (and, for a top-level candidate, its source). It
// is a pure function of the row so both the footer and the help overlay can
// derive identical wording from the same place the Enter dispatch reads its
// Action.
func rowActionDescriptor(r Row) rowActionDescriptorResult {
	switch r.Kind {
	case RowTab:
		return rowActionDescriptorResult{Action: RowActionFocusTab, FooterLabel: footerLabelFocusTab, HelpText: helpLabelFocusTab}
	case RowPane:
		return rowActionDescriptorResult{Action: RowActionFocusTab, FooterLabel: footerLabelFocusContainingTab, HelpText: helpLabelFocusContainingTab}
	default: // RowCandidate
		if r.Candidate.Source == config.SourceSessions {
			return rowActionDescriptorResult{Action: RowActionOpen, FooterLabel: footerLabelOpenSession, HelpText: helpLabelOpenSession}
		}
		return rowActionDescriptorResult{Action: RowActionOpen, FooterLabel: footerLabelOpen, HelpText: helpLabelOpen}
	}
}
