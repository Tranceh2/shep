package tui

import "strings"

// previewKey is every input the preview body depends on, except the spinner
// frame (see previewMemo). Comparable, so an unchanged key is one ==.
type previewKey struct {
	// The highlighted row's identity; present is false with no row.
	present          bool
	rowID            string
	rowKind          RowKind
	rowPath, rowName string
	// The render state: a new request bumps seq; a response flips loading
	// and fills the text or sections; errors set err.
	seq         int
	loading     bool
	err         string
	textLen     int
	sectionsLen int
	// liveSeq counts live agent status updates and tree is replaced by every
	// snapshot generation: both feed the meta line and the Tabs section.
	liveSeq int
	tree    *TreeExpander
	// The preview column's size: the capture tail fits its height.
	width, height int
}

// previewMemo caches the composed preview across Updates: the fitted lines
// a frame prints, and the heavy text (captures, command output) they were
// built from. A message that changes none of the inputs (previewKey) does
// nothing; a spinner frame recomposes an animated preview, but its heavy
// text comes back from the cache, so a frame never re-sanitizes, re-fits or
// re-measures a pane capture.
type previewMemo struct {
	key   previewKey
	valid bool
	// frame is the spinner frame the lines were drawn with; it matters only
	// while animated.
	frame    int
	animated bool
	lines    []string
	fits     []fittedText
	// composed counts compositions and fitted the heavy texts fitted anew:
	// the observables the memoization tests assert on.
	composed, fitted int
}

// previewKeyFor builds the current previewKey for a width x height column.
func (m *Model) previewKeyFor(width, height int) previewKey {
	key := previewKey{
		seq: m.previewSeq, loading: m.previewLoading, err: m.previewErr,
		textLen: len(m.previewText), sectionsLen: len(m.previewSections),
		liveSeq: m.liveSeq, tree: m.tree, width: width, height: height,
	}
	if row, ok := m.currentRow(); ok {
		key.present, key.rowID, key.rowKind = true, row.ID, row.Kind
		key.rowPath, key.rowName = row.Candidate.Path, row.Candidate.Label
	}
	return key
}

// fresh reports whether the memo holds the lines for key at spinner frame.
func (p *previewMemo) fresh(key previewKey, frame int) bool {
	return p.valid && p.key == key && (!p.animated || p.frame == frame)
}

// syncViewport refreshes the preview after every Update (see model.go): the
// memoized lines when an input changed (or the spinner advanced under an
// animated preview), and the viewport's size and line count, which drive
// the scroll keys. The viewport only keeps the scroll position: View prints
// the memo's pre-fitted lines itself (see previewLines), so its content is
// the line count alone, updated only when that count changes. A list-only
// layout shows no preview and composes nothing.
func (m *Model) syncViewport() {
	width, height := m.previewPaneContentSize()
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(height)
	if width == 0 {
		return
	}
	memo := &m.preview
	key := m.previewKeyFor(width, height)
	if memo.fresh(key, m.spinnerFrame) {
		return
	}
	c := m.composePreview(width, height, memo.fits)
	if !memo.valid || len(c.lines) != len(memo.lines) {
		m.viewport.SetContent(strings.Repeat("\n", max(0, len(c.lines)-1)))
	}
	memo.key, memo.valid, memo.frame = key, true, m.spinnerFrame
	memo.animated, memo.lines, memo.fits = c.animated, c.lines, c.fits
	memo.composed++
	memo.fitted += c.built
}

// previewLines returns the preview column's lines for the body: the memo's
// when they are current, else composed on the spot (a model that has not
// been through Update yet), reusing the memo's fitted heavy text. Pure:
// nothing is stored.
func (m Model) previewLines(width, height int) []string {
	if key := m.previewKeyFor(width, height); m.preview.fresh(key, m.spinnerFrame) {
		return m.preview.lines
	}
	return m.composePreview(width, height, m.preview.fits).lines
}

// helpKey is every input the help body depends on: the overlay's size and
// the highlighted row's Enter description.
type helpKey struct {
	built         bool
	width, height int
	enter         string
}

// syncHelpViewport refreshes m.helpViewport's size and content while the
// help overlay is visible — called at the end of every Update, mirroring
// syncViewport. The overlay keeps the grid's chrome rows (tab strip, title,
// rule, footer) and replaces the body, so it spans the content width over
// the body rows. The help text is built only while help is shown, and only
// when it opens, the size changes or the Enter description would change;
// every other message leaves it alone. Only YOffset (the scroll position)
// persists across renders.
func (m *Model) syncHelpViewport() {
	if m.focus != FocusHelp {
		return
	}
	g := m.geometry()
	key := helpKey{built: true, width: g.ContentWidth, height: max(1, g.ListInnerRows), enter: m.enterHelpText()}
	if m.helpKey == key {
		return
	}
	m.helpViewport.SetWidth(key.width)
	m.helpViewport.SetHeight(key.height)
	m.helpViewport.SetContent(m.helpBodyText(key.width))
	m.helpKey = key
}

// previewPaneContentSize returns the preview column's body width/height from
// the single-source-of-truth pickerGeometry: exactly the cells writeBody
// gives the preview, or (0, 0) when there is no preview column. The height
// is at least one row so a not-yet-sized model still renders a preview line.
func (m Model) previewPaneContentSize() (int, int) {
	g := m.geometry()
	if g.PreviewWidth == 0 {
		return 0, 0
	}
	return g.PreviewWidth, max(1, g.PreviewInnerRows)
}
