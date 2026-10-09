package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
)

// Lip Gloss renders every style's ANSI, so these tests tell styles apart by
// their rendered escapes; the other status-icon tests assert glyph identity.

// TestAgentStatusIcon_WorkingUsesStatusWorkingStyle proves the working-status
// pane icon for the unicode tier renders the shared spinner's current frame
// through statusWorkingStyle (warn/bold) — NOT the spinner's own configured
// Style, previewLoadingStyle (muted/italic, wired at construction in
// newModelWithLayout for the preview-loading indicator). The working status
// icon and the preview-loading spinner share one animated Bubble Tea
// component (and its single tick loop), but they are two distinct UI
// affordances and must not visually collapse into the same color/weight.
func TestAgentStatusIcon_WorkingUsesStatusWorkingStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsUnicode)
	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(m.styles.previewLoadingStyle),
	)
	// A freshly built spinner.Model starts at frame index 0 (see
	// bubbles/spinner.New); no Tick has been driven here.
	frame := m.spinner.Spinner.Frames[0]

	got := m.agentStatusIcon("working")

	want := m.styles.statusWorkingStyle.Render(frame)
	if got != want {
		t.Errorf("agentStatusIcon(\"working\") = %q, want statusWorkingStyle-rendered spinner frame %q", got, want)
	}

	wrongPreviewStyled := m.styles.previewLoadingStyle.Render(frame)
	if got == wrongPreviewStyled {
		t.Errorf("agentStatusIcon(\"working\") = %q, must NOT equal the spinner's own previewLoadingStyle rendering %q", got, wrongPreviewStyled)
	}

	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("agentStatusIcon(\"working\") = %q, want an SGR-colored render", got)
	}

	// The animation source is unchanged: the rendered text still carries the
	// spinner's current MiniDot frame glyph, not the ASCII-tier static
	// fallback ("o").
	plain := ansi.Strip(got)
	if plain != frame {
		t.Errorf("agentStatusIcon(\"working\") plain glyph = %q, want the spinner frame %q (animation must be preserved)", plain, frame)
	}
}

// TestAgentStatusIcon_WorkingASCIITierUnaffected proves the ASCII tier keeps
// its static "o" fallback (IconSet.StatusWorking, styled with
// statusWorkingStyle — see agentStatusIcon) instead of the animated spinner
// the unicode tier uses.
func TestAgentStatusIcon_WorkingASCIITierUnaffected(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsASCII)
	set := resolveIconSet(IconsASCII)
	got := m.agentStatusIcon("working")
	want := m.styles.statusWorkingStyle.Render(set.StatusWorking)
	if got != want {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier = %q, want %q", got, want)
	}
	if plain := ansi.Strip(got); plain != "o" {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier plain glyph = %q, want static \"o\"", plain)
	}
}
