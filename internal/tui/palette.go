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
)

var palette = struct {
	queryStyle         lipgloss.Style
	cursorStyle        lipgloss.Style
	rowStyle           lipgloss.Style
	mutedStyle         lipgloss.Style
	previewHeaderStyle lipgloss.Style
	labelStyle         lipgloss.Style
}{
	// query line: bold accent text so the live filter stands out.
	queryStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true),
	// highlighted (cursor) row: accent background, base text.
	cursorStyle: lipgloss.NewStyle().Background(lipgloss.Color(colorAccent)).Foreground(lipgloss.Color(colorBase)).Bold(true).Width(40),
	// normal row: base text on transparent background.
	rowStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorText)).Width(40),
	// muted text (help, no-matches).
	mutedStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	// preview header: bold secondary accent.
	previewHeaderStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent2)).Bold(true),
	// label prefix in the preview pane.
	labelStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
}
