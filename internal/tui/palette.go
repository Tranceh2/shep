// Package tui palette: Catppuccin Mocha. All lipgloss colors live here so the
// rest of the package references named styles; re-skinning means editing one
// file.
//
// Palette: https://catppuccin.com (Mocha variant). Colors are 24-bit hex so the
// renderer is independent of terminal 16-color slots.
package tui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Mocha palette values.
const (
	colorBase    = "#1e1e2e" // base background
	colorSurface = "#313244" // surface (muted blocks)
	colorText    = "#cdd6f4" // base text
	colorMuted   = "#6c7086" // overlay/muted text
	colorAccent  = "#89b4fa" // blue accent (cursor / highlight)
	colorAccent2 = "#f5c2e7" // pink secondary
	colorGreen   = "#a6e3a1" // green for success
	colorBorder  = "#585b70" // Surface2 — subtle frame around list/preview panes
)

var palette = struct {
	queryStyle          lipgloss.Style
	cursorStyle         lipgloss.Style
	rowStyle            lipgloss.Style
	mutedStyle          lipgloss.Style
	previewHeaderStyle  lipgloss.Style
	labelStyle          lipgloss.Style
	previewWarnStyle    lipgloss.Style
	previewLoadingStyle lipgloss.Style
	borderStyle         lipgloss.Style
}{
	// query line: bold accent text so the live filter stands out.
	queryStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true),
	// highlighted (cursor) row: accent background, base text. Width is
	// applied per-render (see renderList) against the actual split width
	// rather than hardcoded here, so the list pane never drifts from the
	// preview pane's computed split.
	cursorStyle: lipgloss.NewStyle().Background(lipgloss.Color(colorAccent)).Foreground(lipgloss.Color(colorBase)).Bold(true),
	// normal row: base text on transparent background. Width applied
	// per-render, same reasoning as cursorStyle above.
	rowStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)),
	// muted text (help, no-matches).
	mutedStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	// preview header: bold secondary accent.
	previewHeaderStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent2)).Bold(true),
	// label prefix in the preview pane.
	labelStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	// transient warning shown when a custom preview.command fell back to the
	// built-in preview (WP-3): italic secondary accent, distinct from an
	// error but still noticeable.
	previewWarnStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent2)).Italic(true),
	// loading indicator shown while an async preview render is in flight
	// (PL-11): muted italic so it reads as transient, not an error.
	previewLoadingStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Italic(true),
	// borderStyle frames the list and preview panes: one shared rounded
	// border + horizontal padding definition (both panes use the exact same
	// frame dimensions) so width/height chrome math has a single source of
	// truth instead of drifting between two independently-tuned borders.
	borderStyle: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(colorBorder)).Padding(0, 1),
}
