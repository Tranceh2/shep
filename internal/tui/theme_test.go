package tui

import (
	"os"
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// withEnv temporarily overrides envLookup for the duration of the test.
func withEnv(t *testing.T, vals map[string]string) {
	t.Helper()
	orig := envLookup
	envLookup = func(key string) string { return vals[key] }
	t.Cleanup(func() { envLookup = orig })
}

// TestResolveThemeName_Precedence proves $NO_COLOR always wins, then
// $SHEP_THEME, then the config-supplied name, then "mocha".
//
// Deliberately NOT t.Parallel(): envLookup is a package-level seam
// (theme.go) that every Model construction reads via resolveTheme. This
// test (via withEnv) temporarily overwrites it, which would race against
// any OTHER parallel test constructing a Model concurrently. Running
// serially guarantees no parallel test's NewModel call is executing while
// envLookup is swapped out from under it — see also
// TestResolveTheme_PlainIsNoColor and TestAllThemeNamesResolve below, which
// share the same constraint.
func TestResolveThemeName_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		cfgTheme   string
		herdrTheme string
		wantTheme  string
	}{
		{"no env, no config: mocha default", nil, "", "", ThemeMocha},
		{"config only", nil, ThemeLatte, "", ThemeLatte},
		{"unknown config falls back to mocha", nil, "bogus", "", ThemeMocha},
		{"SHEP_THEME overrides config", map[string]string{"SHEP_THEME": ThemeFrappe}, ThemeLatte, "", ThemeFrappe},
		{"unknown SHEP_THEME falls back to config", map[string]string{"SHEP_THEME": "bogus"}, ThemeLatte, "", ThemeLatte},
		{"NO_COLOR wins over SHEP_THEME and config", map[string]string{"NO_COLOR": "1", "SHEP_THEME": ThemeMacchiato}, ThemeLatte, "", ThemePlain},
		{"NO_COLOR wins even with empty config", map[string]string{"NO_COLOR": "true"}, "", "", ThemePlain},
		// Herdr theme inheritance (Level 4 precedence)
		{"empty config inherits recognized Herdr theme", nil, "", ThemeLatte, ThemeLatte},
		{"inherit config inherits recognized Herdr theme", nil, "inherit", ThemeLatte, ThemeLatte},
		{"NO_COLOR wins over Herdr inheritance", map[string]string{"NO_COLOR": "1"}, "", ThemeLatte, ThemePlain},
		{"explicit config wins over Herdr inheritance", nil, ThemeFrappe, ThemeLatte, ThemeFrappe},
		{"SHEP_THEME wins over Herdr inheritance", map[string]string{"SHEP_THEME": ThemeMacchiato}, "", ThemeLatte, ThemeMacchiato},
		{"unrecognized SHEP_THEME and unrecognized Herdr theme falls through to mocha", map[string]string{"SHEP_THEME": "bogus"}, "", "bogus", ThemeMocha},
		{"inherit config with flavourless Herdr catppuccin falls through to mocha per #7144", nil, "inherit", "catppuccin", ThemeMocha},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withEnv(t, tt.env)
			if tt.herdrTheme != "" {
				tmp := t.TempDir()
				cfgPath := tmp + "/config.toml"
				_ = os.WriteFile(cfgPath, []byte("[theme]\nname = \""+tt.herdrTheme+"\"\n"), 0o600)
				withHerdrConfigPath(t, func() (string, error) { return cfgPath, nil })
			} else {
				withHerdrConfigPath(t, func() (string, error) { return "", os.ErrNotExist })
			}
			if got := resolveThemeName(tt.cfgTheme); got != tt.wantTheme {
				t.Errorf("resolveThemeName(%q) with env %v, herdr %q = %q, want %q", tt.cfgTheme, tt.env, tt.herdrTheme, got, tt.wantTheme)
			}
		})
	}
}

