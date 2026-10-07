package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/theme"
)

// TestNewPalette_StylesAreThemeRoles proves every style the picker renders
// with is a semantic role of its theme: its foreground (and, for surfaces,
// background) is exactly the role's color.
func TestNewPalette_StylesAreThemeRoles(t *testing.T) {
	t.Parallel()
	th := testTheme(ThemeMocha)
	s := newPalette(th, nil)
	color := func(r theme.Role) lipgloss.TerminalColor { return th.Role(r).Lipgloss() }
	for _, tc := range []struct {
		name  string
		style lipgloss.Style
		role  theme.Role
	}{
		{"query match", s.queryStyle, theme.RoleMatch},
		{"text", s.rowStyle, theme.RoleText},
		{"muted", s.mutedStyle, theme.RoleTextMuted},
		{"secondary", s.secondaryStyle, theme.RoleTextSecondary},
		{"accent", s.accentStyle, theme.RoleAccent},
		{"error", s.previewErrStyle, theme.RoleError},
		{"rule", s.ruleStyle, theme.RoleRule},
		{"status idle", s.statusIdleStyle, theme.RoleStatusIdle},
		{"status working", s.statusWorkingStyle, theme.RoleStatusWorking},
		{"status blocked", s.statusBlockedStyle, theme.RoleStatusBlocked},
		{"status done", s.statusDoneStyle, theme.RoleStatusDone},
		{"status unknown", s.statusUnknownStyle, theme.RoleStatusUnknown},
		{"heading", s.previewHeadingStyle, theme.RoleHeading},
		{"cursor gutter", s.cursorGutterStyle, theme.RoleCursor},
		{"row label", s.rowLabelStyle, theme.RoleRowLabel},
		{"row detail", s.rowDetailStyle, theme.RoleRowDetail},
		{"row marker", s.rowMarkerStyle, theme.RoleRowMarker},
		{"row descendant", s.rowDescendantStyle, theme.RoleRowDescendant},
		{"pin", s.pinStyle, theme.RolePin},
		{"prompt", s.promptStyle, theme.RolePrompt},
		{"warning", s.warnStyle, theme.RoleWarning},
		{"success", s.successStyle, theme.RoleSuccess},
		{"git branch", s.gitBranchStyle, theme.RoleGitBranch},
		{"git clean", s.gitCleanStyle, theme.RoleGitClean},
		{"git changes", s.gitChangesStyle, theme.RoleGitChanges},
		{"active tab", s.tabActiveStyle, theme.RoleTabActiveFg},
	} {
		if got, want := tc.style.GetForeground(), color(tc.role); got != want {
			t.Errorf("%s foreground = %v, want role %s (%v)", tc.name, got, tc.role, want)
		}
	}
	for _, tc := range []struct {
		name  string
		style lipgloss.Style
		role  theme.Role
	}{
		{"selection surface", s.cursorSurfaceStyle, theme.RoleSelection},
		{"active tab", s.tabActiveStyle, theme.RoleTabActive},
		{"query cursor", s.queryCursorStyle, theme.RoleCursor},
	} {
		if got, want := tc.style.GetBackground(), color(tc.role); got != want {
			t.Errorf("%s background = %v, want role %s (%v)", tc.name, got, tc.role, want)
		}
	}
	if s.cursorGutterStyle.GetBackground() != (lipgloss.NoColor{}) {
		t.Error("the cursor gutter must not fill a background behind its chevron")
	}
	if !s.rowSelected.label.GetBold() || s.rowPlain.label.GetBold() {
		t.Error("only the selected row's label is bold")
	}
	if s.rowSelected.detail.GetBackground() != color(theme.RoleSelection) {
		t.Error("the selected row's detail does not carry the selection surface")
	}
	if !s.rowDescendantStyle.GetItalic() {
		t.Error("descendant rows lost their italic")
	}
}

