// Package tui theme resolution: semantic color tokens instead of a single
// hardcoded palette. A Theme carries named color roles (not lipgloss.Style
// values) so the picker's visual language can be re-skinned by swapping one
// Theme for another; stylesFor builds the actual lipgloss.Style set for the
// active theme once per Model, avoiding any shared mutable package state
// (the previous single hardcoded `palette` package var could not vary per
// Model/test and would have raced under t.Parallel() if made mutable).
//
// Precedence (resolveTheme): $NO_COLOR (any non-empty value) always wins and
// forces the "plain" no-color theme, regardless of $SHEP_THEME or config;
// then $SHEP_THEME; then the caller-supplied config name (config.TUIConfig's
// tui.theme, threaded through Layout.Theme — see layoutFromConfig in
// internal/command/open.go); then "mocha" as the final default. An unknown
// theme name at any tier falls back to the next tier rather than erroring —
// the picker must never fail to start over a typo'd theme name.
package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Theme names, mirrored in config.TUIThemeMocha etc. so config validation and
// the TUI resolve the exact same set without an import cycle (config cannot
// import tui).
const (
	ThemeMocha     = "mocha"
	ThemeMacchiato = "macchiato"
	ThemeFrappe    = "frappe"
	ThemeLatte     = "latte"
	ThemePlain     = "plain"
)

// Theme is a named set of semantic color roles consumed by stylesFor. NoColor
// themes leave every color field empty; stylesFor recognizes NoColor and
// builds styles using only structural attributes (bold/italic/underline/
// reverse/faint) plus textual markers elsewhere in the rendering code, never
// emitting a color escape sequence. The roles are intentionally few and each
// carries one meaning (accent=focus/selection/active, text=base, muted=
// secondary, selectedSurface/unfocusedSurface=cursor row backgrounds, rule=
// borders+unfocused gutter, warn=working status, err=blocked status+preview
// error, teal=done status, success=idle status) so the picker's visual
// language can be re-skinned by swapping one Theme for another.
//
// The working/blocked/done/idle status role assignments mirror herdr's own
// verified agent_icon convention (herdr's src/ui/status.rs): working=warn
// (yellow), blocked=err (red), done=teal, idle=success (green), unknown=
// muted — see agentStatusIcon in render.go.
type Theme struct {
	Name             string
	NoColor          bool
	Accent           string // accent (Blue): cursor gutter, preview headings
	Text             string // text: base text, help headings
	Muted            string // muted: secondary text, unknown status, descendant rows
	SelectedSurface  string // selectedSurface: focused cursor row background
	UnfocusedSurface string // unfocusedSurface: cursor row background when the preview owns focus
	Rule             string // rule: unfocused cursor gutter, pane borders
	Warn             string // warn (Yellow): working status
	Err              string // err (Red): blocked status, preview error
	Teal             string // teal: done status
	Success          string // success (Green): idle status
}

// Catppuccin's four official flavors. Hex values are the Phase 2 semantic
// token table (accent/text/muted/selectedSurface/unfocusedSurface/rule/warn/
// err), sourced from https://catppuccin.com per flavor, plus the corrective
// round's teal/success tokens (same source, official Teal/Green swatches).
// selectedSurface and unfocusedSurface are Catppuccin's surface0 and
// crust/mantle shades; rule reuses the old border hex; warn is Catppuccin
// Yellow; err is Catppuccin Red (also backs preview errors).
var themes = map[string]Theme{
	ThemeMocha: {
		Name: ThemeMocha, Accent: "#89b4fa", Text: "#cdd6f4", Muted: "#6c7086",
		SelectedSurface: "#313244", UnfocusedSurface: "#181825", Rule: "#585b70",
		Warn: "#f9e2af", Err: "#f38ba8", Teal: "#94e2d5", Success: "#a6e3a1",
	},
	ThemeMacchiato: {
		Name: ThemeMacchiato, Accent: "#8aadf4", Text: "#cad3f5", Muted: "#6e738d",
		SelectedSurface: "#363a4f", UnfocusedSurface: "#1e2030", Rule: "#5b6078",
		Warn: "#eed49f", Err: "#ed8796", Teal: "#8bd5ca", Success: "#a6da95",
	},
	ThemeFrappe: {
		Name: ThemeFrappe, Accent: "#8caaee", Text: "#c6d0f5", Muted: "#737994",
		SelectedSurface: "#414559", UnfocusedSurface: "#292c3c", Rule: "#626880",
		Warn: "#e5c890", Err: "#e78284", Teal: "#81c8be", Success: "#a6d189",
	},
	ThemeLatte: {
		Name: ThemeLatte, Accent: "#1e66f5", Text: "#4c4f69", Muted: "#8c8fa1",
		SelectedSurface: "#ccd0da", UnfocusedSurface: "#e6e9ef", Rule: "#9ca0b0",
		Warn: "#df8e1d", Err: "#d20f39", Teal: "#179299", Success: "#40a02b",
	},
	ThemePlain: {
		Name: ThemePlain, NoColor: true,
	},
}

