// Package tui is shep's embedded Bubble Tea fuzzy picker. It is the universal
// interactive fallback in the `shep open` selector cascade, used when there is
// no exact match and fzf is unavailable.
//
// The model renders a left list of filtered candidates and a right preview
// showing the highlighted candidate's rendered preview.Result (label/path/
// source/git, or a declared [preview.commands.<name>], via the injected
// preview.Renderer). Filtering uses github.com/sahilm/fuzzy, the same scored
// matcher bubbles/list and gum use (Sublime Text/VSCode style): each candidate
// is searched over its "label path", matches are returned best-match-first
// (first-character, camelCase and separator boundaries, and adjacency all score
// higher), and an empty query lists every candidate in original provider order.
// Navigation uses up/down/ctrl+j/ctrl+k; plain "j"/"k" are typed into the
// query (not bound to movement) so they filter like any other rune; enter
// selects; esc/q/ctrl+c/ctrl+g cancels (Run then returns ErrCancelled);
// ctrl+l toggles the landscape/portrait layout for the current session only
// (never persisted). Below both panes, a full-width footer line always
// shows the highlighted candidate's complete text plus context-sensitive
// keybinding hints (hintsFor), even when the list column truncates its own
// row — the ctrl+t/ctrl+p hint only appears for a Command-only workspace
// (see candidateIsCommandOnly), and it is the sole indicator of which
// entries can be opened as a Herdr tab/pane; list rows carry no per-entry
// type marker. The palette is Catppuccin Mocha, centralised in palette.go
// so colors live in one place.
package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// Layout configures the picker's list/preview pane widths and orientation
// (config.TUIConfig). ListWidth/PreviewWidth are each "auto" (or empty) or a
// percentage string like "60%"; see config.ParsePercent. Orientation is
// LayoutLandscape (default, the zero value) or LayoutPortrait — Television's
// own naming for the same side-by-side vs stacked concept, kept consistent
// since shep already integrates with Television.
type Layout struct {
	ListWidth    string
	PreviewWidth string
	Orientation  string
}

// Orientation values for Layout.Orientation. LayoutLandscape (the zero
// value) is the side-by-side split; LayoutPortrait stacks the list pane
// above the preview pane, both spanning the full terminal width.
const (
	LayoutLandscape = "landscape"
	LayoutPortrait  = "portrait"
)

// minPreviewWidth is the terminal width (PL-11) below which the preview
// panel is hidden entirely to avoid breaking the layout.
const minPreviewWidth = 80

// minPreviewHeight is the terminal height below which the preview panel is
// hidden entirely, mirroring minPreviewWidth: a very short terminal cannot
// fit a bordered two-pane layout without clipping either pane.
const minPreviewHeight = 8

// minListH and minPrevH are the height-axis minimum floors used when
// splitting a portrait layout's vertical share, analogous to minList/
// minPrev on the width axis but sized for ROWS instead of terminal COLUMNS.
// Reusing minList/minPrev (20/10) unchanged for the height axis was the
// root cause of the portrait-overflow bug: those floors were tuned so a
// landscape pane keeps enough columns for readable text, not enough rows —
// any terminal height in [8, 29] hit them and rendered a fixed ~29-line
// block regardless of the actual reported height.
//
// minListH covers the list pane's own chrome (border top/bottom + query
// line — chromeRows) plus 3 candidate rows. It must stay above chromeRows+2:
// renderList's scroll-window logic only activates when maxRows (= listH -
// chromeRows) is > 2 — at maxRows <= 2 it falls through to rendering every
// candidate unbounded instead of capping — so minListH = chromeRows+3 keeps
// maxRows at 3, just above that edge case, while still leaving room for a
// few visible candidate rows.
const minListH = chromeRows + 3

