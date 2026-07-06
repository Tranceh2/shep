// Package tui is shep's embedded Bubble Tea fuzzy picker. It is the universal
// interactive fallback in the `shep open` selector cascade, used when there
// is no exact match and fzf is unavailable.
//
// The model renders a left list of filtered candidates and a right preview
// showing the highlighted candidate's rendered preview.Result (label/path/
// source/git, or a declared [preview.commands.<name>], via the injected
// preview.Renderer). Filtering is case-insensitive subsequence scoring over
// label+path. Navigation uses up/down/ctrl+j/ctrl+k; plain "j"/"k" are typed
// into the query (not bound to movement) so they filter like any other rune;
// enter selects; esc/q/ctrl+c/ctrl+g cancels (Run then returns ErrCancelled).
// The palette is Catppuccin Mocha, centralised in palette.go so colors live in
// one place.
package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// Layout configures the picker's list/preview pane widths (config.TUIConfig).
// Each field is "auto" (or empty) or a percentage string like "60%"; see
// config.ParsePercent. The zero value behaves like {"auto", "auto"}.
type Layout struct {
	ListWidth    string
	PreviewWidth string
}

// minPreviewWidth is the terminal width (PL-11) below which the preview
// panel is hidden entirely to avoid breaking the layout.
const minPreviewWidth = 80

// minPreviewHeight is the terminal height below which the preview panel is
// hidden entirely, mirroring minPreviewWidth: a very short terminal cannot
// fit a bordered two-pane layout without clipping either pane.
const minPreviewHeight = 8

// chromeRows is the fixed vertical overhead of the list pane deducted from
// the reported terminal height before capping visible candidate rows: the
// border's top+bottom edges plus the query line above the candidate rows.
// Without this deduction the last row(s) would render past the bottom
// border and never be visible even when scrolled all the way down.
const chromeRows = 4

// ErrCancelled is the quiet cancellation sentinel returned by Run when the
// user quits without selecting (esc/ctrl+c/ctrl+g). Callers use errors.Is to
// distinguish an intentional cancel from "no selector available" (ok=false
// with a nil error) so they can exit without printing anything.
var ErrCancelled = errors.New("cancelled")

// Model is the Bubble Tea model for the shep picker. It owns the candidate
// list, the filtered view, the query text, the cursor and the final pick.
type Model struct {
	candidates []source.Candidate
	filtered   []int // indices into candidates
	query      string
	cursor     int
	width      int
	height     int
	selected   int // -1 until a candidate is chosen
	cancelled  bool
	layout     Layout

	// renderer produces the preview pane content asynchronously. nil is valid
	// (tests, or wiring not yet available) and degrades to a built-in
	// label/path/source summary with no async requests.
	renderer  preview.Renderer
	renderCtx context.Context
	// previewSeq tags every in-flight preview render. A previewResponseMsg
	// whose seq no longer matches is stale (the user moved on) and is
	// discarded (PL-11).
	previewSeq     int
	previewText    string
	previewLoading bool
	// previewErr holds a short user-visible message when Render itself
	// returned a real error (context cancellation, or any future Renderer
	// implementation) — a mechanism distinct from the removed Result.Warning
	// field. Empty after any successful render or on selection change.
	previewErr string
}

// previewResponseMsg carries the result of an async preview render. seq must
// match the model's current previewSeq or the response is stale and ignored.
type previewResponseMsg struct {
	seq    int
	result preview.Result
	err    error
}

// NewModel builds a model over the supplied candidates. The filtered view is
// initialised to every candidate in order; width/height are populated by the
// first WindowSizeMsg. renderer may be nil, in which case the preview pane
// shows a static label/path/source summary instead of an async render.
func NewModel(candidates []source.Candidate, renderer preview.Renderer) Model {
	return newModel(candidates, renderer, context.TODO())
}

func newModel(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context) Model {
	return newModelWithLayout(candidates, renderer, renderCtx, Layout{})
}

func newModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context, layout Layout) Model {
	if renderCtx == nil {
		renderCtx = context.TODO()
	}
	m := Model{
		candidates: make([]source.Candidate, len(candidates)),
		filtered:   make([]int, len(candidates)),
		selected:   -1,
		renderer:   renderer,
		renderCtx:  renderCtx,
		layout:     layout,
	}
	copy(m.candidates, candidates)
	for i := range candidates {
		m.filtered[i] = i
	}
	m.refreshPreviewLoadingFlag()
	return m
}

