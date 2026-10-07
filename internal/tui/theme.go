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
// secondary, selectedSurface/unfocusedSurface=cursor row and active tab
// backgrounds, rule=rules/dividers+unfocused gutter, warn=working status, err=blocked status+preview
// error, teal=done status, success=idle status) so the picker's visual
// language can be re-skinned by swapping one Theme for another.
//
// The working/blocked/done/idle status role assignments mirror herdr's own
// verified agent_icon convention (herdr's src/ui/status.rs): working=warn
// (yellow), blocked=err (red), done=teal, idle=success (green), unknown=
// muted — see statusGlyph in rowrender.go.
type Theme struct {
	Name             string
	NoColor          bool
	Accent           string // accent: prompt, match highlights, active tab, cursor
	Text             string // text: labels/main content, help headings
	Secondary        string // secondary: paths, metadata, footer hint action labels
	Muted            string // muted: genuinely low-priority text (footer hints counts, unknown status, descendant rows)
	SelectedSurface  string // selectedSurface: focused cursor row background
	UnfocusedSurface string // unfocusedSurface: cursor row background when the preview owns focus
	Rule             string // rule: rules/dividers/separators, unfocused cursor gutter
	Warn             string // warn (Yellow): working status, pin marker, confirmations
	Err              string // err (Red): blocked status, preview error
	Teal             string // teal: done status, session row icons
	Success          string // success (Green): idle status, open Herdr workspace icons
	Blue             string // blue: zoxide row icons
	Peach            string // peach: project and worktree row icons
	Lavender         string // lavender: configured workspace row icons
	Sky              string // sky: custom source row icons
}

