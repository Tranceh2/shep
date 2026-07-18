package tui

import (
	"regexp"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// Phase 8 — strict TDD. This file exercises the icon fallback chain (Nerd
// Font -> Unicode -> ASCII) resolved from [tui].icons (config.TUIConfig.Icons,
// threaded through Layout.Icons -> resolveIconSet), and its effect on the two
// UI surfaces that render shep's OWN semantic icons: agentStatusIcon (pane
// agent-status glyphs) and kindPrefix (row kind/expand markers). It does NOT
// cover source.Candidate.Icon ([sources.<name>].icon in config) — that is a
// raw user-configured string rendered verbatim regardless of the resolved
// tier (see candidateDisplayText/rowDisplayText's `c.Icon` handling).

// reASCIIOnly matches any byte outside the 7-bit ASCII range.
var reNonASCII = regexp.MustCompile(`[^\x00-\x7F]`)

// === resolveIconSet: default + explicit tiers ===

// TestResolveIconSet_DefaultsToUnicode proves an empty configIcons (an unset
// [tui].icons) resolves to the "unicode" tier, whose glyphs are byte-identical
// to the picker's original hardcoded values — so leaving [tui].icons unset
// never changes any existing rendered output.
func TestResolveIconSet_DefaultsToUnicode(t *testing.T) {
	t.Parallel()
	set := resolveIconSet("")
	if set.Name != IconsUnicode {
		t.Errorf("resolveIconSet(\"\").Name = %q, want %q", set.Name, IconsUnicode)
	}
	want := IconSet{
		Name:          IconsUnicode,
		StatusIdle:    "✓",
		StatusDone:    "●",
		StatusBlocked: "◉",
		StatusUnknown: "○",
		ExpandOpen:    "▾",
		ExpandClosed:  "▸",
		TabPrefix:     "»",
		PanePrefix:    "·",
	}
	if set != want {
		t.Errorf("resolveIconSet(\"\") = %+v, want %+v (must match the pre-Phase-8 hardcoded glyphs exactly)", set, want)
	}
}

// TestResolveIconSet_UnknownFallsBackToUnicode proves an unrecognized
// configIcons value degrades to "unicode" rather than erroring — resolution
// must never fail to start the picker over a typo'd icons name, mirroring
// resolveThemeName's precedent.
func TestResolveIconSet_UnknownFallsBackToUnicode(t *testing.T) {
	t.Parallel()
	set := resolveIconSet("emoji")
	if set.Name != IconsUnicode {
		t.Errorf("resolveIconSet(\"emoji\").Name = %q, want %q (unknown falls back)", set.Name, IconsUnicode)
	}
}

// TestResolveIconSet_Nerd proves the "nerd" tier resolves to a distinct,
// fully-populated glyph set (Nerd Font Private Use Area codepoints).
func TestResolveIconSet_Nerd(t *testing.T) {
	t.Parallel()
	set := resolveIconSet(IconsNerd)
	if set.Name != IconsNerd {
		t.Errorf("resolveIconSet(%q).Name = %q, want %q", IconsNerd, set.Name, IconsNerd)
	}
	fields := map[string]string{
		"StatusIdle": set.StatusIdle, "StatusDone": set.StatusDone,
		"StatusBlocked": set.StatusBlocked, "StatusUnknown": set.StatusUnknown,
		"ExpandOpen": set.ExpandOpen, "ExpandClosed": set.ExpandClosed,
		"TabPrefix": set.TabPrefix, "PanePrefix": set.PanePrefix,
	}
	for name, glyph := range fields {
		if glyph == "" {
			t.Errorf("nerd icon set: %s is empty, want a Nerd Font glyph", name)
		}
	}
	unicode := resolveIconSet(IconsUnicode)
	if set.StatusIdle == unicode.StatusIdle {
		t.Error("nerd StatusIdle must differ from the unicode tier's glyph")
	}
}

// TestResolveIconSet_ASCII proves the "ascii" tier resolves to a
// fully-populated glyph set containing only 7-bit ASCII bytes — the tier a
// dumb terminal or non-UTF-8 locale can always render.
func TestResolveIconSet_ASCII(t *testing.T) {
	t.Parallel()
	set := resolveIconSet(IconsASCII)
	if set.Name != IconsASCII {
		t.Errorf("resolveIconSet(%q).Name = %q, want %q", IconsASCII, set.Name, IconsASCII)
	}
	fields := map[string]string{
		"StatusIdle": set.StatusIdle, "StatusDone": set.StatusDone,
		"StatusBlocked": set.StatusBlocked, "StatusUnknown": set.StatusUnknown,
		"StatusWorking": set.StatusWorking,
		"ExpandOpen":    set.ExpandOpen, "ExpandClosed": set.ExpandClosed,
		"TabPrefix": set.TabPrefix, "PanePrefix": set.PanePrefix,
	}
	for name, glyph := range fields {
		if glyph == "" {
			t.Errorf("ascii icon set: %s is empty, want an ASCII glyph", name)
		}
		if reNonASCII.MatchString(glyph) {
			t.Errorf("ascii icon set: %s = %q contains a non-ASCII byte", name, glyph)
		}
	}
}

// === Model.icons() wiring: Layout.Icons -> resolveIconSet ===

// newRenderTestModelWithIcons extends newRenderTestModel with an explicit
// Layout.Icons so agentStatusIcon/kindPrefix tests can exercise a
// non-default tier without driving a full NewModelWithLayout construction.
func newRenderTestModelWithIcons(themeName, icons string) Model {
	m := newRenderTestModel(themeName, FocusList)
	m.layout = Layout{Icons: icons}
	return m
}

// TestModelIcons_DefaultsToUnicodeWhenLayoutIconsUnset proves a Model built
// without ever setting Layout.Icons (the zero value, e.g. every existing
// direct Model{} test literal) resolves the same "unicode" tier as an
// explicit empty string — no test using the old zero-value construction
// pattern needs to change for Phase 8.
func TestModelIcons_DefaultsToUnicodeWhenLayoutIconsUnset(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	if got := m.icons(); got.Name != IconsUnicode {
		t.Errorf("m.icons().Name = %q, want %q for an unset Layout.Icons", got.Name, IconsUnicode)
	}
}

// === agentStatusIcon respects the configured tier ===

// TestAgentStatusIcon_RespectsConfiguredIconSet proves each non-working
// status glyph comes from the resolved IconSet for the Model's Layout.Icons,
// not a hardcoded literal — asserted for both non-default tiers.
func TestAgentStatusIcon_RespectsConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsNerd, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			set := resolveIconSet(tier)
			tests := []struct {
				status string
				want   string
			}{
				{"idle", m.styles.statusIdleStyle.Render(set.StatusIdle)},
				{"done", m.styles.statusDoneStyle.Render(set.StatusDone)},
				{"blocked", m.styles.statusBlockedStyle.Render(set.StatusBlocked)},
				{"unknown", m.styles.statusUnknownStyle.Render(set.StatusUnknown)},
			}
			for _, tt := range tests {
				if got := m.agentStatusIcon(tt.status); got != tt.want {
					t.Errorf("[%s] agentStatusIcon(%q) = %q, want %q", tier, tt.status, got, tt.want)
				}
			}
		})
	}
}