// TestResolveTheme_PlainIsNoColor proves the "plain" theme carries no color
// fields at all, AND that no style in its styleSet ever sets a Foreground or
// Background — so a full View() render under the plain theme can never emit a
// color escape sequence (the Phase 2 "zero color escapes" invariant, asserted
// at the style level so it is independent of the headless test color profile).
// Not t.Parallel(): see TestResolveThemeName_Precedence's doc comment.
func TestResolveTheme_PlainIsNoColor(t *testing.T) {
	withEnv(t, map[string]string{"NO_COLOR": "1"})
	theme := resolveTheme("")
	if !theme.NoColor {
		t.Fatalf("resolveTheme with $NO_COLOR set: theme.NoColor = false, want true (theme=%+v)", theme)
	}
	s := newPalette(theme)
	noColor := lipgloss.NoColor{}
	styles := []struct {
		name string
		st   lipgloss.Style
	}{
		{"queryStyle", s.queryStyle},
		{"rowStyle", s.rowStyle},
		{"mutedStyle", s.mutedStyle},
		{"labelStyle", s.labelStyle},
		{"previewLoadingStyle", s.previewLoadingStyle},
		{"previewErrStyle", s.previewErrStyle},
		{"borderStyle", s.borderStyle},
		{"focusedBorderStyle", s.focusedBorderStyle},
		{"statusIdleStyle", s.statusIdleStyle},
		{"statusWorkingStyle", s.statusWorkingStyle},
		{"statusBlockedStyle", s.statusBlockedStyle},
		{"statusDoneStyle", s.statusDoneStyle},
		{"statusUnknownStyle", s.statusUnknownStyle},
		{"previewHeadingStyle", s.previewHeadingStyle},
		{"helpHeadingStyle", s.helpHeadingStyle},
		{"cursorGutterStyle", s.cursorGutterStyle},
		{"cursorSurfaceStyle", s.cursorSurfaceStyle},
		{"cursorGutterUnfocusedStyle", s.cursorGutterUnfocusedStyle},
		{"cursorSurfaceUnfocusedStyle", s.cursorSurfaceUnfocusedStyle},
		{"rowDescendantStyle", s.rowDescendantStyle},
	}
	for _, e := range styles {
		if fg := e.st.GetForeground(); fg != noColor {
			t.Errorf("plain %s foreground = %#v, want no color", e.name, fg)
		}
		if bg := e.st.GetBackground(); bg != noColor {
			t.Errorf("plain %s background = %#v, want no color", e.name, bg)
		}
	}
}

// TestNewPalette_MochaHasColor proves a normal theme DOES set colors, so the
// no-color test above is meaningfully distinguishing behavior, not just
// testing an always-empty style.
func TestNewPalette_MochaHasColor(t *testing.T) {
	t.Parallel()
	styles := newPalette(resolveTheme(ThemeMocha))
	if fg := styles.queryStyle.GetForeground(); fg == nil {
		t.Error("mocha theme queryStyle.GetForeground() unset, want a color")
	}
}

// TestStatusStyle_Mapping proves statusStyle maps each known agent_status
// value to its dedicated style, and any other value (including the
// explicit "unknown" status) falls back to statusUnknownStyle.
func TestStatusStyle_Mapping(t *testing.T) {
	t.Parallel()
	styles := newPalette(resolveTheme(ThemeMocha))
	tests := []struct {
		status string
		want   interface{}
	}{
		{"idle", styles.statusIdleStyle},
		{"working", styles.statusWorkingStyle},
		{"blocked", styles.statusBlockedStyle},
		{"done", styles.statusDoneStyle},
		{"unknown", styles.statusUnknownStyle},
		{"totally-bogus", styles.statusUnknownStyle},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := styles.statusStyle(tt.status)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("statusStyle(%q) = %#v, want %#v", tt.status, got, tt.want)
			}
		})
	}
}

// TestAllThemeNamesResolve proves every documented theme name (config's
// TUIThemeMocha etc.) resolves in this package without falling back. Not
// t.Parallel(): see TestResolveThemeName_Precedence's doc comment.
func TestAllThemeNamesResolve(t *testing.T) {
	withEnv(t, nil)
	for _, name := range []string{ThemeMocha, ThemeMacchiato, ThemeFrappe, ThemeLatte, ThemePlain} {
		if got := resolveThemeName(name); got != name {
			t.Errorf("resolveThemeName(%q) = %q, want unchanged", name, got)
		}
	}
}