// Selected returns the chosen candidate and ok=true after enter is pressed.
// ok=false means the user cancelled or has not selected yet.
func (m Model) Selected() (source.Candidate, bool) {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		return source.Candidate{}, false
	}
	idx := m.filtered[m.selected]
	if idx < 0 || idx >= len(m.candidates) {
		return source.Candidate{}, false
	}
	return m.candidates[idx], true
}

// Cancelled reports whether the user quit without selecting
// (esc/q/ctrl+c/ctrl+g).
func (m Model) Cancelled() bool { return m.cancelled }

// Init kicks off the first async preview render for the initially
// highlighted candidate (cursor 0) when a Renderer is wired. Its Cmd is
// tagged with the model's initial previewSeq (0) so the resulting
// previewResponseMsg is accepted, not treated as stale.
func (m Model) Init() tea.Cmd {
	if m.renderer == nil {
		return nil
	}
	cand, ok := m.currentCandidate()
	if !ok {
		return nil
	}
	return m.previewCmd(m.previewSeq, cand)
}

// Update handles key presses, window sizing and async preview responses. It
// mutates a copy of the model and returns it; the Bubble Tea runtime
// replaces the model with the returned value.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case previewResponseMsg:
		return m.handlePreviewResponse(msg), nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handlePreviewResponse applies a completed async render, discarding it as
// stale when its seq no longer matches the model's current previewSeq (the
// user has since highlighted a different candidate) — PL-11.
func (m Model) handlePreviewResponse(msg previewResponseMsg) Model {
	if msg.seq != m.previewSeq {
		return m
	}
	m.previewLoading = false
	if msg.err != nil {
		m.previewErr = "preview error"
		m.previewText = ""
		return m
	}
	m.previewErr = ""
	m.previewText = msg.result.Text
	return m
}

// handleKey applies one key press. Enter/esc/quit exit immediately; the
// remaining navigation/filter keys mutate cursor/query state and then, if the
// highlighted candidate changed, enqueue a fresh async preview render so
// cursor movement never blocks on preview generation (PL-11).
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if len(m.filtered) > 0 {
			m.selected = m.cursor
			return m, tea.Quit
		}
		return m, nil
	case "esc", "q", "ctrl+c", "ctrl+g":
		m.cancelled = true
		return m, tea.Quit
	}

	prevKey := m.currentPreviewKey()
	switch msg.String() {
	// ctrl+j/ctrl+k navigate the cursor; plain "j"/"k" are intentionally NOT
	// listed here so they fall through to the default case and get typed into
	// the query instead of moving the cursor.
	case "down", "ctrl+j":
		if len(m.filtered) > 0 && m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	case "up", "ctrl+k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.applyFilter()
		}
	default:
		// Any other printable rune is appended to the query and re-filters.
		if isPrintable(msg.String()) {
			m.query += msg.String()
			m.applyFilter()
		}
	}
	cmd := m.syncPreviewAfterSelectionChange(prevKey)
	return m, cmd
}

// syncPreviewAfterSelectionChange compares the highlighted candidate before
// and after a key mutated cursor/query state. When the highlight changed, it
// clears the previous candidate's previewText (so a stale render arriving
// out of order can never flash the wrong candidate's text), bumps
// previewSeq (invalidating any in-flight render for the old candidate),
// flips on the loading indicator, and returns the Cmd for the new async
// render. A nil renderer or an unchanged highlight returns a nil Cmd.
func (m *Model) syncPreviewAfterSelectionChange(prevKey string) tea.Cmd {
	if m.renderer == nil {
		return nil
	}
	newKey := m.currentPreviewKey()
	if newKey == prevKey {
		return nil
	}
	m.previewSeq++
	m.previewText = ""
	m.previewErr = ""
	if newKey == "" {
		m.previewLoading = false
		return nil
	}
	m.previewLoading = true
	cand, _ := m.currentCandidate()
	return m.previewCmd(m.previewSeq, cand)
}

// previewCmd builds the async Bubble Tea Cmd that renders cand through the
// injected Renderer and reports back as previewResponseMsg tagged with seq,
// so a stale in-flight render (from a since-abandoned cursor position) can be
// discarded by Update.
func (m Model) previewCmd(seq int, cand source.Candidate) tea.Cmd {
	renderer := m.renderer
	renderCtx := m.renderCtx
	return func() tea.Msg {
		res, err := renderer.Render(renderCtx, cand)
		return previewResponseMsg{seq: seq, result: res, err: err}
	}
}

