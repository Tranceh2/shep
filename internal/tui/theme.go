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
// internal/command/open.go) when not "inherit"; then Herdr's active theme
// when config is empty or "inherit"; then "mocha" as the final default. An
// unknown theme name at any tier falls back to the next tier rather than
// erroring — the picker must never fail to start over a typo'd theme name.
package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/config"
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
	Accent           string // accent: query text, active focus, focused border, integrations badge
	Text             string // text: labels/main content, help headings
	Secondary        string // secondary: paths, metadata, footer hint action labels
	Muted            string // muted: genuinely low-priority text (footer hints counts, unknown status, descendant rows)
	SelectedSurface  string // selectedSurface: focused cursor row background
	UnfocusedSurface string // unfocusedSurface: cursor row background when the preview owns focus
	Rule             string // rule: borders/separators, unfocused cursor gutter
	Warn             string // warn (Yellow): working status, pin marker
	Err              string // err (Red): blocked status, preview error
	Teal             string // teal: done status
	Success          string // success (Green): idle status
	SourceHerdr      string // source badge: Herdr workspaces
	SourceProjects   string // source badge: discovered projects
	SourceZoxide     string // source badge: zoxide directories
	SourceWorkspaces string // source badge: configured [[workspaces]] entries
	SourceSessions   string // source badge: sessions
}