// TestNewPalette_PlainKeepsStructuralAttributes proves the no-color theme
// never sets a color and keeps the structural attributes the picker reads
// by: bold key tokens and selection, faint secondary text and rules,
// reverse video for the active tab and the prompt cursor.
func TestNewPalette_PlainKeepsStructuralAttributes(t *testing.T) {
	t.Parallel()
	th := testTheme(ThemePlain)
	if !th.NoColor {
		t.Fatal("setup: plain is not a no-color theme")
	}
	s := newPalette(th, []string{"source.zoxide", "text.muted", "blue"})
	all := []lipgloss.Style{
		s.queryStyle, s.rowStyle, s.mutedStyle, s.secondaryStyle, s.accentStyle, s.previewLoadingStyle,
		s.previewErrStyle, s.ruleStyle, s.statusIdleStyle, s.statusWorkingStyle, s.statusBlockedStyle,
		s.statusDoneStyle, s.statusUnknownStyle, s.previewHeadingStyle, s.cursorGutterStyle, s.cursorSurfaceStyle,
		s.rowLabelStyle, s.rowDetailStyle, s.rowMarkerStyle, s.rowDescendantStyle, s.pinStyle, s.keycapStyle,
		s.keycapLabelStyle, s.tabActiveStyle, s.tabActiveBlockedStyle, s.promptStyle, s.queryTextStyle,
		s.queryCursorStyle, s.placeholderStyle, s.titleStyle, s.warnStyle, s.successStyle,
		s.gitBranchStyle, s.gitCleanStyle, s.gitChangesStyle,
	}
	all = append(all, s.rowPlain.icons...)
	for i, style := range all {
		if style.GetForeground() != (lipgloss.NoColor{}) || style.GetBackground() != (lipgloss.NoColor{}) {
			t.Errorf("plain style %d sets a color", i)
		}
	}
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"match bold", s.queryStyle.GetBold()},
		{"muted faint", s.mutedStyle.GetFaint()},
		{"rule faint", s.ruleStyle.GetFaint()},
		{"detail faint", s.rowDetailStyle.GetFaint()},
		{"marker faint", s.rowMarkerStyle.GetFaint()},
		{"gutter reverse bold", s.cursorGutterStyle.GetReverse() && s.cursorGutterStyle.GetBold()},
		{"active tab reverse", s.tabActiveStyle.GetReverse()},
		{"query cursor reverse", s.queryCursorStyle.GetReverse()},
		{"error bold underline", s.previewErrStyle.GetBold() && s.previewErrStyle.GetUnderline()},
		{"selected label bold", s.rowSelected.label.GetBold()},
		{"muted role icon faint", s.rowPlain.icons[1].GetFaint()},
		{"source role icon unstyled", !s.rowPlain.icons[0].GetFaint() && !s.rowPlain.icons[0].GetBold()},
	} {
		if !tc.ok {
			t.Errorf("plain: %s does not hold", tc.name)
		}
	}
	if s.rowSelected.hasSurface {
		t.Error("plain selection must not paint a surface")
	}
}

// TestIconStyle_TokenRoleOrColor proves icon_color resolves like every
// color reference: a palette token, a role, or a literal color.
func TestIconStyle_TokenRoleOrColor(t *testing.T) {
	t.Parallel()
	th := testTheme(ThemeMocha)
	for _, tc := range []struct {
		ref  string
		want lipgloss.TerminalColor
	}{
		{"blue", th.Palette().Get(theme.TokenBlue).Lipgloss()},
		{"source.projects", th.Role(theme.RoleSourceProjects).Lipgloss()},
		{"#123456", lipgloss.Color("#123456")},
		{"rgb(1,2,3)", lipgloss.Color("#010203")},
	} {
		if got := iconStyle(th, tc.ref).GetForeground(); got != tc.want {
			t.Errorf("icon_color %q = %v, want %v", tc.ref, got, tc.want)
		}
	}
	if got := iconStyle(th, "not-a-color").GetForeground(); got != (lipgloss.NoColor{}) {
		t.Errorf("an invalid icon_color is styled: %v", got)
	}
}

// TestDefaultTheme_IsHerdrDefault proves a Layout without a theme renders
// with Herdr's default theme, catppuccin.
func TestDefaultTheme_IsHerdrDefault(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(nil, nil, Layout{})
	if m.theme.Name != theme.NameDefault || m.theme.NoColor {
		t.Fatalf("default theme = %q (no color %v), want %s", m.theme.Name, m.theme.NoColor, theme.NameDefault)
	}
	if got, want := m.styles.rowDetailStyle.GetForeground(), testTheme(ThemeMocha).Role(theme.RoleRowDetail).Lipgloss(); got != want {
		t.Errorf("row.detail = %v, want catppuccin's %v", got, want)
	}
}

