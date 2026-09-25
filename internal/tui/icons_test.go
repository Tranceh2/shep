package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/source"
)

// Phase 8 — strict TDD. This file exercises the icon fallback chain (Unicode
// -> ASCII) resolved from [tui].icons (config.TUIConfig.Icons, threaded
// through Layout.Icons -> resolveIconSet), and its effect on the two UI
// surfaces that render shep's OWN semantic icons: agentStatusIcon (pane
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
		TreeMid:       "├─",
		TreeLast:      "└─",
		TreeVertical:  "│ ",
		TabIcon:       "◫",
		ActiveMarker:  "◆",
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
		"TreeMid": set.TreeMid, "TreeLast": set.TreeLast,
		"TreeVertical": set.TreeVertical, "TabIcon": set.TabIcon, "ActiveMarker": set.ActiveMarker,
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
// asserted with a forced color profile in spinner_style_test.go, since this
// package's tests never force lipgloss color output.
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

// TestKindPrefix_RowCandidateNeverGetsExpandGlyph proves a RowCandidate
// (top-level workspace row) never gets an expand/collapse glyph regardless
// of Expandable/Expanded state or the configured icon tier (TRL-3: per-
// source icons already differentiate row types, so the ▸/▾ marker was
// removed from workspace rows entirely). Left/Right/Enter still toggle the
// underlying Expandable/Expanded state (see keys.go) — only the glyph is
// gone.
func TestKindPrefix_RowCandidateNeverGetsExpandGlyph(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsUnicode, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			expanded := m.kindPrefix(Row{Kind: RowCandidate, Expandable: true, Expanded: true})
			collapsed := m.kindPrefix(Row{Kind: RowCandidate, Expandable: true, Expanded: false})
			notExpandable := m.kindPrefix(Row{Kind: RowCandidate, Expandable: false})
			if expanded != "" {
				t.Errorf("[%s] kindPrefix(expanded RowCandidate) = %q, want \"\" (no glyph)", tier, expanded)
			}
			if collapsed != "" {
				t.Errorf("[%s] kindPrefix(collapsed RowCandidate) = %q, want \"\" (no glyph)", tier, collapsed)
			}
			if notExpandable != "" {
				t.Errorf("[%s] kindPrefix(non-expandable RowCandidate) = %q, want \"\"", tier, notExpandable)
			}
		})
	}
}

// TestKindPrefix_TreeGlyphsRespectConfiguredIconSet proves the RowTab and
// RowPane tree glyphs come from the resolved IconSet and distinguish last
// siblings. Every RowTab/RowPane prefix is led by a fixed-width blank
// active-marker slot (TRL-4) — m.currentPane is nil here, so isActiveFocusRow
// is always false and the slot is blank space, never the glyph itself (see
// TestKindPrefix_TreeGlyphColumnAlignsRegardlessOfActiveMarker in
// active_focus_test.go for the alignment proof against an active row).
//
// A RowPane's ancestor connector follows its fixed active-marker slot so it
// shares the parent tab's branch column; its own glyph is one level deeper.
func TestKindPrefix_TreeGlyphsRespectConfiguredIconSet(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{IconsUnicode, IconsASCII} {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			m := newRenderTestModelWithIcons(ThemeMocha, tier)
			set := resolveIconSet(tier)
			blankSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
			blankAncestor := strings.Repeat(" ", lipgloss.Width(set.TreeVertical))
			tests := []struct {
				name string
				row  Row
				want string
			}{
				{name: "non-last tab", row: Row{Kind: RowTab, Depth: 1}, want: "  " + blankSlot + set.TreeMid + " "},
				{name: "last tab", row: Row{Kind: RowTab, Depth: 1, IsLast: true}, want: "  " + blankSlot + set.TreeLast + " "},
				{name: "non-last pane, non-last ancestor", row: Row{Kind: RowPane, Depth: 2}, want: "  " + blankSlot + set.TreeVertical + set.TreeMid + " "},
				{name: "last pane, non-last ancestor", row: Row{Kind: RowPane, Depth: 2, IsLast: true}, want: "  " + blankSlot + set.TreeVertical + set.TreeLast + " "},
				{name: "ancestor is last sibling: blank ancestor column", row: Row{Kind: RowPane, Depth: 2, AncestorIsLast: true}, want: "  " + blankSlot + blankAncestor + set.TreeMid + " "},
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
// still distinguishes last siblings, and (Change 2) the primary text now
// unifies to "<tab icon> <resolved label> · <path>" — the tab-number/label
// dedup rule folds "3"+"deploy" into "3 deploy", while a bare number with no
// distinct label collapses to just the number (no path in these cases, so
// composeLabelPath's path-less body — but a path is threaded through where
// the test cares about the label/tree-glyph interaction specifically).
func TestRowDisplayText_TreePrefixDistinguishesLastTab(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsUnicode)
	set := m.icons()
	blankSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{name: "non-last", row: Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}, want: "  " + blankSlot + "├─ " + set.TabIcon + " deploy · /svc"},
		{name: "last tab with number", row: Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc", Meta: map[string]string{"tab_number": "3"}}}, want: "  " + blankSlot + "└─ " + set.TabIcon + " 3 deploy · /svc"},
		{name: "last tab without number", row: Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "deploy", Path: "/svc"}}, want: "  " + blankSlot + "└─ " + set.TabIcon + " deploy · /svc"},
		{name: "candidate without tree", row: Row{Kind: RowCandidate, IsLast: true, AncestorIsLast: true, Candidate: source.Candidate{Label: "workspace", Path: "/ws"}}, want: "/ws"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			primary, _ := m.rowDisplayText(tt.row)
			if got := stripNonSGRANSI(primary); got != tt.want {
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
