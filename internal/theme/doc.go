// Package theme resolves shep's colors from Herdr-compatible themes.
//
// A theme is a palette of Herdr's 19 tokens (see Token) plus shep's semantic
// roles (see Role), each of which maps to a token, another role or a color.
// The built-in themes are Herdr v0.9.3's 18 palettes with Herdr's names and
// aliases, catppuccin-frappe and catppuccin-macchiato (Herdr's Catppuccin
// Mocha token mapping applied to the other two dark flavors), and plain (no
// color). Colors use Herdr's syntax (see ParseColor).
//
// "inherit", the default, reproduces the user's Herdr theme with Herdr's own
// algorithm, including [theme.custom], auto_switch and the per-appearance
// overrides (see HerdrTheme.Resolve). Custom themes ([themes.<name>], see
// Custom and Build) extend a built-in theme, inherit or another custom theme
// and override tokens and roles. Select applies the NO_COLOR, SHEP_THEME and
// [tui].theme precedence.
//
// # Role defaults
//
//	Role               Token          Meaning
//	text               text           ordinary text
//	text.secondary     subtext0       paths, metadata, hint labels
//	text.muted         overlay0       counts, unknown values
//	accent             accent         generic focus/highlight
//	rule               surface1       rules, dividers, separators
//	selection          selection_bg   cursor row background
//	tab.active         selection_bg   active tab background
//	tab.active.fg      accent         active tab foreground
//	prompt             accent         query prompt
//	cursor             accent         cursor gutter, query cursor
//	match              accent         search match highlights
//	heading            accent         section and help headings
//	row.label          text           row name (label_format)
//	row.detail         overlay0       row context (detail_format)
//	row.marker         overlay0       right-aligned row text (marker_format)
//	row.descendant     overlay0       rows nested under a group
//	status.working     yellow         agent working
//	status.blocked     red            agent blocked
//	status.done        teal           agent done
//	status.idle        green          agent idle
//	status.unknown     overlay0       agent state unknown
//	source.herdr       green          open Herdr workspace icons
//	source.workspaces  mauve          configured workspace icons
//	source.zoxide      blue           zoxide folder icons
//	source.projects    peach          project and worktree icons
//	source.sessions    yellow         session icons
//	source.agents      accent         agent row icons
//	source.custom      teal           custom source icons
//	pin                yellow         pin marker
//	git.branch         mauve          branch names
//	git.clean          green          clean working tree
//	git.changes        yellow         working tree with changes
//	error              red            errors
//	warning            yellow         warnings, confirmations
//	success            green          success messages
//
// # References
//
// A role value, and any configuration color such as icon_color, is a
// reference: a token name, a role name or a color literal, looked up in that
// order. "text" and "accent" are both token and role names and mean the
// tokens; "red", "green", "yellow" and "blue" mean the palette tokens, not
// the terminal colors of the same names (set the token itself to "red" to
// get the terminal's red). Role-to-role references are followed with cycle
// detection when a theme is built.
//
// # No color
//
// The plain theme, and any theme under NO_COLOR, has NoColor set: every role
// and reference resolves to Reset, and the TUI renders roles with text
// attributes (bold, faint, reverse) instead of colors.
package theme
