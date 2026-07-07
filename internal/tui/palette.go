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
	colorYellow  = "#f9e2af" // yellow — blocked agent status
	colorOverlay = "#9399b2" // Overlay2 — unknown agent status
)

var palette = struct {
	queryStyle          lipgloss.Style
	cursorStyle         lipgloss.Style
	rowStyle            lipgloss.Style
	mutedStyle          lipgloss.Style
	labelStyle          lipgloss.Style
	previewLoadingStyle lipgloss.Style
	previewErrStyle     lipgloss.Style
	borderStyle         lipgloss.Style
	surfaceStyle        lipgloss.Style
	statusIdleStyle     lipgloss.Style
	statusWorkingStyle  lipgloss.Style
	statusBlockedStyle  lipgloss.Style
	statusDoneStyle     lipgloss.Style
	statusUnknownStyle  lipgloss.Style
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
	// muted text (help, no-matches, footer).
	mutedStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	// label prefix in the preview pane.
	labelStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	// loading indicator shown while an async preview render is in flight
	// (PL-11): muted italic so it reads as transient, not an error.
	previewLoadingStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)).Italic(true),
	// shown when Renderer.Render itself returns a real error (context
	// cancellation, or any future Renderer implementation) — distinct from
	// the loading indicator so a genuine failure never reads as "still
	// working" or silently blank.
	previewErrStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent2)).Italic(true),
	// borderStyle frames the list and preview panes: one shared rounded
	// border + horizontal padding definition (both panes use the exact same
	// frame dimensions) so width/height chrome math has a single source of
	// truth instead of drifting between two independently-tuned borders.
	borderStyle: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(colorBorder)).Padding(0, 1),
	// surfaceStyle: a subtle background block, used to set off the footer's
	// "focused: <status>" segment from the rest of the hint line.
	surfaceStyle: lipgloss.NewStyle().Background(lipgloss.Color(colorSurface)),
	// Per-status styles applied via statusStyle: same mapping in both the
	// preview "status:" line and the footer "focused:" segment (R1).
	statusIdleStyle:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorMuted)),
	statusWorkingStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorGreen)).Bold(true),
	statusBlockedStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorYellow)).Bold(true),
	statusDoneStyle:    lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)),
	statusUnknownStyle: lipgloss.NewStyle().Foreground(lipgloss.Color(colorOverlay)),
}

// statusStyle maps an agent_status value ("idle", "working", "blocked",
// "done") to its dedicated Catppuccin Mocha style. Any other value —
// including the explicit "unknown" status Herdr itself may report, and any
// unrecognized string — falls back to statusUnknownStyle. Both the preview
// "status:" line (see styleStatusLine in model.go) and the footer "focused:"
// segment (see renderFooter) style through this single function so the two
// never drift.
func statusStyle(status string) lipgloss.Style {
	switch status {
	case "idle":
		return palette.statusIdleStyle
	case "working":
		return palette.statusWorkingStyle
	case "blocked":
		return palette.statusBlockedStyle
	case "done":
		return palette.statusDoneStyle
	default:
		return palette.statusUnknownStyle
	}
}