// Catppuccin's four official flavors. Hex values are the semantic token
// table (accent/text/secondary/muted/selectedSurface/unfocusedSurface/rule/
// warn/err/teal/success + per-source badge colors), sourced from
// https://catppuccin.com per flavor. The color roles follow the visual
// redesign: accent = query text/active focus/focused border/integrations,
// text = labels/main content, secondary = paths/metadata, muted = only
// genuinely low-priority text, source badge colors differentiate the picker's
// sources (herdr Blue, projects Green, zoxide Teal, workspaces Yellow,
// sessions Peach); every badge carries a text label so color alone is never
// the signal. selectedSurface is the focused row surface (Catppuccin
// surface1), unfocusedSurface the surface1 shade down (base/overlay tones),
// rule the border hex, warn Catppuccin Yellow, err Catppuccin Red (also
// backs preview errors), teal/success the official Teal/Green swatches.
//
// The working/blocked/done/idle status role assignments mirror herdr's own
// verified agent_icon convention (herdr's src/ui/status.rs): working=warn
// (yellow), blocked=err (red), done=teal, idle=success (green), unknown=
// muted — see agentStatusIcon in render.go.
var themes = map[string]Theme{
	ThemeMocha: {
		Name: ThemeMocha, Accent: "#cba6f7", Text: "#cdd6f4", Secondary: "#a6adc8", Muted: "#6c7086",
		SelectedSurface: "#313244", UnfocusedSurface: "#1e1e2e", Rule: "#585b70",
		Warn: "#f9e2af", Err: "#f38ba8", Teal: "#94e2d5", Success: "#a6e3a1",
		SourceHerdr: "#89b4fa", SourceProjects: "#a6e3a1", SourceZoxide: "#94e2d5",
		SourceWorkspaces: "#f9e2af", SourceSessions: "#fab387",
	},
	ThemeMacchiato: {
		Name: ThemeMacchiato, Accent: "#c6a0f6", Text: "#cad3f5", Secondary: "#a5adcb", Muted: "#6e738d",
		SelectedSurface: "#363a4f", UnfocusedSurface: "#1e2030", Rule: "#5b6078",
		Warn: "#eed49f", Err: "#ed8796", Teal: "#8bd5ca", Success: "#a6da95",
		SourceHerdr: "#8aadf4", SourceProjects: "#a6da95", SourceZoxide: "#8bd5ca",
		SourceWorkspaces: "#eed49f", SourceSessions: "#f5a97f",
	},
	ThemeFrappe: {
		Name: ThemeFrappe, Accent: "#ca9ee6", Text: "#c6d0f5", Secondary: "#a5b0ce", Muted: "#737994",
		SelectedSurface: "#414559", UnfocusedSurface: "#292c3c", Rule: "#626880",
		Warn: "#e5c890", Err: "#e78284", Teal: "#81c8be", Success: "#a6d189",
		SourceHerdr: "#8caaee", SourceProjects: "#a6d189", SourceZoxide: "#81c8be",
		SourceWorkspaces: "#e5c890", SourceSessions: "#ef9f76",
	},
	ThemeLatte: {
		Name: ThemeLatte, Accent: "#8839ef", Text: "#4c4f69", Secondary: "#5c5f77", Muted: "#8c8fa1",
		SelectedSurface: "#ccd0da", UnfocusedSurface: "#e6e9ef", Rule: "#9ca0b0",
		Warn: "#df8e1d", Err: "#d20f39", Teal: "#179299", Success: "#40a02b",
		SourceHerdr: "#1e66f5", SourceProjects: "#40a02b", SourceZoxide: "#179299",
		SourceWorkspaces: "#df8e1d", SourceSessions: "#fe640b",
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
//
// Precedence:
//  1. $NO_COLOR (non-empty -> ThemePlain)
//  2. $SHEP_THEME (recognized only)
//  3. configTheme (recognized, != "inherit")
//  4. Herdr [theme].name (consulted only when configTheme is "" or "inherit")
//  5. ThemeMocha default
func resolveThemeName(configTheme string) string {
	if envLookup("NO_COLOR") != "" {
		return ThemePlain
	}
	if name := envLookup("SHEP_THEME"); name != "" {
		if _, ok := themes[name]; ok {
			return name
		}
	}
	if configTheme != "" && configTheme != "inherit" {
		if _, ok := themes[configTheme]; ok {
			return configTheme
		}
	}
	if configTheme == "" || configTheme == "inherit" {
		if name, ok := herdrThemeName(); ok {
			return name
		}
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
// The plain (NoColor) fallbacks rely purely on structural attributes: a
// source badge emphasizes with bold and dims with faint; the keycap token is
// bold; the preview rule is faint.
type styleSet struct {
	queryStyle                  lipgloss.Style
	rowStyle                    lipgloss.Style
	mutedStyle                  lipgloss.Style
	secondaryStyle              lipgloss.Style
	labelStyle                  lipgloss.Style
	previewLoadingStyle         lipgloss.Style
	previewErrStyle             lipgloss.Style
	previewRuleStyle            lipgloss.Style
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
	pinStyle                    lipgloss.Style
	keycapStyle                 lipgloss.Style
	keycapLabelStyle            lipgloss.Style
	sourceHerdrStyle            lipgloss.Style
	sourceProjectsStyle         lipgloss.Style
	sourceZoxideStyle           lipgloss.Style
	sourceWorkspacesStyle       lipgloss.Style
	sourceSessionsStyle         lipgloss.Style
	sourceBadgeStyle            lipgloss.Style // integration/unknown source badges
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
			secondaryStyle:              lipgloss.NewStyle().Faint(true),
			labelStyle:                  lipgloss.NewStyle().Faint(true),
			previewLoadingStyle:         lipgloss.NewStyle().Faint(true).Italic(true),
			previewErrStyle:             lipgloss.NewStyle().Bold(true).Underline(true),
			previewRuleStyle:            lipgloss.NewStyle().Faint(true),
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
			pinStyle:                    lipgloss.NewStyle(),
			keycapStyle:                 lipgloss.NewStyle().Bold(true),
			keycapLabelStyle:            lipgloss.NewStyle(),
			sourceHerdrStyle:            lipgloss.NewStyle().Bold(true),
			sourceProjectsStyle:         lipgloss.NewStyle().Bold(true),
			sourceZoxideStyle:           lipgloss.NewStyle().Bold(true),
			sourceWorkspacesStyle:       lipgloss.NewStyle().Bold(true),
			sourceSessionsStyle:         lipgloss.NewStyle().Bold(true),
			sourceBadgeStyle:            lipgloss.NewStyle().Bold(true),
		}
	}
	return styleSet{
		queryStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true),
		rowStyle:                    lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)),
		mutedStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),
		secondaryStyle:              lipgloss.NewStyle().Foreground(lipgloss.Color(t.Secondary)),
		labelStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),
		previewLoadingStyle:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true),
		previewErrStyle:             lipgloss.NewStyle().Foreground(lipgloss.Color(t.Err)).Italic(true),
		previewRuleStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.Rule)),
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
		pinStyle:                    lipgloss.NewStyle().Foreground(lipgloss.Color(t.Warn)),
		keycapStyle:                 lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)).Bold(true),
		keycapLabelStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.Secondary)),
		sourceHerdrStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.SourceHerdr)),
		sourceProjectsStyle:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.SourceProjects)),
		sourceZoxideStyle:           lipgloss.NewStyle().Foreground(lipgloss.Color(t.SourceZoxide)),
		sourceWorkspacesStyle:       lipgloss.NewStyle().Foreground(lipgloss.Color(t.SourceWorkspaces)),
		sourceSessionsStyle:         lipgloss.NewStyle().Foreground(lipgloss.Color(t.SourceSessions)),
		sourceBadgeStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)),
	}
}

// sourceBadgeStyleFor maps a source.Candidate.Source value to the dedicated
// source-badge style in s. The declared integration/unknown sources fall back
// to the integration accent badge (the integration's name is uppercased by
// the rendering helper; the color leans on the accent so integrations read as
// third-party surface).
func (s styleSet) sourceBadgeStyleFor(source string) lipgloss.Style {
	switch source {
	case config.SourceHerdr:
		return s.sourceHerdrStyle
	case config.SourceProjects:
		return s.sourceProjectsStyle
	case config.SourceZoxide:
		return s.sourceZoxideStyle
	case config.SourceWorkspaces:
		return s.sourceWorkspacesStyle
	case config.SourceSessions:
		return s.sourceSessionsStyle
	default:
		return s.sourceBadgeStyle
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
