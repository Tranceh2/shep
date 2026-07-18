// Package tui is shep's embedded Bubble Tea fuzzy picker. It is the universal
// interactive fallback in the `shep open` selector cascade, used when there is
// no exact match and fzf is unavailable.
//
// The picker renders a single flat, progressively-disclosed list on the left
// (active Herdr workspaces, discovered projects, zoxide directories, and
// configured [[workspaces]] entries, in the configured general.sources order
// — differentiated only by each row's icon/color per source, with Herdr
// workspaces able to expand into their open tabs and, per tab, its panes)
// and a contextual, scrollable preview on the right. Filtering uses
// github.com/sahilm/fuzzy (see rows.go's fuzzyMatches): within a source,
// candidates are kept in their ORIGINAL PROVIDER ORDER — a query only
// decides visibility, never re-ranks — so parent order never jumps around
// as the user types (see buildRows' doc comment for the full contract).
// Tab/Shift+Tab cycles keyboard focus between the list and the preview pane
// (FocusList/FocusPreview — see the Focus ring in keys.go); while the
// preview is focused, arrow/page keys scroll it (via bubbles/viewport)
// instead of moving the list cursor, and any printable rune returns focus
// to the list and resumes the live filter. Enter opens a candidate/tab/pane
// row; Left/Right expand/collapse a Herdr workspace's tab/pane children —
// both are List-only actions, as is ctrl+l (toggles the session-only layout
// override: auto -> landscape -> auto). "?" opens a modal,
// scrollable help overlay (FocusHelp) from either List or Preview,
// remembering which one so "?"/Esc restores it on close (a resize to a
// list-only size while Preview was remembered degrades that memory to List
// — see degradeFocusIfPreviewUnavailable); esc/ctrl+c/ctrl+g cancels (Run
// then returns ErrCancelled), except Esc first clears a non-empty query
// (returning to FocusList) before ever cancelling. "q" is an ordinary query
// character, not a cancel key. The active color theme (see theme.go)
// resolves from $NO_COLOR, then
// $SHEP_THEME, then Layout.Theme (config.TUIConfig.Theme), then Catppuccin
// Mocha.
package tui