// TestAgentStatusIcon_WorkingIgnoresIconSet proves the "working" status
// renders the model's shared animated spinner for the nerd and unicode
// tiers regardless of icon fallback chain otherwise — the spinner is a
// Bubble Tea component, not one of the icon-fallback-chain glyphs, and
// those two tiers' terminals can always display its Braille dot glyphs.
// Its color role (statusWorkingStyle, not previewLoadingStyle) is asserted
// with a forced color profile in spinner_style_test.go, since this
// package's tests never force lipgloss color output.
func TestAgentStatusIcon_WorkingIgnoresIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsNerd, IconsUnicode} {
		m := newRenderTestModelWithIcons(ThemeMocha, tier)
		if got, want := m.agentStatusIcon("working"), m.spinner.View(); got != want {
			t.Errorf("[%s] agentStatusIcon(\"working\") = %q, want spinner view %q", tier, got, want)
		}
	}
}

// TestAgentStatusIcon_WorkingIsASCIISafeUnderASCIITier proves the ascii
// tier never emits the animated spinner's Bubble Tea MiniDot glyph, which
// draws Unicode Braille dots with no 7-bit ASCII fallback rendering: a pane
// reporting "working" under [tui].icons = "ascii" must render a static
// ASCII marker (IconSet.StatusWorking) styled like every other status
// glyph, instead.
func TestAgentStatusIcon_WorkingIsASCIISafeUnderASCIITier(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsASCII)
	set := resolveIconSet(IconsASCII)
	got := m.agentStatusIcon("working")
	if got == "" {
		t.Fatal("agentStatusIcon(\"working\") under ascii tier is empty, want a static ASCII marker")
	}
	if reNonASCII.MatchString(got) {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier = %q, contains a non-ASCII byte (want 7-bit ASCII only)", got)
	}
	if got == m.spinner.View() {
		t.Error("agentStatusIcon(\"working\") under ascii tier must not equal the animated spinner view")
	}
	want := m.styles.statusWorkingStyle.Render(set.StatusWorking)
	if got != want {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier = %q, want %q", got, want)
	}
}

