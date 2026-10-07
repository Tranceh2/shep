package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// rowrender.go renders a rowView (rowview.go) as one list row:
//
//	[gutter 2][tree prefix][icon] [label][  detail][fill] [marker]

// Truncation (see fitRow): the detail is shortened from the left down to
// minDetailCells before it is dropped; the marker is capped at
// markerMaxPercent of the row, and dropped before the label would go under
// minLabelCells.
const (
	minDetailCells   = 6
	minLabelCells    = 16
	markerMaxPercent = 30
	// detailGap separates the label from its detail.
	detailGap = 2
)

// cursorGlyphUnicode and cursorGlyphASCII are the cursor markers.
// cursorPrefixWidth reserves two stable leading cells for every row: the
// selected row uses its marker plus one space, and other rows use two
// blanks.
const (
	cursorGlyphUnicode = "❯"
	cursorGlyphASCII   = ">"
	cursorPrefixWidth  = 2
)

// rowRenderer renders list rows for one frame. It carries what every row
// needs, resolved once per frame instead of once per row: the shared styles,
// the glyph tier and the spinner (whose frame draws "working").
type rowRenderer struct {
	styles  *styleSet
	icons   IconSet
	spinner spinner.Model
}

func (m Model) newRowRenderer() rowRenderer {
	return rowRenderer{styles: m.styles, icons: m.icons(), spinner: m.spinner}
}

// rowLayout is what of a row's parts fits its width (see fitRow).
type rowLayout struct {
	label, detail, marker part
}

// render renders v as one row of exactly width cells. A selected row
// carries the selection surface on every segment after the gutter, so the
// highlight spans exactly to the column end.
func (r *rowRenderer) render(v *rowView, selected bool, width int) string {
	st := &r.styles.rowPlain
	gutter := "  "
	if selected {
		st = &r.styles.rowSelected
		gutter = cursorGlyphUnicode + " "
		if r.icons.Name == IconsASCII {
			gutter = cursorGlyphASCII + " "
		}
	}
	room := width - cursorPrefixWidth - v.fixedW
	lay := fitRow(v, room, width)

	var b strings.Builder
	b.Grow(width * 3)
	if selected {
		b.WriteString(st.gutter.Render(gutter))
	} else {
		b.WriteString(gutter)
	}
	st.blank(&b, len(v.indent))
	if v.tree != "" {
		b.WriteString(st.tree.Render(v.tree))
	}
	if v.icon.width > 0 {
		switch {
		case v.iconStyle >= len(st.icons):
			r.writePart(&b, &v.icon, lipgloss.Style{}, st)
		case v.icon.runs == nil:
			// An icon is static plain text: written through its pre-rendered
			// style, it costs nothing per frame.
			st.iconWraps[v.iconStyle].write(&b, v.icon.text)
		default:
			r.writePart(&b, &v.icon, st.icons[v.iconStyle], st)
		}
		st.blank(&b, 1)
	}
	label := st.label
	if v.descendant {
		label = st.descendant
	}
	r.writePart(&b, &lay.label, label, st)
	used := lay.label.width
	if lay.detail.width > 0 {
		st.blank(&b, detailGap)
		r.writePart(&b, &lay.detail, st.detail, st)
		used += detailGap + lay.detail.width
	}
	if lay.marker.width > 0 {
		st.blank(&b, max(1, room-used-lay.marker.width))
		r.writePart(&b, &lay.marker, st.marker, st)
	} else {
		st.blank(&b, room-used)
	}
	return fitWidth(b.String(), width)
}

