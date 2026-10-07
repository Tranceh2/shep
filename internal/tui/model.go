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
// internal/fuzzy (via rows.go's fuzzyMatch): without an active ranking snapshot,
// empty queries preserve configured source and provider order; active history
// ranks empty queries by frecency while non-empty queries retain fuzzy dominance
// with history only affecting ties and near-ties (see buildRows' doc comment).
// Tab/Shift+Tab cycle the tabs (views); Enter opens a candidate/tab/pane row;
// Left/Right expand/collapse a Herdr workspace's tab/pane children; ctrl+l
// toggles the session-only layout override (auto -> landscape -> auto);
// pgup/pgdown scroll the preview. "?" opens a modal, scrollable help overlay
// (FocusHelp) that "?"/Esc close back to the list; esc/ctrl+c/ctrl+g cancels
// (Run then returns ErrCancelled), except Esc first clears a non-empty query
// before ever cancelling. "q" is an ordinary query character, not a cancel
// key. Colors come from the theme the command layer selected (Layout.Theme,
// see theme.go); rows are drawn from their presentation templates
// (Layout.Presentation, see rowparts.go).
package tui

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
)

// SnapshotDriver is the small read-only Herdr boundary the picker needs after
// startup hydration: whole-generation refreshes and live pane reads. It keeps
// refresh ownership inside Model without pulling imperative commands into the
// TUI.
type SnapshotDriver interface {
	Snapshot(context.Context) (source.Snapshot, error)
	ReadPane(context.Context, string, int) (string, error)
}

// SnapshotRendererFactory constructs a fresh immutable renderer for a new
// snapshot generation.
type SnapshotRendererFactory func(source.Snapshot) preview.Renderer

// PinToggleResultMsg is the typed result of one persistence request. The
// command layer owns the store; Model is the only writer of visible state.
type PinToggleResultMsg struct {
	Key       string
	Candidate source.Candidate
	Pinned    bool
	Err       error
}

// CloseResultMsg is the typed completion of an open Herdr item close.
type CloseResultMsg struct {
	Kind string
	ID   string
	Err  error
}

// Closer executes an argv-based close outside the TUI update loop.
type Closer func(context.Context, string, string) CloseResultMsg

type closeTarget struct {
	kind, id, label string
}

// AckClearer is the narrow command boundary used by the TUI to invalidate
// stale persisted acknowledgements when a status transition is observed.
type AckClearer func(context.Context, string)

// PinToggler is the narrow command boundary used by the TUI for pin changes.
// It performs no I/O itself; the returned message is delivered to Update.
type PinToggler func(context.Context, source.Candidate) PinToggleResultMsg

const snapshotTTL = 5 * time.Second