// refreshPreviewLoadingFlag sets previewLoading to match whether a renderer
// is wired and a candidate is currently highlighted. Used at construction so
// the first frame shows the loading indicator immediately when an async
// render for the initial cursor is in flight.
func (m *Model) refreshPreviewLoadingFlag() {
	if m.renderer == nil {
		m.previewLoading = false
		return
	}
	_, ok := m.currentCandidate()
	m.previewLoading = ok
}

// currentCandidate returns the candidate under the cursor in the filtered
// view, or ok=false when there is nothing to highlight (empty filter result).
func (m Model) currentCandidate() (source.Candidate, bool) {
	if len(m.filtered) == 0 {
		return source.Candidate{}, false
	}
	idx := m.cursor
	if idx < 0 || idx >= len(m.filtered) {
		idx = 0
	}
	ci := m.filtered[idx]
	if ci < 0 || ci >= len(m.candidates) {
		return source.Candidate{}, false
	}
	return m.candidates[ci], true
}

// currentPreviewKey identifies the highlighted candidate for before/after
// comparisons: the normalised path when present, else the raw path, else ""
// when nothing is highlighted.
func (m Model) currentPreviewKey() string {
	cand, ok := m.currentCandidate()
	if !ok {
		return ""
	}
	if cand.NormalizedPath != "" {
		return cand.NormalizedPath
	}
	return cand.Path
}

// applyFilter recomputes the filtered indices from the query using
// case-insensitive subsequence matching over "label path". The cursor is
// clamped back into range so an empty result never leaves a dangling cursor.
func (m *Model) applyFilter() {
	m.filtered = m.filtered[:0]
	needle := strings.ToLower(m.query)
	for i, c := range m.candidates {
		hay := strings.ToLower(c.Label + " " + c.Path)
		if needle == "" || subsequence(needle, hay) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(len(m.filtered)-1, 0)
	}
}

// subsequence reports whether every rune of needle appears in haystack in
// order (case already normalised by the caller). Used for fuzzy filtering.
func subsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	ni := 0
	for hi := 0; hi < len(haystack) && ni < len(needle); hi++ {
		if haystack[hi] == needle[ni] {
			ni++
		}
	}
	return ni == len(needle)
}

// isPrintable returns true for single-rune printable input that should extend
// the query. We avoid pulling in unicode classes for the v1 picker.
func isPrintable(s string) bool {
	if s == "" || len([]rune(s)) != 1 {
		return false
	}
	r := []rune(s)[0]
	return r >= 0x20 && r != 0x7f
}

// View renders the two-pane UI: a left candidate list with the cursor and a
// right preview of the highlighted candidate, each wrapped in a rounded
// border (palette.borderStyle). Widths auto-balance based on the reported
// window size (falling back to 60/40 when no size yet); the border's frame
// size is subtracted from each pane's allotted width so content never
// overflows its own border. Below minPreviewWidth columns or
// minPreviewHeight rows the preview pane is hidden entirely (PL-11) so a
// narrow or very short terminal never breaks the layout.
func (m Model) View() string {
	hidePreview := (m.width > 0 && m.width < minPreviewWidth) ||
		(m.height > 0 && m.height < minPreviewHeight)
	if hidePreview {
		return palette.borderStyle.Render(m.renderList(paneContentWidth(m.width)))
	}
	listW, prevW := splitWidths(m.width, m.layout)
	listPane := palette.borderStyle.Render(m.renderList(paneContentWidth(listW)))
	previewPane := palette.borderStyle.Render(m.renderPreview(paneContentWidth(prevW)))
	return lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
}