// fitRow decides what of v fits in room cells (the row's width after the
// gutter and the fixed prefix). Space is given up in this order: the detail
// shrinks from the left down to minDetailCells and is then dropped; the
// marker is capped at markerMaxPercent of the row (keeping its start), then
// dropped when the label would otherwise go below minLabelCells; finally the
// label is truncated — keeping its start, or its end when it is a path (see
// rowView.keepStart).
func fitRow(v *rowView, room, rowWidth int) rowLayout {
	lay := rowLayout{label: v.label, detail: v.detail, marker: v.marker}
	markerCost := partCost(&lay.marker)
	if lay.detail.width > 0 && lay.label.width+detailGap+lay.detail.width+markerCost > room {
		if fit := room - lay.label.width - markerCost - detailGap; fit >= minDetailCells {
			lay.detail = truncatePart(lay.detail, fit, false)
		} else {
			lay.detail = part{}
		}
	}
	if lay.detail.width > 0 {
		return lay
	}
	if lay.label.width+markerCost > room {
		if limit := rowWidth * markerMaxPercent / 100; lay.marker.width > limit {
			lay.marker = truncatePart(lay.marker, max(1, limit), true)
			markerCost = partCost(&lay.marker)
		}
		if room-markerCost < min(lay.label.width, minLabelCells) {
			lay.marker, markerCost = part{}, 0
		}
	}
	if budget := room - markerCost; lay.label.width > budget {
		lay.label = truncatePart(lay.label, budget, v.keepStart)
	}
	return lay
}

// partCost is the cells a marker takes with the blank before it.
func partCost(p *part) int {
	if p.width == 0 {
		return 0
	}
	return p.width + 1
}

// truncatePart fits p into width cells with an ellipsis — keeping its start
// (keepStart) or its end — and cuts its runs and highlight mask to match.
// The ellipsis is drawn in the part's own role and never highlighted.
func truncatePart(p part, width int, keepStart bool) part {
	if width <= 0 {
		return part{}
	}
	if p.width <= width {
		return p
	}
	const ellipsis = "…"
	var out, kept string
	var from, to int // the byte range of p.text that is kept
	if keepStart {
		out = truncateToWidth(p.text, width)
		kept = strings.TrimSuffix(out, ellipsis)
		from, to = 0, len(kept)
		if !strings.HasPrefix(p.text, kept) {
			return plainPart(out)
		}
	} else {
		out = truncateFromLeftToWidth(p.text, width)
		kept = strings.TrimPrefix(out, ellipsis)
		from, to = len(p.text)-len(kept), len(p.text)
		if !strings.HasSuffix(p.text, kept) {
			return plainPart(out)
		}
	}
	t := part{text: out, width: ansi.StringWidth(out)}
	if p.runs != nil {
		t.runs = make([]run, 0, len(p.runs)+1)
		shift := 0
		if !keepStart {
			t.runs = append(t.runs, run{end: len(ellipsis)})
			shift = len(ellipsis)
		}
		start := 0
		for _, r := range p.runs {
			lo, hi := max(start, from), min(r.end, to)
			start = r.end
			if lo >= hi {
				continue
			}
			r.end = hi - from + shift
			t.runs = append(t.runs, r)
		}
		if keepStart {
			t.runs = append(t.runs, run{end: len(out)})
		}
	}
	if p.hl != nil {
		lo := utf8.RuneCountInString(p.text[:from])
		n := utf8.RuneCountInString(kept)
		mask := make([]bool, 0, n+1)
		if !keepStart {
			mask = append(mask, false)
		}
		mask = append(mask, p.hl[min(lo, len(p.hl)):min(lo+n, len(p.hl))]...)
		if keepStart {
			mask = append(mask, false)
		}
		t.hl = mask
	}
	return t
}

// writePart writes p's runs: its own role in base, the template's inline
// styles and the live markers in their roles, and the runes the query
// matched in the highlight style. The working status glyph draws the shared
// spinner's current frame.
func (r *rowRenderer) writePart(b *strings.Builder, p *part, base lipgloss.Style, st *rowStyles) {
	if p.runs == nil {
		writeRuns(b, p.text, p.hl, base, st.highlight)
		return
	}
	start, at := 0, 0
	for _, run := range p.runs {
		text := p.text[start:run.end]
		start = run.end
		if run.role == roleStatusWorking && r.icons.StatusWorking == "" {
			sp := r.spinner // a copy keeps the shared spinner's own Style
			sp.Style = st.statusWorking
			b.WriteString(sp.View())
			at += utf8.RuneCountInString(text)
			continue
		}
		var mask []bool
		if p.hl != nil {
			n := utf8.RuneCountInString(text)
			mask = p.hl[min(at, len(p.hl)):min(at+n, len(p.hl))]
			at += n
		}
		writeRuns(b, text, mask, st.runStyle(run, base), st.highlight)
	}
}