// Layout configures the picker's list/preview pane widths, orientation
// override, color theme, and row presentations. ListWidth/PreviewWidth are
// each "auto" (or empty) or a percentage string like "60%"; see
// config.ParsePercent. Orientation is "" (auto — the responsive width-based
// mode described in nextResponsiveMode applies) or LayoutLandscape (forces
// wide/side-by-side mode).
type Layout struct {
	ListWidth    string
	PreviewWidth string
	Orientation  string
	// Theme is the resolved color theme (theme.Select, run by the command
	// layer before the program starts). The zero Theme means Herdr's default
	// theme, catppuccin.
	Theme theme.Theme
	// SourceOrder is the configured group iteration order (config's
	// general.sources, in declaration order — the same order
	// source.Registry.Enabled() already collects candidates in). Threaded
	// through the same Layout vehicle as Theme/widths so the picker's row
	// order matches the configured provider order instead of a hardcoded
	// literal. Empty falls back to rows.go's defaultSourceOrder.
	SourceOrder []string
	// Tabs is the ordered visible tab list; empty preserves all/agents.
	Tabs []TabDefinition
	// Icons selects the fallback tier (IconsUnicode/IconsASCII) for the
	// picker's own semantic icons — see icons.go's resolveIconSet and
	// Model.icons(). Empty defaults to IconsUnicode.
	Icons string
	// Presentation is how every kind of row is drawn: the resolved
	// [sources.<name>] (and [sources.herdr.tab]/[sources.herdr.pane])
	// presentations. nil means the built-in defaults for Icons.
	Presentation    *config.Presentations
	RankingSnapshot ranking.Snapshot
	StatusDialer    StatusDialer
	PinToggler      PinToggler
	Closer          Closer
	ConfirmClose    []string
	AckClearer      AckClearer
	InitialScope    FilterScope
	InitialTab      string
	// HomeDir is the home directory displayed paths under it are shown
	// relative to ("~/...") when Templates is nil. Empty resolves
	// os.UserHomeDir once at construction; tests set it for deterministic
	// output.
	HomeDir string
	// Templates renders the row templates and abbreviates displayed paths
	// (tmpl.Engine.Tilde). The command layer passes the process's single
	// engine; nil builds one for HomeDir at construction.
	Templates *tmpl.Engine
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

// Focus identifies what owns keyboard input: FocusList (the default) routes
// keys to the row cursor and query editing; FocusHelp is the modal "?" help
// overlay, which only closes via "?" or Esc, back to the list.
type Focus int

const (
	FocusList Focus = iota
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
	// allTab is the all tab's candidate set while [tui].tabs is configured:
	// the enabled sources' results, deduplicated in source order.
	// Deduplication resolves symlinks and stats directories, so it runs only
	// where those results change (see rebuildAllTab); frames and keystrokes
	// read this slice and never touch the disk.
	allTab []source.Candidate
	// dedupFn deduplicates candidates; nil means resolver.Dedup. Tests count
	// its calls to prove no frame or keystroke pays for it.
	dedupFn func([]source.Candidate) []source.Candidate
	// tree fetches/caches a Herdr workspace's tabs+panes so buildRows can
	// synthesize RowTab/RowPane children. nil means tree-expand is
	// inactive: every group's candidates render flat with no descendants.
	tree *TreeExpander

	// rows is the current visible, grouped row list — the single source of
	// truth for rendering and navigation. Rebuilt by applyFilter whenever
	// the query, expand/collapse state, or tree contents change.
	rows          []Row
	cursor        int  // index into rows
	cursorTouched bool // true only after explicit user navigation
	// listOffset is the first row of the list window. It follows the cursor
	// with a scroll-off margin (see ensureCursorVisible) instead of
	// re-centering on every move, and resets with the cursor on a new query.
	listOffset int
	// rowWindow caches the display models of the rows in the list window
	// across frames (see syncRowWindow).
	rowWindow rowWindow

	query            string
	lastAppliedQuery string
	width            int
	height           int
	// mode is the resolved responsive display mode ("wide"/"list-only"),
	// recomputed on every tea.WindowSizeMsg (see nextResponsiveMode) — never
	// inside View, which must stay a pure projection of already-settled
	// state.
	mode string

	selected            source.Candidate
	hasSelected         bool
	selectedAction      RowAction
	cancelled           bool
	scope               FilterScope
	activeTab           string
	groupCandidates     map[string][]source.Candidate
	groupLoading        map[string]bool
	groupErrors         map[string]error
	groupGeneration     int
	snapshotUnavailable error
	layout              Layout
	theme               theme.Theme
	styles              *styleSet
	// formats is every kind of row's prepared presentation (see rowparts.go).
	formats *rowFormats

	// currentPane is the Herdr pane shep is running inside, queried once by
	// the caller and threaded in via WithCurrentPane. nil means "no current
	// pane": the footer's ctrl+t/ctrl+p hints are hidden and handleKey
	// ignores both bindings (see selectWithTarget).
	currentPane *source.Pane
	// snapshotDriver is non-nil only for a model hydrated from a full Herdr
	// snapshot. Model is the single owner of eligible refreshes.
	snapshotDriver      SnapshotDriver
	rendererForSnapshot SnapshotRendererFactory
	snapshotIcons       map[string]string
	// snapshotSources records which candidate sources the active snapshot
	// generation feeds (see SourceResultMsg.SnapshotSources); the periodic
	// refresh re-derives exactly those source slices.
	snapshotSources    map[string]bool
	snapshotSeq        int
	snapshotRefreshing bool
	lastSnapshotAt     time.Time
	// chosenTarget records which target the user picked via ctrl+t ("tab")
	// or ctrl+p ("pane"). Empty means enter was pressed (or the run was
	// cancelled), so the caller's --target flag value applies unchanged.
	chosenTarget string
	// pinPending prevents overlapping toggles for the same visible action and
	// pinStatus is the truthful, short feedback shown in the footer.
	pinPending          bool
	pinKey              string
	pinStatus           footerStatus
	closePending        bool
	closeRefreshPending bool
	// closeConfirm is the close awaiting its y/n answer; the footer renders
	// the question from it. closeStatus is the close flow's latest message.
	closeConfirm *closeTarget
	closeStatus  footerStatus

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
	sourceOrder     []string
	rankingSnapshot ranking.Snapshot

	// focus is the list or the help overlay.
	focus Focus
	// viewport backs the preview pane's internal scroll position. Its
	// Width/Height/Content are refreshed every Update call (syncViewport) —
	// transient, never itself the source of truth for preview text — so
	// only YOffset (mutated by pgup/pgdown) needs to persist across renders.
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
	// spinnerFrame counts the spinner's advances, so the preview memo knows
	// when a drawn spinner frame went stale (see previewMemo).
	spinnerFrame int

	// preview memoizes the preview body; helpKey records what the help
	// viewport's content was built for (see syncViewport/syncHelpViewport).
	preview previewMemo
	helpKey helpKey

	producers          []SourceProducer
	pendingProducers   map[int]bool
	candidatesBySource map[string][]source.Candidate
	loadingCandidates  bool
	startupSnapshot    *source.Snapshot

	liveStatuses       map[string]liveObservation
	liveSeq            int
	snapshotRequestSeq int
	nowFn              func() time.Time
	liveStatusEvents   <-chan StatusEvent
}

// SourceResultMsg carries the asynchronously loaded state from an independent
// producer (workspaces, zoxide, projects, herdr snapshot/tree, ranking).
type SourceResultMsg struct {
	Source              string
	Candidates          []source.Candidate
	Tree                *TreeExpander
	SnapshotDriver      SnapshotDriver
	Snapshot            *source.Snapshot
	RendererForSnapshot SnapshotRendererFactory
	// SnapshotIcons supplies configured icons for candidates re-derived from
	// the shared Herdr snapshot, keyed by source name.
	SnapshotIcons map[string]string
	// SnapshotSources names the candidate sources this message's Snapshot
	// generation feeds (config.SourceHerdr, config.SourceAgents): rows of
	// those sources are re-derived from every later snapshot refresh instead
	// of going stale in the all view. A message that leaves this empty keeps
	// the historical herdr-only refresh ownership.
	SnapshotSources []string
	Renderer        preview.Renderer
	CurrentPane     *source.Pane
	RankingSnapshot *ranking.Snapshot
	Err             error
	producerID      int
}

// SourceProducer is an independent candidate or state loader executed concurrently
// as a tea.Cmd during streaming startup.
type SourceProducer func(ctx context.Context) SourceResultMsg

type groupResultMsg struct {
	id         string
	generation int
	candidates []source.Candidate
	err        error
}

// activateGroup schedules a lazy group or tab-only provider once per tab.
func (m *Model) activateGroup() tea.Cmd {
	tab := m.activeDefinition()
	if tab.Load == nil || m.groupLoading[tab.ID] {
		return nil
	}
	if _, loaded := m.groupCandidates[tab.ID]; loaded {
		return nil
	}
	m.groupLoading[tab.ID] = true
	snapshot := m.startupSnapshot
	generation := m.groupGeneration
	ctx := m.renderCtx
	return func() tea.Msg {
		candidates, err := tab.Load(ctx, snapshot)
		return groupResultMsg{id: tab.ID, generation: generation, candidates: candidates, err: err}
	}
}

type liveObservation struct {
	status string
	seq    int
}

type paneStatusMsg struct {
	PaneID      string
	WorkspaceID string
	TabID       string
	Status      string
}

func (m Model) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
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

// snapshotResponseMsg carries an asynchronous full-generation refresh. The
// request seq is independent from previewSeq because it protects source state,
// while previewSeq protects renderer output for a particular generation/row.
type snapshotResponseMsg struct {
	seq      int
	snapshot source.Snapshot
	err      error
}

// NewModel builds a model over the supplied candidates. renderer may be nil,
// in which case the preview pane shows a static built-in summary instead of
// an async render.
func NewModel(candidates []source.Candidate, renderer preview.Renderer) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), Layout{}, ranking.Snapshot{})
}