// TestStatusStyle_Mapping proves each agent status word selects its style
// and anything else, the literal "unknown" included, the unknown style.
func TestStatusStyle_Mapping(t *testing.T) {
	t.Parallel()
	s := newPalette(testTheme(ThemeMocha), nil)
	for status, want := range map[string]lipgloss.Style{
		"idle": s.statusIdleStyle, "working": s.statusWorkingStyle, "blocked": s.statusBlockedStyle,
		"done": s.statusDoneStyle, "unknown": s.statusUnknownStyle, "": s.statusUnknownStyle, "bogus": s.statusUnknownStyle,
	} {
		if got := s.statusStyle(status); got.GetForeground() != want.GetForeground() || got.GetBold() != want.GetBold() {
			t.Errorf("statusStyle(%q) differs from its style", status)
		}
	}
}

// TestThemeWiring_CustomRoleColorsTheRowDetail proves the whole chain in a
// true-color render: a custom theme's role override reaches the drawn row.
// Not t.Parallel: it swaps lipgloss's global color profile.
func TestThemeWiring_CustomRoleColorsTheRowDetail(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	customs := map[string]theme.Custom{"mine": {Base: "nord", Roles: map[string]string{"row.detail": "#ff0000"}}}
	mine, err := theme.Build("mine", customs, theme.HerdrTheme{}, true)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	row := Row{Kind: RowCandidate, Candidate: zoxideCandidate("~/Proyectos/shep", "/home/dev/Proyectos/shep")}
	render := func(th theme.Theme) string {
		m := NewModelWithLayout(nil, nil, Layout{Theme: th, HomeDir: "/home/dev"})
		return m.renderRowLine(row, false, 60)
	}
	const red = "38;2;255;0;0"
	if got := render(mine); !strings.Contains(got, red+"m~/Proyectos") {
		t.Errorf("custom theme row = %q, want the detail drawn in its row.detail color", got)
	}
	if got := render(testTheme("nord")); strings.Contains(got, red) {
		t.Errorf("base theme row = %q, carries the override", got)
	}
	if got := render(testTheme(ThemePlain)); strings.Contains(got, "38;") || strings.Contains(got, "48;") {
		t.Errorf("plain row = %q, carries a color", got)
	}
}

// TestThemeWiring_PresentationIconColor proves a presentation's icon_color
// colors that row kind's icon, through the shared icon style table.
// Not t.Parallel: it swaps lipgloss's global color profile.
func TestThemeWiring_PresentationIconColor(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	p := config.DefaultPresentations("")
	p.Zoxide.Icon, p.Zoxide.IconColor = "Z", "#00ff00"
	m := NewModelWithLayout(nil, nil, Layout{Theme: testTheme(ThemeMocha), Presentation: &p})
	got := m.renderRowLine(Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "x", Source: config.SourceZoxide}}, false, 30)
	if !strings.Contains(got, "38;2;0;255;0mZ") {
		t.Errorf("row = %q, want the icon in its icon_color", got)
	}
}

// TestStyleWrap_MatchesRender proves an icon written through its
// pre-rendered style is byte-identical to rendering it, plain and selected,
// colored and not. Not t.Parallel: it swaps lipgloss's global color profile.
func TestStyleWrap_MatchesRender(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	for _, name := range []string{ThemeMocha, ThemePlain} {
		s := newPalette(testTheme(name), []string{"source.herdr", "text.muted", "#123456", "nope"})
		for _, st := range []*rowStyles{&s.rowPlain, &s.rowSelected} {
			for i, style := range st.icons {
				var b strings.Builder
				st.iconWraps[i].write(&b, "\U000f0cc6 ")
				if want := style.Render("\U000f0cc6 "); b.String() != want {
					t.Errorf("%s icon %d: wrap = %q, Render = %q", name, i, b.String(), want)
				}
			}
		}
	}
}