// Catppuccin's four official flavors. Hex values are the semantic token
// table (accent/text/secondary/muted/selectedSurface/unfocusedSurface/rule/
// warn/err/teal/success, plus the blue/peach/lavender/sky icon roles),
// sourced from https://catppuccin.com per flavor.
//
// The working/blocked/done/idle status role assignments mirror herdr's own
// verified agent_icon convention (herdr's src/ui/status.rs): working=warn
// (yellow), blocked=err (red), done=teal, idle=success (green), unknown=
// muted — see statusGlyph in rowrender.go.
var themes = map[string]Theme{
	ThemeMocha: {
		Name: ThemeMocha, Accent: "#cba6f7", Text: "#cdd6f4", Secondary: "#a6adc8", Muted: "#6c7086",
		SelectedSurface: "#313244", UnfocusedSurface: "#1e1e2e", Rule: "#585b70",
		Warn: "#f9e2af", Err: "#f38ba8", Teal: "#94e2d5", Success: "#a6e3a1",
		Blue: "#89b4fa", Peach: "#fab387", Lavender: "#b4befe", Sky: "#89dceb",
	},
	ThemeMacchiato: {
		Name: ThemeMacchiato, Accent: "#c6a0f6", Text: "#cad3f5", Secondary: "#a5adcb", Muted: "#6e738d",
		SelectedSurface: "#363a4f", UnfocusedSurface: "#1e2030", Rule: "#5b6078",
		Warn: "#eed49f", Err: "#ed8796", Teal: "#8bd5ca", Success: "#a6da95",
		Blue: "#8aadf4", Peach: "#f5a97f", Lavender: "#b7bdf8", Sky: "#91d7e3",
	},
	ThemeFrappe: {
		Name: ThemeFrappe, Accent: "#ca9ee6", Text: "#c6d0f5", Secondary: "#a5b0ce", Muted: "#737994",
		SelectedSurface: "#414559", UnfocusedSurface: "#292c3c", Rule: "#626880",
		Warn: "#e5c890", Err: "#e78284", Teal: "#81c8be", Success: "#a6d189",
		Blue: "#8caaee", Peach: "#ef9f76", Lavender: "#babbf1", Sky: "#99d1db",
	},
	ThemeLatte: {
		Name: ThemeLatte, Accent: "#8839ef", Text: "#4c4f69", Secondary: "#5c5f77", Muted: "#8c8fa1",
		SelectedSurface: "#ccd0da", UnfocusedSurface: "#e6e9ef", Rule: "#9ca0b0",
		Warn: "#df8e1d", Err: "#d20f39", Teal: "#179299", Success: "#40a02b",
		Blue: "#1e66f5", Peach: "#fe640b", Lavender: "#7287fd", Sky: "#04a5e5",
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
// data race between parallel tests or a future multi-theme session. It is
// immutable once built and shared by pointer: a lipgloss.Style is over 500
// bytes, and Bubble Tea copies the Model on every Update and value-receiver
// call, so embedding the set would copy tens of kilobytes per call.
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
//
// The chrome styles (tab*, prompt, queryText/queryCursor/placeholder, title,
// rule, warn) render the borderless grid's tab strip, prompt row, preview
// title, rules and footer — see chrome.go.
//
// The plain (NoColor) fallbacks rely purely on structural attributes:
// key tokens are bold; rules are faint; the active tab and the prompt cursor
// are reverse video.
type styleSet struct {
	queryStyle                  lipgloss.Style
	rowStyle                    lipgloss.Style
	mutedStyle                  lipgloss.Style
	secondaryStyle              lipgloss.Style
	labelStyle                  lipgloss.Style
	previewLoadingStyle         lipgloss.Style
	previewErrStyle             lipgloss.Style
	ruleStyle                   lipgloss.Style
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
	tabActiveStyle              lipgloss.Style
	tabActiveBlockedStyle       lipgloss.Style
	promptStyle                 lipgloss.Style
	queryTextStyle              lipgloss.Style
	queryCursorStyle            lipgloss.Style
	placeholderStyle            lipgloss.Style
	titleStyle                  lipgloss.Style
	warnStyle                   lipgloss.Style

	// iconStyles colors a row's icon by its iconRole (unstyled in plain).
	iconStyles [iconRoleCount]lipgloss.Style
	// rowPlain, rowSelected and rowSelectedUnfocused are the prebuilt
	// segment styles of a list row in each selection state (see rowStyles).
	rowPlain             rowStyles
	rowSelected          rowStyles
	rowSelectedUnfocused rowStyles
}

// rowStyles is every segment style one list row renders with in one
// selection state. The three states are prebuilt per theme so rendering a
// row never builds a style: a selected row carries the selection surface on
// every segment after the gutter, and its primary text is bold.
type rowStyles struct {
	// hasSurface reports a selection surface: blanks must be styled too, or
	// the row's background would show gaps.
	hasSurface                                                          bool
	surface                                                             lipgloss.Style // blanks between segments and the fill
	gutter                                                              lipgloss.Style // the two-cell cursor gutter (never on the surface)
	marker                                                              lipgloss.Style // the active-focus marker
	tree                                                                lipgloss.Style // tree indentation glyphs
	icons                                                               [iconRoleCount]lipgloss.Style
	primary                                                             lipgloss.Style
	descendant                                                          lipgloss.Style // primary text of a descendant-only match
	highlight                                                           lipgloss.Style // runes the query matched
	secondary                                                           lipgloss.Style // the filename-first parent path
	muted                                                               lipgloss.Style // plain accessories
	err                                                                 lipgloss.Style // the "missing" accessory
	pin                                                                 lipgloss.Style // the pinned accessory
	statusIdle, statusWorking, statusBlocked, statusDone, statusUnknown lipgloss.Style
}

// statusStyle maps an agent status word to its glyph style, mirroring
// styleSet.statusStyle.
func (r *rowStyles) statusStyle(status string) lipgloss.Style {
	switch status {
	case "idle":
		return r.statusIdle
	case "working":
		return r.statusWorking
	case "blocked":
		return r.statusBlocked
	case "done":
		return r.statusDone
	default:
		return r.statusUnknown
	}
}

// newRowStyles derives one selection state's row styles from s. surface is
// merged into every segment (see applySurface); gutter styles the cursor
// gutter alone.
func newRowStyles(s *styleSet, surface, gutter lipgloss.Style, selected bool) rowStyles {
	on := func(style lipgloss.Style) lipgloss.Style { return applySurface(style, surface) }
	r := rowStyles{
		hasSurface:    surface.GetBackground() != (lipgloss.NoColor{}) || surface.GetFaint(),
		surface:       on(lipgloss.NewStyle()),
		gutter:        gutter,
		marker:        on(s.promptStyle),
		tree:          on(s.ruleStyle),
		primary:       on(s.rowStyle.Bold(selected)),
		descendant:    on(s.rowDescendantStyle.Bold(selected)),
		highlight:     on(s.queryStyle),
		secondary:     on(s.mutedStyle),
		muted:         on(s.mutedStyle),
		err:           on(s.previewErrStyle),
		pin:           on(s.pinStyle),
		statusIdle:    on(s.statusIdleStyle),
		statusWorking: on(s.statusWorkingStyle),
		statusBlocked: on(s.statusBlockedStyle),
		statusDone:    on(s.statusDoneStyle),
		statusUnknown: on(s.statusUnknownStyle),
	}
	for role, style := range s.iconStyles {
		r.icons[role] = on(style)
	}
	return r
}

// newIconStyles colors icons by source family (see sourceIconRole): open
// Herdr workspaces green (live), configured workspaces lavender, zoxide
// blue, projects and worktrees peach, sessions teal, agents in the accent,
// custom sources sky. Tab rows keep their glyph muted. The plain theme
// leaves every source icon unstyled.
func newIconStyles(t Theme, muted lipgloss.Style) [iconRoleCount]lipgloss.Style {
	var icons [iconRoleCount]lipgloss.Style
	icons[iconRoleTab] = muted
	if t.NoColor {
		return icons
	}
	fg := func(color string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(color)) }
	icons[iconRoleHerdr] = fg(t.Success)
	icons[iconRoleWorkspaces] = fg(t.Lavender)
	icons[iconRoleZoxide] = fg(t.Blue)
	icons[iconRoleProjects] = fg(t.Peach)
	icons[iconRoleSessions] = fg(t.Teal)
	icons[iconRoleAgents] = fg(t.Accent)
	icons[iconRoleCustom] = fg(t.Sky)
	return icons
}

// newPalette builds the full styleSet for t. A NoColor theme (ThemePlain, or
// $NO_COLOR forced) never sets Foreground/Background: it relies purely on
// structural attributes (bold/italic/underline/reverse/faint) plus textual
// markers elsewhere in the rendering code, never emitting a color escape.
//
// Plain selection treatment: the selected row must stand out from its
// neighbours, so its gutter glyph is bold reverse video and its primary text
// bold (see newRowStyles); there is no surface at all. A faint surface — the
// closest attribute to a tinted background — made the selection the
// dimmest row on screen.
func newPalette(t Theme) *styleSet {
	s := baseStyles(t)
	s.iconStyles = newIconStyles(t, s.mutedStyle)
	s.rowPlain = newRowStyles(&s, lipgloss.Style{}, lipgloss.Style{}, false)
	s.rowSelected = newRowStyles(&s, s.cursorSurfaceStyle, s.cursorGutterStyle, true)
	s.rowSelectedUnfocused = newRowStyles(&s, s.cursorSurfaceUnfocusedStyle, s.cursorGutterUnfocusedStyle, true)
	return &s
}

// baseStyles builds the role styles every other style derives from.
func baseStyles(t Theme) styleSet {
	if t.NoColor {
		return styleSet{
			queryStyle:                  lipgloss.NewStyle().Bold(true),
			rowStyle:                    lipgloss.NewStyle(),
			mutedStyle:                  lipgloss.NewStyle().Faint(true),
			secondaryStyle:              lipgloss.NewStyle().Faint(true),
			labelStyle:                  lipgloss.NewStyle().Faint(true),
			previewLoadingStyle:         lipgloss.NewStyle().Faint(true).Italic(true),
			previewErrStyle:             lipgloss.NewStyle().Bold(true).Underline(true),
			ruleStyle:                   lipgloss.NewStyle().Faint(true),
			statusIdleStyle:             lipgloss.NewStyle().Faint(true),
			statusWorkingStyle:          lipgloss.NewStyle().Bold(true),
			statusBlockedStyle:          lipgloss.NewStyle().Bold(true).Underline(true),
			statusDoneStyle:             lipgloss.NewStyle(),
			statusUnknownStyle:          lipgloss.NewStyle().Faint(true),
			previewHeadingStyle:         lipgloss.NewStyle().Bold(true).Underline(true),
			helpHeadingStyle:            lipgloss.NewStyle().Bold(true),
			cursorGutterStyle:           lipgloss.NewStyle().Reverse(true).Bold(true),
			cursorSurfaceStyle:          lipgloss.NewStyle(),
			cursorGutterUnfocusedStyle:  lipgloss.NewStyle().Faint(true),
			cursorSurfaceUnfocusedStyle: lipgloss.NewStyle(),
			rowDescendantStyle:          lipgloss.NewStyle().Italic(true),
			pinStyle:                    lipgloss.NewStyle(),
			keycapStyle:                 lipgloss.NewStyle().Bold(true),
			keycapLabelStyle:            lipgloss.NewStyle(),
			tabActiveStyle:              lipgloss.NewStyle().Reverse(true).Bold(true),
			tabActiveBlockedStyle:       lipgloss.NewStyle().Reverse(true).Bold(true).Underline(true),
			promptStyle:                 lipgloss.NewStyle().Bold(true),
			queryTextStyle:              lipgloss.NewStyle().Bold(true),
			queryCursorStyle:            lipgloss.NewStyle().Reverse(true),
			placeholderStyle:            lipgloss.NewStyle().Faint(true).Italic(true),
			titleStyle:                  lipgloss.NewStyle().Bold(true),
			warnStyle:                   lipgloss.NewStyle().Bold(true),
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
		ruleStyle:                   lipgloss.NewStyle().Foreground(lipgloss.Color(t.Rule)),
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
		tabActiveStyle:              lipgloss.NewStyle().Background(lipgloss.Color(t.SelectedSurface)).Foreground(lipgloss.Color(t.Accent)).Bold(true),
		tabActiveBlockedStyle:       lipgloss.NewStyle().Background(lipgloss.Color(t.SelectedSurface)).Foreground(lipgloss.Color(t.Err)).Bold(true),
		promptStyle:                 lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true),
		queryTextStyle:              lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)).Bold(true),
		queryCursorStyle:            lipgloss.NewStyle().Background(lipgloss.Color(t.Accent)),
		placeholderStyle:            lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)).Italic(true),
		titleStyle:                  lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)).Bold(true),
		warnStyle:                   lipgloss.NewStyle().Foreground(lipgloss.Color(t.Warn)),
	}
}

// statusStyle maps an agent_status value ("idle", "working", "blocked",
// "done") to its dedicated style in s. Any other value — including the
// explicit "unknown" status Herdr itself may report, and any unrecognized
// string — falls back to statusUnknownStyle.
func (s *styleSet) statusStyle(status string) lipgloss.Style {
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