// NewModelWithLayout builds a model like NewModel but with an explicit
// Layout (list/preview widths, orientation override, and theme).
func NewModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, layout Layout) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), layout, layout.RankingSnapshot)
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

// NewModelWithProducers constructs a Model that renders its initial frame
// immediately in a loading state and streams candidates incrementally from independent
// concurrent producers launched as tea.Cmds.
func NewModelWithProducers(producers []SourceProducer, query string, renderer preview.Renderer, renderCtx context.Context, layout Layout) Model {
	if renderCtx == nil {
		renderCtx = context.TODO()
	}
	m := newModelWithLayout(nil, renderer, renderCtx, layout, layout.RankingSnapshot)
	m.query = query
	m.producers = producers
	m.pendingProducers = make(map[int]bool, len(producers))
	for i := range producers {
		m.pendingProducers[i] = true
	}
	m.loadingCandidates = len(producers) > 0
	m.candidatesBySource = make(map[string][]source.Candidate)
	m.applyFilter()
	m.refreshPreviewLoadingFlag()
	return m
}

func newModelWithTreeLayout(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context, tree *TreeExpander, layout Layout) Model {
	m := newModelWithLayout(candidates, renderer, renderCtx, layout, layout.RankingSnapshot)
	m.baseCandidates = make([]source.Candidate, len(candidates))
	copy(m.baseCandidates, candidates)
	m.tree = tree
	m.applyFilter()
	return m
}

func newModelWithLayout(candidates []source.Candidate, renderer preview.Renderer, renderCtx context.Context, layout Layout, snapshots ...ranking.Snapshot) Model {
	if renderCtx == nil {
		renderCtx = context.TODO()
	}
	th := layout.Theme
	if th.Name == "" {
		th = defaultTheme()
	}
	presentation := layout.Presentation
	if presentation == nil {
		defaults := config.DefaultPresentations(layout.Icons)
		presentation = &defaults
	}
	formats := newRowFormats(*presentation)
	styles := newPalette(th, formats.iconRefs)
	if layout.Templates == nil {
		home := layout.HomeDir
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		layout.Templates = tmpl.New(home)
	}
	var snapshot ranking.Snapshot
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	m := Model{
		candidates:         make([]source.Candidate, len(candidates)),
		selected:           source.Candidate{},
		renderer:           renderer,
		renderCtx:          renderCtx,
		layout:             layout,
		theme:              th,
		styles:             styles,
		formats:            formats,
		expandedWorkspaces: map[string]bool{},
		sourceOrder:        layout.SourceOrder,
		rankingSnapshot:    snapshot,
		scope:              layout.InitialScope,
		groupCandidates:    make(map[string][]source.Candidate),
		groupLoading:       make(map[string]bool),
		groupErrors:        make(map[string]error),
		spinner:            spinner.New(spinner.WithSpinner(loadingSpinner(layout.Icons)), spinner.WithStyle(styles.previewLoadingStyle)),
		// mode starts "" (unknown/not yet sized): View treats "" the same
		// as modeWide (side-by-side, using the same width<=0 fallback
		// splitSizes already applies) until the first real
		// tea.WindowSizeMsg arrives and nextResponsiveMode takes over —
		// matching the previous picker's "landscape by default in a
		// headless/test context" behavior.
	}
	copy(m.candidates, candidates)
	if layout.InitialTab != "" {
		m.activeTab = layout.InitialTab
		m.scope = scopeForTab(m.activeTab)
	} else if layout.InitialScope == ScopeAgents {
		m.activeTab = "agents"
	} else {
		m.activeTab = m.tabs()[0].ID
		m.scope = scopeForTab(m.activeTab)
	}
	m.applyFilter()
	m.refreshPreviewLoadingFlag()
	return m
}

// loadingSpinner picks the shared spinner's frames for the icon tier: the
// Braille MiniDot frames, or ASCII line frames ("|/-\\") under the ASCII
// tier, whose loading indicators must stay 7-bit. Working status glyphs use
// IconSet.StatusWorking under that tier instead (see statusGlyph).
func loadingSpinner(icons string) spinner.Spinner {
	if resolveIconSetName(icons) == IconsASCII {
		return spinner.Line
	}
	return spinner.MiniDot
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

// Scope returns the legacy all/agents view category. ActiveTab identifies
// the exact configured source, custom source or group tab.
func (m Model) Scope() FilterScope { return m.scope }

// WithScope returns a copy of the model with the given filter scope activated.
func (m Model) WithScope(s FilterScope) Model {
	m.scope = s
	if s == ScopeAgents {
		m.activeTab = "agents"
	} else {
		m.activeTab = "all"
	}
	m.cursor = 0
	m.cursorTouched = false
	return m
}

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
	m.invalidateRowWindow() // the active-focus marker may move
	return m
}

// WithLiveStatus attaches a live status events channel to the model.
func (m Model) WithLiveStatus(events <-chan StatusEvent) Model {
	m.liveStatusEvents = events
	return m
}