// minPrevH reuses minPreviewHeight: it is already the terminal's own real
// minimum height for a preview pane to render sensibly (previewChromeRows'
// border-only chrome plus 1 body line — see capPreviewBodyLines'
// height-previewChromeRows accounting), independent of
// whether that height budget comes from the full terminal (landscape, where
// both panes share m.height) or a height-axis split share (portrait, where
// the preview only gets prevH).
const minPrevH = minPreviewHeight

// minPortraitHeight is the raw terminal height (m.height, before View's
// footer-line subtraction) below which portrait mode falls back to the
// list-only single-pane layout — the same fallback landscape already uses
// via minPreviewHeight — instead of attempting a stacked split that cannot
// honour minListH+minPrevH without overflowing. Derived as minListH+
// minPrevH+1 (mirroring clampWidths'/clampSizes' own list+prev+1 overflow
// invariant) applied to the budget portrait actually receives (m.height-1,
// one row reserved for the footer), so +1 again for that reserved row.
const minPortraitHeight = minListH + minPrevH + 2

// chromeRows is the fixed vertical overhead of the list pane deducted from
// the reported terminal height before capping visible candidate rows: the
// border's top+bottom edges plus the query line above the candidate rows.
// Without this deduction the last row(s) would render past the bottom
// border and never be visible even when scrolled all the way down.
const chromeRows = 4

// previewChromeRows is the fixed vertical overhead of the preview pane
// deducted from the pane's outer height before capping body lines: 2
// (border top+bottom). The preview pane has no header or help line — see
// renderPreview — so the body gets the full remaining budget.
const previewChromeRows = 2

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

	// currentPane is the Herdr pane shep is running inside, queried once by
	// the caller and threaded in via WithCurrentPane. nil means "no current
	// pane" (shep is not running inside a Herdr workspace pane, or the query
	// failed): the footer's ctrl+t/ctrl+p hints are hidden entirely and
	// handleKey ignores both bindings in that case (see selectWithTarget).
	currentPane *source.Pane
	// chosenTarget records which target the user picked via ctrl+t ("tab")
	// or ctrl+p ("pane"). Empty means no override: enter was pressed (or the
	// run was cancelled), and the caller's --target flag value applies
	// unchanged. Set by selectWithTarget, read back via ChosenTarget once
	// Run returns.
	chosenTarget string

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

// NewModelWithLayout builds a model like NewModel but with an explicit
// Layout (list/preview widths and orientation) — e.g. the caller's loaded
// config.TUIConfig threaded through layoutFromConfig. This lets a caller
// construct and drive a Model (Update/View) directly, without going through
// the full Run bubbletea program loop, when it only needs to seed the
// picker's session-only starting orientation.
func NewModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, layout Layout) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), layout)
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

// Layout returns the model's current session-only Layout (list/preview
// widths and orientation), reflecting any live ctrl+l toggle. It never
// reads back from — or writes to — the config.TUIConfig the caller may have
// built it from; see toggleLayoutOrientation.
func (m Model) Layout() Layout { return m.layout }

// ChosenTarget returns the target the user picked via ctrl+t ("tab") or
// ctrl+p ("pane"). Empty means no override — enter was pressed, or the run
// was cancelled — so the caller's --target flag value applies unchanged.
func (m Model) ChosenTarget() string { return m.chosenTarget }

// WithCurrentPane returns a copy of m with currentPane set to p. Run calls
// this to thread the Herdr pane shep is running inside into the model
// before driving it, so the footer hints and ctrl+t/ctrl+p bindings can
// react to it without extending every existing NewModel/NewModelWithLayout
// call site (most of which never need a current pane at all).
func (m Model) WithCurrentPane(p *source.Pane) Model {
	m.currentPane = p
	return m
}

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
	case "ctrl+l":
		m.toggleLayoutOrientation()
		return m, nil
	case "ctrl+t":
		return m.selectWithTarget("tab")
	case "ctrl+p":
		return m.selectWithTarget("pane")
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