import (
	"context"
	"errors"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// Layout configures the picker's list/preview pane widths, orientation
// override, and color theme (config.TUIConfig). ListWidth/PreviewWidth are
// each "auto" (or empty) or a percentage string like "60%"; see
// config.ParsePercent. Orientation is "" (auto — the responsive width-based
// mode described in nextResponsiveMode applies) or LayoutLandscape (forces
// wide/side-by-side mode). The stacked "portrait" orientation was removed.
// Theme is a theme.go theme name (or empty for the default resolution chain:
// $NO_COLOR > $SHEP_THEME > Theme > "mocha").
type Layout struct {
	ListWidth    string
	PreviewWidth string
	Orientation  string
	Theme        string
	// SourceOrder is the configured group iteration order (config's
	// general.sources, in declaration order — the same order
	// source.Registry.Enabled() already collects candidates in). Threaded
	// through the same Layout vehicle as Theme/widths so the picker's row
	// order matches the configured provider order instead of a hardcoded
	// literal. Empty falls back to rows.go's defaultSourceOrder.
	SourceOrder []string
	// Icons selects the fallback tier (IconsUnicode/IconsASCII) for the
	// picker's own semantic icons — see icons.go's resolveIconSet and
	// Model.icons(). Empty defaults to IconsUnicode, byte-identical to
	// the picker's pre-Phase-8 hardcoded glyphs.
	Icons string
}

// Orientation values for Layout.Orientation. The empty string means "auto":
// the responsive width-based mode (see nextResponsiveMode) picks wide vs.
// list-only from the reported terminal size, with hysteresis so a borderline
// resize never flaps between modes every frame. LayoutLandscape forces wide
// mode (still subject to the terminal-height floor). The "portrait" (stacked)
// orientation was removed.
const LayoutLandscape = "landscape"

// ErrCancelled is the quiet cancellation sentinel returned by Run when the
// user quits without selecting (esc/ctrl+c/ctrl+g). Callers use errors.Is to
// distinguish an intentional cancel from "no selector available" (ok=false
// with a nil error) so they can exit without printing anything.
var ErrCancelled = errors.New("cancelled")

// Focus identifies which pane currently owns keyboard input for
// navigation/scrolling: FocusList (the default) routes up/down/left/right to
// the row cursor and query editing; FocusPreview routes them to the preview
// viewport's scroll position instead; FocusHelp is the modal "?" help
// overlay — it is NOT a member of the Tab/Shift+Tab ring (see focusRing in
// keys.go), it only opens from FocusList/FocusPreview (recording that state
// in Model.prevFocus) and only closes via "?" or Esc, restoring prevFocus.
type Focus int

const (
	FocusList Focus = iota
	FocusPreview
	FocusHelp
)

// Model is the Bubble Tea model for the shep picker.
type Model struct {
	// candidates is the flat, ungrouped candidate set as supplied by the
	// caller (== baseCandidates for a tree-wired model — see
	// newModelWithTreeLayout).
	candidates []source.Candidate
	// baseCandidates is the immutable flat set a tree-wired model
	// (NewModelWithTree) was constructed from; nil for a plain
	// NewModel/NewModelWithLayout model (no tree, no children ever
	// synthesized — see fetchAllChildren).
	baseCandidates []source.Candidate
	// tree fetches/caches a Herdr workspace's tabs+panes so buildRows can
	// synthesize RowTab/RowPane children. nil means tree-expand is
	// inactive: every group's candidates render flat with no descendants.
	tree *TreeExpander

	// rows is the current visible, grouped row list — the single source of
	// truth for rendering and navigation. Rebuilt by applyFilter whenever
	// the query, expand/collapse state, or tree contents change.
	rows   []Row
	cursor int // index into rows

	query  string
	width  int
	height int
	// mode is the resolved responsive display mode ("wide"/"list-only"),
	// recomputed on every tea.WindowSizeMsg (see nextResponsiveMode) — never
	// inside View, which must stay a pure projection of already-settled
	// state.
	mode string

	selected       source.Candidate
	hasSelected    bool
	selectedAction RowAction
	cancelled      bool
	layout         Layout
	theme          Theme
	styles         styleSet

	// currentPane is the Herdr pane shep is running inside, queried once by
	// the caller and threaded in via WithCurrentPane. nil means "no current
	// pane": the footer's ctrl+t/ctrl+p hints are hidden and handleKey
	// ignores both bindings (see selectWithTarget).
	currentPane *source.Pane
	// chosenTarget records which target the user picked via ctrl+t ("tab")
	// or ctrl+p ("pane"). Empty means enter was pressed (or the run was
	// cancelled), so the caller's --target flag value applies unchanged.
	chosenTarget string

	// renderer produces the preview pane content asynchronously for a
	// RowCandidate row. nil degrades to a built-in label/path/source
	// summary with no async requests.
	renderer  preview.Renderer
	renderCtx context.Context
	// previewSeq tags every in-flight preview render (candidate or pane
	// buffer); a response whose seq no longer matches is stale and
	// discarded.
	previewSeq  int
	previewText string
	// previewSections stores the structured Result.Sections from the last
	// successful async candidate render, so preview_body.go can consume
	// each section by Kind without parsing the joined text. nil when no
	// render has resolved (loading), when the renderer returned no Sections
	// (safe degradation to compact identity), or when the current row is
	// not a RowCandidate (RowPane uses panePreviewMsg, which is raw text).
	previewSections []preview.Section
	previewLoading  bool
	// previewErr holds a short user-visible message when Render (or
	// TreeExpander.ReadPane) itself returned a real error. Empty after any
	// successful render or on selection change.
	previewErr string

	// expandedWorkspaces is the set of Herdr workspace_ids the user
	// manually expanded (Left/Right/Enter on a RowCandidate) at an empty
	// query — progressive disclosure: an empty query never shows any
	// workspace's tabs/panes unless the user asked for them (see
	// expandedChildren in rows.go).
	expandedWorkspaces map[string]bool
	// sourceOrder is the resolved row-group iteration order (see
	// Layout.SourceOrder), threaded straight into every rowBuildInput by
	// applyFilter.
	sourceOrder []string

	// focus is which pane currently owns up/down/left/right/page navigation.
	focus Focus
	// prevFocus is the focus state (FocusList or FocusPreview) recorded the
	// moment "?" opens FocusHelp, so closing help ("?" or Esc) restores
	// keyboard focus to wherever the user actually was instead of always
	// snapping back to the list.
	prevFocus Focus
	// viewport backs the preview pane's internal scroll position. Its
	// Width/Height/Content are refreshed every Update call (syncViewport) —
	// transient, never itself the source of truth for preview text — so
	// only YOffset (mutated while focus==FocusPreview) needs to persist
	// across renders.
	viewport viewport.Model
	// helpViewport backs the "?" help overlay's own scroll position,
	// independent of the preview pane's viewport. Its Width/Height/Content
	// are refreshed every Update call (syncHelpViewport) so the help body
	// text is never silently clipped at a short terminal height — only
	// YOffset (mutated while focus==FocusHelp) needs to persist.
	helpViewport viewport.Model

	// spinner animates the "loading…" preview indicator. spinnerRunning
	// guards against scheduling more than one tick loop: a fresh
	// spinner.Tick() Cmd is only ever issued on the false->true edge of
	// previewLoading (see syncPreviewAfterSelectionChange/handleSpinnerTick),
	// and the loop self-terminates (returns no further Cmd) the moment
	// previewLoading goes false, rather than ticking forever in the
	// background.
	spinner        spinner.Model
	spinnerRunning bool
}

// previewResponseMsg carries the result of an async candidate preview
// render. seq must match the model's current previewSeq or the response is
// stale and ignored.
type previewResponseMsg struct {
	seq    int
	result preview.Result
	err    error
}

// panePreviewMsg carries the result of an async RowPane buffer capture
// (TreeExpander.ReadPane). seq must match the model's current previewSeq or
// the response is stale and ignored — identical contract to
// previewResponseMsg, kept as a distinct type so Update's type switch stays
// exhaustive and self-documenting about which preview path produced it.
type panePreviewMsg struct {
	seq  int
	text string
	err  error
}

// NewModel builds a model over the supplied candidates. renderer may be nil,
// in which case the preview pane shows a static built-in summary instead of
// an async render.
func NewModel(candidates []source.Candidate, renderer preview.Renderer) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), Layout{})
}

