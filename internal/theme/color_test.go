package theme

import (
	"fmt"
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

func mustColor(t *testing.T, s string) Color {
	t.Helper()
	c, err := ParseColor(s)
	if err != nil {
		t.Fatalf("ParseColor(%q): %v", s, err)
	}
	return c
}

func ansi(i uint8) Color { return Color{kind: kindANSI, ansi: i} }

func TestParseColor_AcceptsHerdrSyntax(t *testing.T) {
	tests := []struct {
		in   string
		want Color
	}{
		{"#89b4fa", RGB(0x89, 0xb4, 0xfa)},
		{"#89B4FA", RGB(0x89, 0xb4, 0xfa)},
		{"  #89b4fa\t", RGB(0x89, 0xb4, 0xfa)},
		{"#000000", RGB(0, 0, 0)},
		{"#fff", RGB(255, 255, 255)},
		{"#abc", RGB(0xaa, 0xbb, 0xcc)},
		{"#0F8", RGB(0x00, 0xff, 0x88)},
		{"rgb(137,180,250)", RGB(137, 180, 250)},
		{"rgb( 255 , 85 , 85 )", RGB(255, 85, 85)},
		{"RGB(0,0,0)", RGB(0, 0, 0)},
		{"rgb(007,8,255)", RGB(7, 8, 255)},
		{"black", ansi(0)},
		{"red", ansi(1)},
		{"green", ansi(2)},
		{"yellow", ansi(3)},
		{"blue", ansi(4)},
		{"magenta", ansi(5)},
		{"purple", ansi(5)},
		{"cyan", ansi(6)},
		{"gray", ansi(7)},
		{"grey", ansi(7)},
		{"darkgray", ansi(8)},
		{"darkgrey", ansi(8)},
		{"lightred", ansi(9)},
		{"lightgreen", ansi(10)},
		{"lightyellow", ansi(11)},
		{"lightblue", ansi(12)},
		{"lightmagenta", ansi(13)},
		{"lightcyan", ansi(14)},
		{"white", ansi(15)},
		{"Magenta", ansi(5)},
		{" DarkGrey ", ansi(8)},
		{"reset", Color{}},
		{"default", Color{}},
		{"none", Color{}},
		{"transparent", Color{}},
		{"RESET", Color{}},
	}
	for _, tt := range tests {
		got, err := ParseColor(tt.in)
		if err != nil {
			t.Errorf("ParseColor(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseColor(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseColor_RejectsWithActionableError(t *testing.T) {
	for _, in := range []string{
		"", "   ", "#", "#12", "#1234", "#12345", "#1234567", "#ggg", "#gggggg", "#+f+f+f",
		"rgb(1,2)", "rgb(1,2,3,4)", "rgb(256,0,0)", "rgb(-1,0,0)", "rgb(+1,2,3)", "rgb(1,2,3",
		"rgb 1,2,3", "orange", "lightwhite", "bright-red", "89b4fa", "accent",
	} {
		_, err := ParseColor(in)
		if err == nil {
			t.Errorf("ParseColor(%q) succeeded, want error", in)
			continue
		}
		want := fmt.Sprintf("invalid color %q: want #rrggbb, #rgb, rgb(r,g,b), a named color or reset", in)
		if err.Error() != want {
			t.Errorf("ParseColor(%q) error = %q, want %q", in, err, want)
		}
	}
}

func TestColor_StringRoundTrips(t *testing.T) {
	colors := []Color{{}, RGB(1, 2, 3), RGB(0x89, 0xb4, 0xfa)}
	for i := range uint8(16) {
		colors = append(colors, ansi(i))
	}
	for _, c := range colors {
		got := mustColor(t, c.String())
		if got != c {
			t.Errorf("ParseColor(%q) = %v, want %v", c.String(), got, c)
		}
	}
	if got := RGB(0x89, 0xb4, 0xfa).String(); got != "#89b4fa" {
		t.Errorf("String = %q, want #89b4fa", got)
	}
	if got := mustColor(t, "grey").String(); got != "gray" {
		t.Errorf("alias String = %q, want canonical gray", got)
	}
	if !(Color{}).IsReset() || RGB(0, 0, 0).IsReset() || ansi(0).IsReset() {
		t.Error("IsReset is true only for Reset")
	}
}

func TestColor_Lipgloss(t *testing.T) {
	tests := []struct {
		in   string
		want color.Color
	}{
		{"#89b4fa", lipgloss.Color("#89b4fa")},
		{"#ABC", lipgloss.Color("#aabbcc")},
		{"rgb(1,2,3)", lipgloss.Color("#010203")},
		{"black", lipgloss.ANSIColor(0)},
		{"blue", lipgloss.ANSIColor(4)},
		{"gray", lipgloss.ANSIColor(7)},
		{"darkgray", lipgloss.ANSIColor(8)},
		{"lightred", lipgloss.ANSIColor(9)},
		{"white", lipgloss.ANSIColor(15)},
		{"reset", lipgloss.NoColor{}},
	}
	for _, tt := range tests {
		got := mustColor(t, tt.in).Lipgloss()
		if got != tt.want {
			t.Errorf("%q.Lipgloss() = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}