// envLookup is the os.Getenv seam so tests can control $NO_COLOR/$SHEP_THEME
// without mutating the real process environment.
var envLookup = os.Getenv

// resolveThemeName applies the documented precedence and returns a theme
// name guaranteed to exist in themes (falling back to ThemeMocha for an
// empty or unrecognized configTheme).
func resolveThemeName(configTheme string) string {
	if envLookup("NO_COLOR") != "" {
		return ThemePlain
	}
	if name := envLookup("SHEP_THEME"); name != "" {
		if _, ok := themes[name]; ok {
			return name
		}
	}
	if _, ok := themes[configTheme]; ok {
		return configTheme
	}
	return ThemeMocha
}

// resolveTheme resolves configTheme (typically Layout.Theme, itself sourced
// from config.TUIConfig.Theme) into a concrete Theme.
func resolveTheme(configTheme string) Theme {
	return themes[resolveThemeName(configTheme)]
}

// styleSet is the full set of lipgloss styles the picker renders with,
// derived once per Model from its resolved Theme (see newPalette). Replaces
// the old package-level `palette` var so per-Model theming never risks a
// data race between parallel tests or a future multi-theme session.
//
// Selection is split into a gutter (cursorGutter*Style) plus a surface
// background (cursorSurface*Style) instead of one full-row cursorStyle: the
// gutter is a leading glyph-only indicator (a colored "❯" chevron, NO
// background fill — a solid background block behind a thin chevron looks
// like it obscures the marker rather than pointing at it, TRL-2) and the
// surface tints the row's background while the row's OWN text style
// (groupHeader/rowDescendant/row) is preserved — see render.go's
// renderSelectedFromParts. The *Unfocused variants apply when the preview
// pane owns focus (FocusPreview).
type styleSet struct {
	queryStyle                  lipgloss.Style
	rowStyle                    lipgloss.Style
	mutedStyle                  lipgloss.Style
	labelStyle                  lipgloss.Style
	previewLoadingStyle         lipgloss.Style
	previewErrStyle             lipgloss.Style
	borderStyle                 lipgloss.Style
	focusedBorderStyle          lipgloss.Style
	statusIdleStyle             lipgloss.Style
	statusWorkingStyle          lipgloss.Style
	statusBlockedStyle          lipgloss.Style
	statusDoneStyle             lipgloss.Style
	statusUnknownStyle          lipgloss.Style
	previewHeadingStyle         lipgloss.Style
	helpHeadingStyle            lipgloss.Style
	cursorGutterStyle           lipgloss.Style
	cursorSurfaceStyle          lipgloss.Style
	cursorGutterUnfocusedStyle  lipgloss.Style
	cursorSurfaceUnfocusedStyle lipgloss.Style
	rowDescendantStyle          lipgloss.Style
}