// NewModelWithLayout builds a model like NewModel but with an explicit
// Layout (list/preview widths, orientation override, and theme).
func NewModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, layout Layout) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), layout)
}

// NewModelWithTree builds a tree-expand-aware model: candidates is the flat
// base row set (retained as baseCandidates), and tree fetches/caches each
// SourceHerdr candidate's tabs/panes so buildRows can synthesize matching
// RowTab/RowPane children. A nil tree degrades to identical flat-per-group
// behavior, so this constructor is always safe to call even when the caller
// has no HerdrDriver wired.
func NewModelWithTree(candidates []source.Candidate, renderer preview.Renderer, tree *TreeExpander, layout Layout) Model {
	return newModelWithTreeLayout(candidates, renderer, context.TODO(), tree, layout)
}

func newModelWithTreeLayout(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context, tree *TreeExpander, layout Layout) Model {
	m := newModelWithLayout(candidates, renderer, renderCtx, layout)
	m.baseCandidates = make([]source.Candidate, len(candidates))
	copy(m.baseCandidates, candidates)
	m.tree = tree
	m.applyFilter()
	return m
}

func newModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context, layout Layout) Model {
	if renderCtx == nil {
		renderCtx = context.TODO()
	}
	theme := resolveTheme(layout.Theme)
	styles := newPalette(theme)
	m := Model{
		candidates:         make([]source.Candidate, len(candidates)),
		selected:           source.Candidate{},
		renderer:           renderer,
		renderCtx:          renderCtx,
		layout:             layout,
		theme:              theme,
		styles:             styles,
		expandedWorkspaces: map[string]bool{},
		sourceOrder:        layout.SourceOrder,
		spinner:            spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styles.previewLoadingStyle)),
		// mode starts "" (unknown/not yet sized): View treats "" the same
		// as modeWide (side-by-side, using the same width<=0 fallback
		// splitSizes already applies) until the first real
		// tea.WindowSizeMsg arrives and nextResponsiveMode takes over —
		// matching the previous picker's "landscape by default in a
		// headless/test context" behavior.
	}
	copy(m.candidates, candidates)
	m.applyFilter()
	m.refreshPreviewLoadingFlag()
	return m
}