// splitWidths divides the total reported width into list/preview pane
// budgets (outer widths, before border+padding is subtracted), honouring
// layout.ListWidth/PreviewWidth when set to a percentage (config.ParsePercent).
// With only one set as a percentage, that field is authoritative and the
// other gets the remainder (width minus the 1-column gap between panes) so
// the two panes always sum to exactly width-1. With neither set (both
// "auto"/empty, the zero value), the original 3/5 heuristic applies
// unchanged. With both set, config.Load's validateTUI already rejects a
// combination that would overflow the terminal; splitBothPercent still
// reconciles defensively here for a Layout built outside that validated
// path (e.g. constructed directly in tests or future callers). Each budget
// still needs paneContentWidth to get the actual content width fed to
// renderList/renderPreview.
func splitWidths(width int, layout Layout) (int, int) {
	if width <= 0 {
		width = 80
	}
	listFrac, listOK := config.PercentOrAuto(layout.ListWidth)
	prevFrac, prevOK := config.PercentOrAuto(layout.PreviewWidth)

	var list, prev int
	switch {
	case listOK && prevOK:
		list, prev = splitBothPercent(width, listFrac, prevFrac)
	case listOK:
		list = int(float64(width) * listFrac)
		prev = width - list - 1
	case prevOK:
		prev = int(float64(width) * prevFrac)
		list = width - prev - 1
	default:
		list = width * 3 / 5
		prev = width - list - 1
	}
	return clampWidths(width, list, prev)
}

// clampWidths enforces the 20/10 minimum pane widths and the invariant that
// list+gap+prev never exceeds the reported terminal width. A one-sided
// extreme percentage (e.g. list_width=95%) leaves almost nothing for the
// derived remainder, which the naive minimum floor would then bump up
// without shrinking the oversized side back down, overflowing the
// terminal; this reconciles the two by shrinking whichever pane is above
// its own floor first (list, then preview) to make room. When width itself
// is too small to fit both floors plus the gap, the floors still win and
// the result may overflow — an unavoidable floor case on a very narrow
// terminal, not a regression from this reconciliation.
func clampWidths(width, list, prev int) (int, int) {
	const minList = 20
	const minPrev = 10
	if list < minList {
		list = minList
	}
	if prev < minPrev {
		prev = minPrev
	}
	if overflow := list + prev + 1 - width; overflow > 0 {
		if room := list - minList; room > 0 {
			shrink := room
			if shrink > overflow {
				shrink = overflow
			}
			list -= shrink
			overflow -= shrink
		}
		if overflow > 0 {
			if room := prev - minPrev; room > 0 {
				shrink := room
				if shrink > overflow {
					shrink = overflow
				}
				prev -= shrink
			}
		}
	}
	return list, prev
}

// splitBothPercent computes list/preview widths when both list_width and
// preview_width are configured percentages. listFrac is scaled down
// proportionally whenever the two fractions would sum past 1 (100%); prev
// is then always derived as the remainder (width-list-1), so the two
// bordered panes plus the 1-column gap between them never exceed the
// reported terminal width.
func splitBothPercent(width int, listFrac, prevFrac float64) (int, int) {
	if total := listFrac + prevFrac; total > 1 {
		listFrac /= total
	}
	list := int(float64(width) * listFrac)
	prev := width - list - 1
	return list, prev
}

// paneContentWidth converts a pane's outer width budget into the inner
// content width available once palette.borderStyle's border+padding are
// subtracted. Both panes share the same borderStyle, so this is the single
// site where border chrome is subtracted from width — no per-pane drift.
func paneContentWidth(outer int) int {
	inner := outer - palette.borderStyle.GetHorizontalFrameSize()
	if inner < 1 {
		inner = 1
	}
	return inner
}

func gap() string { return " " }