// runStyle is the style one run draws in: base for the part's own role, else
// the role's style; bold adds to either.
func (st *rowStyles) runStyle(r run, base lipgloss.Style) lipgloss.Style {
	style := base
	switch r.role {
	case roleMuted:
		style = st.muted
	case roleAccent:
		style = st.accent
	case rolePin:
		style = st.pin
	case roleMissing:
		style = st.err
	case roleStatusIdle:
		style = st.statusIdle
	case roleStatusWorking:
		style = st.statusWorking
	case roleStatusBlocked:
		style = st.statusBlocked
	case roleStatusDone:
		style = st.statusDone
	case roleStatusUnknown:
		style = st.statusUnknown
	}
	if r.bold {
		style = style.Bold(true)
	}
	return style
}

// statusGlyph returns the styled glyph for an agent status, matching herdr's
// own agent_icon convention (herdr's src/ui/status.rs): the shared animated
// spinner for "working" (colored like working, never like the preview
// loading indicator that shares its tick loop), a check-mark for "idle", a
// filled circle for "done", a fisheye for "blocked" and a hollow circle for
// the literal "unknown" status (Herdr reporting it could not classify the
// agent) — never a status word. Any other value, including "" (no status
// reported at all), renders nothing: "unknown" is a real reported value and
// must not be conflated with the absent case.
//
// A tier whose terminal cannot render the spinner's Braille frames
// (IconSet.StatusWorking non-empty — the ASCII tier) shows a static marker
// instead, so [tui].icons = "ascii" never emits a non-ASCII status glyph.
func statusGlyph(set *IconSet, sp spinner.Model, status string, style lipgloss.Style) string {
	switch status {
	case "working":
		if set.StatusWorking != "" {
			return style.Render(set.StatusWorking)
		}
		// A copy keeps the shared spinner's own Style (the preview loading
		// indicator's) untouched.
		sp.Style = style
		return sp.View()
	case "idle":
		return style.Render(set.StatusIdle)
	case "done":
		return style.Render(set.StatusDone)
	case "blocked":
		return style.Render(set.StatusBlocked)
	case "unknown":
		return style.Render(set.StatusUnknown)
	default:
		return ""
	}
}

// blanks backs the common short runs of blank cells without allocating.
const blanks = "                                                                "

// blank appends n blank cells, on the selection surface when the row has one.
func (st *rowStyles) blank(b *strings.Builder, n int) {
	if n <= 0 {
		return
	}
	var sp string
	if n <= len(blanks) {
		sp = blanks[:n]
	} else {
		sp = strings.Repeat(" ", n)
	}
	if st.hasSurface {
		b.WriteString(st.surface.Render(sp))
		return
	}
	b.WriteString(sp)
}

// writeRuns writes text in base, with the runes mask marks in hl. Each run
// of equally styled runes is one Render call — never one per rune, which
// would multiply the escape sequences and the rendering cost by the label
// length.
func writeRuns(b *strings.Builder, text string, mask []bool, base, hl lipgloss.Style) {
	if text == "" {
		return
	}
	if len(mask) == 0 {
		b.WriteString(base.Render(text))
		return
	}
	style := func(on bool) lipgloss.Style {
		if on {
			return hl
		}
		return base
	}
	start, run := 0, mask[0]
	r := 0
	for i := range text {
		on := r < len(mask) && mask[r]
		if on != run {
			b.WriteString(style(run).Render(text[start:i]))
			start, run = i, on
		}
		r++
	}
	b.WriteString(style(run).Render(text[start:]))
}

// scrollThumb returns the rows [start, start+length) of a visible-row track
// the scroll thumb covers for a window at offset over total rows: its length
// is proportional to the visible share (at least one row) and its position
// to the offset.
func scrollThumb(offset, total, visible int) (start, length int) {
	if visible <= 0 || total <= visible {
		return 0, 0
	}
	length = max(1, (visible*visible+total/2)/total)
	span := total - visible
	start = (offset*(visible-length)*2 + span) / (2 * span)
	return clamp(start, 0, visible-length), length
}
