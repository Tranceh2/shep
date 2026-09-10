package tui

import "fmt"

// helpChordColumnWidth is the fixed width of the help overlay's key-chord
// column (including its leading 2-space indent). Every renderHelpLine
// output aligns its description to this column — keep any future chord
// text well under 26 runes or the table stops lining up.
const helpChordColumnWidth = 26

// Canonical key chord notation, in the exact spelling tea.KeyMsg.String()
// produces (see keys.go's switch statements). These are the single source
// of truth for every user-facing chord string: the footer's compact hints
// (footerHints) and the full "?" help overlay (helpBodyText) both build
// their text from these constants and the keyBinding values below, so a
// chord can never be spelled differently on one surface than the other.
const (
	keyChordUpDown    = "up/down, ctrl+j/ctrl+k"
	keyChordLeftRight = "left/right"
	keyChordEnter     = "enter"
	keyChordTab       = "tab"
	keyChordShiftTab  = "shift+tab"
	keyChordCtrlU     = "ctrl+u"
	keyChordBackspace = "backspace"
	keyChordPgUpDown  = "pgup/pgdown"
	keyChordHomeEnd   = "home/end"
	keyChordAnyLetter = "any letter"
	keyChordCtrlT     = "ctrl+t"
	keyChordCtrlP     = "ctrl+p"
	keyChordCtrlL     = "ctrl+l"
	keyChordPin       = "alt+p"
	keyChordEsc       = "esc"
	keyChordQuestion  = "?"
	keyChordCtrlC     = "ctrl+c"
	keyChordCtrlG     = "ctrl+g"
)

// keyBinding is one entry in the app's single keybinding source of truth.
// chord is the display text for the help overlay's key column; help is its
// description. footerChord/footerLabel are only set for bindings that also
// appear in the compact footer — footerChord is empty for every
// help-overlay-only binding (List/Preview-focus actions with no footer
// real estate).
type keyBinding struct {
	chord       string
	help        string
	footerChord string
	footerLabel string
}

// keySection groups related bindings under one help-overlay heading.
type keySection struct {
	heading  string
	bindings []keyBinding
}

// Bindings that appear in BOTH the footer and the help overlay. Each is
// defined exactly once here; footerHints and keyMap below both reference
// these same values instead of retyping the chord/label text.
var (
	keyBindingEnter = keyBinding{
		chord: keyChordEnter, help: "open the highlighted row",
		footerChord: keyChordEnter, footerLabel: "open",
	}
	keyBindingTab = keyBinding{
		chord: keyChordTab + " / " + keyChordShiftTab, help: "switch focus between list and preview",
		footerChord: keyChordTab, footerLabel: "preview",
	}
	keyBindingCtrlT = keyBinding{
		chord: keyChordCtrlT, help: "open in a new tab of the current workspace (list or preview focus)",
		footerChord: keyChordCtrlT, footerLabel: "tab",
	}
	keyBindingCtrlP = keyBinding{
		chord: keyChordCtrlP, help: "open in a new pane of the current workspace (list or preview focus)",
		footerChord: keyChordCtrlP, footerLabel: "pane",
	}
	keyBindingPin = keyBinding{
		chord: keyChordPin, help: "pin/unpin the highlighted top-level candidate",
		footerChord: keyChordPin, footerLabel: "pin",
	}
	keyBindingHelp = keyBinding{
		chord: keyChordQuestion + ", " + keyChordEsc, help: "close help and return to what you were doing",
		footerChord: keyChordQuestion, footerLabel: "help",
	}
	keyBindingEsc = keyBinding{
		chord: keyChordEsc + ", " + keyChordCtrlC + ", " + keyChordCtrlG, help: "cancel the picker",
		footerChord: keyChordEsc, footerLabel: "quit",
	}
)

// keyMap is the single source of truth for every keybinding's chord
// notation and description, feeding both footerHints (compact footer
// hints, via the shared keyBinding* vars above) and helpBodyText (full "?"
// help overlay, via this whole table). Edit a chord or description once,
// here; there is no second, independently typed copy anywhere else in this
// package.
var keyMap = []keySection{
	{
		heading: "Navigation",
		bindings: []keyBinding{
			{chord: keyChordUpDown, help: "move the cursor"},
			{chord: keyChordLeftRight, help: "collapse/expand a workspace's tabs/panes"},
			keyBindingEnter,
			keyBindingPin,
			keyBindingTab,
			{chord: keyChordCtrlU, help: "clear the query"},
			{chord: keyChordBackspace, help: "delete the last query character"},
		},
	},
	{
		heading: "Preview (while focused)",
		bindings: []keyBinding{
			{chord: keyChordUpDown, help: "scroll one line"},
			{chord: keyChordPgUpDown, help: "scroll one page"},
			{chord: keyChordHomeEnd, help: "jump to top/bottom"},
			{chord: keyChordAnyLetter, help: "return to the list and search"},
		},
	},
	{
		heading:  "Herdr",
		bindings: []keyBinding{keyBindingCtrlT, keyBindingCtrlP},
	},
	{
		heading: "Layout",
		bindings: []keyBinding{
			{chord: keyChordCtrlL, help: "toggle layout: auto / landscape (list focus only)"},
		},
	},
	{
		heading: "Help (this screen)",
		bindings: []keyBinding{
			{chord: keyChordUpDown, help: "scroll one line"},
			{chord: keyChordPgUpDown, help: "scroll one page"},
			{chord: keyChordHomeEnd, help: "jump to top/bottom"},
			keyBindingHelp,
		},
	},
	{
		heading:  "Session",
		bindings: []keyBinding{keyBindingEsc},
	},
}

// renderHelpLine formats one keyMap binding as a help-overlay row: a
// 2-space indent, the chord left-padded to helpChordColumnWidth, then the
// description. Shared by helpBodyText so every section renders through the
// identical formatting rule.
func renderHelpLine(b keyBinding) string {
	return fmt.Sprintf("  %-*s%s", helpChordColumnWidth, b.chord, b.help)
}
