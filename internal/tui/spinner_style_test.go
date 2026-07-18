package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Follow-up style alignment — strict TDD. internal/tui's other tests never
// force lipgloss color output, so a rendered style's ANSI escapes are always
// stripped by the package's own no-tty detection and every Render() call
// collapses to the same plain glyph regardless of which style was actually
// used (see status_icon_test.go / icons_test.go's working-status tests,
// which therefore only assert glyph identity, not color). This file forces
// a real color profile for the one assertion that needs to distinguish
// styles by their rendered ANSI, then restores the previous profile so it
// never leaks into the rest of the (parallel) suite.
//
// Not marked t.Parallel(): Go defers every t.Parallel() test in this
// package until every non-parallel top-level test (this one included) has
// returned, so mutating the package-level lipgloss color profile here for
// the duration of this test body is safe — no parallel test's Render call
// can observe it.

// TestAgentStatusIcon_WorkingUsesStatusWorkingStyle proves the working-status
// pane icon for the unicode tier renders the shared spinner's current frame
// through statusWorkingStyle (warn/bold) — NOT the spinner's own configured
// Style, previewLoadingStyle (muted/italic, wired at construction in
// newModelWithLayout for the preview-loading indicator). The working status
// icon and the preview-loading spinner share one animated Bubble Tea
// component (and its single tick loop), but they are two distinct UI
// affordances and must not visually collapse into the same color/weight.
func TestAgentStatusIcon_WorkingUsesStatusWorkingStyle(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

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

	if !reSGR.MatchString(got) {
		t.Fatalf("agentStatusIcon(\"working\") = %q, want an SGR-colored render (color profile forced to TrueColor for this test)", got)
	}

	// The animation source is unchanged: the rendered text still carries the
	// spinner's current MiniDot frame glyph, not the ASCII-tier static
	// fallback ("o").
	plain := reSGR.ReplaceAllString(got, "")
	if plain != frame {
		t.Errorf("agentStatusIcon(\"working\") plain glyph = %q, want the spinner frame %q (animation must be preserved)", plain, frame)
	}
}

// TestAgentStatusIcon_WorkingASCIITierUnaffected is a regression guard: the
// ASCII tier's static "o" fallback (IconSet.StatusWorking, already styled
// with statusWorkingStyle — see agentStatusIcon) must not change behavior
// from this fix, which only touches the animated-spinner branch.
func TestAgentStatusIcon_WorkingASCIITierUnaffected(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	m := newRenderTestModelWithIcons(ThemeMocha, IconsASCII)
	set := resolveIconSet(IconsASCII)
	got := m.agentStatusIcon("working")
	want := m.styles.statusWorkingStyle.Render(set.StatusWorking)
	if got != want {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier = %q, want %q", got, want)
	}
	if plain := reSGR.ReplaceAllString(got, ""); plain != "o" {
		t.Errorf("agentStatusIcon(\"working\") under ascii tier plain glyph = %q, want static \"o\"", plain)
	}
}