// WithSnapshotRefresh wires a startup generation into the model. The initial
// state is already resolved by command/open; this method merely establishes
// the one refresh owner and generation-scoped tree/focus references.
func (m Model) WithSnapshotRefresh(driver SnapshotDriver, snapshot source.Snapshot, rendererForSnapshot SnapshotRendererFactory, snapshotIcons map[string]string) Model {
	m.snapshotDriver = driver
	m.rendererForSnapshot = rendererForSnapshot
	m.snapshotIcons = make(map[string]string, len(snapshotIcons))
	for name, icon := range snapshotIcons {
		m.snapshotIcons[name] = icon
	}
	// The synchronous picker only refreshes families eligible in its source
	// order. Direct callers with no order retain the historical Herdr default.
	m.snapshotSources = make(map[string]bool)
	if m.layout.SourceOrder == nil {
		m.snapshotSources[config.SourceHerdr] = true
	} else {
		for _, name := range m.layout.SourceOrder {
			if name == config.SourceHerdr || name == config.SourceAgents {
				m.snapshotSources[name] = true
			}
		}
	}
	for _, tab := range m.tabs() {
		if tab.Kind == TabSource && tab.ID == config.SourceHerdr {
			m.snapshotSources[config.SourceHerdr] = true
		}
	}
	m.tree = NewTreeExpanderFromSnapshot(snapshot)
	if pane, ok := source.ResolveFocusedPane(snapshot); ok {
		copy := *pane
		m.currentPane = &copy
	} else {
		m.currentPane = nil
	}
	m.lastSnapshotAt = time.Now()
	m.invalidateRowWindow()
	return m
}

// Init kicks off the first async preview render for the initially
// highlighted row when a Renderer (or tree, for a pane row) is wired, and launches
// all streaming producers concurrently as bounded tea.Cmds.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.initialPreviewCmd(),
		waitForStatusCmd(m.renderCtx, m.liveStatusEvents),
	}
	for i, producer := range m.producers {
		cmds = append(cmds, m.makeProducerCmd(i, producer))
	}
	cmds = append(cmds, m.maybeLoadGroup())
	return tea.Batch(cmds...)
}

func (m *Model) maybeLoadGroup() tea.Cmd {
	// A snapshot-backed group waits for the shared Herdr generation before
	// collecting; otherwise its registry would ask the driver for a second one.
	if m.groupNeedsSnapshot() {
		if m.snapshotUnavailable != nil || (m.loadingCandidates && m.startupSnapshot == nil) {
			return nil
		}
	}
	return m.activateGroup()
}

func (m Model) makeProducerCmd(idx int, p SourceProducer) tea.Cmd {
	if p == nil {
		return nil
	}
	ctx := m.renderCtx
	return func() tea.Msg {
		msg := p(ctx)
		msg.producerID = idx
		return msg
	}
}

// Update handles key presses, window sizing, and async preview responses.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.mode = nextResponsiveMode(m, m.mode)
	case SourceResultMsg:
		m, cmd = m.handleSourceResult(msg)
	case groupResultMsg:
		for _, tab := range m.tabs() {
			if tab.ID == msg.id && m.groupUsesSnapshot(tab) && msg.generation != m.groupGeneration {
				return m, nil
			}
		}
		m.groupLoading[msg.id] = false
		m.groupCandidates[msg.id] = m.projectLiveAgentStatuses(msg.candidates)
		m.groupErrors[msg.id] = msg.err
		if m.ActiveTab() == msg.id {
			cmd = tea.Batch(m.applyFilter(), m.syncPreviewAfterSelectionChange())
		}
	case paneStatusMsg:
		m, cmd = m.handlePaneStatus(msg)
	case previewResponseMsg:
		m = m.handlePreviewResponse(msg)
	case panePreviewMsg:
		m = m.handlePanePreviewResponse(msg)
	case snapshotResponseMsg:
		m, cmd = m.handleSnapshotResponse(msg)
	case PinToggleResultMsg:
		m, cmd = m.handlePinToggleResult(msg)
	case CloseResultMsg:
		if m.closePending {
			m.closePending = false
			if msg.Err != nil {
				m.closeStatus = errorStatus("close failed: " + msg.Err.Error())
			} else {
				m.closeStatus = successStatus("closed " + msg.Kind)
				// Reuse the TTL-gated snapshot owner; expire it only after a successful
				// close so a fresh snapshot cannot leave the closed row visible.
				if m.snapshotDriver != nil {
					m.closeRefreshPending = true
					if !m.snapshotRefreshing {
						m.lastSnapshotAt = m.now().Add(-snapshotTTL)
						cmd = m.maybeRefreshSnapshot()
						if cmd != nil {
							m.closeRefreshPending = false
						}
					}
				}
			}
		}
	case spinner.TickMsg:
		m, cmd = m.handleSpinnerTick(msg)
	case tea.KeyMsg:
		var next tea.Model
		next, cmd = m.handleKey(msg)
		m = next.(Model)
	}
	m.ensureCursorVisible()
	m.syncRowWindow()
	m.syncViewport()
	m.syncHelpViewport()
	return m, cmd
}

