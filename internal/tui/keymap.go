package tui

// Canonical key chord notation, in the exact spelling tea.KeyPressMsg.String()
// produces (see keys.go's switch statements). These are the single source
// of truth for every user-facing chord string: the footer's compact hints
// (footerHints) and the "?" help cheat sheet (helpBodyText) both build
// their text from these constants and the keyBinding values below, so a
// chord can never be spelled differently on one surface than the other.
const (
	keyChordEnter     = "enter"
	keyChordTab       = "tab"
	keyChordShiftTab  = "shift+tab"
	keyChordBackspace = "backspace"
	keyChordCtrlW     = "ctrl+w"
	keyChordAltBksp   = "alt+backspace"
	keyChordCtrlT     = "ctrl+t"
	keyChordCtrlP     = "ctrl+p"
	keyChordLayout    = "ctrl+r"
	keyChordPin       = "ctrl+f"
	keyChordClose     = "ctrl+x"
	keyChordEsc       = "esc"
	keyChordQuestion  = "?"
	keyChordCtrlC     = "ctrl+c"
	keyChordCtrlG     = "ctrl+g"
	keyChordConfirm   = "y"

	// keyChordScrollArrows is the compact arrow notation of the help
	// overlay's footer; the ASCII icon tier spells it out instead.
	keyChordScrollArrows      = "↑↓"
	keyChordScrollArrowsASCII = "up/down"
)

// keyBinding is one entry in the app's single keybinding source of truth.
// chord is the help cheat sheet's key column (chordASCII its spelling under
// the ASCII icon tier, when the chord draws arrows) and help its
// description. footerChord/footerLabel are only set for bindings that also
// appear in the compact footer. footerAltLabel is the footer label while the
// binding's alternate state applies (esc with a query clears it; ctrl+f on a
// pinned row unpins it), so the footer always names what the key will do
// now.
type keyBinding struct {
	chord          string
	chordASCII     string
	help           string
	footerChord    string
	footerLabel    string
	footerAltLabel string
}

// chordFor returns the binding's key column for the icon tier.
func (b keyBinding) chordFor(ascii bool) string {
	if ascii && b.chordASCII != "" {
		return b.chordASCII
	}
	return b.chord
}

// Bindings that appear in BOTH the footer and the help cheat sheet. Each is
// defined exactly once here; footerHints and helpSections both reference
// these same values instead of retyping the chord/label text.
var (
	keyBindingEnter = keyBinding{
		chord: keyChordEnter, help: "open the highlighted row",
		footerChord: keyChordEnter, footerLabel: "open",
	}
	keyBindingTab = keyBinding{
		chord: keyChordTab + "/" + keyChordShiftTab, help: "next/previous view",
		footerChord: keyChordTab, footerLabel: "agents",
	}
	keyBindingCtrlT = keyBinding{
		chord: keyChordCtrlT, help: "open as a tab here",
		footerChord: keyChordCtrlT, footerLabel: "tab",
	}
	keyBindingCtrlP = keyBinding{
		chord: keyChordCtrlP, help: "open as a pane here",
		footerChord: keyChordCtrlP, footerLabel: "pane",
	}
	keyBindingPin = keyBinding{
		chord: keyChordPin, help: "pin/unpin",
		footerChord: keyChordPin, footerLabel: "pin", footerAltLabel: "unpin",
	}
	keyBindingClose = keyBinding{
		chord: keyChordClose, help: "close the open Herdr item (y/n when configured)",
		footerChord: keyChordClose, footerLabel: "close",
	}
	keyBindingHelp = keyBinding{
		chord: keyChordQuestion, help: "this help",
		footerChord: keyChordQuestion, footerLabel: "help",
	}
	keyBindingEsc = keyBinding{
		chord: keyChordEsc, help: "clear search, then quit",
		footerChord: keyChordEsc, footerLabel: "quit", footerAltLabel: "clear",
	}

	// Footer-only bindings, with no line of their own in the cheat sheet:
	// the help overlay's footer and the close confirmation (keyBindingClose's
	// help mentions the y/n).
	keyBindingHelpClose    = keyBinding{footerChord: keyChordEsc, footerLabel: "close"}
	keyBindingHelpScroll   = keyBinding{footerChord: keyChordScrollArrows, footerLabel: "scroll"}
	keyBindingConfirmClose = keyBinding{footerChord: keyChordConfirm, footerLabel: "confirm"}
)

// closeCancelHint follows the close confirmation's y hint: every key but
// keyChordConfirm backs out of the close (see handleKey).
const closeCancelHint = "any other key cancels"

// helpSection is one titled group of the help cheat sheet.
type helpSection struct {
	title    string
	bindings []keyBinding
}

// keyMap is the help cheat sheet's shortcut column, grouped by what the user
// is doing. Its Enter line is rewritten per highlighted row (see
// helpBodyText), so the help tells the same truth as the footer and Enter.
var keyMap = []helpSection{
	{"Navigate", []keyBinding{
		{chord: "↑/↓, ctrl+k/ctrl+j", chordASCII: "up/down, ctrl+k/ctrl+j", help: "move"},
		{chord: "ctrl+u/ctrl+d", help: "half page up/down"},
		{chord: "pgup/pgdn", help: "scroll the preview"},
		{chord: "←/→, ctrl+h/ctrl+l", chordASCII: "left/right, ctrl+h/ctrl+l", help: "collapse/expand"},
		keyBindingTab,
	}},
	{"Act", []keyBinding{keyBindingEnter, keyBindingCtrlT, keyBindingCtrlP, keyBindingPin, keyBindingClose}},
	{"Search", []keyBinding{
		{chord: "type", help: "filter the list"},
		{chord: keyChordBackspace, help: "delete a character"},
		{chord: keyChordCtrlW + ", " + keyChordAltBksp, help: "delete a word"},
		keyBindingEsc,
	}},
	{"Layout", []keyBinding{{chord: keyChordLayout, help: "show or hide the preview"}}},
	{"Session", []keyBinding{{chord: keyChordCtrlC + ", " + keyChordCtrlG, help: "quit"}}},
}

// searchSyntax documents the extended query syntax (internal/fuzzy
// ParseExtendedQuery) as example/meaning pairs. Only what the parser really
// does is listed; TestSearchSyntax_DocumentedExamplesParse parses every
// example and checks the documented kind, so the help cannot drift from the
// parser. Matching ignores case throughout.
var searchSyntax = helpSection{"Search syntax", []keyBinding{
	{chord: "api web", help: "both terms (space is AND)"},
	{chord: "api|web", help: "either term (OR)"},
	{chord: "'api", help: "exact text, closing quote optional"},
	{chord: `"api gw"`, help: "exact phrase ('api gw' too)"},
	{chord: "^back", help: "starts with"},
	{chord: "end$", help: "ends with"},
	{chord: "^shep$", help: "the whole text is exactly"},
	{chord: "/v[0-9]+/", help: "regular expression"},
	{chord: "!test", help: "exclude matches"},
	{chord: "!^tmp", help: "negation works with every form"},
	{chord: "status:working", help: "agent status: idle working blocked done unknown"},
	{chord: "agent:claude", help: "agent name contains"},
	{chord: "source:zoxide", help: "source: herdr workspaces zoxide projects sessions agents, or a custom name"},
	{chord: "path:allsafe", help: "path contains"},
	{chord: "s:blocked", help: "short forms s: a: src: p:"},
}}
