package theme

import (
	"fmt"
	"strings"
)

// Token names one of the 19 colors of a Herdr palette. The constants follow
// the field order of Herdr's Palette struct, and each comment gives the
// meaning Herdr documents for the field (shep assigns its own uses through
// roles, see Role).
type Token uint8

const (
	// TokenAccent is the primary accent (highlight, active borders).
	TokenAccent Token = iota
	// TokenPanelBg is the background for the tab bar, floating panels,
	// overlays, and modals.
	TokenPanelBg
	// TokenSidebarBg is the optional desktop sidebar background. Reset
	// preserves the terminal background.
	TokenSidebarBg
	// TokenActiveRowBg is the background for the active workspace and
	// focused agent rows.
	TokenActiveRowBg
	// TokenSelectionBg is the background for the Navigate-mode cursor row in
	// the sidebar.
	TokenSelectionBg
	// TokenSurface0 is a subtle surface background for selected/focused
	// items.
	TokenSurface0
	// TokenSurface1 is a slightly lighter surface for hover/active states.
	TokenSurface1
	// TokenSurfaceDim is a very dim surface for separators.
	TokenSurfaceDim
	// TokenOverlay0 is muted text (secondary info, numbers).
	TokenOverlay0
	// TokenOverlay1 is slightly brighter overlay text.
	TokenOverlay1
	// TokenText is the main text color.
	TokenText
	// TokenSubtext0 is subdued text (workspace numbers, dim labels).
	TokenSubtext0
	// TokenMauve is the branch name / special label color.
	TokenMauve
	// TokenGreen is the done / idle state color.
	TokenGreen
	// TokenYellow is the working / running state color.
	TokenYellow
	// TokenRed is the needs attention / blocked state color.
	TokenRed
	// TokenBlue is the unseen / done notification accent.
	TokenBlue
	// TokenTeal is the notification accent / unseen markers color.
	TokenTeal
	// TokenPeach is the interrupted / warning state color.
	TokenPeach

	// numTokens is the number of palette tokens.
	numTokens = iota
)

// tokenNames are the token names as written in Herdr's [theme.custom] and
// shep's [themes.<name>] tables.
var tokenNames = [numTokens]string{
	TokenAccent:      "accent",
	TokenPanelBg:     "panel_bg",
	TokenSidebarBg:   "sidebar_bg",
	TokenActiveRowBg: "active_row_bg",
	TokenSelectionBg: "selection_bg",
	TokenSurface0:    "surface0",
	TokenSurface1:    "surface1",
	TokenSurfaceDim:  "surface_dim",
	TokenOverlay0:    "overlay0",
	TokenOverlay1:    "overlay1",
	TokenText:        "text",
	TokenSubtext0:    "subtext0",
	TokenMauve:       "mauve",
	TokenGreen:       "green",
	TokenYellow:      "yellow",
	TokenRed:         "red",
	TokenBlue:        "blue",
	TokenTeal:        "teal",
	TokenPeach:       "peach",
}

// String returns the token's configuration name, e.g. "panel_bg".
func (t Token) String() string {
	if int(t) < numTokens {
		return tokenNames[t]
	}
	return fmt.Sprintf("Token(%d)", uint8(t))
}

// ParseToken returns the token with the given configuration name. Names are
// matched exactly, like TOML keys.
func ParseToken(name string) (Token, bool) {
	for i, n := range tokenNames {
		if n == name {
			return Token(i), true
		}
	}
	return 0, false
}

// Tokens returns every token in Herdr's order.
func Tokens() []Token {
	out := make([]Token, numTokens)
	for i := range out {
		out[i] = Token(i)
	}
	return out
}

// tokenList is the comma-separated token names, for error messages.
func tokenList() string { return strings.Join(tokenNames[:], ", ") }

// Palette holds one color per Token. The zero Palette is all Reset.
type Palette struct {
	colors [numTokens]Color
}

// Get returns the color of token t (Reset for an out-of-range token).
func (p Palette) Get(t Token) Color {
	if int(t) >= numTokens {
		return Color{}
	}
	return p.colors[t]
}

// With returns a copy of p with token t set to c (p unchanged for an
// out-of-range token).
func (p Palette) With(t Token, c Color) Palette {
	if int(t) < numTokens {
		p.colors[t] = c
	}
	return p
}