// allColorThemes returns the four colored Catppuccin flavors directly from the
// themes map (bypassing resolveTheme/env) so style-role tests stay
// deterministic and t.Parallel-safe regardless of the real process
// environment ($NO_COLOR/$SHEP_THEME never perturbs them).
func allColorThemes() []Theme {
	return []Theme{themes[ThemeMocha], themes[ThemeMacchiato], themes[ThemeFrappe], themes[ThemeLatte]}
}

// TestStyleRoles_PreviewHeading proves previewHeadingStyle is the accent role
// + bold for color variants (the preview pane's "captured pane"/"workspace"/
// "agent status"/"git: " section markers), and bold + underline for the plain
// theme — a structural section marker since no color is available.
func TestStyleRoles_PreviewHeading(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if !s.previewHeadingStyle.GetBold() {
			t.Errorf("%s: previewHeadingStyle not bold", th.Name)
		}
		if s.previewHeadingStyle.GetUnderline() {
			t.Errorf("%s: previewHeadingStyle underlined, want no underline (color variant)", th.Name)
		}
		if fg := s.previewHeadingStyle.GetForeground(); fg != lipgloss.Color(th.Accent) {
			t.Errorf("%s: previewHeadingStyle foreground = %#v, want accent %q", th.Name, fg, th.Accent)
		}
	}
	s := newPalette(themes[ThemePlain])
	if !s.previewHeadingStyle.GetBold() {
		t.Error("plain previewHeadingStyle not bold")
	}
	if !s.previewHeadingStyle.GetUnderline() {
		t.Error("plain previewHeadingStyle not underlined, want bold+underline (structural section marker)")
	}
}

// TestStyleRoles_HelpHeading proves helpHeadingStyle is the text role + bold
// for color variants (the "?" help overlay's "Navigation"/"Preview"/"Herdr"/
// "Layout"/"Session" headings) and bold for the plain theme. It is distinct
// from groupHeader by color (text vs muted) so a help heading never reads as
// a collapsible group header.
func TestStyleRoles_HelpHeading(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if !s.helpHeadingStyle.GetBold() {
			t.Errorf("%s: helpHeadingStyle not bold", th.Name)
		}
		if s.helpHeadingStyle.GetUnderline() {
			t.Errorf("%s: helpHeadingStyle underlined, want no underline", th.Name)
		}
		if fg := s.helpHeadingStyle.GetForeground(); fg != lipgloss.Color(th.Text) {
			t.Errorf("%s: helpHeadingStyle foreground = %#v, want text %q", th.Name, fg, th.Text)
		}
	}
	s := newPalette(themes[ThemePlain])
	if !s.helpHeadingStyle.GetBold() {
		t.Error("plain helpHeadingStyle not bold")
	}
	if s.helpHeadingStyle.GetUnderline() {
		t.Error("plain helpHeadingStyle underlined, want bold only")
	}
}

// TestStyleRoles_HeadingsDistinct proves the two remaining heading roles are
// visually distinct in every color variant (previewHeading=accent,
// helpHeading=text) and in plain (previewHeading=bold+underline,
// helpHeading=bold) — so a preview section heading and a help heading never
// collide. The old third role (groupHeader) was removed along with group
// headers themselves.
func TestStyleRoles_HeadingsDistinct(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		ph := s.previewHeadingStyle.GetForeground()
		hh := s.helpHeadingStyle.GetForeground()
		if ph == hh {
			t.Errorf("%s: previewHeading == helpHeading foreground (%#v), must differ", th.Name, ph)
		}
	}
	// Plain: distinction is by structural attribute, not color.
	s := newPalette(themes[ThemePlain])
	if !s.previewHeadingStyle.GetUnderline() {
		t.Error("plain previewHeading not underlined — its distinction from the other heading")
	}
	if s.helpHeadingStyle.GetFaint() || s.helpHeadingStyle.GetUnderline() {
		t.Error("plain helpHeading must be bold-only — faint/underline belong to the other role")
	}
}

