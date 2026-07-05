// Package tui is shep's embedded Bubble Tea fuzzy picker. It is the universal
// interactive fallback in the `shep open` selector cascade, used when there
// is no exact match and fzf is unavailable.
//
// The model renders a left list of filtered candidates and a right preview
// showing the highlighted candidate's rendered preview.Result (label/path/
// source/git or [[preview.sections]] output, via the injected
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
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

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
	previewWarn    string
	previewLoading bool
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
	if renderCtx == nil {
		renderCtx = context.TODO()
	}
	m := Model{
		candidates: make([]source.Candidate, len(candidates)),
		filtered:   make([]int, len(candidates)),
		selected:   -1,
		renderer:   renderer,
		renderCtx:  renderCtx,
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
		m.previewWarn = "preview error"
		m.previewText = ""
		return m
	}
	m.previewText = msg.result.Text
	m.previewWarn = msg.result.Warning
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
// bumps previewSeq (invalidating any in-flight render for the old
// candidate), flips on the loading indicator, and returns the Cmd for the new
// async render. A nil renderer or an unchanged highlight returns a nil Cmd.
func (m *Model) syncPreviewAfterSelectionChange(prevKey string) tea.Cmd {
	if m.renderer == nil {
		return nil
	}
	newKey := m.currentPreviewKey()
	if newKey == prevKey {
		return nil
	}
	m.previewSeq++
	if newKey == "" {
		m.previewLoading = false
		m.previewText = ""
		m.previewWarn = ""
		return nil
	}
	m.previewLoading = true
	m.previewWarn = ""
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
	width := m.width
	return func() tea.Msg {
		res, err := renderer.Render(renderCtx, cand, preview.RenderOptions{Width: width})
		return previewResponseMsg{seq: seq, result: res, err: err}
	}
}

// refreshPreviewLoadingFlag sets previewLoading to match whether a renderer
// is wired and a candidate is currently highlighted. Used at construction and
// whenever the filtered set is rebuilt outside the normal key-handling path
// (e.g. Run seeding an initial query).
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
	// scoring: stable order preserved; subsequence match is enough for v1.
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
	listW, prevW := splitWidths(m.width)
	listPane := palette.borderStyle.Render(m.renderList(paneContentWidth(listW)))
	previewPane := palette.borderStyle.Render(m.renderPreview(paneContentWidth(prevW)))
	return lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
}

// splitWidths divides the total reported width into list/preview pane
// budgets (outer widths, before border+padding is subtracted). Each budget
// still needs paneContentWidth to get the actual content width fed to
// renderList/renderPreview.
func splitWidths(width int) (int, int) {
	if width <= 0 {
		width = 80
	}
	list := width * 3 / 5
	if list < 20 {
		list = 20
	}
	prev := width - list - 1
	if prev < 10 {
		prev = 10
	}
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
	b.WriteString(styleQuery.Render("> " + m.query))
	b.WriteString("\n")
	if len(m.filtered) == 0 {
		b.WriteString(palette.mutedStyle.Width(width).Render("  no matches"))
		b.WriteString("\n")
		return b.String()
	}
	// Cap visible rows to a sane height when we know it, deducting chromeRows
	// (border top/bottom + query line) so the border never clips the last
	// visible candidate.
	visible := m.filtered
	maxRows := m.height
	if maxRows > 0 {
		maxRows -= chromeRows
	}
	if maxRows <= 0 {
		maxRows = len(visible)
	}
	if len(visible) > maxRows && maxRows > 2 {
		visible = visible[clamp(m.cursor-maxRows/2, 0, len(visible)-maxRows):]
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
		if i == m.cursor {
			marker = " >"
			b.WriteString(palette.cursorStyle.Width(width).Render(marker + " " + row))
		} else {
			b.WriteString(palette.rowStyle.Width(width).Render(marker + " " + row))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderPreview shows the highlighted candidate's rendered preview plus a
// short help line so the user always knows the keybindings.
func (m Model) renderPreview(width int) string {
	header := palette.previewHeaderStyle.Width(width).Render("preview")
	body := m.previewBody(width)
	help := palette.mutedStyle.Width(width).Render("enter select  esc cancel  ctrl+j/k move")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, "", help)
}

// previewBody renders the preview pane content: "(no selection)" when
// nothing is highlighted, a built-in label/path/source summary when no
// Renderer is wired, a loading indicator while an async render is in flight,
// or the rendered text plus any transient warning (WP-3's safe command
// fallback surfaces here).
func (m Model) previewBody(width int) string {
	if len(m.filtered) == 0 {
		return palette.mutedStyle.Width(width).Render("(no selection)")
	}
	if m.renderer == nil {
		cand, _ := m.currentCandidate()
		return lipgloss.NewStyle().Width(width).Render(
			palette.labelStyle.Render("label  ") + cand.Label + "\n" +
				palette.labelStyle.Render("path   ") + cand.Path + "\n" +
				palette.labelStyle.Render("source ") + cand.Source,
		)
	}
	if m.previewLoading {
		return palette.previewLoadingStyle.Width(width).Render("loading…")
	}
	text := m.previewText
	if m.previewWarn != "" {
		text += "\n" + palette.previewWarnStyle.Render("warn: "+m.previewWarn)
	}
	return lipgloss.NewStyle().Width(width).Render(text)
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
func Run(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer) (source.Candidate, bool, error) {
	m := newModel(candidates, renderer, ctx)
	m.query = query
	m.applyFilter()
	m.refreshPreviewLoadingFlag()
	p := tea.NewProgram(m, tea.WithContext(ctx))
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