// renderList draws the filtered candidates with a cursor marker and the query
// line at the top. Every rendered line is explicitly padded to width so the
// list pane never drifts from the split computed by View (previously rows
// used a style-level hardcoded width instead of the width passed in here).
func (m Model) renderList(width int) string {
	var b strings.Builder
	styleQuery := palette.queryStyle.Width(width)
	queryLine := truncateToWidth("> "+m.query, width)
	b.WriteString(styleQuery.Render(queryLine))
	b.WriteString("\n")
	if len(m.filtered) == 0 {
		b.WriteString(palette.mutedStyle.Width(width).Render(truncateToWidth("  no matches", width)))
		b.WriteString("\n")
		return b.String()
	}
	// Cap visible rows to a sane height when we know it, deducting chromeRows
	// (border top/bottom + query line) so the border never clips the last
	// visible candidate.
	full := m.filtered
	maxRows := m.height
	if maxRows > 0 {
		maxRows -= chromeRows
	}
	if maxRows <= 0 {
		maxRows = len(full)
	}
	// offset is the absolute index (into m.filtered) of the first visible
	// row. Tracking it here lets the cursor check below compare the correct
	// absolute index instead of the slice-relative index, which previously
	// made the cursor disappear whenever the list scrolled.
	offset := 0
	visible := full
	if len(full) > maxRows && maxRows > 2 {
		offset = clamp(m.cursor-maxRows/2, 0, len(full)-maxRows)
		visible = full[offset:]
		if len(visible) > maxRows {
			visible = visible[:maxRows]
		}
	}
	for i, candIdx := range visible {
		c := m.candidates[candIdx]
		marker := "  "
		row := c.Label
		if row == "" {
			row = c.Path
		}
		if c.Icon != "" {
			row = c.Icon + " " + row
		}
		if c.Missing {
			row += " (missing)"
		}
		if i+offset == m.cursor {
			marker = " >"
		}
		// Truncate the full rendered text to width before styling: lipgloss's
		// Width() word-wraps rather than truncates, so a candidate whose
		// icon+label/path exceeds the pane's content width would otherwise
		// wrap into 2+ physical terminal lines that the height budget above
		// never accounts for (only counting logical candidates), silently
		// pushing content past m.height and scrolling the top of the TUI off
		// screen.
		line := truncateToWidth(marker+" "+row, width)
		if i+offset == m.cursor {
			b.WriteString(palette.cursorStyle.Width(width).Render(line))
		} else {
			b.WriteString(palette.rowStyle.Width(width).Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// truncateToWidth trims s so it never exceeds maxW cells of visible width,
// appending an ellipsis ("…") when truncation occurs. A non-positive maxW
// returns s unchanged. Delegates to ansi.Truncate (charmbracelet/x/ansi),
// which is ANSI-escape-aware (never severs a color/style code mid-sequence)
// and measures wide characters (nerd font icons, emoji, East-Asian glyphs)
// as their real cell width instead of naively counting runes. This matters
// even though every shep built-in preview section returns plain text: a
// user-declared [preview.commands.<name>] custom command is outside shep's
// control and can still emit ANSI color codes, which naive rune counting
// would miscount and potentially cut mid-escape-sequence. Used to keep the
// query line — and any preview line — on a single row instead of wrapping
// and pushing the list off screen.
func truncateToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return s
	}
	return ansi.Truncate(s, maxW, "…")
}

// renderPreview shows the highlighted candidate's rendered preview plus a
// short help line so the user always knows the keybindings. When the terminal
// height is known, the body is capped to m.height - chromeRows lines so a long
// output (e.g. dir or active pane content) never expands infinitely and
// breaks JoinHorizontal / pushes the search box off screen.
func (m Model) renderPreview(width int) string {
	header := palette.previewHeaderStyle.Width(width).Render(truncateToWidth("preview", width))
	body := m.previewBody(width)
	body = capPreviewBodyLines(body, m.height)
	help := palette.mutedStyle.Width(width).Render(truncateToWidth("enter select  esc cancel  ctrl+j/k move", width))
	return lipgloss.JoinVertical(lipgloss.Left, header, body, "", help)
}

// capPreviewBodyLines truncates body to at most height-chromeRows content
// lines when height is positive, appending an ellipsis line when truncation
// occurs. A non-positive height (unknown) returns body unchanged so previews
// still render fully in headless/test contexts.
func capPreviewBodyLines(body string, height int) string {
	if height <= 0 {
		return body
	}
	maxLines := height - chromeRows
	if maxLines <= 0 {
		maxLines = 1
	}
	// lipgloss.JoinVertical adds the header, blank, and help lines around
	// the body; subtract those (3) plus the border (2) so the total pane
	// height stays within `height`. chromeRows already covers border+query
	// for the list pane; the preview adds header+blank+help (3 extra).
	maxLines -= 3
	if maxLines <= 0 {
		maxLines = 1
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= maxLines {
		return body
	}
	truncated := append(lines[:maxLines], "…")
	return strings.Join(truncated, "\n")
}

// previewBody renders the preview pane content: "(no selection)" when
// nothing is highlighted, a built-in label/path/source summary when no
// Renderer is wired, a loading indicator while an async render is in flight,
// a short error indicator when Render returned a real error, or the
// rendered text.
func (m Model) previewBody(width int) string {
	if len(m.filtered) == 0 {
		return palette.mutedStyle.Width(width).Render(truncateToWidth("(no selection)", width))
	}
	if m.renderer == nil {
		cand, _ := m.currentCandidate()
		lines := []string{
			styleLinePrefix("label  ", cand.Label, width, palette.labelStyle),
			styleLinePrefix("path   ", cand.Path, width, palette.labelStyle),
			styleLinePrefix("source ", cand.Source, width, palette.labelStyle),
		}
		return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
	}
	if m.previewLoading {
		return palette.previewLoadingStyle.Width(width).Render(truncateToWidth("loading…", width))
	}
	if m.previewErr != "" {
		return palette.previewErrStyle.Width(width).Render(truncateToWidth(m.previewErr, width))
	}
	text := truncateLinesToWidth(m.previewText, width)
	return lipgloss.NewStyle().Width(width).Render(text)
}

// truncateLinesToWidth splits text on "\n" and truncates each individual
// line to width via truncateToWidth before rejoining. Used before any
// lipgloss.NewStyle().Width(width).Render(text) call so that call can only
// ever pad, never word-wrap: every logical line is already guaranteed to
// fit within width, keeping capPreviewBodyLines' logical-line-count cap
// accurate for the actual rendered height.
func truncateLinesToWidth(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = truncateToWidth(line, width)
	}
	return strings.Join(lines, "\n")
}

// styleLinePrefix builds a "<prefix><value>" line, truncates it to width as
// RAW text first (so the rune budget is never eaten by ANSI escape bytes),
// then applies style to whatever portion of prefix survived the truncation.
// Truncating before styling — rather than styling the prefix and truncating
// the already-styled result — keeps the visible width exact and guarantees
// no ANSI escape sequence is ever cut in half.
func styleLinePrefix(prefix, value string, width int, style lipgloss.Style) string {
	line := truncateToWidth(prefix+value, width)
	runes := []rune(line)
	prefixLen := len([]rune(prefix))
	if prefixLen > len(runes) {
		prefixLen = len(runes)
	}
	return style.Render(string(runes[:prefixLen])) + string(runes[prefixLen:])
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Run drives the model through a Bubble Tea program and returns the selected
// candidate. The query seeds the live filter so users get a head-start (the
// fzf path forwards a query the same way). renderer backs the async preview
// pane (nil degrades to the built-in summary). It is the entry point used by
// the selector's TUI selector. A cancelled run (esc/ctrl+c/ctrl+g) returns
// ErrCancelled rather than a plain ok=false so callers can exit quietly
// instead of treating it as "selector unavailable".
func Run(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, layout ...Layout) (source.Candidate, bool, error) {
	var l Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	m := newModelWithLayout(candidates, renderer, ctx, l)
	m.query = query
	m.applyFilter()
	// refreshPreviewLoadingFlag is not re-called here: newModelWithLayout
	// already set it from the full candidate list, and the only thing that
	// could change it (applyFilter emptying the filtered set) is
	// unobservable — previewBody short-circuits on len(filtered)==0 before
	// reading previewLoading, and Init returns a nil Cmd when no candidate
	// is highlighted.
	// WithAltScreen is required: without it, Bubble Tea renders inline and
	// repaints by moving the cursor up N lines on every update. Any render
	// taller than the previous one (e.g. a long query trimming the match
	// list, or a tall preview) desyncs that cursor math, which looks like
	// the top of the screen scrolling away / content getting pushed off the
	// terminal. The alt screen gives Bubble Tea an isolated full-screen
	// buffer so it can always redraw the whole frame instead of patching
	// deltas against terminal scrollback. This is not unit-testable: Bubble
	// Tea's tea.ProgramOption values close over unexported Program fields
	// with no exported inspector, so there is no way to assert this from
	// outside the tea package. Verified manually: scrolling, long queries,
	// and normal navigation no longer corrupt the visible frame.
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return source.Candidate{}, false, err
	}
	return finalizeRun(final.(Model))
}

// finalizeRun turns a terminated model's end state into Run's return triple.
// Factored out so cancellation handling is unit-testable without driving a
// real Bubble Tea program (Run itself always talks to a real tea.Program).
func finalizeRun(m Model) (source.Candidate, bool, error) {
	if m.Cancelled() {
		return source.Candidate{}, false, ErrCancelled
	}
	res, ok := m.Selected()
	return res, ok, nil
}