// TestStyleRoles_CursorGutter proves the cursor gutter is a clean glyph-only
// indicator: an accent FOREGROUND (focused) / rule foreground (unfocused) on
// the thin "❯" chevron, with NO background fill. A solid background behind a
// thin chevron glyph looks like a colored block obscuring the marker rather
// than a clean cursor (TRL-2) — the gutter's own background must stay
// transparent (no color escape at all for a colored theme; the row's own
// selection tint is applied separately by cursorSurfaceStyle, not the
// gutter). Plain uses reverse-video (focused) and faint (unfocused) as the
// structural fallbacks — those already carry no explicit Background/
// Foreground field (see TestResolveTheme_PlainIsNoColor) and are unaffected.
func TestStyleRoles_CursorGutter(t *testing.T) {
	t.Parallel()
	noColor := lipgloss.NoColor{}
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if fg := s.cursorGutterStyle.GetForeground(); fg != lipgloss.Color(th.Accent) {
			t.Errorf("%s: cursorGutterStyle foreground = %#v, want accent %q", th.Name, fg, th.Accent)
		}
		if bg := s.cursorGutterStyle.GetBackground(); bg != noColor {
			t.Errorf("%s: cursorGutterStyle background = %#v, want no background (transparent, glyph-only)", th.Name, bg)
		}
		if fg := s.cursorGutterUnfocusedStyle.GetForeground(); fg != lipgloss.Color(th.Rule) {
			t.Errorf("%s: cursorGutterUnfocusedStyle foreground = %#v, want rule %q", th.Name, fg, th.Rule)
		}
		if bg := s.cursorGutterUnfocusedStyle.GetBackground(); bg != noColor {
			t.Errorf("%s: cursorGutterUnfocusedStyle background = %#v, want no background (transparent, glyph-only)", th.Name, bg)
		}
		if s.cursorGutterStyle.GetForeground() == s.cursorGutterUnfocusedStyle.GetForeground() {
			t.Errorf("%s: focused gutter fg == unfocused gutter fg, must differ (accent vs rule)", th.Name)
		}
	}
	s := newPalette(themes[ThemePlain])
	if !s.cursorGutterStyle.GetReverse() {
		t.Error("plain cursorGutterStyle not reverse-video, want reverse (focused fallback)")
	}
	if !s.cursorGutterUnfocusedStyle.GetFaint() {
		t.Error("plain cursorGutterUnfocusedStyle not faint, want faint (unfocused fallback)")
	}
}

// TestStyleRoles_CursorSurface proves the focused selection surface carries a
// selectedSurface background and the unfocused variant an unfocusedSurface
// background; plain uses faint for both (the closest structural attribute to a
// tinted row background).
func TestStyleRoles_CursorSurface(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if bg := s.cursorSurfaceStyle.GetBackground(); bg != lipgloss.Color(th.SelectedSurface) {
			t.Errorf("%s: cursorSurfaceStyle background = %#v, want selectedSurface %q", th.Name, bg, th.SelectedSurface)
		}
		if bg := s.cursorSurfaceUnfocusedStyle.GetBackground(); bg != lipgloss.Color(th.UnfocusedSurface) {
			t.Errorf("%s: cursorSurfaceUnfocusedStyle background = %#v, want unfocusedSurface %q", th.Name, bg, th.UnfocusedSurface)
		}
		if s.cursorSurfaceStyle.GetBackground() == s.cursorSurfaceUnfocusedStyle.GetBackground() {
			t.Errorf("%s: focused surface bg == unfocused surface bg, must differ", th.Name)
		}
	}
	s := newPalette(themes[ThemePlain])
	if !s.cursorSurfaceStyle.GetFaint() {
		t.Error("plain cursorSurfaceStyle not faint, want faint (surface fallback)")
	}
	if !s.cursorSurfaceUnfocusedStyle.GetFaint() {
		t.Error("plain cursorSurfaceUnfocusedStyle not faint, want faint (surface fallback)")
	}
}

