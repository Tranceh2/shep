package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/theme"
)

// theme.go builds the picker's styles from a resolved theme.Theme. The
// command layer selects the theme once (theme.Select: NO_COLOR, SHEP_THEME,
// [tui].theme, Herdr inheritance) and hands it over in Layout.Theme; every
// style here is a semantic role of that theme plus the structural attributes
// (bold, italic, faint, reverse) the picker's visual language uses.

// defaultTheme is the theme of a Layout that carries none (a direct test or
// programmatic construction): Herdr's own default, catppuccin.
func defaultTheme() theme.Theme {
	t, err := theme.Build(theme.NameDefault, nil, theme.HerdrTheme{}, true)
	if err != nil {
		panic("tui: built-in theme " + theme.NameDefault + ": " + err.Error())
	}
	return t
}

// styleSet is the full set of lipgloss styles the picker renders with,
// derived once per Model from its theme (see newPalette). It is immutable
// once built and shared by pointer: a lipgloss.Style is over 500 bytes, and
// Bubble Tea copies the Model on every Update and value-receiver call, so
// embedding the set would copy tens of kilobytes per call.
//
// Selection is a gutter (cursorGutterStyle: a colored "❯", no background)
// plus a surface (cursorSurfaceStyle: the selection background) merged into
// every segment of the selected row, so the row's own text roles survive
// (see newRowStyles).
//
// A no-color theme never sets a color: it keeps the structural attributes
// only — key tokens and the selected row are bold, secondary text and rules
// faint, the active tab and the prompt cursor reverse video.
type styleSet struct {
	queryStyle            lipgloss.Style // match: characters the search matched
	rowStyle              lipgloss.Style // text: ordinary text
	mutedStyle            lipgloss.Style // text.muted: counts, hints, unknown values
	secondaryStyle        lipgloss.Style // text.secondary: paths, inactive tabs
	accentStyle           lipgloss.Style // accent: the accent template function
	previewLoadingStyle   lipgloss.Style
	previewErrStyle       lipgloss.Style // error
	ruleStyle             lipgloss.Style // rule: rules, dividers, tree glyphs
	statusIdleStyle       lipgloss.Style
	statusWorkingStyle    lipgloss.Style
	statusBlockedStyle    lipgloss.Style
	statusDoneStyle       lipgloss.Style
	statusUnknownStyle    lipgloss.Style
	previewHeadingStyle   lipgloss.Style // heading
	cursorGutterStyle     lipgloss.Style // cursor
	cursorSurfaceStyle    lipgloss.Style // selection (background)
	rowLabelStyle         lipgloss.Style // row.label
	rowDetailStyle        lipgloss.Style // row.detail
	rowMarkerStyle        lipgloss.Style // row.marker
	rowDescendantStyle    lipgloss.Style // row.descendant
	pinStyle              lipgloss.Style // pin
	keycapStyle           lipgloss.Style
	keycapLabelStyle      lipgloss.Style
	tabActiveStyle        lipgloss.Style // tab.active (background), tab.active.fg
	tabActiveBlockedStyle lipgloss.Style
	promptStyle           lipgloss.Style // prompt
	queryTextStyle        lipgloss.Style
	queryCursorStyle      lipgloss.Style // cursor (background)
	placeholderStyle      lipgloss.Style
	titleStyle            lipgloss.Style
	warnStyle             lipgloss.Style // warning
	successStyle          lipgloss.Style // success
	gitBranchStyle        lipgloss.Style // git.branch
	gitCleanStyle         lipgloss.Style // git.clean
	gitChangesStyle       lipgloss.Style // git.changes

	// rowPlain and rowSelected are the prebuilt segment styles of a list row
	// in each selection state (see rowStyles).
	rowPlain    rowStyles
	rowSelected rowStyles
}