// selectWithTarget handles ctrl+t ("tab") and ctrl+p ("pane"): when shep is
// running inside a Herdr pane (currentPane != nil), there is a highlighted
// candidate to launch (filtered is non-empty), and that candidate is a
// Command-only workspace (candidateIsCommandOnly), it selects the
// highlighted candidate exactly like enter, records target as the chosen
// launch target (read back via ChosenTarget), and quits. It is a no-op —
// the binding is disabled — when currentPane is nil (the tab/pane launch
// targets require shep to already be running inside a Herdr workspace pane;
// see App.launchInCurrentWorkspace), when filtered is empty (no candidate is
// highlighted, mirroring enter's own len(m.filtered) > 0 guard — without
// this, chosenTarget would be set for a launch that never had a candidate),
// or when the highlighted candidate is not Command-only (a group/template/
// plain entry has no command to launch — App.launchInCurrentWorkspace would
// otherwise fail with "requires an entry with a command" after the TUI has
// already quit, which reads as a crash instead of simply staying put).
func (m Model) selectWithTarget(target string) (tea.Model, tea.Cmd) {
	if m.currentPane == nil || len(m.filtered) == 0 {
		return m, nil
	}
	cand, ok := m.currentCandidate()
	if !ok || !candidateIsCommandOnly(cand) {
		return m, nil
	}
	m.selected = m.cursor
	m.chosenTarget = target
	return m, tea.Quit
}

