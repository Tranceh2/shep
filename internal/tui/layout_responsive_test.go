package tui

import "testing"

// TestAutoWidthMode_Breakpoints proves the plain width->mode mapping with no
// prior mode (fresh session) picks the expected mode at each breakpoint. With
// the stacked mode removed there is a single breakpoint: below it list-only,
// at/above it wide.
func TestAutoWidthMode_Breakpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		width int
		want  string
	}{
		{40, modeListOnly},
		{wideBreakpoint - 1, modeListOnly},
		{wideBreakpoint, modeWide},
		{200, modeWide},
	}
	for _, tt := range tests {
		if got := autoWidthMode(tt.width, ""); got != tt.want {
			t.Errorf("autoWidthMode(%d, \"\") = %q, want %q", tt.width, got, tt.want)
		}
	}
}

// TestAutoWidthMode_Hysteresis proves a resize that lands exactly on (or
// just barely across) the breakpoint does not flap: leaving "wide" requires
// dropping hysteresisMargin columns BELOW wideBreakpoint, not merely below
// it; leaving "list-only" requires rising hysteresisMargin columns above
// wideBreakpoint.
func TestAutoWidthMode_Hysteresis(t *testing.T) {
	t.Parallel()

	// Currently wide at exactly the breakpoint; a tiny jitter down to
	// wideBreakpoint-1 must NOT immediately drop to list-only.
	if got := autoWidthMode(wideBreakpoint-1, modeWide); got != modeWide {
		t.Errorf("autoWidthMode(%d, wide) = %q, want wide to stick (within hysteresis band)", wideBreakpoint-1, got)
	}
	// Once it drops far enough (past the margin), it does leave wide.
	if got := autoWidthMode(wideBreakpoint-hysteresisMargin-1, modeWide); got != modeListOnly {
		t.Errorf("autoWidthMode(%d, wide) = %q, want list-only once past the hysteresis margin", wideBreakpoint-hysteresisMargin-1, got)
	}

	// Currently list-only at exactly the breakpoint; a tiny jitter up to
	// wideBreakpoint must NOT immediately promote to wide.
	if got := autoWidthMode(wideBreakpoint, modeListOnly); got != modeListOnly {
		t.Errorf("autoWidthMode(%d, list-only) = %q, want list-only to stick (within hysteresis band)", wideBreakpoint, got)
	}
	// Once it rises far enough, it does leave list-only.
	if got := autoWidthMode(wideBreakpoint+hysteresisMargin, modeListOnly); got != modeWide {
		t.Errorf("autoWidthMode(%d, list-only) = %q, want wide once past the hysteresis margin", wideBreakpoint+hysteresisMargin, got)
	}

	// A direct jump straight to wide always wins regardless of hysteresis.
	if got := autoWidthMode(wideBreakpoint+50, modeListOnly); got != modeWide {
		t.Errorf("autoWidthMode(wide jump) = %q, want wide", got)
	}
}

// TestNextResponsiveMode_ForcedLandscapeForcesWide proves a ctrl+l forced
// landscape orientation (Layout.Orientation set) always wins over the
// width-based auto mode.
func TestNextResponsiveMode_ForcedLandscapeForcesWide(t *testing.T) {
	t.Parallel()
	m := Model{width: 40, height: 40, layout: Layout{Orientation: LayoutLandscape}}
	if got := nextResponsiveMode(m, ""); got != modeWide {
		t.Errorf("forced landscape at a narrow width = %q, want modeWide", got)
	}
	m.layout.Orientation = ""
	if got := nextResponsiveMode(m, ""); got != modeListOnly {
		t.Errorf("auto at the same narrow width = %q, want modeListOnly", got)
	}
}

// TestNextResponsiveMode_HeightFloorForcesListOnly proves a terminal too
// short for the preview pane falls back to list-only regardless of width or
// a forced orientation.
func TestNextResponsiveMode_HeightFloorForcesListOnly(t *testing.T) {
	t.Parallel()
	m := Model{width: 200, height: 3} // far below minPreviewHeight
	if got := nextResponsiveMode(m, ""); got != modeListOnly {
		t.Errorf("very short terminal, auto mode = %q, want modeListOnly", got)
	}
	m.layout.Orientation = LayoutLandscape
	if got := nextResponsiveMode(m, ""); got != modeListOnly {
		t.Errorf("very short terminal, forced landscape = %q, want modeListOnly (height floor must still apply)", got)
	}
}

// TestNextResponsiveMode_UnknownHeightNeverForcesListOnly proves height<=0
// (unknown, e.g. no WindowSizeMsg yet) never trips the height floor.
func TestNextResponsiveMode_UnknownHeightNeverForcesListOnly(t *testing.T) {
	t.Parallel()
	m := Model{width: 200, height: 0}
	if got := nextResponsiveMode(m, ""); got != modeWide {
		t.Errorf("width=200 height=0 = %q, want modeWide", got)
	}
}

