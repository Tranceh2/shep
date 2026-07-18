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
// config.TUIIconsNerd etc. so config validation and the TUI resolve the
// exact same set without an import cycle (config cannot import tui).
const (
	IconsNerd    = "nerd"
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
	// Empty means the tier renders the animated spinner as-is (nerd and
	// unicode terminals can always display Braille); only IconsASCII sets
	// this, since Braille has no 7-bit ASCII fallback rendering.
	StatusWorking string

	ExpandOpen   string // kindPrefix, expanded RowCandidate
	ExpandClosed string // kindPrefix, collapsed RowCandidate
	TabPrefix    string // kindPrefix, RowTab
	PanePrefix   string // kindPrefix, RowPane
}

// iconSets holds every documented tier. IconsUnicode is byte-identical to
// the picker's pre-Phase-8 hardcoded glyphs — the default tier, so an unset
// [tui].icons never changes existing rendered output (and every pre-Phase-8
// golden fixture stays valid unchanged). IconsNerd uses Font Awesome Nerd
// Font Private Use Area codepoints (requires a patched terminal font).
// IconsASCII is 7-bit ASCII only, for terminals/locales that cannot render
// Unicode at all.
var iconSets = map[string]IconSet{
	IconsNerd: {
		Name:          IconsNerd,
		StatusIdle:    "\uf00c", // nf-fa-check
		StatusDone:    "\uf111", // nf-fa-circle
		StatusBlocked: "\uf071", // nf-fa-exclamation_triangle
		StatusUnknown: "\uf059", // nf-fa-question_circle
		ExpandOpen:    "\uf078", // nf-fa-chevron_down
		ExpandClosed:  "\uf054", // nf-fa-chevron_right
		TabPrefix:     "\uf24d", // nf-fa-clone
		PanePrefix:    "\uf2d2", // nf-fa-window_maximize
	},
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
