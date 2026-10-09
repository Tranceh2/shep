package tui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/source"
)

// This file exercises the icon fallback chain (Unicode -> ASCII) resolved
// from [tui].icons (config.TUIConfig.Icons, threaded through Layout.Icons ->
// resolveIconSet), and its effect on the two UI
// surfaces that render shep's OWN semantic icons: agentStatusIcon (pane
// agent-status glyphs) and kindPrefix (row kind/expand markers). It does NOT
// cover source.Candidate.Icon ([sources.<name>].icon in config) — that is a
// raw user-configured string rendered verbatim regardless of the resolved
// tier (see candidateDisplayText/rowDisplayText's `c.Icon` handling).

// reASCIIOnly matches any byte outside the 7-bit ASCII range.
var reNonASCII = regexp.MustCompile(`[^\x00-\x7F]`)

// === resolveIconSet: default + explicit tiers ===

// TestResolveIconSet_DefaultsToUnicode proves an empty configIcons (an unset
// [tui].icons) resolves to the "unicode" tier with its exact glyph table, so
// a glyph change is always a deliberate, reviewed edit.
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
		TreeMid:       "├─",
		TreeLast:      "└─",
		TreeVertical:  "│ ",
		SearchPrompt:  "❯",

		RuleHorizontal: "─",
		RuleVertical:   "│",
		RuleJunction:   "┼",
		HintSeparator:  "·",
		Overflow:       "…",
		ScrollThumb:    "┃",
		Pinned:         "★",
		Group:          "›",
	}
	if set != want {
		t.Errorf("resolveIconSet(\"\") = %+v, want %+v", set, want)
	}
}

// TestResolveIconSet_UnknownFallsBackToUnicode proves an unrecognized
// configIcons value degrades to "unicode" rather than erroring — resolution
// must never fail to start the picker over a typo'd icons name.
func TestResolveIconSet_UnknownFallsBackToUnicode(t *testing.T) {
	t.Parallel()
	set := resolveIconSet("emoji")
	if set.Name != IconsUnicode {
		t.Errorf("resolveIconSet(\"emoji\").Name = %q, want %q (unknown falls back)", set.Name, IconsUnicode)
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
		"TreeMid":       set.TreeMid, "TreeLast": set.TreeLast,
		"TreeVertical":   set.TreeVertical,
		"SearchPrompt":   set.SearchPrompt,
		"RuleHorizontal": set.RuleHorizontal, "RuleVertical": set.RuleVertical,
		"RuleJunction": set.RuleJunction, "HintSeparator": set.HintSeparator,
		"Overflow": set.Overflow, "ScrollThumb": set.ScrollThumb,
		"Pinned": set.Pinned, "Group": set.Group,
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
	m.layout.Icons = icons
	return m.withPresentation(nil)
}

// TestModelIcons_DefaultsToUnicodeWhenLayoutIconsUnset proves a Model built
// without ever setting Layout.Icons (the zero value, e.g. every existing
// direct Model{} test literal) resolves the same "unicode" tier as an
// explicit empty string, so a zero-value Model{} literal needs no icon
// setup.
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
// not a hardcoded literal — asserted for both tiers.
func TestAgentStatusIcon_RespectsConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsUnicode, IconsASCII} {
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
// renders the model's shared animated spinner for the unicode tier — the
// spinner is a Bubble Tea component, not one of the icon-fallback-chain
// glyphs, and a unicode-capable terminal can always display its Braille dot
// glyphs. Its color role (statusWorkingStyle, not previewLoadingStyle) is
// asserted in spinner_style_test.go.
func TestAgentStatusIcon_WorkingIgnoresIconSet(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsUnicode)
	if got, want := m.agentStatusIcon("working"), m.spinner.View(); got != want {
		t.Errorf("agentStatusIcon(\"working\") = %q, want spinner view %q", got, want)
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

// TestKindPrefix_RowCandidateNeverGetsExpandGlyph proves a top-level row
// never gets a tree or expand glyph, expandable or not, under any icon tier:
// per-source icons already differentiate row types.
func TestKindPrefix_RowCandidateNeverGetsExpandGlyph(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsUnicode, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			for _, expandable := range []bool{true, false} {
				if got := m.kindPrefix(Row{Kind: RowCandidate, Expandable: expandable}); got != "" {
					t.Errorf("[%s] kindPrefix(RowCandidate, expandable=%v) = %q, want \"\"", tier, expandable, got)
				}
			}
		})
	}
}

