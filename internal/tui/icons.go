// Package tui icon fallback resolution: [tui].icons (config.TUIConfig.Icons,
// threaded through Layout.Icons — see layoutFromConfig in
// internal/command/open.go) selects one of three glyph tiers for the
// picker's OWN semantic icons — pane agent-status markers (agentStatusIcon)
// and row expand/tab/pane markers (kindPrefix). It intentionally does NOT
// cover source.Candidate.Icon (each [sources.<name>].icon in config): that
// is a raw user-configured string rendered verbatim by rowDisplayText /
// candidateDisplayText regardless of the resolved tier, since shep has no
// way to know what codepoints the user's own choice needs.
package tui

// Icon fallback tier names for Layout.Icons, mirrored in
// config.TUIIconsUnicode etc. so config validation and the TUI resolve the
// exact same set without an import cycle (config cannot import tui). The
// "nerd" tier was removed — config validation rejects [tui].icons = "nerd"
// explicitly rather than silently falling back to another tier.
const (
	IconsUnicode = "unicode"
	IconsASCII   = "ascii"
)

// IconSet is the resolved glyph table for one icon fallback tier.
type IconSet struct {
	Name string

	StatusIdle    string // agentStatusIcon("idle")
	StatusDone    string // agentStatusIcon("done")
	StatusBlocked string // agentStatusIcon("blocked")
	StatusUnknown string // agentStatusIcon("unknown")

	// StatusWorking is a static agentStatusIcon("working") fallback for a
	// tier whose terminal/locale cannot render the model's shared animated
	// spinner (spinner.MiniDot, which draws Unicode Braille dot glyphs).
	// Empty means the tier renders the animated spinner as-is (a unicode
	// terminal can always display Braille); only IconsASCII sets this, since
	// Braille has no 7-bit ASCII fallback rendering.
	StatusWorking string

	ExpandOpen   string // kindPrefix, expanded RowCandidate
	ExpandClosed string // kindPrefix, collapsed RowCandidate
	TabPrefix    string // kindPrefix, RowTab
	PanePrefix   string // kindPrefix, RowPane

	// ActiveMarker prefixes a RowTab/RowPane that identifies the Herdr tab or
	// pane shep is currently running inside (see Model.isActiveFocusRow) — a
	// truthful "you are here" indicator, since Enter can only ever focus the
	// containing tab (Herdr has no per-pane focus command), never claim to
	// focus one exact pane.
	ActiveMarker string
}

// iconSets holds every documented tier. IconsUnicode is byte-identical to
// the picker's pre-Phase-8 hardcoded glyphs — the default tier, so an unset
// [tui].icons never changes existing rendered output (and every pre-Phase-8
// golden fixture stays valid unchanged). IconsASCII is 7-bit ASCII only, for
// terminals/locales that cannot render Unicode at all.
var iconSets = map[string]IconSet{
	IconsUnicode: {
		Name:          IconsUnicode,
		StatusIdle:    "✓",
		StatusDone:    "●",
		StatusBlocked: "◉",
		StatusUnknown: "○",
		ExpandOpen:    "▾",
		ExpandClosed:  "▸",
		TabPrefix:     "»",
		PanePrefix:    "·",
		ActiveMarker:  "◆",
	},
	IconsASCII: {
		Name:          IconsASCII,
		StatusIdle:    "v",
		StatusDone:    "*",
		StatusBlocked: "!",
		StatusUnknown: "?",
		StatusWorking: "o",
		ExpandOpen:    "v",
		ExpandClosed:  ">",
		TabPrefix:     ">>",
		PanePrefix:    "-",
		ActiveMarker:  "@",
	},
}

// resolveIconSetName applies the documented default: empty or unrecognized
// configIcons resolves to IconsUnicode — the picker's original hardcoded
// glyphs — so resolution never fails to start the picker over a typo'd
// icons name, mirroring resolveThemeName's precedent.
func resolveIconSetName(configIcons string) string {
	if _, ok := iconSets[configIcons]; ok {
		return configIcons
	}
	return IconsUnicode
}

// resolveIconSet resolves configIcons (typically Layout.Icons) into a
// concrete IconSet.
func resolveIconSet(configIcons string) IconSet {
	return iconSets[resolveIconSetName(configIcons)]
}