// rowStyles is every segment style one list row renders with in one
// selection state. Both states are prebuilt per theme so rendering a row
// never builds a style: a selected row carries the selection surface on
// every segment after the gutter, and its label is bold.
type rowStyles struct {
	// hasSurface reports a selection surface: blanks must be styled too, or
	// the row's background would show gaps.
	hasSurface bool
	surface    lipgloss.Style // blanks between segments and the fill
	gutter     lipgloss.Style // the two-cell cursor gutter (never on the surface)
	tree       lipgloss.Style // tree indentation glyphs
	// icons holds one style per icon color reference (see rowFormats.iconRefs),
	// and iconWraps the same styles rendered once (see styleWrap).
	icons      []lipgloss.Style
	iconWraps  []styleWrap
	label      lipgloss.Style // the label part
	descendant lipgloss.Style // the label of a descendant-only match
	detail     lipgloss.Style // the detail part
	marker     lipgloss.Style // the marker part
	muted      lipgloss.Style // the muted template function
	accent     lipgloss.Style // the accent template function
	highlight  lipgloss.Style // runes the query matched
	err        lipgloss.Style // the missing marker
	pin        lipgloss.Style // the pin marker

	statusIdle, statusWorking, statusBlocked, statusDone, statusUnknown lipgloss.Style
}

// newPalette builds the full styleSet for t. iconRefs are the icon color
// references of the row presentations, each a token, a role or a color (see
// theme.Theme.Resolve); rowStyles.icons holds their styles in the same order.
//
// Plain selection treatment: the selected row must stand out from its
// neighbours, so its gutter glyph is bold reverse video and its label bold;
// there is no surface at all (a faint surface made the selection the dimmest
// row on screen).
func newPalette(t theme.Theme, iconRefs []string) *styleSet {
	s := baseStyles(t)
	icons := make([]lipgloss.Style, len(iconRefs))
	for i, ref := range iconRefs {
		icons[i] = iconStyle(t, ref)
	}
	s.rowPlain = newRowStyles(&s, icons, lipgloss.Style{}, lipgloss.Style{}, false)
	s.rowSelected = newRowStyles(&s, icons, s.cursorSurfaceStyle, s.cursorGutterStyle, true)
	return &s
}

// newRowStyles derives one selection state's row styles from s. surface is
// merged into every segment (see applySurface); gutter styles the cursor
// gutter alone.
func newRowStyles(s *styleSet, icons []lipgloss.Style, surface, gutter lipgloss.Style, selected bool) rowStyles {
	on := func(style lipgloss.Style) lipgloss.Style { return applySurface(style, surface) }
	r := rowStyles{
		hasSurface:    surface.GetBackground() != (lipgloss.NoColor{}) || surface.GetFaint(),
		surface:       on(lipgloss.NewStyle()),
		gutter:        gutter,
		tree:          on(s.ruleStyle),
		icons:         make([]lipgloss.Style, len(icons)),
		iconWraps:     make([]styleWrap, len(icons)),
		label:         on(s.rowLabelStyle.Bold(selected)),
		descendant:    on(s.rowDescendantStyle.Bold(selected)),
		detail:        on(s.rowDetailStyle),
		marker:        on(s.rowMarkerStyle),
		muted:         on(s.mutedStyle),
		accent:        on(s.accentStyle),
		highlight:     on(s.queryStyle),
		err:           on(s.previewErrStyle),
		pin:           on(s.pinStyle),
		statusIdle:    on(s.statusIdleStyle),
		statusWorking: on(s.statusWorkingStyle),
		statusBlocked: on(s.statusBlockedStyle),
		statusDone:    on(s.statusDoneStyle),
		statusUnknown: on(s.statusUnknownStyle),
	}
	for i, style := range icons {
		r.icons[i] = on(style)
		r.iconWraps[i] = newStyleWrap(r.icons[i])
	}
	return r
}

// styleWrap is a style rendered once: the escape sequences it opens and
// closes a single line of plain text with. Writing a static segment (a row's
// icon) through it costs no Render call — and no allocation — per frame.
// The color profile is the one in force when the palette is built, which is
// fixed for the life of a program.
type styleWrap struct{ open, close string }