// TestWideBreakpoint_EqualsMinPreviewWidth pins wideBreakpoint to the same
// named render-constraint constant that hides the preview pane entirely
// (minPreviewWidth): with the stacked layout removed, wide mode is worth
// showing exactly when the terminal is wide enough for a usable preview pane
// at all. A future change to minPreviewWidth must deliberately reconsider
// wideBreakpoint too; this test documents that coupling instead of leaving it
// an unexplained literal.
func TestWideBreakpoint_EqualsMinPreviewWidth(t *testing.T) {
	t.Parallel()
	if wideBreakpoint != minPreviewWidth {
		t.Errorf("wideBreakpoint = %d, want minPreviewWidth(%d)", wideBreakpoint, minPreviewWidth)
	}
}

// TestAutoWidthMode_BoundaryMatrix extends TestAutoWidthMode_Breakpoints
// with every named-constant boundary (not just spot literals), proving the
// mode transition lands exactly on wideBreakpoint rather than approximately
// near it.
func TestAutoWidthMode_BoundaryMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"wideBreakpoint-1 -> list-only", wideBreakpoint - 1, modeListOnly},
		{"wideBreakpoint -> wide", wideBreakpoint, modeWide},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoWidthMode(tt.width, ""); got != tt.want {
				t.Errorf("autoWidthMode(%d, \"\") = %q, want %q", tt.width, got, tt.want)
			}
		})
	}
}

// TestNextResponsiveMode_HeightBoundaryMatrix proves the height floor
// (heightForcesListOnly) lands exactly on minPreviewHeight, for both the AUTO
// path and a user-forced landscape orientation.
func TestNextResponsiveMode_HeightBoundaryMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		width  int
		height int
		forced string // "" for auto
		want   string
	}{
		{"auto wide width, minPreviewHeight-1 -> list-only", 200, minPreviewHeight - 1, "", modeListOnly},
		{"auto wide width, minPreviewHeight -> wide", 200, minPreviewHeight, "", modeWide},
		{"auto narrow width, minPreviewHeight -> list-only (width floor wins)", wideBreakpoint - 1, minPreviewHeight, "", modeListOnly},
		{"forced landscape, minPreviewHeight-1 -> list-only", 200, minPreviewHeight - 1, LayoutLandscape, modeListOnly},
		{"forced landscape, minPreviewHeight -> wide", 200, minPreviewHeight, LayoutLandscape, modeWide},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{width: tt.width, height: tt.height, layout: Layout{Orientation: tt.forced}}
			if got := nextResponsiveMode(m, ""); got != tt.want {
				t.Errorf("nextResponsiveMode(width=%d,height=%d,forced=%q) = %q, want %q",
					tt.width, tt.height, tt.forced, got, tt.want)
			}
		})
	}
}

// TestSurfaceDimensions_NeverNegativeAcrossBoundaries proves the list,
// preview, and help surfaces never resolve to a negative usable dimension
// at any breakpoint boundary or degenerate (0/1 row/col) terminal size, in
// every reachable mode. previewPaneContentSize/paneContentWidth/
// previewBodyHeight already clamp defensively; this test makes that
// contract explicit and regression-checked rather than merely assumed.
func TestSurfaceDimensions_NeverNegativeAcrossBoundaries(t *testing.T) {
	t.Parallel()
	widths := []int{0, 1, 2, wideBreakpoint - 1, wideBreakpoint, 200}
	heights := []int{0, 1, 2, minPreviewHeight - 1, minPreviewHeight, 60}

	for _, w := range widths {
		for _, h := range heights {
			m := NewModel(nil, nil)
			m, _ = update(t, m, sizeMsg(w, h))

			// List pane inner content width (used by renderList).
			if got := m.paneContentWidth(m.width); got < 0 {
				t.Errorf("width=%d height=%d mode=%q: paneContentWidth = %d, want >= 0", w, h, m.mode, got)
			}

			// Preview pane inner content width/height (viewport.go).
			prevW, prevH := m.previewPaneContentSize()
			if prevW < 0 || prevH < 0 {
				t.Errorf("width=%d height=%d mode=%q: previewPaneContentSize = (%d,%d), want both >= 0", w, h, m.mode, prevW, prevH)
			}

			// Help overlay viewport (help.go's own resolvedContentWidth/Height
			// path — syncHelpViewport, exercised indirectly via Update, which
			// runs it unconditionally every frame regardless of focus).
			if m.helpViewport.Width < 0 || m.helpViewport.Height < 0 {
				t.Errorf("width=%d height=%d mode=%q: helpViewport = (%d,%d), want both >= 0",
					w, h, m.mode, m.helpViewport.Width, m.helpViewport.Height)
			}

			if m.mode != modeListOnly && (prevW < 1 || prevH < 1) {
				t.Errorf("width=%d height=%d mode=%q: preview pane advertised as available but content size = (%d,%d), want both >= 1",
					w, h, m.mode, prevW, prevH)
			}
		}
	}
}
