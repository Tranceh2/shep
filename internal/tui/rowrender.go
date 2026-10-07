package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// rowrender.go renders a rowView (rowview.go) as one list row:
//
//	[gutter 2][tree prefix][icon] [status] [primary][  secondary][fill] [accessories]

// Truncation floors (see fitRow): the secondary path is shortened down to
// minSecondaryCells before it is dropped, and the accessories give way when
// the primary would otherwise shrink below minPrimaryCells.
const (
	minSecondaryCells = 6
	minPrimaryCells   = 8
	// secondaryGap separates the primary name from its parent path.
	secondaryGap = 2
)

// cursorGlyphUnicode and cursorGlyphASCII are the FocusList-only cursor
// markers. cursorPrefixWidth reserves two stable leading cells for every row:
// the selected row uses its marker plus one space, and other rows use two
// blanks.
const (
	cursorGlyphUnicode = "❯"
	cursorGlyphASCII   = ">"
	cursorPrefixWidth  = 2
)

// rowRenderer renders list rows for one frame. It carries what every row
// needs, resolved once per frame instead of once per row: the shared styles,
// the glyph tier, the spinner (whose frame draws "working") and whether the
// list owns focus (only then does the selected row show the cursor glyph).
type rowRenderer struct {
	styles  *styleSet
	icons   IconSet
	spinner spinner.Model
	focused bool
}

func (m Model) newRowRenderer() rowRenderer {
	return rowRenderer{styles: m.styles, icons: m.icons(), spinner: m.spinner, focused: m.focus == FocusList}
}

// rowLayout is what of a row fits its width (see fitRow).
type rowLayout struct {
	primary, secondary     string
	primaryHL, secondaryHL []bool
	primaryW, secondaryW   int
	accessories            []accessory
	accessoriesW           int
}