// TestStyleRoles_RowDescendant proves rowDescendantStyle is the muted role +
// italic across every color variant (unchanged semantics from the old
// matchDescStyle, renamed to the Phase 2 vocabulary) and italic-only for plain.
func TestStyleRoles_RowDescendant(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if !s.rowDescendantStyle.GetItalic() {
			t.Errorf("%s: rowDescendantStyle not italic", th.Name)
		}
		if fg := s.rowDescendantStyle.GetForeground(); fg != lipgloss.Color(th.Muted) {
			t.Errorf("%s: rowDescendantStyle foreground = %#v, want muted %q", th.Name, fg, th.Muted)
		}
	}
	s := newPalette(themes[ThemePlain])
	if !s.rowDescendantStyle.GetItalic() {
		t.Error("plain rowDescendantStyle not italic")
	}
	noColor := lipgloss.NoColor{}
	if fg := s.rowDescendantStyle.GetForeground(); fg != noColor {
		t.Errorf("plain rowDescendantStyle foreground = %#v, want no color", fg)
	}
}

// TestStatusStyle_Colors proves the corrected agent_status color mapping
// (ground truth: herdr's own src/ui/status.rs agent_icon convention):
// working->warn/yellow+bold (NOT accent/blue), blocked->err/red+bold (NOT
// warn/yellow), done->teal (no bold), idle->success/green, unknown->muted.
// The prior corrective round mistakenly left working on accent and blocked
// on warn; this proves the fix without regressing the bold/no-bold
// structural distinction each status already carried.
func TestStatusStyle_Colors(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if fg := s.statusWorkingStyle.GetForeground(); fg != lipgloss.Color(th.Warn) {
			t.Errorf("%s: working foreground = %#v, want warn %q (not accent)", th.Name, fg, th.Warn)
		}
		if !s.statusWorkingStyle.GetBold() {
			t.Errorf("%s: working not bold", th.Name)
		}
		if fg := s.statusBlockedStyle.GetForeground(); fg != lipgloss.Color(th.Err) {
			t.Errorf("%s: blocked foreground = %#v, want err %q (not warn)", th.Name, fg, th.Err)
		}
		if !s.statusBlockedStyle.GetBold() {
			t.Errorf("%s: blocked not bold", th.Name)
		}
		if fg := s.statusDoneStyle.GetForeground(); fg != lipgloss.Color(th.Teal) {
			t.Errorf("%s: done foreground = %#v, want teal %q", th.Name, fg, th.Teal)
		}
		if s.statusDoneStyle.GetBold() {
			t.Errorf("%s: done bold, want no bold", th.Name)
		}
		if fg := s.statusIdleStyle.GetForeground(); fg != lipgloss.Color(th.Success) {
			t.Errorf("%s: idle foreground = %#v, want success %q", th.Name, fg, th.Success)
		}
		if fg := s.statusUnknownStyle.GetForeground(); fg != lipgloss.Color(th.Muted) {
			t.Errorf("%s: unknown foreground = %#v, want muted %q", th.Name, fg, th.Muted)
		}
	}
}

// --- Visual redesign role tests (every rendered surface's role binding) ---

