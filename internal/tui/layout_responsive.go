package tui

// Responsive display modes. modeWide splits list/preview side by side;
// modeListOnly hides the preview entirely (a terminal too small to show a
// usable preview pane alongside the list). These are the only two modes the
// picker resolves to.
const (
	modeWide     = "wide"
	modeListOnly = "list-only"
)

// wideBreakpoint is the terminal width from which the AUTO responsive mode
// shows the preview column. The borderless grid spends only the divider and
// the side margins on chrome, so at 80 columns the list keeps its
// minListColumns floor and the preview still gets a useful column; below it a
// side-by-side preview only starves the list.
const wideBreakpoint = 80

// minPreviewHeight is the terminal height below which the preview column is
// hidden: with the four chrome rows taken, a shorter terminal leaves too few
// body rows to show a list and a preview side by side.
const minPreviewHeight = 12

// hysteresisMargin is the extra width a resize must cross BACK OUT of a mode
// by before the auto mode reverts, so a terminal resized to exactly the
// breakpoint (or jittering by a column or two, as some terminal emulators do
// while a user drags a window edge) never flaps between two modes every frame.
const hysteresisMargin = 6

// nextResponsiveMode computes the AUTO responsive mode for m.width, given the
// previous mode prev (hysteresis band around wideBreakpoint — see
// hysteresisMargin), UNLESS the user has forced an orientation via ctrl+l
// (m.layout.Orientation == LayoutLandscape), in which case wide is forced
// (still subject to the terminal-height floor so a forced layout can never
// overflow a too-short terminal).
func nextResponsiveMode(m Model, prev string) string {
	if forced := m.layout.Orientation; forced != "" {
		if heightForcesListOnly(m) {
			return modeListOnly
		}
		return modeWide
	}
	mode := autoWidthMode(m.width, prev)
	if heightForcesListOnly(m) {
		return modeListOnly
	}
	return mode
}

// autoWidthMode is the pure width+hysteresis core of nextResponsiveMode,
// isolated so it is directly unit-testable without constructing a Model: a
// single-breakpoint wide/list-only decision.
func autoWidthMode(width int, prev string) string {
	switch prev {
	case modeWide:
		if width >= wideBreakpoint-hysteresisMargin {
			return modeWide
		}
	case modeListOnly:
		if width < wideBreakpoint+hysteresisMargin {
			return modeListOnly
		}
	}
	if width >= wideBreakpoint {
		return modeWide
	}
	return modeListOnly
}

// heightForcesListOnly reports whether m's reported terminal height is too
// short for the preview column's own minimum floor (minPreviewHeight) — the
// safety net the original picker applied unconditionally, now folded into the
// responsive mode computation so it composes with both auto and a user-forced
// orientation.
func heightForcesListOnly(m Model) bool {
	if m.height <= 0 {
		return false
	}
	return m.height < minPreviewHeight
}
