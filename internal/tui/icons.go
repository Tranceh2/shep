// Package tui icon fallback resolution: [tui].icons (config.TUIConfig.Icons,
// threaded through Layout.Icons — see layoutFromConfig in
// internal/command/open.go) selects one of the glyph tiers for the
// picker's OWN semantic icons — the glyphs of the status, pin and group live
// markers (see rowparts.go), row tree glyphs (treePrefix) and the grid chrome
// (rules, divider, prompt, footer separator — see chrome.go). Row icons are
// the presentations' icon templates; their defaults follow the same tier
// (config.DefaultPresentations).
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

	StatusIdle    string // statusGlyph("idle")
	StatusDone    string // statusGlyph("done")
	StatusBlocked string // statusGlyph("blocked")
	StatusUnknown string // statusGlyph("unknown")

	// StatusWorking is a static statusGlyph("working") fallback for a
	// tier whose terminal/locale cannot render the model's shared animated
	// spinner (spinner.MiniDot, which draws Unicode Braille dot glyphs).
	// Empty means the tier renders the animated spinner as-is (a unicode
	// terminal can always display Braille); only IconsASCII sets this, since
	// Braille has no 7-bit ASCII fallback rendering.
	StatusWorking string

	// ExpandOpen/ExpandClosed were the RowCandidate expand/collapse glyphs
	// (▸/▾) kindPrefix used to prefix an expandable workspace row with. TRL-3
	// removed that glyph entirely — per-source icons already differentiate
	// row types, so it was redundant — and no production code path surfaces
	// these two fields anymore. Left declared (rather than deleted) because
	// icons_test.go still exercises them as part of the resolved tier's data
	// (TestResolveIconSet_DefaultsToUnicode/_ASCII).
	ExpandOpen   string
	ExpandClosed string
	TreeMid      string // kindPrefix, non-last RowTab/RowPane
	TreeLast     string // kindPrefix, last RowTab/RowPane
	TreeVertical string // continuation from an ancestor tree level

	// TabIcon prefixes a RowTab's own primary text (placed right after
	// kindPrefix's tree glyph/ancestor column, before the label), the same
	// slot a RowCandidate's per-source icon or a RowPane's agent-status icon
	// occupies — so a synthesized tab row is visually distinguishable from a
	// top-level workspace/candidate row at a glance, not just by its tree
	// glyph.
	TabIcon string

	// SearchPrompt prefixes the query on the prompt row (❯ for Unicode, >
	// for ASCII) — the same chevron the list cursor uses, so "this is where
	// you are" reads identically on both.
	SearchPrompt string

	// RuleHorizontal, RuleVertical and RuleJunction draw the borderless
	// grid: the rule under the prompt row, the divider between the list and
	// preview columns, and the junction where the two cross.
	RuleHorizontal string
	RuleVertical   string
	RuleJunction   string

	// HintSeparator joins footer hints ("enter open · tab agents").
	HintSeparator string

	// Overflow marks tab strip tabs hidden off either edge.
	Overflow string

	// ScrollThumb draws the list's scroll position over the divider (the
	// track is RuleVertical) when the rows do not fit.
	ScrollThumb string

	// Pinned and Group are the pin and group live markers: a pinned
	// candidate, and a group workspace that opens a nested picker.
	Pinned string
	Group  string
}

// iconSets holds every documented tier. IconsUnicode is the default tier
// (an unset [tui].icons). IconsASCII is 7-bit ASCII only, for
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
		TreeMid:       "├─",
		TreeLast:      "└─",
		TreeVertical:  "│ ",
		TabIcon:       "◫",
		SearchPrompt:  "❯",

		RuleHorizontal: "─",
		RuleVertical:   "│",
		RuleJunction:   "┼",
		HintSeparator:  "·",
		Overflow:       "…",
		ScrollThumb:    "┃",
		Pinned:         "★",
		Group:          "›",
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
		TreeMid:       "|-",
		TreeLast:      "`-",
		TreeVertical:  "| ",
		TabIcon:       "t",
		SearchPrompt:  ">",

		RuleHorizontal: "-",
		RuleVertical:   "|",
		RuleJunction:   "+",
		HintSeparator:  "-",
		Overflow:       "...",
		ScrollThumb:    "#",
		Pinned:         "*",
		Group:          ">",
	},
}

// resolveIconSetName applies the documented default: empty or unrecognized
// configIcons resolves to IconsUnicode — the picker's original hardcoded
// glyphs — so resolution never fails to start the picker over a typo'd
// icons name.
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
