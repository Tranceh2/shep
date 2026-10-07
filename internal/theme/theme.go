package theme

import "strings"

// Theme is a resolved theme: a palette plus the color of every role. Build
// and Select produce themes; the zero Theme renders every role as Reset.
type Theme struct {
	// Name is the selected theme: a canonical built-in name, "plain",
	// "inherit" or a custom theme name.
	Name string
	// NoColor is true for the plain theme (and under NO_COLOR). Role and
	// Resolve then return Reset for everything; the TUI is expected to map
	// roles to text attributes (bold, faint, reverse) instead of colors.
	NoColor bool
	// Source describes where the theme came from, for shep doctor.
	Source Source

	palette Palette
	roles   [numRoles]Color
}

// Palette returns the theme's resolved token palette (all Reset for a
// no-color theme).
func (t Theme) Palette() Palette { return t.palette }

// Role returns the color of role r (Reset for a no-color theme or an
// out-of-range role).
func (t Theme) Role(r Role) Color {
	if t.NoColor || int(r) >= numRoles {
		return Color{}
	}
	return t.roles[r]
}

// Resolve resolves a color reference as written in configuration (for
// example icon_color): a palette token ("blue"), a role ("source.zoxide") or
// a color literal in Herdr's syntax ("#89b4fa"). Tokens are looked up before
// roles and roles before colors (see ValidateRef). A no-color theme returns
// Reset for every valid reference; an invalid reference is an error either
// way.
func (t Theme) Resolve(s string) (Color, error) {
	r, err := parseRef(s)
	if err != nil {
		return Color{}, err
	}
	if t.NoColor {
		return Color{}, nil
	}
	switch r.kind {
	case refToken:
		return t.palette.Get(r.token), nil
	case refRole:
		return t.roles[r.role], nil
	default:
		return r.color, nil
	}
}

// SourceKind is the kind of theme that was selected.
type SourceKind string

// Theme source kinds.
const (
	// SourceBuiltin is a built-in theme, plain included.
	SourceBuiltin SourceKind = "builtin"
	// SourceCustom is a [themes.<name>] theme.
	SourceCustom SourceKind = "custom"
	// SourceInherit is Herdr's own active theme.
	SourceInherit SourceKind = "inherit"
	// SourceNoColor is the no-color theme forced by $NO_COLOR.
	SourceNoColor SourceKind = "no-color"
)

// Source describes where a theme came from.
type Source struct {
	Kind SourceKind
	// Setting is what chose the theme: "NO_COLOR", "SHEP_THEME" (the
	// environment), "tui.theme" (the configuration) or "default" (no
	// setting, so inherit). Build leaves it empty; Select fills it.
	Setting string
	// Herdr is the canonical Herdr theme name the palette is based on, when
	// the theme is inherit or a custom theme whose base chain reaches
	// inherit.
	Herdr string
	// HerdrPath is the Herdr configuration file consulted for Herdr.
	HerdrPath string
	// FollowsAppearance reports a theme inheriting a Herdr configuration
	// with auto_switch on: it has a variant for each terminal appearance
	// (see Options.Light).
	FollowsAppearance bool
	// Notes explain ignored settings and fallbacks: an unknown or invalid
	// SHEP_THEME, a missing or unreadable Herdr configuration, Herdr's own
	// diagnostics for its theme settings.
	Notes []string
}

// String summarizes the source, e.g. "inherit:catppuccin (default)",
// "builtin (SHEP_THEME)" or "custom on inherit:nord (tui.theme)". Notes are
// not included.
func (s Source) String() string {
	var b strings.Builder
	b.WriteString(string(s.Kind))
	switch {
	case s.Herdr == "":
	case s.Kind == SourceInherit:
		b.WriteString(":" + s.Herdr)
	default:
		b.WriteString(" on inherit:" + s.Herdr)
	}
	if s.Setting != "" {
		b.WriteString(" (" + s.Setting + ")")
	}
	return b.String()
}