// Selected returns the chosen candidate and ok=true after enter is pressed.
// ok=false means the user cancelled or has not selected yet.
func (m Model) Selected() (source.Candidate, bool) { return m.selected, m.hasSelected }

// SelectedAction returns the typed RowAction of the row Enter was pressed on
// (RowActionOpen for a normal candidate, RowActionFocusTab for a synthesized
// tab/pane row). It is the typed launch signal the command layer dispatches
// on, replacing the old candidate.Source string check. RowActionOpen before
// any selection.
func (m Model) SelectedAction() RowAction { return m.selectedAction }

// Cancelled reports whether the user quit without selecting
// (esc/ctrl+c/ctrl+g).
func (m Model) Cancelled() bool { return m.cancelled }

// Layout returns the model's current session-only Layout (list/preview
// widths, orientation override, theme), reflecting any live ctrl+l toggle.
// It never reads back from — or writes to — the config.TUIConfig the caller
// may have built it from.
func (m Model) Layout() Layout { return m.layout }

// icons resolves this Model's configured icon fallback tier from
// Layout.Icons — see resolveIconSet. Computed on demand (not cached as a
// Model field) so every existing test/production construction path,
// including a bare Model{} literal with a zero-value Layout, resolves the
// same backward-compatible IconsUnicode default without needing to be
// updated for Phase 8.
func (m Model) icons() IconSet {
	return resolveIconSet(m.layout.Icons)
}

// ChosenTarget returns the target the user picked via ctrl+t ("tab") or
// ctrl+p ("pane"). Empty means enter was pressed, or the run was cancelled.
func (m Model) ChosenTarget() string { return m.chosenTarget }

// WithCurrentPane returns a copy of m with currentPane set to p. Run calls
// this to thread the Herdr pane shep is running inside into the model
// before driving it.
func (m Model) WithCurrentPane(p *source.Pane) Model {
	m.currentPane = p
	return m
}

// Init kicks off the first async preview render for the initially
// highlighted row when a Renderer (or tree, for a pane row) is wired.
func (m Model) Init() tea.Cmd {
	return m.initialPreviewCmd()
}

// Update handles key presses, window sizing, and async preview responses.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.mode = nextResponsiveMode(m, m.mode)
		m.degradeFocusIfPreviewUnavailable()
	case previewResponseMsg:
		m = m.handlePreviewResponse(msg)
	case panePreviewMsg:
		m = m.handlePanePreviewResponse(msg)
	case spinner.TickMsg:
		m, cmd = m.handleSpinnerTick(msg)
	case tea.KeyMsg:
		var next tea.Model
		next, cmd = m.handleKey(msg)
		m = next.(Model)
	}
	m.syncViewport()
	m.syncHelpViewport()
	return m, cmd
}

// degradeFocusIfPreviewUnavailable corrects m.focus/m.prevFocus after a
// resize that just resolved to modeListOnly (no preview pane at all): a
// stale FocusPreview would otherwise strand the user — cycleFocusForward/
// Backward are no-op in modeListOnly (nothing to Tab back to) and
// handlePreviewFocusedKey keeps routing every key regardless of m.mode, so
// Down/Enter/Tab would all be silently swallowed. Called only from the
// tea.WindowSizeMsg branch of Update, right after m.mode is recomputed.
//
// Two cases:
//   - m.focus == FocusPreview: refocus straight to FocusList.
//   - m.focus == FocusHelp with m.prevFocus == FocusPreview: the overlay
//     stays open (a resize must never silently close Help), but the
//     recorded prevFocus is degraded to FocusList so closing Help
//     afterwards ("?"/Esc) restores an available focus instead of the
//     now-stale FocusPreview.
func (m *Model) degradeFocusIfPreviewUnavailable() {
	if m.mode != modeListOnly {
		return
	}
	if m.focus == FocusPreview {
		m.focus = FocusList
	}
	if m.focus == FocusHelp && m.prevFocus == FocusPreview {
		m.prevFocus = FocusList
	}
}