// === kindPrefix respects the configured tier ===

// TestKindPrefix_ExpandMarkersRespectConfiguredIconSet proves the
// expanded/collapsed RowCandidate markers come from the resolved IconSet.
func TestKindPrefix_ExpandMarkersRespectConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsNerd, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			set := resolveIconSet(tier)
			expanded := m.kindPrefix(Row{Kind: RowCandidate, Expandable: true, Expanded: true})
			collapsed := m.kindPrefix(Row{Kind: RowCandidate, Expandable: true, Expanded: false})
			if expanded != set.ExpandOpen+" " {
				t.Errorf("[%s] kindPrefix(expanded) = %q, want %q", tier, expanded, set.ExpandOpen+" ")
			}
			if collapsed != set.ExpandClosed+" " {
				t.Errorf("[%s] kindPrefix(collapsed) = %q, want %q", tier, collapsed, set.ExpandClosed+" ")
			}
		})
	}
}

// TestKindPrefix_TabAndPaneMarkersRespectConfiguredIconSet proves the
// RowTab/RowPane markers come from the resolved IconSet.
func TestKindPrefix_TabAndPaneMarkersRespectConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsNerd, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			set := resolveIconSet(tier)
			tab := m.kindPrefix(Row{Kind: RowTab})
			pane := m.kindPrefix(Row{Kind: RowPane})
			if tab != set.TabPrefix+" " {
				t.Errorf("[%s] kindPrefix(tab) = %q, want %q", tier, tab, set.TabPrefix+" ")
			}
			if pane != set.PanePrefix+" " {
				t.Errorf("[%s] kindPrefix(pane) = %q, want %q", tier, pane, set.PanePrefix+" ")
			}
		})
	}
}

// TestRowDisplayText_RespectsConfiguredIconSetEndToEnd proves the
// end-to-end row rendering path (rowDisplayText -> kindPrefix/agentStatusIcon)
// picks up a non-default configured icon tier, not just the unit-level
// methods in isolation.
func TestRowDisplayText_RespectsConfiguredIconSetEndToEnd(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsASCII)
	set := resolveIconSet(IconsASCII)
	row := Row{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api",
			Meta: map[string]string{"agent_status": "idle"},
		},
	}
	primary, _ := m.rowDisplayText(row)
	plain := stripNonSGRANSI(primary)
	if !containsFold(plain, set.StatusIdle) {
		t.Errorf("rowDisplayText primary %q must contain the ascii idle glyph %q", plain, set.StatusIdle)
	}
	if containsFold(plain, "✓") {
		t.Errorf("rowDisplayText primary %q must not contain the unicode idle glyph when ascii is configured", plain)
	}
}

func containsFold(s, substr string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(substr)).MatchString(s)
}
