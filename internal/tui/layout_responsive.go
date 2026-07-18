package tui

// Responsive display modes. modeWide splits list/preview side by side;
// modeStacked stacks the list above the preview, both full width;
// modeListOnly hides the preview entirely (a terminal too small to show
// both panes without clipping either one).
const (
	modeWide     = "wide"
	modeStacked  = "stacked"
	modeListOnly = "list-only"
)

// Width breakpoints (columns) for the AUTO responsive mode (Layout.
// Orientation == ""), Phase 9: both tied to named render-constraint
// constants (render.go) instead of independent literals.
//
// mediumBreakpoint mirrors minPreviewWidth — below it, even a full-width
// stacked layout cannot show a usable preview pane, so the mode drops
// straight to list-only.
//
// wideBreakpoint is mediumBreakpoint PLUS a full list pane's own width
// floor (minList): side-by-side only earns its keep once the terminal is
// roomy enough for BOTH a fully usable preview (mediumBreakpoint) AND a
// full list pane next to it — anything narrower and a stacked (full-width)
// layout serves both panes better than a side-by-side split cramped down
// toward its own minList/minPrev floors (see splitWidths/clampSizes).
const (
	mediumBreakpoint = minPreviewWidth
	wideBreakpoint   = mediumBreakpoint + minList
	// hysteresisMargin is the extra width a resize must cross BACK OUT of a
	// mode by before the auto mode reverts, so a terminal resized to
	// exactly a breakpoint (or jittering by a column or two, as some
	// terminal emulators do while a user drags a window edge) never flaps
	// between two modes every frame.
	hysteresisMargin = 6
)

// nextResponsiveMode computes the AUTO responsive mode for m.width, given
// the previous mode prev (hysteresis band around each breakpoint — see
// hysteresisMargin), UNLESS the user has forced an orientation via ctrl+l
// (m.layout.Orientation != ""), in which case the forced orientation always
// wins (mapped straight to modeWide/modeStacked), still subject to the
// terminal-height floor (heightForcesListOnly) so a forced layout can never
// overflow a too-short terminal.
func nextResponsiveMode(m Model, prev string) string {
	if forced := m.layout.Orientation; forced != "" {
		if heightForcesListOnly(m, forced) {
			return modeListOnly
		}
		if forced == LayoutPortrait {
			return modeStacked
		}
		return modeWide
	}

	mode := autoWidthMode(m.width, prev)
	orientation := LayoutLandscape
	if mode != modeWide {
		orientation = LayoutPortrait
	}
	if heightForcesListOnly(m, orientation) {
		return modeListOnly
	}
	return mode
}

// autoWidthMode is the pure width+hysteresis core of nextResponsiveMode,
// isolated so it is directly unit-testable without constructing a Model.
func autoWidthMode(width int, prev string) string {
	switch prev {
	case modeWide:
		if width >= wideBreakpoint-hysteresisMargin {
			return modeWide
		}
	case modeListOnly:
		if width < mediumBreakpoint+hysteresisMargin {
			return modeListOnly
		}
	}
	switch {
	case width >= wideBreakpoint:
		return modeWide
	case width < mediumBreakpoint:
		return modeListOnly
	default:
		return modeStacked
	}
}

// heightForcesListOnly reports whether m's reported terminal height is too
// short for orientation's own minimum floor (minPreviewHeight for
// landscape, minPortraitHeight for a stacked/portrait split) — the same
// safety net the original picker applied unconditionally, now folded into
// the responsive mode computation so it composes with both auto and a
// user-forced orientation.
func heightForcesListOnly(m Model, orientation string) bool {
	if m.height <= 0 {
		return false
	}
	floor := minPreviewHeight
	if orientation == LayoutPortrait {
		floor = minPortraitHeight
	}
	return m.height < floor
}