func (m Model) handleSourceResult(msg SourceResultMsg) (Model, tea.Cmd) {
	if m.pendingProducers != nil {
		delete(m.pendingProducers, msg.producerID)
	}
	m.loadingCandidates = len(m.pendingProducers) > 0

	if msg.RankingSnapshot != nil {
		m.rankingSnapshot = *msg.RankingSnapshot
		if m.startupSnapshot != nil {
			m.rankingSnapshot = m.rankingSnapshot.WithFilteredWorkspaceMRU(m.startupSnapshot.Workspaces)
			if m.startupSnapshot.FocusedWorkspaceID != "" {
				m.rankingSnapshot = m.rankingSnapshot.WithCurrentExact(ranking.Identity(source.Candidate{
					Source: config.SourceHerdr,
					Meta:   map[string]string{"workspace_id": m.startupSnapshot.FocusedWorkspaceID},
				}))
			}
		}
	}

	if msg.SnapshotDriver != nil {
		m.snapshotDriver = msg.SnapshotDriver
	}
	if msg.RendererForSnapshot != nil {
		m.rendererForSnapshot = msg.RendererForSnapshot
	}
	if msg.SnapshotIcons != nil {
		m.snapshotIcons = make(map[string]string, len(msg.SnapshotIcons))
		for name, icon := range msg.SnapshotIcons {
			m.snapshotIcons[name] = icon
		}
	}
	if len(msg.SnapshotSources) > 0 {
		if m.snapshotSources == nil {
			m.snapshotSources = make(map[string]bool, len(msg.SnapshotSources))
		}
		for _, src := range msg.SnapshotSources {
			m.snapshotSources[src] = true
		}
	}
	if msg.CurrentPane != nil {
		m.currentPane = msg.CurrentPane
	}
	if msg.Renderer != nil {
		m.renderer = msg.Renderer
	}
	if msg.Tree != nil {
		m.tree = msg.Tree
	}
	if msg.Snapshot != nil {
		m.snapshotUnavailable = nil
		m.startupSnapshot = msg.Snapshot
		m.lastSnapshotAt = m.now()
		m.rankingSnapshot = m.rankingSnapshot.WithFilteredWorkspaceMRU(msg.Snapshot.Workspaces)
		if m.rankingSnapshot.Active() && msg.Snapshot.FocusedWorkspaceID != "" {
			m.rankingSnapshot = m.rankingSnapshot.WithCurrentExact(ranking.Identity(source.Candidate{
				Source: config.SourceHerdr,
				Meta:   map[string]string{"workspace_id": msg.Snapshot.FocusedWorkspaceID},
			}))
		}
	}

	hasCandidateChanges := false
	if msg.Source != "ranking" {
		if m.candidatesBySource == nil {
			m.candidatesBySource = make(map[string][]source.Candidate)
		}
		if len(msg.Candidates) == 0 {
			if cur, exists := m.candidatesBySource[msg.Source]; exists && len(cur) > 0 {
				m.candidatesBySource[msg.Source] = nil
				hasCandidateChanges = true
			} else if !exists {
				m.candidatesBySource[msg.Source] = nil
			}
		} else {
			grouped := make(map[string][]source.Candidate)
			for _, c := range msg.Candidates {
				src := c.Source
				if src == "" {
					src = msg.Source
				}
				grouped[src] = append(grouped[src], c)
			}
			for src, cands := range grouped {
				m.candidatesBySource[src] = m.projectLiveAgentStatuses(cands)
				hasCandidateChanges = true
			}
		}
	}

	if hasCandidateChanges || msg.RankingSnapshot != nil {
		m.rebuildCandidatesFromSources()
	}

	if msg.Source == config.SourceHerdr && msg.Snapshot == nil && m.startupSnapshot == nil {
		m.snapshotUnavailable = msg.Err
		if m.snapshotUnavailable == nil {
			m.snapshotUnavailable = errors.New("Herdr is unavailable")
		}
	}
	filterCmd := m.applyFilter()
	groupCmd := m.maybeLoadGroup()
	m.refreshPreviewLoadingFlag()
	previewCmd := m.syncPreviewAfterSelectionChange()

	// A source's error text (a command's output) is shown in the preview,
	// so it is kept plain (see plainText).
	if msg.Err != nil && len(m.baseFlatCandidates()) == 0 {
		m.previewErr = plainText(msg.Err.Error())
	} else if len(m.baseFlatCandidates()) > 0 {
		if msg.Err == nil || m.previewErr == plainText(msg.Err.Error()) {
			m.previewErr = ""
		}
	}

	return m, tea.Batch(filterCmd, groupCmd, previewCmd, m.maybeStartSpinner())
}

func (m *Model) rebuildCandidatesFromSources() {
	order := m.resolvedSourceOrder()
	var all []source.Candidate
	seenSources := make(map[string]bool, len(order))
	for _, src := range order {
		seenSources[src] = true
		if cands, ok := m.candidatesBySource[src]; ok && len(cands) > 0 {
			all = append(all, cands...)
		}
	}
	tabOnly := false
	for src, cands := range m.candidatesBySource {
		if !seenSources[src] && len(cands) > 0 {
			all = append(all, cands...)
			tabOnly = true
		}
	}

	deduped := m.dedup(all)
	m.baseCandidates = deduped
	m.candidates = deduped
	if tabOnly {
		m.rebuildAllTab()
	} else {
		// Every source with results is enabled, in order: the all tab's
		// deduplication would repeat this one exactly.
		m.allTab = deduped
	}
}

// rebuildAllTab recomputes allTab from the enabled sources' results. Callers
// are the Update handlers that replace those results; it deduplicates, so it
// must never run while rendering or filtering.
func (m *Model) rebuildAllTab() {
	if len(m.layout.Tabs) == 0 || m.candidatesBySource == nil {
		m.allTab = nil // read only while tabs are configured
		return
	}
	var enabled []source.Candidate
	for _, name := range m.resolvedSourceOrder() {
		enabled = append(enabled, m.candidatesBySource[name]...)
	}
	m.allTab = m.dedup(enabled)
}

// dedup collapses candidates that name the same directory (see
// resolver.Dedup). It touches the filesystem.
func (m *Model) dedup(candidates []source.Candidate) []source.Candidate {
	if m.dedupFn != nil {
		return m.dedupFn(candidates)
	}
	return resolver.Dedup(candidates)
}

func waitForStatusCmd(ctx context.Context, events <-chan StatusEvent) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			return paneStatusMsg{
				PaneID:      ev.PaneID,
				WorkspaceID: ev.WorkspaceID,
				TabID:       ev.TabID,
				Status:      ev.Status,
			}
		}
	}
}

// projectLiveAgentStatuses overlays observed status onto newly arrived source
// results without changing their provider-owned metadata maps.
func (m Model) projectLiveAgentStatuses(candidates []source.Candidate) []source.Candidate {
	var projected []source.Candidate
	for i, c := range candidates {
		if c.Source != config.SourceAgents || c.Meta["pane_id"] == "" {
			continue
		}
		obs, ok := m.liveStatuses[c.Meta["pane_id"]]
		if !ok || obs.status == "" {
			continue
		}
		if projected == nil {
			projected = append([]source.Candidate(nil), candidates...)
		}
		meta := make(map[string]string, len(c.Meta))
		for key, value := range c.Meta {
			meta[key] = value
		}
		meta["agent_status"] = obs.status
		projected[i].Meta = meta
	}
	if projected != nil {
		return projected
	}
	return candidates
}