// toggleLayoutOrientation flips m.layout.Orientation between landscape and
// portrait for the current session only (ctrl+l). This never touches the
// loaded config.TUIConfig — Model only ever holds the Layout value it was
// constructed with, so there is nothing here to persist back to disk.
func (m *Model) toggleLayoutOrientation() {
	if m.layout.Orientation == LayoutPortrait {
		m.layout.Orientation = LayoutLandscape
		return
	}
	m.layout.Orientation = LayoutPortrait
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

// applyFilter recomputes the filtered indices from the query.
//
// With no query every candidate is kept in its original (provider) order:
// ranking is meaningless when nothing was typed, and existing UX/tests depend on
// the un-ranked order.
//
// With a query, candidates are ranked by github.com/sahilm/fuzzy — the same
// scored matcher bubbles/list and gum use (Sublime Text/VSCode style). Each
// candidate is searched over its "label path" haystack (via candidateSource, so
// no intermediate []string is allocated per keystroke) and fuzzy.FindFrom
// returns matches already sorted best-match-first (first-character,
// camelCase-boundary, separator-boundary and adjacency matches all score
// higher, with penalties for unmatched and leading characters). We do NOT
// re-sort: the library order is the contract.
//
// sahilm/fuzzy matches case-insensitively via equalFold (see its fuzzy.go) while
// still using the haystack's real case for camelCase scoring, so we intentionally
// do NOT strings.ToLower anything — lowercasing would erase the camelCase signal
// that is the main reason for adopting this matcher.
//
// The cursor is clamped back into range at the end so an empty result never
// leaves a dangling cursor.
func (m *Model) applyFilter() {
	m.filtered = m.filtered[:0]
	if m.query == "" {
		for i := range m.candidates {
			m.filtered = append(m.filtered, i)
		}
	} else {
		for _, mt := range fuzzy.FindFrom(m.query, candidateSource(m.candidates)) {
			m.filtered = append(m.filtered, mt.Index)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(len(m.filtered)-1, 0)
	}
}

// candidateSource adapts []source.Candidate to fuzzy.Source so applyFilter can
// match directly against each candidate's "label path" haystack without
// allocating an intermediate []string on every keystroke. It preserves the
// exact same haystack the previous boolean matcher used (label + " " + path),
// so the UX of matching against either field is unchanged.
type candidateSource []source.Candidate

// String returns the searchable haystack for candidate i: its label and path
// joined by a space. The original (mixed) case is kept on purpose so
// sahilm/fuzzy can award camelCase-boundary bonuses.
func (cs candidateSource) String(i int) string {
	c := cs[i]
	return c.Label + " " + c.Path
}

// Len reports the number of candidates, satisfying fuzzy.Source.
func (cs candidateSource) Len() int { return len(cs) }

// isPrintable returns true for single-rune printable input that should extend
// the query. We avoid pulling in unicode classes for the v1 picker.
func isPrintable(s string) bool {
	if s == "" || len([]rune(s)) != 1 {
		return false
	}
	r := []rune(s)[0]
	return r >= 0x20 && r != 0x7f
}

// View renders the two-pane UI plus a full-width footer line: a left
// candidate list with the cursor and a right preview of the highlighted
// candidate, each wrapped in a rounded border (palette.borderStyle), and
// below both a single footer line spanning the FULL terminal width showing
// the currently highlighted candidate's full, untruncated icon+label/path
// (footerText) — inspired by Atuin's "always show the full command"
// pattern, useful because the list column can be narrow and truncate rows.
// Widths auto-balance based on the reported window size (falling back to
// 60/40 when no size yet); the border's frame size is subtracted from each
// pane's allotted width so content never overflows its own border. The
// footer reserves exactly 1 line: the pane budget fed to renderList/
// renderPreview (paneHeight, via a shallow copy so chromeRows/
// capPreviewBodyLines accounting is unaffected otherwise) is m.height-1, not
// m.height, so the panes shrink to make room rather than the footer
// overflowing the reported terminal height. Below minPreviewWidth columns or
// minPreviewHeight rows the preview pane is hidden entirely (PL-11) so a
// narrow or very short terminal never breaks the layout. Portrait mode uses
// its own, higher threshold (minPortraitHeight) instead of minPreviewHeight:
// stacking list+preview needs room for BOTH panes' own minimum floors
// (minListH+minPrevH), which landscape's side-by-side share of the full
// m.height does not.
func (m Model) View() string {
	paneHeight := m.height
	if paneHeight > 0 {
		paneHeight--
	}
	paneModel := m
	paneModel.height = paneHeight

	minHeightForPreview := minPreviewHeight
	if m.layout.Orientation == LayoutPortrait {
		minHeightForPreview = minPortraitHeight
	}
	hidePreview := (m.width > 0 && m.width < minPreviewWidth) ||
		(m.height > 0 && m.height < minHeightForPreview)

	footer := m.renderFooter()

	var body string
	switch {
	case hidePreview:
		body = paneBoxStyle(paneHeight).Render(paneModel.renderList(paneContentWidth(m.width)))
	case m.layout.Orientation == LayoutPortrait:
		body = paneModel.renderPortrait()
	default:
		listW, prevW := splitWidths(m.width, m.layout)
		listPane := paneBoxStyle(paneHeight).Render(paneModel.renderList(paneContentWidth(listW)))
		previewPane := paneBoxStyle(paneHeight).Render(paneModel.renderPreview(paneContentWidth(prevW)))
		body = lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

// footerSeparator joins the candidate label and the keybinding hints in the
// footer line.
const footerSeparator = "  ·  "

// hintsFor returns the context-sensitive keybinding hints shown in the
// footer for cand. enter/esc/ctrl+l are always live, so they always appear.
// ctrl+t (open a new Herdr tab) and ctrl+p (split a new Herdr pane) only
// appear when BOTH shep is running inside a Herdr pane (hasCurrentPane) AND
// cand is a Command-only workspace (candidateIsCommandOnly) — the only kind
// of entry selectWithTarget actually launches. Advertising a binding that
// would silently no-op (a group/template/plain entry, or no current pane at
// all) would be misleading, so those hints are hidden entirely rather than
// shown dimmed.
func hintsFor(cand source.Candidate, hasCurrentPane bool) string {
	if hasCurrentPane && candidateIsCommandOnly(cand) {
		return "enter: open · ctrl+t: tab · ctrl+p: pane · esc: cancel · ctrl+l: layout"
	}
	return "enter: open · esc: cancel · ctrl+l: layout"
}

// renderFooter builds the full-width footer line: the currently highlighted
// candidate's full text (footerText) followed by the context-sensitive
// keybinding hints (hintsFor), separated by footerSeparator. When the full
// line would overflow m.width, the hints are kept intact (they are the
// actionable part) and the label is truncated instead — the reverse of
// naively truncating the whole composed string, which would eat into the
// hints first since they come last. Merged into the single existing footer
// line (rather than a separate line) so the panes' height budget math —
// carefully tuned around exactly one reserved footer row, see
// minPortraitHeight — never has to change.
func (m Model) renderFooter() string {
	cand, _ := m.currentCandidate()
	hints := hintsFor(cand, m.currentPane != nil)
	label := m.footerText()
	if m.width > 0 {
		budget := m.width - lipgloss.Width(footerSeparator) - lipgloss.Width(hints)
		label = truncateToWidth(label, budget)
	}
	full := palette.mutedStyle.Render(label) + footerSeparator + palette.mutedStyle.Render(hints)
	return lipgloss.NewStyle().Width(m.width).Render(truncateToWidth(full, m.width))
}

// renderPortrait stacks the list pane above the preview pane, each spanning
// the full terminal width — Television's "portrait" layout naming. list_
// width/preview_width are reinterpreted as the size share along the height
// (split) axis: the percent-parsing/overflow-reconciliation core (splitSizes)
// is genuinely axis-agnostic (it only ever operates on an opaque "total" int
// and returns two shares summing to total-1), so it is reused unchanged here
// fed m.height instead of m.width — no new percent-parsing code. It IS fed
// its own height-axis minimum floors (minListH/minPrevH) instead of
// splitWidths' minList/minPrev: those are column-width floors and produced a
// fixed, overflowing pane split when reused unchanged for rows (see
// minListH's doc comment).
func (m Model) renderPortrait() string {
	listH, prevH := splitSizes(m.height, m.layout, minListH, minPrevH)
	listModel := m
	listModel.height = listH
	prevModel := m
	prevModel.height = prevH
	listPane := paneBoxStyle(listH).Render(listModel.renderList(paneContentWidth(m.width)))
	previewPane := paneBoxStyle(prevH).Render(prevModel.renderPreview(paneContentWidth(m.width)))
	return lipgloss.JoinVertical(lipgloss.Left, listPane, previewPane)
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
//
// This function is axis-agnostic in principle (it only ever operates on an
// opaque "total" int and returns two shares summing to total-1); splitSizes
// below is the actual axis-agnostic core, parameterized on the minimum floor
// pair so a caller splitting a HEIGHT (portrait) is never forced through
// splitWidths' width-tuned minList/minPrev floors — see minListH's doc
// comment for why that reuse-unchanged used to overflow.
func splitWidths(width int, layout Layout) (int, int) {
	return splitSizes(width, layout, minList, minPrev)
}

// splitSizes is the axis-agnostic core: it computes list/preview shares of
// total from layout's percent config, then clamps to whichever minimum
// floor pair the caller supplies via clampSizes — minList/minPrev (width
// axis, splitWidths) or minListH/minPrevH (height axis, renderPortrait).
// Column-width floors and row-height floors are NOT interchangeable (their
// confusion was the portrait-overflow bug this parameterization fixes), so
// every caller must supply floors tuned for its own axis.
func splitSizes(total int, layout Layout, minA, minB int) (int, int) {
	if total <= 0 {
		total = 80
	}
	listFrac, listOK := config.PercentOrAuto(layout.ListWidth)
	prevFrac, prevOK := config.PercentOrAuto(layout.PreviewWidth)

	var a, b int
	switch {
	case listOK && prevOK:
		a, b = splitBothPercent(total, listFrac, prevFrac)
	case listOK:
		a = int(float64(total) * listFrac)
		b = total - a - 1
	case prevOK:
		b = int(float64(total) * prevFrac)
		a = total - b - 1
	default:
		a = total * 3 / 5
		b = total - a - 1
	}
	return clampSizes(total, a, b, minA, minB)
}

// minList and minPrev are the width-axis minimum pane floors (columns),
// used by splitWidths/clampWidths for landscape splits. See minListH/
// minPrevH for the height-axis equivalents used by portrait.
const minList = 20
const minPrev = 10

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
	return clampSizes(width, list, prev, minList, minPrev)
}

// clampSizes is the axis-agnostic core previously hardcoded inside
// clampWidths as minList/minPrev: it enforces the supplied minA/minB
// minimum floors and the invariant that a+gap+b never exceeds total,
// reconciling any overflow by shrinking whichever share is still above its
// own floor (a first, then b) to make room. When total itself is too small
// to fit both floors plus the gap, the floors still win and the result may
// overflow — an unavoidable floor case on a very narrow terminal/height,
// not a regression from this reconciliation. Callers pick the floor pair
// for their axis: minList/minPrev (columns, clampWidths) or minListH/
// minPrevH (rows, renderPortrait via splitSizes).
func clampSizes(total, a, b, minA, minB int) (int, int) {
	if a < minA {
		a = minA
	}
	if b < minB {
		b = minB
	}
	if overflow := a + b + 1 - total; overflow > 0 {
		if room := a - minA; room > 0 {
			shrink := room
			if shrink > overflow {
				shrink = overflow
			}
			a -= shrink
			overflow -= shrink
		}
		if overflow > 0 {
			if room := b - minB; room > 0 {
				shrink := room
				if shrink > overflow {
					shrink = overflow
				}
				b -= shrink
			}
		}
	}
	return a, b
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

// paneBoxStyle returns palette.borderStyle with a fixed content height so a
// pane's outer border sits at exactly outerHeight rows regardless of how
// many lines the pane's own content naturally renders — the fix for the
// preview (and list) pane border growing/shrinking with the highlighted
// candidate's content instead of staying anchored at its assigned budget
// (the full pane height in landscape, or the splitSizes share in portrait).
//
// lipgloss.Style.Render applies Height() BEFORE the border is drawn (pads/
// aligns the content to `height` lines, then wraps it in the border), so
// the height passed to Height() must be the CONTENT height, i.e.
// outerHeight minus the border's own vertical frame size (2 rows: top+
// bottom, palette.borderStyle has no vertical padding). Height() only PADS
// short content — it never truncates long content (lipgloss's
// alignTextVertical returns oversized input unchanged) — so callers must
// independently guarantee their content never exceeds outerHeight-2 lines
// (see capPreviewBodyLines for the preview pane; renderList's maxRows cap
// already does this for the list pane).
//
// outerHeight<=0 means unknown (headless/test contexts without a
// WindowSizeMsg): the unmodified borderStyle is returned so panes still
// render at their natural content height, matching every other height<=0
// fallback in this file (see capPreviewBodyLines, View's hidePreview check).
func paneBoxStyle(outerHeight int) lipgloss.Style {
	if outerHeight <= 0 {
		return palette.borderStyle
	}
	inner := outerHeight - palette.borderStyle.GetVerticalFrameSize()
	if inner < 1 {
		inner = 1
	}
	return palette.borderStyle.Height(inner)
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
		row := candidateDisplayText(c)
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

// candidateDisplayText builds the full, untruncated "icon label-or-path
// (missing)" text for one candidate — the same construction renderList uses
// for each row, factored out so the footer (which must show the currently
// highlighted candidate's FULL text, not a width-truncated row) can reuse it
// without duplicating the icon/label-or-path/missing-suffix logic.
func candidateDisplayText(c source.Candidate) string {
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
	return row
}

// candidateIsCommandOnly reports whether cand is a Command-only workspace —
// a plain `command = "..."` entry that is neither a group (Meta["group"] ==
// "true") nor a template (Meta["template"] != ""). This is the only kind of
// entry that can actually be opened as a Herdr tab/pane target
// (App.launchInCurrentWorkspace requires a command); selectWithTarget and
// hintsFor both use this to keep the ctrl+t/ctrl+p binding — and its footer
// hint — a no-op/hidden on any entry that can't honour it.
func candidateIsCommandOnly(cand source.Candidate) bool {
	return cand.Meta["command"] != "" && cand.Meta["group"] != "true" && cand.Meta["template"] == ""
}

// footerText returns the full, untruncated display text for the currently
// highlighted candidate (icon+label-or-path+missing-suffix, via
// candidateDisplayText), or a muted "(no selection)" placeholder when the
// filtered set is empty — matching previewBody's own empty-state text.
func (m Model) footerText() string {
	cand, ok := m.currentCandidate()
	if !ok {
		return "(no selection)"
	}
	return candidateDisplayText(cand)
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

// renderPreview shows the highlighted candidate's rendered preview. There is
// no header or help line — the keybinding hints live in the single footer
// line (see hintsFor) so the preview pane's full budget goes to content.
// When the terminal height is known, the body is capped to m.height -
// previewChromeRows lines so a long output (e.g. dir or active pane content)
// never expands infinitely and breaks JoinHorizontal / pushes the search box
// off screen.
func (m Model) renderPreview(width int) string {
	body := m.previewBody(width)
	return capPreviewBodyLines(body, m.height)
}

// capPreviewBodyLines truncates body so it never exceeds the preview pane's
// fixed body-line budget, appending an ellipsis line in place of the last
// surviving line when truncation occurs. height is the pane's own outer
// height budget (m.height — already the correct per-pane budget in both
// landscape and portrait; see View/renderPortrait), the same value fed to
// paneBoxStyle for the border. A non-positive height (unknown) returns body
// unchanged so previews still render fully in headless/test contexts.
//
// The body budget is height minus the border's 2 rows (top+bottom, see
// paneBoxStyle) — renderPreview has no header/help chrome around the body.
// Capping strictly to this budget is what lets paneBoxStyle's Height()
// modifier safely PAD shorter bodies up to the same budget without ever
// having to truncate: Height() never truncates oversized content on its
// own (see paneBoxStyle's doc comment), so this cap is the only thing
// standing between a long preview body and the pane overflowing past its
// fixed bottom border.
func capPreviewBodyLines(body string, height int) string {
	if height <= 0 {
		return body
	}
	maxLines := height - previewChromeRows
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= maxLines {
		return body
	}
	if maxLines == 1 {
		return "…"
	}
	truncated := append(lines[:maxLines-1], "…")
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
// candidate plus the target the user chose (ctrl+t => "tab", ctrl+p =>
// "pane", or "" for the default via enter). The query seeds the live filter
// so users get a head-start (the fzf path forwards a query the same way).
// renderer backs the async preview pane (nil degrades to the built-in
// summary). currentPane is the Herdr pane shep is running inside (nil when
// not running inside one), threaded into the model so the footer hints and
// ctrl+t/ctrl+p bindings can react to it. It is the entry point used by the
// selector's TUI selector. A cancelled run (esc/ctrl+c/ctrl+g) returns
// ErrCancelled rather than a plain ok=false so callers can exit quietly
// instead of treating it as "selector unavailable".
func Run(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, currentPane *source.Pane, layout ...Layout) (source.Candidate, string, bool, error) {
	var l Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	m := newModelWithLayout(candidates, renderer, ctx, l).WithCurrentPane(currentPane)
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
		return source.Candidate{}, "", false, err
	}
	return finalizeRun(final.(Model))
}

// finalizeRun turns a terminated model's end state into Run's return
// quadruple. Factored out so cancellation handling is unit-testable without
// driving a real Bubble Tea program (Run itself always talks to a real
// tea.Program).
func finalizeRun(m Model) (source.Candidate, string, bool, error) {
	if m.Cancelled() {
		return source.Candidate{}, "", false, ErrCancelled
	}
	res, ok := m.Selected()
	return res, m.ChosenTarget(), ok, nil
}