// TestStyleRoles_RedesignRoleBindings proves the redesigned roles bind
// exactly per the approved direction, in every color flavor:
//
//	queryStyle        = accent + bold  (query text, active focus)
//	keycapStyle       = text + bold    (footer keycap chord token)
//	keycapLabelStyle  = secondary      (footer keycap action labels)
//	rowStyle          = text           (labels/main content — muted is never
//	                                   the primary-label role)
//	labelStyle        = muted          (paths/metadata inside previews)
//	previewRuleStyle  = rule           (the thin heading rule line)
//	pinStyle          = warn           (the pinned marker)
//
// Plain keeps the same structure with structural attributes only.
func TestStyleRoles_RedesignRoleBindings(t *testing.T) {
	t.Parallel()
	for _, th := range allColorThemes() {
		s := newPalette(th)
		if fg := s.queryStyle.GetForeground(); fg != lipgloss.Color(th.Accent) {
			t.Errorf("%s: queryStyle fg = %#v, want accent %q", th.Name, fg, th.Accent)
		}
		if fg := s.keycapStyle.GetForeground(); fg != lipgloss.Color(th.Text) {
			t.Errorf("%s: keycapStyle fg = %#v, want text %q", th.Name, fg, th.Text)
		}
		if !s.keycapStyle.GetBold() {
			t.Errorf("%s: keycapStyle not bold (keycap token)", th.Name)
		}
		if fg := s.keycapLabelStyle.GetForeground(); fg != lipgloss.Color(th.Secondary) {
			t.Errorf("%s: keycapLabelStyle fg = %#v, want secondary %q", th.Name, fg, th.Secondary)
		}
		if fg := s.rowStyle.GetForeground(); fg != lipgloss.Color(th.Text) {
			t.Errorf("%s: rowStyle fg = %#v, want text (muted is never the primary-label role)", th.Name, fg)
		}
		if fg := s.labelStyle.GetForeground(); fg != lipgloss.Color(th.Muted) {
			t.Errorf("%s: labelStyle fg = %#v, want muted (paths/metadata)", th.Name, fg)
		}
		if fg := s.previewRuleStyle.GetForeground(); fg != lipgloss.Color(th.Rule) {
			t.Errorf("%s: previewRuleStyle fg = %#v, want rule %q", th.Name, fg, th.Rule)
		}
		if fg := s.pinStyle.GetForeground(); fg != lipgloss.Color(th.Warn) {
			t.Errorf("%s: pinStyle fg = %#v, want warn %q", th.Name, fg, th.Warn)
		}
	}
	// Plain: same structure, no color attribute anywhere.
	s := newPalette(themes[ThemePlain])
	if !s.keycapStyle.GetBold() {
		t.Error("plain keycapStyle not bold, want a bold keycap token")
	}
	if fg := s.keycapStyle.GetForeground(); fg != (lipgloss.NoColor{}) {
		t.Errorf("plain keycapStyle fg = %#v, want no color", fg)
	}
	if !s.secondaryStyle.GetFaint() {
		t.Error("plain secondaryStyle not faint (structural secondary fallback)")
	}
	if s.keycapLabelStyle.GetBold() {
		t.Error("plain keycapLabelStyle must stay plain (contrasts with the bold token)")
	}
	if s.pinStyle.GetBold() || s.pinStyle.GetItalic() {
		t.Error("plain pinStyle must stay visually plain")
	}
	if fg := s.previewRuleStyle.GetForeground(); fg != (lipgloss.NoColor{}) {
		t.Errorf("plain previewRuleStyle fg = %#v, want no color (faint is the structural rule)", fg)
	}
}

// TestStyleRoles_FlavorMirrorsSpecHexes proves the mocha flavor carries the
// approved design's exact hex values (the binding palette anchor) and that
// secondary differs from both text and muted in every flavor.
func TestStyleRoles_FlavorMirrorsSpecHexes(t *testing.T) {
	t.Parallel()
	mocha := themes[ThemeMocha]
	if mocha.Accent != "#cba6f7" || mocha.Text != "#cdd6f4" || mocha.Secondary != "#a6adc8" ||
		mocha.Muted != "#6c7086" || mocha.SelectedSurface != "#313244" ||
		mocha.UnfocusedSurface != "#1e1e2e" || mocha.Rule != "#585b70" {
		t.Errorf("mocha theme hexes drifted from the approved design: %+v", mocha)
	}
	for _, th := range allColorThemes() {
		if th.Secondary == th.Text || th.Secondary == th.Muted {
			t.Errorf("%s: secondary %q must differ from both text and muted", th.Name, th.Secondary)
		}
	}
}

// TestStyleRoles_PlainNeverColorsPrimaryLabels cross-checks the plain theme's
// rowStyle (primary labels) and queryStyle carry no color — muted/faint may
// dim UI but never recolor the primary content.
func TestStyleRoles_PlainNeverColorsPrimaryLabels(t *testing.T) {
	t.Parallel()
	s := newPalette(themes[ThemePlain])
	noColor := lipgloss.NoColor{}
	if fg := s.rowStyle.GetForeground(); fg != noColor {
		t.Errorf("plain rowStyle fg = %#v, want no color (primary labels stay uncolored)", fg)
	}
	if fg := s.queryStyle.GetForeground(); fg != noColor {
		t.Errorf("plain queryStyle fg = %#v, want no color", fg)
	}
}
