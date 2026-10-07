package theme

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

type colorKind uint8

const (
	kindReset colorKind = iota
	kindRGB
	kindANSI
)

// Color is one palette or role color: a 24-bit RGB value, one of the 16
// named terminal colors Herdr accepts, or Reset (the terminal's own default
// foreground or background). The zero Color is Reset. Colors are comparable
// with ==.
type Color struct {
	kind    colorKind
	r, g, b uint8 // kindRGB
	ansi    uint8 // kindANSI: standard terminal color index 0-15
}

// RGB returns a 24-bit color.
func RGB(r, g, b uint8) Color {
	return Color{kind: kindRGB, r: r, g: g, b: b}
}

// ansiNames are the canonical spellings of the 16 named terminal colors,
// indexed by their standard ANSI color number. The mapping follows ratatui,
// which Herdr renders with: "gray" is ANSI 7 (normal white), "darkgray" is
// ANSI 8 (bright black) and "white" is ANSI 15 (bright white).
var ansiNames = [16]string{
	"black", "red", "green", "yellow", "blue", "magenta", "cyan", "gray",
	"darkgray", "lightred", "lightgreen", "lightyellow", "lightblue", "lightmagenta", "lightcyan", "white",
}

// namedColors is Herdr's parse_color name list (lower case), aliases
// included, mapped to ANSI color numbers.
var namedColors = map[string]uint8{
	"black":        0,
	"red":          1,
	"green":        2,
	"yellow":       3,
	"blue":         4,
	"magenta":      5,
	"purple":       5,
	"cyan":         6,
	"gray":         7,
	"grey":         7,
	"darkgray":     8,
	"darkgrey":     8,
	"lightred":     9,
	"lightgreen":   10,
	"lightyellow":  11,
	"lightblue":    12,
	"lightmagenta": 13,
	"lightcyan":    14,
	"white":        15,
}

// ParseColor parses a color written in Herdr's syntax (Herdr's parse_color):
// "#rrggbb", "#rgb", "rgb(r,g,b)" with decimal 0-255 components, a named
// terminal color (black, red, green, yellow, blue, magenta or purple, cyan,
// white, gray or grey, darkgray or darkgrey, lightred, lightgreen,
// lightyellow, lightblue, lightmagenta, lightcyan) or a reset alias (reset,
// default, none, transparent). Surrounding spaces and letter case are
// ignored.
//
// Herdr itself silently renders an unparsable color as cyan; shep reports
// it instead (see HerdrTheme for the Herdr-compatible lenient reading).
func ParseColor(s string) (Color, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "reset", "default", "none", "transparent":
		return Color{}, nil
	}
	if hex, ok := strings.CutPrefix(v, "#"); ok {
		if c, ok := parseHex(hex); ok {
			return c, nil
		}
	} else if inner, ok := strings.CutPrefix(v, "rgb("); ok {
		if inner, ok := strings.CutSuffix(inner, ")"); ok {
			if c, ok := parseRGBTriple(inner); ok {
				return c, nil
			}
		}
	} else if idx, ok := namedColors[v]; ok {
		return Color{kind: kindANSI, ansi: idx}, nil
	}
	return Color{}, fmt.Errorf("invalid color %q: want #rrggbb, #rgb, rgb(r,g,b), a named color or reset", s)
}

// parseHex parses the digits after "#": six digits are rrggbb, three are
// rgb with each digit doubled (#abc = #aabbcc).
func parseHex(hex string) (Color, bool) {
	switch len(hex) {
	case 6:
		var c [3]uint8
		for i := range c {
			n, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
			if err != nil {
				return Color{}, false
			}
			c[i] = uint8(n)
		}
		return RGB(c[0], c[1], c[2]), true
	case 3:
		var c [3]uint8
		for i := range c {
			n, err := strconv.ParseUint(hex[i:i+1], 16, 8)
			if err != nil {
				return Color{}, false
			}
			c[i] = uint8(n) * 17
		}
		return RGB(c[0], c[1], c[2]), true
	}
	return Color{}, false
}

// parseRGBTriple parses "r,g,b" (spaces around each component allowed).
func parseRGBTriple(inner string) (Color, bool) {
	parts := strings.Split(inner, ",")
	if len(parts) != 3 {
		return Color{}, false
	}
	var c [3]uint8
	for i, p := range parts {
		n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 8)
		if err != nil {
			return Color{}, false
		}
		c[i] = uint8(n)
	}
	return RGB(c[0], c[1], c[2]), true
}

// IsReset reports whether c is Reset, the terminal's default color.
func (c Color) IsReset() bool { return c.kind == kindReset }

// String returns c in a form ParseColor accepts: "#rrggbb", the canonical
// name of a terminal color, or "reset".
func (c Color) String() string {
	switch c.kind {
	case kindRGB:
		return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b)
	case kindANSI:
		return ansiNames[c.ansi&0x0f]
	default:
		return "reset"
	}
}

// Lipgloss converts c for lipgloss: an RGB color stays true color (Bubble Tea
// degrades it to the terminal's color profile), a named color becomes the
// lipgloss.ANSIColor with its standard 0-15 index, and Reset becomes
// lipgloss.NoColor{} so the terminal default shows through.
func (c Color) Lipgloss() color.Color {
	switch c.kind {
	case kindRGB:
		return lipgloss.Color(c.String())
	case kindANSI:
		return lipgloss.ANSIColor(c.ansi)
	default:
		return lipgloss.NoColor{}
	}
}