// Names accepted by Build and Select besides the built-in palettes.
const (
	// NameInherit selects Herdr's own active theme (see HerdrTheme).
	NameInherit = "inherit"
	// NamePlain selects the no-color theme.
	NamePlain = "plain"
	// NameDefault is the base of a custom theme that sets no base, and
	// Herdr's own default theme.
	NameDefault = "catppuccin"
)

// builtinSpec is one built-in palette as written in palettes.go.
type builtinSpec struct {
	name   string
	colors [numTokens]string
}

// builtinPalettes maps each canonical built-in name to its parsed palette.
var builtinPalettes = func() map[string]Palette {
	out := make(map[string]Palette, len(builtinSpecs))
	for _, spec := range builtinSpecs {
		var p Palette
		for i, s := range spec.colors {
			c, err := ParseColor(s)
			if err != nil {
				panic(fmt.Sprintf("theme: built-in %s.%s: %v", spec.name, Token(i), err))
			}
			p.colors[i] = c
		}
		out[spec.name] = p
	}
	return out
}()

// BuiltinNames returns the canonical names of the built-in themes: Herdr's 18
// palettes in Herdr's order, catppuccin-frappe, catppuccin-macchiato and
// plain.
func BuiltinNames() []string {
	out := make([]string, 0, len(builtinSpecs)+1)
	for _, spec := range builtinSpecs {
		out = append(out, spec.name)
	}
	return append(out, NamePlain)
}

// normalizeName applies Herdr's theme-name normalization: lower case, with
// spaces and underscores turned into hyphens.
func normalizeName(name string) string {
	return strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(name))
}

// herdrCanonicalName is Herdr's canonical_theme_name: it maps a theme name
// or alias to one of Herdr's 18 built-in names. It deliberately knows none of
// shep's additions, so inheriting a Herdr config resolves exactly as Herdr
// does (Herdr renders "frappe" as its catppuccin fallback, and so does
// inherit).
func herdrCanonicalName(name string) (string, bool) {
	switch normalizeName(name) {
	case "catppuccin", "catppuccin-mocha":
		return "catppuccin", true
	case "catppuccin-latte", "latte", "light":
		return "catppuccin-latte", true
	case "terminal":
		return "terminal", true
	case "tokyo-night", "tokyonight":
		return "tokyo-night", true
	case "tokyo-night-day", "tokyo-day", "tokyonight-day":
		return "tokyo-night-day", true
	case "dracula":
		return "dracula", true
	case "nord":
		return "nord", true
	case "gruvbox", "gruvbox-dark":
		return "gruvbox", true
	case "gruvbox-light":
		return "gruvbox-light", true
	case "one-dark", "onedark":
		return "one-dark", true
	case "one-light", "onelight":
		return "one-light", true
	case "solarized", "solarized-dark":
		return "solarized", true
	case "solarized-light":
		return "solarized-light", true
	case "kanagawa":
		return "kanagawa", true
	case "kanagawa-lotus", "lotus":
		return "kanagawa-lotus", true
	case "rose-pine", "rosepine":
		return "rose-pine", true
	case "rose-pine-dawn", "rosepine-dawn", "dawn":
		return "rose-pine-dawn", true
	case "vesper":
		return "vesper", true
	}
	return "", false
}

// CanonicalName maps a built-in theme name or alias to its canonical name:
// Herdr's names and aliases (normalized like Herdr: case-insensitive, spaces
// and underscores read as hyphens), plus shep's catppuccin-frappe (alias
// frappe), catppuccin-macchiato (alias macchiato), the alias mocha for
// catppuccin, and plain. "inherit" and custom theme names are not built-in
// names.
func CanonicalName(name string) (string, bool) {
	if c, ok := herdrCanonicalName(name); ok {
		return c, true
	}
	switch normalizeName(name) {
	case "mocha":
		return "catppuccin", true
	case "catppuccin-frappe", "frappe":
		return "catppuccin-frappe", true
	case "catppuccin-macchiato", "macchiato":
		return "catppuccin-macchiato", true
	case NamePlain:
		return NamePlain, true
	}
	return "", false
}
