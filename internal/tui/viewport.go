package tui

import "github.com/charmbracelet/bubbles/viewport"

// syncViewport refreshes m.viewport's Width/Height/Content from the current
// pane geometry and preview body — called once at the end of every Update
// (see model.go), so by the time a scroll key is processed the viewport's
// Height (page-size math) and content (line count for AtBottom/maxYOffset)
// already reflect the latest resize/selection/response. Only YOffset (the
// actual scroll position) is meant to persist across renders; Width/Height/
// Content are cheap to recompute and must never drift from the pane's real
// budget, so they are refreshed unconditionally rather than cached.
func (m *Model) syncViewport() {
	width, height := m.previewPaneContentSize()
	m.viewport.Width = width
	m.viewport.Height = height
	m.viewport.SetContent(m.previewBodyPlain(width))
}

// syncHelpViewport refreshes m.helpViewport's Width/Height/Content from the
// current terminal geometry and help body text — called once at the end of
// every Update (see model.go), mirroring syncViewport's contract for the
// preview pane. The help overlay replaces the list+preview body entirely
// (see View), keeping the header hidden and only the footer below it, so
// the available outer height is m.height minus g.FooterLines before
// previewBodyHeight further subtracts the pane border. Only YOffset (the
// scroll position, mutated while focus==FocusHelp) is meant to persist
// across renders.
func (m *Model) syncHelpViewport() {
	g := m.geometry()
	outer := max(0, m.height-g.FooterLines)
	m.helpViewport.Width = m.paneContentWidth(m.width)
	m.helpViewport.Height = previewBodyHeight(outer)
	m.helpViewport.SetContent(m.helpBodyText())
}

// previewPaneContentSize returns the preview pane's current inner content
// width/height from the single-source-of-truth pickerGeometry.
func (m Model) previewPaneContentSize() (int, int) {
	g := m.geometry()
	if m.mode == modeListOnly {
		return 0, 0
	}
	h := g.PreviewInnerRows
	if h < 1 {
		h = 1
	}
	return m.paneContentWidth(g.PreviewWidth), h
}

// ensure the viewport package import is exercised even if a future edit
// temporarily removes every direct viewport.Model reference from this file
// (Model.viewport itself is declared in model.go).
var _ viewport.Model