// TestKindPrefix_TreeGlyphsRespectConfiguredIconSet proves the RowTab and
// RowPane tree glyphs come from the resolved IconSet and distinguish last
// siblings. Every RowTab/RowPane prefix is led by a fixed-width blank
// active-marker slot — m.currentPane is nil here, so isActiveFocusRow
// is always false and the slot is blank space, never the glyph itself (see
// TestKindPrefix_TreeGlyphColumnsAlign in
// active_focus_test.go for the alignment proof against an active row).
//
// A RowPane's ancestor connector shares the parent tab's branch column; its
// own glyph is one level deeper.
func TestKindPrefix_TreeGlyphsRespectConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsUnicode, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			set := resolveIconSet(tier)
			blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
			tests := []struct {
				name string
				row  Row
				want string
			}{
				{name: "non-last tab", row: Row{Kind: RowTab, Depth: 1}, want: "  " + set.TreeMid + " "},
				{name: "last tab", row: Row{Kind: RowTab, Depth: 1, IsLast: true}, want: "  " + set.TreeLast + " "},
				{name: "non-last pane, non-last ancestor", row: Row{Kind: RowPane, Depth: 2}, want: "  " + set.TreeVertical + set.TreeMid + " "},
				{name: "last pane, non-last ancestor", row: Row{Kind: RowPane, Depth: 2, IsLast: true}, want: "  " + set.TreeVertical + set.TreeLast + " "},
				{name: "ancestor is last sibling: blank ancestor column", row: Row{Kind: RowPane, Depth: 2, AncestorIsLast: true}, want: "  " + blankAncestor + set.TreeMid + " "},
				{name: "candidate has no tree prefix", row: Row{Kind: RowCandidate, IsLast: true, AncestorIsLast: true}, want: ""},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					if got := m.kindPrefix(tt.row); got != tt.want {
						t.Errorf("[%s] kindPrefix(%+v) = %q, want %q", tier, tt.row, got, tt.want)
					}
				})
			}
		})
	}
}

// TestRowDisplayText_TreePrefixDistinguishesLastTab proves the tree glyph
// still distinguishes last siblings, and the default tab format is the
// label alone — the tab-number/label dedup rule folds "3"+"deploy" into
// "3 deploy". A top-level candidate has no tree prefix.
func TestRowDisplayText_TreePrefixDistinguishesLastTab(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsUnicode)
	set := m.icons()
	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{name: "non-last", row: Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}, want: "  " + "├─ " + defaultTabIcon(set.Name) + " deploy"},
		{name: "last tab with number", row: Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc", Meta: map[string]string{"tab_number": "3"}}}, want: "  " + "└─ " + defaultTabIcon(set.Name) + " 3 deploy"},
		{name: "last tab without number", row: Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}, want: "  " + "└─ " + defaultTabIcon(set.Name) + " deploy"},
		{name: "candidate without tree", row: Row{Kind: RowCandidate, IsLast: true, AncestorIsLast: true, Candidate: source.Candidate{Label: "workspace", Path: "/srv/ws"}}, want: "ws"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary, _ := m.rowDisplayText(tt.row)
			if got := ansi.Strip(primary); got != tt.want {
				t.Errorf("rowDisplayText(%+v) primary = %q, want %q", tt.row, got, tt.want)
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
	plain := ansi.Strip(primary)
	if !containsFold(plain, set.StatusIdle) {
		t.Errorf("rowDisplayText primary %q must contain the ascii idle glyph %q", plain, set.StatusIdle)
	}
	if containsFold(plain, "✓") {
		t.Errorf("rowDisplayText primary %q must not contain the unicode idle glyph when ascii is configured", plain)
	}
}

// TestSearchPrompt_RespectsConfiguredIconSet proves the prompt row's glyph
// comes from the configured tier ("❯" Unicode, ">" ASCII), followed by the
// cursor cell and the placeholder, with no keycap brackets.
func TestSearchPrompt_RespectsConfiguredIconSet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		icons      string
		wantPrompt string
	}{
		{IconsUnicode, "❯"},
		{IconsASCII, ">"},
	} {
		t.Run(tc.icons, func(t *testing.T) {
			m := NewModelWithLayout([]source.Candidate{{Label: "a", Source: "zoxide"}}, nil, Layout{Icons: tc.icons, Theme: testTheme(ThemeMocha)})
			prompt := promptText(m)
			if strings.Contains(prompt, "[/]") {
				t.Errorf("[%s] prompt row contains keycap brackets: %q", tc.icons, prompt)
			}
			if !strings.HasPrefix(prompt, tc.wantPrompt+"  Search workspaces") {
				t.Errorf("[%s] prompt row %q, want %q, the cursor cell and the placeholder", tc.icons, prompt, tc.wantPrompt)
			}
		})
	}
}

// TestASCIITier_ChromeIsASCIIOnly proves the ASCII icon tier keeps every
// chrome row (tab strip, prompt, rule, divider, footer) free of non-ASCII
// glyphs, in wide and list-only layouts and in the help overlay. The
// placeholder and the ellipsis inside truncated user text are excluded by
// keeping every label short.
func TestASCIITier_ChromeIsASCIIOnly(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ w, h int }{{120, 30}, {64, 20}} {
		for _, help := range []bool{false, true} {
			m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Icons: IconsASCII, Theme: testTheme(ThemeMocha)})
			m, _ = update(t, m, sizeMsg(size.w, size.h))
			if help {
				m, _ = update(t, m, key("?"))
			} else {
				m, _ = update(t, m, key("a"))
			}
			lines := viewLines(m)
			for _, i := range []int{0, 1, 2, 3, len(lines) - 1} {
				if reNonASCII.MatchString(lines[i]) {
					t.Errorf("%dx%d help=%v line %d = %q, contains a non-ASCII glyph", size.w, size.h, help, i, lines[i])
				}
			}
		}
	}
}

func containsFold(s, substr string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(substr)).MatchString(s)
}