// newStyleWrap renders style around a probe rune and keeps what surrounds it.
func newStyleWrap(style lipgloss.Style) styleWrap {
	const probe = "\x00"
	open, close, ok := strings.Cut(style.Render(probe), probe)
	if !ok {
		return styleWrap{}
	}
	return styleWrap{open: open, close: close}
}

// write writes text, a single line of plain text, in the wrapped style.
func (w styleWrap) write(b *strings.Builder, text string) {
	b.WriteString(w.open)
	b.WriteString(text)
	b.WriteString(w.close)
}

// iconStyle colors an icon by its configured reference. A no-color theme
// keeps the structural attribute of a role reference (text.muted is faint,
// so the tab glyph stays dim) and leaves tokens and colors unstyled; an
// invalid reference (configuration validation rejects it) is unstyled.
func iconStyle(t theme.Theme, ref string) lipgloss.Style {
	if t.NoColor {
		if r, ok := refRole(ref); ok {
			return noColorRole(r)
		}
		return lipgloss.NewStyle()
	}
	c, err := t.Resolve(ref)
	if err != nil {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(c.Lipgloss())
}

// refRole reports the role a color reference names. References are looked
// up as tokens first (theme.ParseToken), so "text" and "accent" are tokens.
func refRole(ref string) (theme.Role, bool) {
	v := strings.ToLower(strings.TrimSpace(ref))
	if _, ok := theme.ParseToken(v); ok {
		return 0, false
	}
	return theme.ParseRole(v)
}

// noColorRole is the structural attribute a role keeps without color.
func noColorRole(r theme.Role) lipgloss.Style {
	switch r {
	case theme.RoleTextSecondary, theme.RoleTextMuted, theme.RoleRule, theme.RoleRowDetail,
		theme.RoleRowMarker, theme.RoleStatusIdle, theme.RoleStatusUnknown, theme.RoleGitClean, theme.RoleSuccess:
		return lipgloss.NewStyle().Faint(true)
	case theme.RoleRowDescendant:
		return lipgloss.NewStyle().Italic(true)
	case theme.RoleAccent, theme.RolePrompt, theme.RoleMatch, theme.RoleHeading, theme.RoleStatusWorking,
		theme.RoleWarning, theme.RoleGitBranch, theme.RoleGitChanges, theme.RoleTabActiveFg:
		return lipgloss.NewStyle().Bold(true)
	case theme.RoleError, theme.RoleStatusBlocked:
		return lipgloss.NewStyle().Bold(true).Underline(true)
	default:
		return lipgloss.NewStyle()
	}
}

// baseStyles builds the role styles every other style derives from.
func baseStyles(t theme.Theme) styleSet {
	if t.NoColor {
		return styleSet{
			queryStyle:            lipgloss.NewStyle().Bold(true),
			rowStyle:              lipgloss.NewStyle(),
			mutedStyle:            lipgloss.NewStyle().Faint(true),
			secondaryStyle:        lipgloss.NewStyle().Faint(true),
			accentStyle:           lipgloss.NewStyle().Bold(true),
			previewLoadingStyle:   lipgloss.NewStyle().Faint(true).Italic(true),
			previewErrStyle:       lipgloss.NewStyle().Bold(true).Underline(true),
			ruleStyle:             lipgloss.NewStyle().Faint(true),
			statusIdleStyle:       lipgloss.NewStyle().Faint(true),
			statusWorkingStyle:    lipgloss.NewStyle().Bold(true),
			statusBlockedStyle:    lipgloss.NewStyle().Bold(true).Underline(true),
			statusDoneStyle:       lipgloss.NewStyle(),
			statusUnknownStyle:    lipgloss.NewStyle().Faint(true),
			previewHeadingStyle:   lipgloss.NewStyle().Bold(true).Underline(true),
			cursorGutterStyle:     lipgloss.NewStyle().Reverse(true).Bold(true),
			cursorSurfaceStyle:    lipgloss.NewStyle(),
			rowLabelStyle:         lipgloss.NewStyle(),
			rowDetailStyle:        lipgloss.NewStyle().Faint(true),
			rowMarkerStyle:        lipgloss.NewStyle().Faint(true),
			rowDescendantStyle:    lipgloss.NewStyle().Italic(true),
			pinStyle:              lipgloss.NewStyle(),
			keycapStyle:           lipgloss.NewStyle().Bold(true),
			keycapLabelStyle:      lipgloss.NewStyle(),
			tabActiveStyle:        lipgloss.NewStyle().Reverse(true).Bold(true),
			tabActiveBlockedStyle: lipgloss.NewStyle().Reverse(true).Bold(true).Underline(true),
			promptStyle:           lipgloss.NewStyle().Bold(true),
			queryTextStyle:        lipgloss.NewStyle().Bold(true),
			queryCursorStyle:      lipgloss.NewStyle().Reverse(true),
			placeholderStyle:      lipgloss.NewStyle().Faint(true).Italic(true),
			titleStyle:            lipgloss.NewStyle().Bold(true),
			warnStyle:             lipgloss.NewStyle().Bold(true),
			successStyle:          lipgloss.NewStyle().Faint(true),
			gitBranchStyle:        lipgloss.NewStyle().Bold(true),
			gitCleanStyle:         lipgloss.NewStyle().Faint(true),
			gitChangesStyle:       lipgloss.NewStyle().Bold(true),
		}
	}
	fg := func(r theme.Role) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Role(r).Lipgloss()) }
	bg := func(r theme.Role) lipgloss.Style { return lipgloss.NewStyle().Background(t.Role(r).Lipgloss()) }
	return styleSet{
		queryStyle:            fg(theme.RoleMatch).Bold(true),
		rowStyle:              fg(theme.RoleText),
		mutedStyle:            fg(theme.RoleTextMuted),
		secondaryStyle:        fg(theme.RoleTextSecondary),
		accentStyle:           fg(theme.RoleAccent),
		previewLoadingStyle:   fg(theme.RoleTextMuted).Italic(true),
		previewErrStyle:       fg(theme.RoleError).Italic(true),
		ruleStyle:             fg(theme.RoleRule),
		statusIdleStyle:       fg(theme.RoleStatusIdle),
		statusWorkingStyle:    fg(theme.RoleStatusWorking).Bold(true),
		statusBlockedStyle:    fg(theme.RoleStatusBlocked).Bold(true),
		statusDoneStyle:       fg(theme.RoleStatusDone),
		statusUnknownStyle:    fg(theme.RoleStatusUnknown),
		previewHeadingStyle:   fg(theme.RoleHeading).Bold(true),
		cursorGutterStyle:     fg(theme.RoleCursor),
		cursorSurfaceStyle:    bg(theme.RoleSelection),
		rowLabelStyle:         fg(theme.RoleRowLabel),
		rowDetailStyle:        fg(theme.RoleRowDetail),
		rowMarkerStyle:        fg(theme.RoleRowMarker),
		rowDescendantStyle:    fg(theme.RoleRowDescendant).Italic(true),
		pinStyle:              fg(theme.RolePin),
		keycapStyle:           fg(theme.RoleText).Bold(true),
		keycapLabelStyle:      fg(theme.RoleTextSecondary),
		tabActiveStyle:        bg(theme.RoleTabActive).Foreground(t.Role(theme.RoleTabActiveFg).Lipgloss()).Bold(true),
		tabActiveBlockedStyle: bg(theme.RoleTabActive).Foreground(t.Role(theme.RoleStatusBlocked).Lipgloss()).Bold(true),
		promptStyle:           fg(theme.RolePrompt).Bold(true),
		queryTextStyle:        fg(theme.RoleText).Bold(true),
		queryCursorStyle:      bg(theme.RoleCursor),
		placeholderStyle:      fg(theme.RoleTextMuted).Italic(true),
		titleStyle:            fg(theme.RoleText).Bold(true),
		warnStyle:             fg(theme.RoleWarning),
		successStyle:          fg(theme.RoleSuccess),
		gitBranchStyle:        fg(theme.RoleGitBranch).Bold(true),
		gitCleanStyle:         fg(theme.RoleGitClean),
		gitChangesStyle:       fg(theme.RoleGitChanges),
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