func (m Model) handlePaneStatus(msg paneStatusMsg) (Model, tea.Cmd) {
	if msg.PaneID == "" {
		return m, waitForStatusCmd(m.renderCtx, m.liveStatusEvents)
	}
	status := normalizeStatus(msg.Status)
	m.liveSeq++
	if m.liveStatuses == nil {
		m.liveStatuses = make(map[string]liveObservation)
	}
	m.liveStatuses[msg.PaneID] = liveObservation{
		status: status,
		seq:    m.liveSeq,
	}
	if m.tree != nil {
		m.tree.UpdatePaneAgentStatus(msg.PaneID, status)
	}
	// Status glyphs (a pane's own, a workspace's aggregate) live in the row
	// display models; rows below may also be patched in place.
	m.invalidateRowWindow()

	var clearCmd tea.Cmd
	if (m.rankingSnapshot.IsPaneAcknowledged(msg.PaneID, "blocked") && status != "blocked") ||
		(m.rankingSnapshot.IsPaneAcknowledged(msg.PaneID, "done") && status != "done") {
		m.rankingSnapshot = m.rankingSnapshot.WithClearedAcknowledgement(msg.PaneID)
		if m.layout.AckClearer != nil {
			paneID := msg.PaneID
			clearer := m.layout.AckClearer
			clearCmd = func() tea.Msg {
				clearer(context.Background(), paneID)
				return nil
			}
		}
	}

	// Candidate metadata maps may alias provider results or other model copies.
	// Replace only matching owned entries before rebuilding the active view.
	updateAgents := func(candidates []source.Candidate) []source.Candidate {
		var updated []source.Candidate
		for i, c := range candidates {
			if c.Source != config.SourceAgents || c.Meta["pane_id"] != msg.PaneID {
				continue
			}
			if updated == nil {
				updated = append([]source.Candidate(nil), candidates...)
			}
			meta := make(map[string]string, len(c.Meta))
			for key, value := range c.Meta {
				meta[key] = value
			}
			meta["agent_status"] = status
			updated[i].Meta = meta
		}
		if updated != nil {
			return updated
		}
		return candidates
	}
	m.candidates = updateAgents(m.candidates)
	m.baseCandidates = updateAgents(m.baseCandidates)
	// A status is not part of deduplication's identity, so patching the
	// stored set equals deduplicating the patched results, without the I/O.
	m.allTab = updateAgents(m.allTab)
	for name, candidates := range m.candidatesBySource {
		m.candidatesBySource[name] = updateAgents(candidates)
	}
	for name, candidates := range m.groupCandidates {
		m.groupCandidates[name] = updateAgents(candidates)
	}
	row, selected := m.currentRow()
	selectedAgent := selected && row.Candidate.Source == config.SourceAgents && row.Candidate.Meta["pane_id"] == msg.PaneID
	hasAgentRow := false
	for _, visible := range m.rows {
		if visible.Candidate.Source == config.SourceAgents && visible.Candidate.Meta["pane_id"] == msg.PaneID {
			hasAgentRow = true
			break
		}
	}
	activeKind := m.activeDefinition().Kind
	if activeKind == TabAgents || activeKind == TabGroup || activeKind == TabSource || hasAgentRow {
		filterCmd := m.applyFilter()
		var previewCmd tea.Cmd
		if selectedAgent {
			previewCmd = m.syncPreviewAfterSelectionChange()
		}
		return m, tea.Batch(filterCmd, previewCmd, clearCmd, m.maybeStartSpinner(), waitForStatusCmd(m.renderCtx, m.liveStatusEvents))
	}
	for i := range m.rows {
		if m.rows[i].Kind == RowPane && m.rows[i].Candidate.Meta["pane_id"] == msg.PaneID {
			meta := make(map[string]string, len(m.rows[i].Candidate.Meta))
			for key, value := range m.rows[i].Candidate.Meta {
				meta[key] = value
			}
			meta["agent_status"] = status
			m.rows[i].Candidate.Meta = meta
		}
	}
	// A status that turns working (a pane's own glyph or its workspace's
	// aggregate) needs the shared spinner ticking.
	return m, tea.Batch(clearCmd, m.maybeStartSpinner(), waitForStatusCmd(m.renderCtx, m.liveStatusEvents))
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

func (m Model) handlePinToggleResult(msg PinToggleResultMsg) (Model, tea.Cmd) {
	m.pinPending = false
	if msg.Err != nil {
		m.pinStatus = errorStatus("pin update failed: " + msg.Err.Error())
		return m, nil
	}
	m.rankingSnapshot = m.rankingSnapshot.WithPinned(msg.Key, msg.Pinned)
	if msg.Pinned {
		m.pinStatus = successStatus("pinned")
	} else {
		m.pinStatus = successStatus("unpinned")
	}
	filterCmd := m.applyFilter()
	return m, filterCmd
}

func (m Model) handleSnapshotResponse(msg snapshotResponseMsg) (Model, tea.Cmd) {
	if msg.seq != m.snapshotSeq {
		return m, nil
	}
	m.snapshotRefreshing = false
	if m.closeRefreshPending {
		// A refresh that was already in flight when close completed can still
		// contain the open row. Request a new generation through the same owner.
		m.closeRefreshPending = false
		m.lastSnapshotAt = m.now().Add(-snapshotTTL)
		return m, m.maybeRefreshSnapshot()
	}
	if msg.err != nil {
		m.lastSnapshotAt = m.now()
		m.previewErr = "snapshot refresh failed"
		return m, nil
	}

	if m.snapshotRefreshesSource(config.SourceHerdr) {
		replacement := source.HerdrCandidates(msg.snapshot)
		for i := range replacement {
			replacement[i].Icon = m.snapshotIcons[config.SourceHerdr]
		}
		m.baseCandidates = spliceHerdrCandidates(m.baseCandidates, replacement)
		if _, loaded := m.groupCandidates[config.SourceHerdr]; loaded {
			m.groupCandidates[config.SourceHerdr] = replacement
		}
		if m.candidatesBySource != nil {
			m.candidatesBySource[config.SourceHerdr] = replacement
		}
	}
	m.candidates = m.baseCandidates
	m.tree = NewTreeExpanderFromSnapshot(msg.snapshot)
	snapshotCopy := msg.snapshot
	m.startupSnapshot = &snapshotCopy
	m.snapshotUnavailable = nil
	m.groupGeneration++
	for _, tab := range m.tabs() {
		if tab.Kind == TabGroup && m.groupUsesSnapshot(tab) {
			delete(m.groupCandidates, tab.ID)
			delete(m.groupErrors, tab.ID)
			delete(m.groupLoading, tab.ID)
		}
	}
	if len(m.liveStatuses) > 0 {
		for paneID, obs := range m.liveStatuses {
			if obs.seq <= m.snapshotRequestSeq {
				delete(m.liveStatuses, paneID)
			} else {
				if m.tree != nil && m.tree.UpdatePaneAgentStatus(paneID, obs.status) {
					// Pane exists in new generation, keep and re-applied
				} else {
					delete(m.liveStatuses, paneID)
				}
			}
		}
	}
	if m.snapshotRefreshesSource(config.SourceAgents) {
		// Once observed, the agents slice stays refresh-owned so panes that
		// leave and return are re-derived instead of frozen.
		if m.snapshotSources == nil {
			m.snapshotSources = make(map[string]bool, 1)
		}
		m.snapshotSources[config.SourceAgents] = true
		agentReplacement := source.AgentCandidates(snapshotWithLiveStatuses(msg.snapshot, m.liveStatuses))
		for i := range agentReplacement {
			agentReplacement[i].Icon = m.snapshotIcons[config.SourceAgents]
		}
		m.baseCandidates = spliceSourceCandidates(m.baseCandidates, agentReplacement, config.SourceAgents, "pane_id")
		m.candidates = m.baseCandidates
		if m.candidatesBySource != nil {
			m.candidatesBySource[config.SourceAgents] = agentReplacement
		}
	}
	// The all tab's set changes only when a refreshed source is enabled.
	for _, name := range []string{config.SourceHerdr, config.SourceAgents} {
		if m.snapshotRefreshesSource(name) && slices.Contains(m.resolvedSourceOrder(), name) {
			m.rebuildAllTab()
			break
		}
	}
	if pane, ok := source.ResolveFocusedPane(msg.snapshot); ok {
		copy := *pane
		m.currentPane = &copy
	} else {
		m.currentPane = nil
	}
	if m.rendererForSnapshot != nil {
		m.renderer = m.rendererForSnapshot(msg.snapshot)
	}
	if m.rankingSnapshot.Active() {
		m.rankingSnapshot = m.rankingSnapshot.WithFilteredWorkspaceMRU(msg.snapshot.Workspaces)
		if msg.snapshot.FocusedWorkspaceID != "" {
			m.rankingSnapshot = m.rankingSnapshot.WithCurrentExact(ranking.Identity(source.Candidate{
				Source: config.SourceHerdr,
				Meta:   map[string]string{"workspace_id": msg.snapshot.FocusedWorkspaceID},
			}))
		}
	}
	var clearCmds []tea.Cmd
	if msg.snapshot.Panes != nil {
		for _, p := range msg.snapshot.Panes {
			status := normalizeStatus(p.AgentStatus)
			if (m.rankingSnapshot.IsPaneAcknowledged(p.ID, "blocked") && status != "blocked") ||
				(m.rankingSnapshot.IsPaneAcknowledged(p.ID, "done") && status != "done") {
				m.rankingSnapshot = m.rankingSnapshot.WithClearedAcknowledgement(p.ID)
				if m.layout.AckClearer != nil {
					paneID := p.ID
					clearer := m.layout.AckClearer
					clearCmds = append(clearCmds, func() tea.Msg {
						clearer(context.Background(), paneID)
						return nil
					})
				}
			}
		}
	}
	m.lastSnapshotAt = m.now()
	m.previewSeq++
	m.previewText = ""
	m.previewSections = nil
	m.previewErr = ""
	filterCmd := m.applyFilter()
	groupCmd := m.maybeLoadGroup()
	_, previewCmd := m.dispatchPreviewForRow(m.previewSeq)
	cmds := append(clearCmds, filterCmd, groupCmd, previewCmd, m.maybeStartSpinner())
	return m, tea.Batch(cmds...)
}

// snapshotRefreshesSource reports whether the periodic full-generation
// refresh must re-derive candidates for name. Producers declare the families
// their shared snapshot feeds (SourceResultMsg.SnapshotSources), while
// WithSnapshotRefresh uses the synchronous picker's eligible source order.
// Direct nil-order layouts retain the historical Herdr default and can also
// refresh observed agent rows. Explicit orders never acquire excluded sources.
func (m Model) snapshotRefreshesSource(name string) bool {
	if m.snapshotSources[name] {
		return true
	}
	if name == config.SourceAgents {
		// With an explicit order, row presence cannot enable an excluded source.
		if m.layout.SourceOrder != nil {
			return false
		}
		for _, c := range m.baseCandidates {
			if c.Source == config.SourceAgents {
				return true
			}
		}
		return false
	}
	// Historical default: a generation whose families were never declared
	// still owns the herdr rows.
	return len(m.snapshotSources) == 0 && m.layout.SourceOrder == nil && name == config.SourceHerdr
}

// snapshotWithLiveStatuses returns snapshot with each pane's AgentStatus
// overridden by the newest surviving live observation, so rows re-derived from
// the new generation keep live statuses that arrived after the refresh was
// requested instead of regressing to the snapshot's own (older) value.
func snapshotWithLiveStatuses(snapshot source.Snapshot, live map[string]liveObservation) source.Snapshot {
	if len(live) == 0 {
		return snapshot
	}
	effective := snapshot
	effective.Panes = make([]source.Pane, len(snapshot.Panes))
	copy(effective.Panes, snapshot.Panes)
	for i := range effective.Panes {
		if obs, ok := live[effective.Panes[i].ID]; ok && obs.status != "" {
			effective.Panes[i].AgentStatus = obs.status
		}
	}
	return effective
}

// spliceHerdrCandidates preserves every non-Herdr candidate in its original
// order while replacing, dropping, and appending only the Herdr slice from a
// new full snapshot generation.
func spliceHerdrCandidates(base, replacement []source.Candidate) []source.Candidate {
	return spliceSourceCandidates(base, replacement, config.SourceHerdr, "workspace_id")
}

// spliceSourceCandidates preserves every candidate outside sourceName in its
// original order while replacing, dropping, and appending only the named
// source's slice, matched by Meta[idKey]. Candidates without an id are kept
// as-is and never matched.
func spliceSourceCandidates(base, replacement []source.Candidate, sourceName, idKey string) []source.Candidate {
	byID := make(map[string]source.Candidate, len(replacement))
	for _, candidate := range replacement {
		id := candidate.Meta[idKey]
		if id == "" {
			continue
		}
		byID[id] = candidate
	}
	last := -1
	for i, candidate := range base {
		if candidate.Source == sourceName {
			last = i
		}
	}
	out := make([]source.Candidate, 0, len(base)+len(replacement))
	used := make(map[string]struct{}, len(replacement))
	appendNew := func() {
		for _, candidate := range replacement {
			id := candidate.Meta[idKey]
			if id == "" {
				out = append(out, candidate)
				continue
			}
			if _, exists := used[id]; exists {
				continue
			}
			out = append(out, candidate)
			used[id] = struct{}{}
		}
	}
	for i, candidate := range base {
		if candidate.Source == sourceName {
			id := candidate.Meta[idKey]
			if replacementCandidate, exists := byID[id]; id != "" && exists {
				out = append(out, replacementCandidate)
				used[id] = struct{}{}
			} else if id == "" {
				out = append(out, candidate)
			}
		} else {
			out = append(out, candidate)
		}
		if i == last {
			appendNew()
		}
	}
	if last == -1 {
		appendNew()
	}
	return out
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
	if cmd != nil { // the spinner accepted the tick and advanced its frame
		m.spinnerFrame++
	}
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

// RunWithSnapshot drives a picker from one coherent startup generation and
// gives the model the sole eligible-refresh driver. The renderer factory builds
// an immutable renderer each time a newer generation succeeds.
func RunWithSnapshot(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, snapshot source.Snapshot, driver SnapshotDriver, rendererForSnapshot SnapshotRendererFactory, snapshotIcons map[string]string, currentPane *source.Pane, layout ...Layout) (source.Candidate, RowAction, string, bool, error) {
	var l Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	m := newModelWithTreeLayout(candidates, renderer, ctx, NewTreeExpanderFromSnapshot(snapshot), l).
		WithCurrentPane(currentPane).
		WithSnapshotRefresh(driver, snapshot, rendererForSnapshot, snapshotIcons)
	m.query = query
	m.applyFilter()
	return runProgram(ctx, m)
}

// StartupSnapshot returns the coherent startup snapshot if one was hydrated
// into the model during streaming or snapshot initialization.
func (m Model) StartupSnapshot() *source.Snapshot { return m.startupSnapshot }

// RankingSnapshot returns the model's active ranking snapshot.
func (m Model) RankingSnapshot() ranking.Snapshot { return m.rankingSnapshot }

// CurrentPane returns the resolved Herdr current pane if available.
func (m Model) CurrentPane() *source.Pane { return m.currentPane }

// RunWithProducers drives a picker through Bubble Tea immediately with independent
// concurrent candidate producers, entering raw input mode immediately and
// streaming results progressively as each finishes.
func RunWithProducers(ctx context.Context, producers []SourceProducer, query string, renderer preview.Renderer, layout Layout) (source.Candidate, RowAction, string, *source.Pane, bool, error) {
	m := NewModelWithProducers(producers, query, renderer, ctx, layout)
	return runProgramWithPane(ctx, m)
}

func runProgramWithPane(ctx context.Context, m Model, opts ...tea.ProgramOption) (source.Candidate, RowAction, string, *source.Pane, bool, error) {
	ls := startLiveStatus(ctx, m.layout.StatusDialer)
	if ls != nil {
		defer func() {
			_ = ls.Close()
		}()
		m = m.WithLiveStatus(ls.Events())
	}
	allOpts := make([]tea.ProgramOption, 0, 2+len(opts))
	allOpts = append(allOpts, tea.WithContext(ctx), tea.WithAltScreen())
	allOpts = append(allOpts, opts...)
	p := tea.NewProgram(m, allOpts...)
	final, err := p.Run()
	if err != nil {
		return source.Candidate{}, RowActionOpen, "", nil, false, err
	}
	finalModel := final.(Model)
	cand, action, target, ok, ferr := finalizeRun(finalModel)
	return cand, action, target, finalModel.CurrentPane(), ok, ferr
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
func runProgram(ctx context.Context, m Model, opts ...tea.ProgramOption) (source.Candidate, RowAction, string, bool, error) {
	ls := startLiveStatus(ctx, m.layout.StatusDialer)
	if ls != nil {
		defer func() {
			_ = ls.Close()
		}()
		m = m.WithLiveStatus(ls.Events())
	}
	allOpts := make([]tea.ProgramOption, 0, 2+len(opts))
	allOpts = append(allOpts, tea.WithContext(ctx), tea.WithAltScreen())
	allOpts = append(allOpts, opts...)
	p := tea.NewProgram(m, allOpts...)
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
