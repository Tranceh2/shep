package tui

import (
	"strings"
	"testing"
)

// Corrective round 2 — strict TDD. This file exercises the fix to the
// pane agent-status icon/color mapping against herdr's own verified
// convention (ground truth: herdr's src/ui/status.rs, function agent_icon):
// working keeps the animated spinner colored warn/yellow (was wrongly
// accent/blue); blocked is "◉" in err/red (was wrongly "⚠" in warn/yellow);
// done is "●" in a new teal token (was wrongly accent/blue); idle is "✓" in
// a new success/green token (was wrongly muted); the literal "unknown"
// status is "○" in muted (was previously swallowed by the empty-icon
// default case, conflating it with "no status at all"); and an entirely
// empty/absent agent_status still renders no icon (unchanged, must not
// regress).

// === agentStatusIcon glyph + color mapping ===

// TestAgentStatusIcon_GlyphsAndColors proves the corrected glyph+style pairs
// for every non-working status, and that an empty or unrecognized status
// (anything other than the 5 known values) still renders no icon at all.
func TestAgentStatusIcon_GlyphsAndColors(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	tests := []struct {
		status string
		want   string
	}{
		{"idle", m.styles.statusIdleStyle.Render("✓")},
		{"done", m.styles.statusDoneStyle.Render("●")},
		{"blocked", m.styles.statusBlockedStyle.Render("◉")},
		{"unknown", m.styles.statusUnknownStyle.Render("○")},
		{"", ""},
		{"totally-bogus", ""},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := m.agentStatusIcon(tt.status)
			if got != tt.want {
				t.Errorf("agentStatusIcon(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

// TestAgentStatusIcon_WorkingUsesSpinner proves "working" still renders the
// model's shared animated spinner (glyph unchanged) — only its color role
// changed (see TestAgentStatusIcon_WorkingUsesStatusWorkingStyle), not the
// glyph source itself.
func TestAgentStatusIcon_WorkingUsesSpinner(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	got := m.agentStatusIcon("working")
	want := m.spinner.View()
	if got != want {
		t.Errorf("agentStatusIcon(\"working\") = %q, want the model's spinner view %q", got, want)
	}
}

// TestAgentStatusIcon_PlainRendersGlyphsNoColor proves every icon glyph
// still renders under the Plain theme (icons are structural information,
// not color-only) while carrying zero SGR color escapes — mirroring the
// existing Plain-mode convention used elsewhere in the codebase (e.g.
// descendant-match markers) where structural glyphs survive NoColor and
// only the color styling is dropped.
func TestAgentStatusIcon_PlainRendersGlyphsNoColor(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	tests := []struct {
		status string
		glyph  string
	}{
		{"idle", "✓"},
		{"done", "●"},
		{"blocked", "◉"},
		{"unknown", "○"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := m.agentStatusIcon(tt.status)
			if hasColor(got) {
				t.Errorf("plain agentStatusIcon(%q) = %q, contains a color, want none", tt.status, got)
			}
			if !strings.Contains(got, tt.glyph) {
				t.Errorf("plain agentStatusIcon(%q) = %q, want it to contain glyph %q", tt.status, got, tt.glyph)
			}
		})
	}
	// working: the spinner itself must still render (non-empty) and stay
	// color-free under Plain (spinner.WithStyle uses previewLoadingStyle,
	// which sets no Foreground in the NoColor branch of newPalette).
	working := m.agentStatusIcon("working")
	if working == "" {
		t.Error("plain agentStatusIcon(\"working\") empty, want the spinner glyph to still render")
	}
	if hasColor(working) {
		t.Errorf("plain agentStatusIcon(\"working\") = %q, contains a color, want none", working)
	}
}

// TestAgentStatusIcon_NoStatusStillMeansNoIcon is a targeted regression
// guard (independent of corrective_test.go's broader coverage) proving the
// literal "unknown" fix did not accidentally make the empty-string /
// absent-status case start rendering an icon too.
func TestAgentStatusIcon_NoStatusStillMeansNoIcon(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	if got := m.agentStatusIcon(""); got != "" {
		t.Errorf("agentStatusIcon(\"\") = %q, want empty (no status = no icon, must not regress)", got)
	}
}