// render renders v as one row of exactly width cells. A selected row
// carries the selection surface on every segment after the gutter, so the
// highlight spans exactly to the column end.
func (r *rowRenderer) render(v *rowView, selected bool, width int) string {
	st := &r.styles.rowPlain
	gutter := "  "
	if selected {
		st = &r.styles.rowSelectedUnfocused
		if r.focused {
			st = &r.styles.rowSelected
			gutter = cursorGlyphUnicode + " "
			if r.icons.Name == IconsASCII {
				gutter = cursorGlyphASCII + " "
			}
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
	if v.icon != "" {
		b.WriteString(st.icons[v.iconRole].Render(v.icon))
		st.blank(&b, 1)
	}
	if v.statusGlyph {
		b.WriteString(r.statusGlyph(v.status, st))
		st.blank(&b, 1)
	}
	primary := st.primary
	if v.descendant {
		primary = st.descendant
	}
	text, mask := lay.primary, lay.primaryHL
	if v.lead != "" && strings.HasPrefix(text, v.lead) {
		n := utf8.RuneCountInString(v.lead)
		writeRuns(&b, v.lead, subMask(mask, 0, min(n, len(mask))), st.muted, st.highlight)
		text = text[len(v.lead):]
		if mask != nil {
			mask = mask[min(n, len(mask)):]
		}
	}
	writeRuns(&b, text, mask, primary, st.highlight)
	used := lay.primaryW
	if lay.secondaryW > 0 {
		st.blank(&b, secondaryGap)
		writeRuns(&b, lay.secondary, lay.secondaryHL, st.secondary, st.highlight)
		used += secondaryGap + lay.secondaryW
	}
	if lay.accessoriesW > 0 {
		st.blank(&b, max(1, room-used-lay.accessoriesW))
		r.writeAccessories(&b, lay.accessories, st)
	} else {
		st.blank(&b, room-used)
	}
	return fitWidth(b.String(), width)
}

// fitRow decides what of v fits in room cells (the row's width after the
// gutter and the fixed prefix). Space is given up in this order: the
// secondary parent path shrinks from the left down to minSecondaryCells and
// is then dropped; the accessories are dropped when the primary would
// otherwise go below minPrimaryCells (agentTitleMinCells beside an agent's
// workspace label); finally the primary is truncated — keeping its tail, or
// its start for titles (see rowView.keepStart).
func fitRow(v *rowView, room, rowWidth int) rowLayout {
	lay := rowLayout{
		primary: v.primary, primaryHL: v.primaryHL, primaryW: v.primaryW,
		secondary: v.secondary, secondaryHL: v.secondaryHL, secondaryW: v.secondaryW,
	}
	lay.accessories, lay.accessoriesW = fitAccessories(v.accessories, rowWidth)
	accessoryCost := 0
	if lay.accessoriesW > 0 {
		accessoryCost = lay.accessoriesW + 1
	}
	if lay.secondaryW > 0 && lay.primaryW+secondaryGap+lay.secondaryW+accessoryCost > room {
		if fit := room - lay.primaryW - accessoryCost - secondaryGap; fit >= minSecondaryCells {
			lay.secondary, lay.secondaryHL = truncateMasked(lay.secondary, lay.secondaryHL, fit, false)
			lay.secondaryW = ansi.StringWidth(lay.secondary)
		} else {
			lay.secondary, lay.secondaryHL, lay.secondaryW = "", nil, 0
		}
	}
	if lay.secondaryW > 0 {
		return lay
	}
	floor := minPrimaryCells
	if hasShrinkable(lay.accessories) {
		floor = agentTitleMinCells
	}
	if lay.primaryW+accessoryCost > room && room-accessoryCost < min(lay.primaryW, floor) {
		lay.accessories, lay.accessoriesW, accessoryCost = nil, 0, 0
	}
	if budget := room - accessoryCost; lay.primaryW > budget {
		lay.primary, lay.primaryHL = truncateMasked(lay.primary, lay.primaryHL, budget, v.keepStart)
		lay.primaryW = ansi.StringWidth(lay.primary)
	}
	return lay
}

// fitAccessories applies the shrinkable part's cap (agentLabelMaxPercent of
// the row, at most agentLabelMaxCells; the label keeps its start) and
// returns the parts with their total width, single spaces between parts
// included.
func fitAccessories(parts []accessory, rowWidth int) ([]accessory, int) {
	if len(parts) == 0 {
		return nil, 0
	}
	limit := min(rowWidth*agentLabelMaxPercent/100, agentLabelMaxCells)
	total := len(parts) - 1
	var fitted []accessory
	for i, p := range parts {
		if p.shrink && p.width > limit {
			if fitted == nil {
				fitted = append([]accessory(nil), parts...)
			}
			p.text = truncateToWidth(p.text, max(1, limit))
			p.width = ansi.StringWidth(p.text)
			fitted[i] = p
		}
		total += p.width
	}
	if fitted != nil {
		return fitted, total
	}
	return parts, total
}

// hasShrinkable reports an agent workspace label among parts.
func hasShrinkable(parts []accessory) bool {
	for _, p := range parts {
		if p.shrink {
			return true
		}
	}
	return false
}

// writeAccessories writes parts joined by single spaces.
func (r *rowRenderer) writeAccessories(b *strings.Builder, parts []accessory, st *rowStyles) {
	for i, p := range parts {
		if i > 0 {
			st.blank(b, 1)
		}
		switch p.role {
		case accessoryStatus:
			// In the list this column answers "which workspace needs me", so
			// only attention states (working, blocked, done) are colored; idle
			// is the resting state and reads muted. Status glyphs elsewhere
			// (agent and pane rows, the preview) keep Herdr's colors.
			if p.text == "idle" {
				b.WriteString(statusGlyph(&r.icons, r.spinner, p.text, st.muted))
			} else {
				b.WriteString(r.statusGlyph(p.text, st))
			}
		case accessoryPin:
			b.WriteString(st.pin.Render(p.text))
		case accessoryError:
			b.WriteString(st.err.Render(p.text))
		default:
			b.WriteString(st.muted.Render(p.text))
		}
	}
}

// statusGlyph draws an agent status as its glyph in st's status style.
func (r *rowRenderer) statusGlyph(status string, st *rowStyles) string {
	return statusGlyph(&r.icons, r.spinner, status, st.statusStyle(status))
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

// truncateMasked fits text into width cells with an ellipsis — keeping its
// start (keepStart) or its end — and cuts its highlight mask to match. The
// ellipsis is never highlighted.
func truncateMasked(text string, mask []bool, width int, keepStart bool) (string, []bool) {
	if width <= 0 {
		return "", nil
	}
	if ansi.StringWidth(text) <= width {
		return text, mask
	}
	var out string
	if keepStart {
		out = truncateToWidth(text, width)
	} else {
		out = truncateFromLeftToWidth(text, width)
	}
	if mask == nil {
		return out, nil
	}
	kept := min(len(mask), max(0, utf8.RuneCountInString(out)-1))
	cut := make([]bool, 0, kept+1)
	if keepStart {
		return out, append(append(cut, mask[:kept]...), false)
	}
	return out, append(append(cut, false), mask[len(mask)-kept:]...)
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