// newPalette builds the full styleSet for t. A NoColor theme (ThemePlain, or
// $NO_COLOR forced) never sets Foreground/Background: it relies purely on
// structural attributes (bold/italic/underline/reverse/faint) plus textual
// markers elsewhere in the rendering code, never emitting a color escape.
//
// Plain selection treatment: the focused gutter is reverse-video and the
// unfocused gutter faint; the surface is faint (the closest structural
// attribute to a "tinted row background" lipgloss offers without color — a
// documented choice matching how the muted role renders as Faint elsewhere).
func newPalette(t Theme) styleSet {
	if t.NoColor {
		return styleSet{
			queryStyle:                  lipgloss.NewStyle().Bold(true),
			rowStyle:                    lipgloss.NewStyle(),
			mutedStyle:                  lipgloss.NewStyle().Faint(true),
			labelStyle:                  lipgloss.NewStyle().Faint(true),
			previewLoadingStyle:         lipgloss.NewStyle().Faint(true).Italic(true),
			previewErrStyle:             lipgloss.NewStyle().Bold(true).Underline(true),
			borderStyle:                 lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1),
			focusedBorderStyle:          lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Bold(true).Padding(0, 1),
			statusIdleStyle:             lipgloss.NewStyle().Faint(true),
			statusWorkingStyle:          lipgloss.NewStyle().Bold(true),
			statusBlockedStyle:          lipgloss.NewStyle().Bold(true).Underline(true),
			statusDoneStyle:             lipgloss.NewStyle(),
			statusUnknownStyle:          lipgloss.NewStyle().Faint(true),
			previewHeadingStyle:         lipgloss.NewStyle().Bold(true).Underline(true),
			helpHeadingStyle:            lipgloss.NewStyle().Bold(true),
			cursorGutterStyle:           lipgloss.NewStyle().Reverse(true),
			cursorSurfaceStyle:          lipgloss.NewStyle().Faint(true),
			cursorGutterUnfocusedStyle:  lipgloss.NewStyle().Faint(true),
			cursorSurfaceUnfocusedStyle: lipgloss.NewStyle().Faint(true),
			rowDescendantStyle:          lipgloss.NewStyle().Italic(true),
		}
	}
	return styleSet{
		queryStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true),
		rowStyle:                    lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)),
		mutedStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),
		labelStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),
		previewLoadingStyle:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true),
		previewErrStyle:             lipgloss.NewStyle().Foreground(lipgloss.Color(t.Err)).Italic(true),
		borderStyle:                 lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(t.Rule)).Padding(0, 1),
		focusedBorderStyle:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(t.Accent)).Padding(0, 1),
		statusIdleStyle:             lipgloss.NewStyle().Foreground(lipgloss.Color(t.Success)),
		statusWorkingStyle:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.Warn)).Bold(true),
		statusBlockedStyle:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.Err)).Bold(true),
		statusDoneStyle:             lipgloss.NewStyle().Foreground(lipgloss.Color(t.Teal)),
		statusUnknownStyle:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),
		previewHeadingStyle:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true),
		helpHeadingStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)).Bold(true),
		cursorGutterStyle:           lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)),
		cursorSurfaceStyle:          lipgloss.NewStyle().Background(lipgloss.Color(t.SelectedSurface)),
		cursorGutterUnfocusedStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Rule)),
		cursorSurfaceUnfocusedStyle: lipgloss.NewStyle().Background(lipgloss.Color(t.UnfocusedSurface)),
		rowDescendantStyle:          lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true),
	}
}

// statusStyle maps an agent_status value ("idle", "working", "blocked",
// "done") to its dedicated style in s. Any other value — including the
// explicit "unknown" status Herdr itself may report, and any unrecognized
// string — falls back to statusUnknownStyle.
func (s styleSet) statusStyle(status string) lipgloss.Style {
	switch status {
	case "idle":
		return s.statusIdleStyle
	case "working":
		return s.statusWorkingStyle
	case "blocked":
		return s.statusBlockedStyle
	case "done":
		return s.statusDoneStyle
	default:
		return s.statusUnknownStyle
	}
}