// handlePreviewResponse applies a completed async candidate render,
// discarding it as stale when its seq no longer matches previewSeq.
func (m Model) handlePreviewResponse(msg previewResponseMsg) Model {
	if msg.seq != m.previewSeq {
		return m
	}
	m.previewLoading = false
	if msg.err != nil {
		m.previewErr = "preview error"
		m.previewText = ""
		m.previewSections = nil
		return m
	}
	m.previewErr = ""
	m.previewText = msg.result.Text
	m.previewSections = msg.result.Sections
	return m
}

// handlePanePreviewResponse applies a completed async RowPane buffer
// capture, discarding it as stale when its seq no longer matches
// previewSeq — identical contract to handlePreviewResponse.
func (m Model) handlePanePreviewResponse(msg panePreviewMsg) Model {
	if msg.seq != m.previewSeq {
		return m
	}
	m.previewLoading = false
	if msg.err != nil {
		m.previewErr = ""
		m.previewText = ""
		return m
	}
	m.previewText = msg.text
	return m
}

// handleSpinnerTick advances the loading spinner while spinnerNeeded() is
// still true (a preview render in flight, or a visible working-status pane
// icon), and lets the tick loop die (returns a nil Cmd) the moment neither
// condition holds — the single mechanism preventing more than one live tick
// loop (see spinnerRunning's doc comment).
func (m Model) handleSpinnerTick(msg spinner.TickMsg) (Model, tea.Cmd) {
	if !m.spinnerNeeded() {
		m.spinnerRunning = false
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

// Run drives the model through a Bubble Tea program and returns the selected
// candidate, the typed RowAction of the picked row (RowActionFocusTab for a
// synthesized tab/pane row, RowActionOpen otherwise), and the target the user
// chose (ctrl+t => "tab", ctrl+p => "pane", or "" for the default via enter).
func Run(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, currentPane *source.Pane, layout ...Layout) (source.Candidate, RowAction, string, bool, error) {
	var l Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	m := newModelWithLayout(candidates, renderer, ctx, l).WithCurrentPane(currentPane)
	m.query = query
	m.applyFilter()
	return runProgram(ctx, m)
}

// RunWithTree is Run's tree-expand-active counterpart: identical contract,
// but the model is built via newModelWithTreeLayout so a non-empty query (or
// a manual expand) can synthesize Herdr tab/pane child rows under a matching
// SourceHerdr candidate.
func RunWithTree(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, tree *TreeExpander, currentPane *source.Pane, layout ...Layout) (source.Candidate, RowAction, string, bool, error) {
	var l Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	m := newModelWithTreeLayout(candidates, renderer, ctx, tree, l).WithCurrentPane(currentPane)
	m.query = query
	m.applyFilter()
	return runProgram(ctx, m)
}

// runProgram drives m through a real Bubble Tea program and turns its
// terminated state into the (Candidate, RowAction, target, ok, error)
// quintuple both Run and RunWithTree return.
//
// WithAltScreen is required: without it, Bubble Tea renders inline and
// repaints by moving the cursor up N lines on every update, which desyncs
// against any render taller than the previous one. This is not
// unit-testable (tea.ProgramOption values close over unexported Program
// fields); verified manually.
func runProgram(ctx context.Context, m Model) (source.Candidate, RowAction, string, bool, error) {
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return source.Candidate{}, RowActionOpen, "", false, err
	}
	return finalizeRun(final.(Model))
}

// finalizeRun turns a terminated model's end state into Run's return
// quintuple. Factored out so cancellation handling is unit-testable without
// driving a real Bubble Tea program.
func finalizeRun(m Model) (source.Candidate, RowAction, string, bool, error) {
	if m.Cancelled() {
		return source.Candidate{}, RowActionOpen, "", false, ErrCancelled
	}
	res, ok := m.Selected()
	return res, m.SelectedAction(), m.ChosenTarget(), ok, nil
}
