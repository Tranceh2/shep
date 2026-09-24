package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/history"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/templates"
	"github.com/tranceh2/shep/internal/tui"
	"github.com/tranceh2/shep/internal/workspacename"
)

// sessionAttachFunc is the command-layer seam for a foreground session
// attach. It receives the resolved binary, validated session name, and already
// filtered child environment; it must block until the child exits.
type sessionAttachFunc func(context.Context, string, string, []string) error

// asyncTUIRunFunc drives the interactive input-first TUI picker. Tests substitute
// a fake to assert async loader behavior without driving a real terminal program.
type asyncTUIRunFunc func(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error)

// openCmd builds `shep open [query]`. The command resolves the query (or the
// --path override) to a single candidate, drilling into a nested picker when
// the resolved candidate is a group workspace, then invokes Herdr to
// focus/create a workspace for it (applying its template on creation), and
// falls back to printing the resolved absolute path when Herdr is absent or
// unavailable.
func (a *App) openCmd() *cobra.Command {
	var pathFlag string
	var targetFlag string
	cmd := &cobra.Command{
		Use:   "open [query]",
		Short: "Open a project with Herdr (or print its path when Herdr is absent)",
		Long: `shep open resolves a query to a single project candidate and asks Herdr
to focus it if it is already an open workspace, or to create a new focused
workspace otherwise. When Herdr is not installed or its daemon is unreachable,
shep prints the resolved absolute path and exits 0 so the caller can still
reach the project through any shell cd / file manager.

A selector cascade short-circuits an exact match, accelerates with fzf when
installed, and falls back to an interactive TUI. With multiple candidates and
no selection, shep prints the candidates and exits 1.

The --target flag selects WHERE a candidate opens:
workspace (default) creates/focuses a standalone Herdr workspace; tab opens it
as a new tab in the Herdr workspace shep is running inside; pane splits it into
a new pane beside the current one. tab and pane require shep to be running
inside a Herdr pane and support command-type workspace entries, zoxide, and
projects (already-open herdr workspaces, templates, and groups are rejected).`,
		Args: cobra.MaximumNArgs(1),
		// PreRunE (not PersistentPreRunE) so the root's inherited
		// PersistentPreRunE still loads config + probes first; this hook then
		// validates --target before RunE fires. The root has SilenceErrors,
		// so the error is printed here to stderr and errExitOne is returned
		// (matching how launch surfaces its user-facing errors).
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateTarget(targetFlag); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return errExitOne
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			return a.runOpen(cmd, query, pathFlag, targetFlag)
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"open the given absolute path directly, bypassing query resolution (used by the Television cable)")
	cmd.Flags().StringVar(&targetFlag, "target", "workspace",
		"where to open an entry: workspace (default), tab, or pane")
	return cmd
}

// validTargets is the closed set accepted by --target. workspace preserves the
// pre-flag behaviour (focus/create a standalone Herdr workspace); tab and pane
// open the entry inside the Herdr workspace shep is currently running in.
var validTargets = map[string]bool{
	"workspace": true,
	"tab":       true,
	"pane":      true,
}

// validateTarget rejects an unknown --target value at the cobra layer so a
// typo never silently falls through to the workspace launch path.
func validateTarget(target string) error {
	if !validTargets[target] {
		return fmt.Errorf("invalid --target %q: must be one of workspace, tab, pane", target)
	}
	return nil
}

// selectorFactory builds the cascade for `shep open` honouring
// [general].selector. Tests override the cascade via the selectorBuilder
// field. Direct always runs first regardless of selector value. a.currentPane
// (queried once by runOpen before candidate resolution) and a.setChosenTarget
// are threaded into the TUI selector so its footer hints/ctrl+t/ctrl+p
// bindings can react to it and write a target override back onto a.
func (a *App) resolveStatusDialer() tui.StatusDialer {
	if a.statusDialer != nil {
		return a.statusDialer
	}
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	if strings.TrimSpace(socketPath) == "" {
		return nil
	}
	return tui.NewUnixStatusDialer(socketPath)
}

func (a *App) selectorFactory(matches []source.Candidate) *selector.Cascade {
	if a.selectorBuilder != nil {
		return a.selectorBuilder()
	}
	cfg := a.Config()
	if a.startupSnapshot != nil {
		layout := layoutFromConfigWithIntegrations(cfg.TUI, cfg.General.SourceOrder, cfg.Integrations, cfg.Sources)
		layout.RankingSnapshot = a.rankingSnapshot(matches)
		layout.StatusDialer = a.resolveStatusDialer()
		layout.PinToggler = a.pinToggler()
		return snapshotCascadeFor(cfg.General.Selector, a.buildPreviewRendererForSnapshot(*a.startupSnapshot), a.currentPane, a.setChosenTarget, a.setChosenAction, *a.startupSnapshot, a.Driver(), a.buildPreviewRendererForSnapshot, cfg.Sources.Herdr.Icon, matches, layout)
	}
	layout := layoutFromConfigWithIntegrations(cfg.TUI, cfg.General.SourceOrder, cfg.Integrations, cfg.Sources)
	layout.RankingSnapshot = a.rankingSnapshot(matches)
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), a.currentPane, a.setChosenTarget, a.setChosenAction, a.buildTreeExpander(), matches, layout)
}

func (a *App) rankingSnapshot(_ []source.Candidate) ranking.Snapshot {
	a.rankingMu.Lock()
	defer a.rankingMu.Unlock()
	return a.rankingData
}

func (a *App) pinToggler() tui.PinToggler {
	return func(ctx context.Context, candidate source.Candidate) tui.PinToggleResultMsg {
		key := ranking.PinKey(candidate)
		if err := a.openRankingForPins(ctx); err != nil {
			return tui.PinToggleResultMsg{Key: key, Err: err}
		}
		a.rankingMu.Lock()
		defer a.rankingMu.Unlock()
		if a.rankingStore == nil {
			return tui.PinToggleResultMsg{Key: key, Err: errors.New("ranking store unavailable")}
		}
		pinned, err := a.rankingStore.TogglePin(ctx, key)
		return tui.PinToggleResultMsg{Key: key, Pinned: pinned, Err: err}
	}
}

func (a *App) loadWorkspaceMRU(ctx context.Context) ([]string, error) {
	if a.historyMRUReader != nil {
		return a.historyMRUReader(ctx)
	}
	socketPath := currentHerdrSocketPath()
	if socketPath == "" {
		return nil, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	return history.ReadWorkspaceMRU(readCtx, socketPath)
}

func (a *App) openRankingForPins(ctx context.Context) error {
	a.rankingMu.Lock()
	defer a.rankingMu.Unlock()
	if a.rankingStore != nil {
		return nil
	}
	openRanking := a.rankingOpen
	if openRanking == nil {
		openRanking = func() (rankingStore, error) { return ranking.Open() }
	}
	store, err := openRanking()
	if err != nil {
		return err
	}
	a.rankingStore = store
	snapshot := store.Snapshot(ctx, "")
	if mru, err := a.loadWorkspaceMRU(ctx); err == nil && len(mru) > 0 {
		snapshot = snapshot.WithWorkspaceMRU(mru)
	}
	if !a.Config().Ranking.Enabled {
		snapshot = snapshot.WithAdaptiveEnabled(false)
	}
	a.rankingData = snapshot
	return nil
}

// selectorFactoryForOrder mirrors selectorFactory but threads an explicit
// source order into the picker layout (R3-1). It is used ONLY on the nested
// group-recursion branch of resolveFromRegistry so the group's effective order
// drives Layout.SourceOrder identically to ranking.SortBySourceOrder; the
// top-level path keeps selectorFactory(matches) and cfg.General.SourceOrder
// byte-identical. A non-nil selectorBuilder override still wins so nested
// picker tests can script the cascade the same way top-level tests do.
func (a *App) selectorFactoryForOrder(order []string, matches []source.Candidate) *selector.Cascade {
	if a.selectorBuilder != nil {
		return a.selectorBuilder()
	}
	cfg := a.Config()
	if a.startupSnapshot != nil {
		layout := layoutFromConfigWithIntegrations(cfg.TUI, order, cfg.Integrations, cfg.Sources)
		layout.RankingSnapshot = a.rankingSnapshot(matches)
		layout.StatusDialer = a.resolveStatusDialer()
		layout.PinToggler = a.pinToggler()
		return snapshotCascadeFor(cfg.General.Selector, a.buildPreviewRendererForSnapshot(*a.startupSnapshot), a.currentPane, a.setChosenTarget, a.setChosenAction, *a.startupSnapshot, a.Driver(), a.buildPreviewRendererForSnapshot, cfg.Sources.Herdr.Icon, matches, layout)
	}
	layout := layoutFromConfigWithIntegrations(cfg.TUI, order, cfg.Integrations, cfg.Sources)
	layout.RankingSnapshot = a.rankingSnapshot(matches)
	layout.StatusDialer = a.resolveStatusDialer()
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), a.currentPane, a.setChosenTarget, a.setChosenAction, a.buildTreeExpander(), matches, layout)
}

// buildTreeExpander returns the startup generation's pure child tree. State
// refreshes construct an entirely new tree inside tui.Model; no cache or
// fragmented driver loader remains here.
func (a *App) buildTreeExpander() *tui.TreeExpander {
	if a.startupSnapshot == nil {
		return nil
	}
	return tui.NewTreeExpanderFromSnapshot(*a.startupSnapshot)
}

// treeActiveFor reports whether tree-expand should replace the normal
// selector cascade for this resolution pass (R6): active only when there is
// more than one candidate to choose from AND at least one is an
// already-open SourceHerdr workspace. Fzf cannot render synthesized child
// rows, so when this is true the cascade skips it entirely and routes
// through the tree-aware TUI selector instead (see cascadeFor).
func treeActiveFor(matches []source.Candidate) bool {
	if len(matches) <= 1 {
		return false
	}
	for _, c := range matches {
		if c.Source == config.SourceHerdr {
			return true
		}
	}
	return false
}

// layoutFromConfig builds the tui.Layout consumed by the picker from the
// loaded [tui] config, threading list_width/preview_width and the layout
// orientation through the same way. This is the user's configured DEFAULT
// orientation for the session — the live ctrl+l keybinding may flip it
// in-memory afterwards without ever writing back to cfg. sources is
// [general].sources (already normalized non-empty by config.Load()), threaded
// through as Layout.SourceOrder so the picker's row order matches the
// configured provider order instead of a hardcoded literal — the same order
// source.Registry.Enabled() already collects candidates in. t.Icons threads
// through as Layout.Icons, selecting the picker's own icon fallback tier
// (unicode/ascii — see internal/tui/icons.go). sourceConfigs is optional for
// compatibility with direct callers; production passes the normalized loaded
// config to thread resolved row format templates into the Model.
func layoutFromConfig(t config.TUIConfig, sources []string, sourceConfigs ...config.SourcesConfig) tui.Layout {
	return layoutFromConfigWithIntegrations(t, sources, nil, sourceConfigs...)
}

// layoutFromConfigWithIntegrations extends layoutFromConfig with the
// declared [[integrations]] label formats, keyed by name so each
// integration's own label_format resolves per-source at render time (see
// tui.LabelFormats.Integrations / rowLabelFormat).
func layoutFromConfigWithIntegrations(t config.TUIConfig, sources []string, integrations []config.IntegrationConfig, sourceConfigs ...config.SourcesConfig) tui.Layout {
	layout := tui.Layout{
		ListWidth:    t.ListWidth,
		PreviewWidth: t.PreviewWidth,
		Orientation:  t.Layout,
		Theme:        t.Theme,
		SourceOrder:  sources,
		Icons:        t.Icons,
	}
	if len(integrations) > 0 {
		formats := make(map[string]string, len(integrations))
		for _, integration := range integrations {
			formats[integration.Name] = integration.LabelFormat
		}
		layout.LabelFormats.Integrations = formats
	}
	if len(sourceConfigs) == 0 {
		return layout
	}
	s := sourceConfigs[0]
	layout.LabelFormats.Herdr = s.Herdr.LabelFormat
	layout.LabelFormats.Sessions = s.Sessions.LabelFormat
	layout.LabelFormats.Workspaces = s.Workspaces.LabelFormat
	layout.LabelFormats.Zoxide = s.Zoxide.LabelFormat
	layout.LabelFormats.Projects = s.Projects.LabelFormat
	layout.LabelFormats.Tab = s.Herdr.TabLabelFormat
	layout.LabelFormats.Pane = s.Herdr.PaneLabelFormat
	return layout
}

// buildPreviewRenderer wires the production preview.Renderer from the loaded
// config and binary probes so the TUI's preview pane and the `shep preview`
// command share identical rendering behaviour. A CommandRunner is always
// constructed (it powers both the "dir" built-in and any declared
// preview.commands); the active Herdr driver (if any) supplies only live pane
// reads. Workspace and agent-status state require a startup snapshot.
func (a *App) buildPreviewRenderer() preview.Renderer {
	if a.startupSnapshot != nil {
		return a.buildPreviewRendererForSnapshot(*a.startupSnapshot)
	}
	cfg := a.Config()
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	runner := preview.NewCommandRunner()
	var opts []preview.RendererOption
	if driver := a.Driver(); driver != nil {
		opts = append(opts, preview.WithPaneReader(driver))
	}
	return preview.NewRenderer(cfg, a.Probes(), git, runner, opts...)
}

func (a *App) buildPreviewRendererForSnapshot(snapshot source.Snapshot) preview.Renderer {
	cfg := a.Config()
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	runner := preview.NewCommandRunner()
	opts := []preview.RendererOption{preview.WithSnapshot(snapshot)}
	if driver := a.Driver(); driver != nil {
		opts = append(opts, preview.WithPaneReader(driver))
	}
	return preview.NewRenderer(cfg, a.Probes(), git, runner, opts...)
}

// cascadeFor builds the selector cascade for a [general].selector value.
// builtin skips fzf and uses the Bubble Tea TUI; fzf and auto include fzf
// (Fzf.Select no-ops when the binary is absent, so both fall back to the TUI).
// Direct is always first so exact / single matches short-circuit. currentPane,
// onTarget and onAction are threaded into the TUI selector (see
// newTUISelector); layout is optional (variadic so existing callers keep
// compiling) and configures the TUI's list/preview pane widths.
//
// When treeActiveFor(matches) reports true (R6), [general].selector is
// ignored entirely: the cascade becomes [direct, tui_tree] — fzf is always
// skipped (it cannot render synthesized Herdr-tab child rows) and the
// tree-aware tuiTreeSelector (backed by tree) runs instead of the plain TUI.
func cascadeFor(sel string, renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), tree *tui.TreeExpander, matches []source.Candidate, layout ...tui.Layout) *selector.Cascade {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	direct := selector.Direct{}
	if treeActiveFor(matches) {
		return selector.New(direct, newTUITreeSelector(renderer, currentPane, onTarget, onAction, tree, l))
	}
	tuiSel := newTUISelector(renderer, currentPane, onTarget, onAction, l)
	switch sel {
	case config.SelectorFzf, config.SelectorAuto:
		return selector.New(direct, selector.NewFzf(), tuiSel)
	default: // SelectorBuiltin, empty, or unknown
		return selector.New(direct, tuiSel)
	}
}

// tuiRunFunc matches tui.Run's signature so tests can substitute a fake
// picker (scripting a ctrl+t/ctrl+p target) without driving a real Bubble Tea
// program.
type tuiRunFunc func(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, currentPane *source.Pane, layout ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error)

// tuiSelector is the universal interactive fallback: it runs the embedded
// Bubble Tea picker over the candidates, threading through the shared
// preview.Renderer so the picker's preview pane matches `shep preview`
// output. If the interactive selector cannot run, the open command falls back
// to printing the ambiguous candidate list and exits 1.
type tuiSelector struct {
	renderer    preview.Renderer
	layout      tui.Layout
	currentPane *source.Pane
	// onTarget receives the target the TUI picker resolved (ctrl+t => "tab",
	// ctrl+p => "pane", "" for the default via enter) once Select returns a
	// successful pick. It exists because selector.Selector's Select signature
	// is fixed and shared by every cascade member (Direct, Fzf, tuiSelector),
	// so a target override cannot itself be an extra return value there;
	// runOpen instead reads it back via App.chosenTarget, set through this
	// callback (App.setChosenTarget).
	onTarget func(string)
	// onAction receives the typed RowAction of the picked row once Select
	// returns a successful pick — same out-of-band pattern as onTarget, for
	// the same reason (Select's signature is fixed). runOpen reads it back
	// via App.chosenAction (App.setChosenAction) and passes it to launch,
	// which dispatches on the typed action instead of the candidate's Source
	// string.
	onAction func(tui.RowAction)
	// run defaults to tui.Run; tests substitute a fake to simulate a
	// ctrl+t/ctrl+p pick without driving a real Bubble Tea program.
	run tuiRunFunc
}

// newTUISelector builds a tuiSelector carrying the given Renderer (nil is
// valid in tests and degrades to the picker's built-in candidate summary),
// the Herdr pane shep is currently running inside (nil when not running
// inside one), callbacks receiving the chosen target and typed RowAction
// after a successful pick, and pane-width Layout (zero value falls back to
// the built-in heuristic).
func newTUISelector(renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), layout ...tui.Layout) *tuiSelector {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	return &tuiSelector{renderer: renderer, layout: l, currentPane: currentPane, onTarget: onTarget, onAction: onAction, run: tui.Run}
}

func (tuiSelector) Name() string { return "tui" }

func (s tuiSelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	run := s.run
	if run == nil {
		run = tui.Run
	}
	cand, action, target, ok, err := run(ctx, candidates, query, s.renderer, s.currentPane, s.layout)
	if ok {
		if s.onTarget != nil {
			s.onTarget(target)
		}
		if s.onAction != nil {
			s.onAction(action)
		}
	}
	return cand, ok, err
}

// tuiTreeRunFunc matches tui.RunWithTree's signature so tests can substitute
// a fake tree-aware picker without driving a real Bubble Tea program.
type tuiTreeRunFunc func(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, tree *tui.TreeExpander, currentPane *source.Pane, layout ...tui.Layout) (source.Candidate, tui.RowAction, string, bool, error)

// tuiTreeSelector is tuiSelector's tree-expand-active counterpart (R6):
// identical shape and callback contract, but drives tui.RunWithTree so the
// picker can synthesize Herdr-tab child rows under a matching workspace
// parent. cascadeFor only ever builds this selector in place of tuiSelector
// (never alongside it) when treeActiveFor(matches) is true, and fzf is
// never part of that cascade — it cannot render synthesized rows.
type tuiTreeSelector struct {
	renderer    preview.Renderer
	layout      tui.Layout
	currentPane *source.Pane
	tree        *tui.TreeExpander
	onTarget    func(string)
	onAction    func(tui.RowAction)
	// run defaults to tui.RunWithTree; tests substitute a fake to simulate a
	// pick (including a ctrl+t/ctrl+p target) without driving a real Bubble
	// Tea program.
	run tuiTreeRunFunc
}

// newTUITreeSelector builds a tuiTreeSelector carrying the given Renderer
// (nil degrades to the picker's built-in candidate summary, same as
// newTUISelector), the Herdr pane shep is currently running inside, target
// and action callbacks, the TreeExpander backing child-row synthesis, and
// pane-width Layout.
func newTUITreeSelector(renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), tree *tui.TreeExpander, layout ...tui.Layout) *tuiTreeSelector {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	return &tuiTreeSelector{renderer: renderer, layout: l, currentPane: currentPane, tree: tree, onTarget: onTarget, onAction: onAction, run: tui.RunWithTree}
}

func (tuiTreeSelector) Name() string { return "tui_tree" }

func (s tuiTreeSelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	run := s.run
	if run == nil {
		run = tui.RunWithTree
	}
	cand, action, target, ok, err := run(ctx, candidates, query, s.renderer, s.tree, s.currentPane, s.layout)
	if ok {
		if s.onTarget != nil {
			s.onTarget(target)
		}
		if s.onAction != nil {
			s.onAction(action)
		}
	}
	return cand, ok, err
}

// snapshotTUISelector is the production selector for a startup-hydrated Herdr
// session. It gives Model its snapshot driver and immutable-renderer factory;
// the older generic selectors remain useful only when no Herdr state exists.
type snapshotTUISelector struct {
	name                string
	renderer            preview.Renderer
	layout              tui.Layout
	currentPane         *source.Pane
	snapshot            source.Snapshot
	driver              source.HerdrDriver
	rendererForSnapshot tui.SnapshotRendererFactory
	herdrIcon           string
	onTarget            func(string)
	onAction            func(tui.RowAction)
}

func (s snapshotTUISelector) Name() string { return s.name }

func (s snapshotTUISelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	cand, action, target, ok, err := tui.RunWithSnapshot(ctx, candidates, query, s.renderer, s.snapshot, s.driver, s.rendererForSnapshot, s.herdrIcon, s.currentPane, s.layout)
	if ok {
		if s.onTarget != nil {
			s.onTarget(target)
		}
		if s.onAction != nil {
			s.onAction(action)
		}
	}
	return cand, ok, err
}

func snapshotCascadeFor(sel string, renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), snapshot source.Snapshot, driver source.HerdrDriver, rendererForSnapshot tui.SnapshotRendererFactory, herdrIcon string, matches []source.Candidate, layout tui.Layout) *selector.Cascade {
	direct := selector.Direct{}
	picker := snapshotTUISelector{
		name:                "tui",
		renderer:            renderer,
		layout:              layout,
		currentPane:         currentPane,
		snapshot:            snapshot,
		driver:              driver,
		rendererForSnapshot: rendererForSnapshot,
		herdrIcon:           herdrIcon,
		onTarget:            onTarget,
		onAction:            onAction,
	}
	if treeActiveFor(matches) {
		picker.name = "tui_tree"
		return selector.New(direct, picker)
	}
	switch sel {
	case config.SelectorFzf, config.SelectorAuto:
		return selector.New(direct, selector.NewFzf(), picker)
	default:
		return selector.New(direct, picker)
	}
}

func (a *App) runAsyncTUI(ctx context.Context, producers []tui.SourceProducer, query string, layout tui.Layout) (source.Candidate, tui.RowAction, string, *source.Pane, bool, error) {
	if a.asyncTUIRun != nil {
		return a.asyncTUIRun(ctx, producers, query, layout)
	}
	return tui.RunWithProducers(ctx, producers, query, nil, layout)
}

func (a *App) buildProviderProducer(p source.Provider, icon string) tui.SourceProducer {
	return func(ctx context.Context) tui.SourceResultMsg {
		raw, err := p.List(ctx)
		cands := make([]source.Candidate, 0, len(raw))
		for _, c := range raw {
			clone := c.Clone()
			if icon != "" {
				clone.Icon = icon
			}
			cands = append(cands, clone)
		}
		return tui.SourceResultMsg{
			Source:     p.Name(),
			Candidates: cands,
			Err:        err,
		}
	}
}

func (a *App) buildHerdrProducer(icon string, sessionsIcon string) tui.SourceProducer {
	cfg := a.Config()
	driver := a.Driver()
	sessionsEnabled := false
	for _, src := range cfg.General.SourceOrder {
		if src == config.SourceSessions {
			sessionsEnabled = true
			break
		}
	}
	return func(ctx context.Context) tui.SourceResultMsg {
		if driver == nil || !driver.Detect(ctx) {
			return tui.SourceResultMsg{Source: config.SourceHerdr}
		}
		snapshotCtx, cancel := context.WithTimeout(ctx, source.SnapshotTimeout)
		snapshot, err := driver.Snapshot(snapshotCtx)
		cancel()
		if err != nil {
			return tui.SourceResultMsg{
				Source: config.SourceHerdr,
				Err:    err,
			}
		}

		rawHerdr := source.HerdrCandidates(snapshot)
		cands := make([]source.Candidate, 0, len(rawHerdr))
		for _, c := range rawHerdr {
			clone := c.Clone()
			if icon != "" {
				clone.Icon = icon
			}
			cands = append(cands, clone)
		}

		if sessionsEnabled {
			sessCtx, sCancel := context.WithTimeout(ctx, source.SessionsListTimeout)
			sessions, sErr := driver.ListSessions(sessCtx)
			sCancel()
			if sErr == nil {
				rawSessions := source.SessionCandidates(sessions, os.Getenv)
				for _, c := range rawSessions {
					clone := c.Clone()
					if sessionsIcon != "" {
						clone.Icon = sessionsIcon
					}
					cands = append(cands, clone)
				}
			}
		}

		var currentPane *source.Pane
		if pane, ok := source.ResolveFocusedPane(snapshot); ok {
			copy := *pane
			currentPane = &copy
		}

		tree := tui.NewTreeExpanderFromSnapshot(snapshot)
		renderer := a.buildPreviewRendererForSnapshot(snapshot)

		return tui.SourceResultMsg{
			Source:              config.SourceHerdr,
			Candidates:          cands,
			Tree:                tree,
			SnapshotDriver:      driver,
			Snapshot:            &snapshot,
			RendererForSnapshot: a.buildPreviewRendererForSnapshot,
			HerdrIcon:           cfg.Sources.Herdr.Icon,
			Renderer:            renderer,
			CurrentPane:         currentPane,
		}
	}
}

func (a *App) buildRankingProducer() tui.SourceProducer {
	cfg := a.Config()
	return func(ctx context.Context) tui.SourceResultMsg {
		if !cfg.Ranking.Enabled {
			return tui.SourceResultMsg{Source: "ranking"}
		}
		a.rankingMu.Lock()
		store := a.rankingStore
		a.rankingMu.Unlock()
		if store == nil {
			if err := a.openRankingForPins(ctx); err != nil {
				return tui.SourceResultMsg{Source: "ranking", Err: err}
			}
			a.rankingMu.Lock()
			store = a.rankingStore
			a.rankingMu.Unlock()
		}
		a.rankingMu.Lock()
		snap := store.Snapshot(ctx, "")
		a.rankingMu.Unlock()
		if mru, err := a.loadWorkspaceMRU(ctx); err == nil && len(mru) > 0 {
			snap = snap.WithWorkspaceMRU(mru)
		}
		return tui.SourceResultMsg{
			Source:          "ranking",
			RankingSnapshot: &snap,
		}
	}
}

func (a *App) buildStreamingProducers(cmdCtx context.Context) []tui.SourceProducer {
	cfg := a.Config()
	probes := a.Probes()
	registry := source.NewRegistry(cfg, probes, a.Driver())

	var producers []tui.SourceProducer

	for _, p := range registry.Enabled() {
		switch p.Name() {
		case config.SourceHerdr:
			producers = append(producers, a.buildHerdrProducer(registry.IconFor(config.SourceHerdr), registry.IconFor(config.SourceSessions)))
		case config.SourceSessions:
			hasHerdr := false
			for _, src := range cfg.General.SourceOrder {
				if src == config.SourceHerdr {
					hasHerdr = true
					break
				}
			}
			if !hasHerdr {
				producers = append(producers, a.buildProviderProducer(p, registry.IconFor(config.SourceSessions)))
			}
		default:
			// workspaces, zoxide, projects, and every declared integration
			// share the same generic producer builder (see
			// App.buildProviderProducer): they all just call p.List(ctx) and
			// stream the result through tui.SourceResultMsg.Err on failure.
			producers = append(producers, a.buildProviderProducer(p, registry.IconFor(p.Name())))
		}
	}

	if cfg.Ranking.Enabled {
		producers = append(producers, a.buildRankingProducer())
	}

	return producers
}

// runOpen is the pipeline so tests can call it directly against a fresh App.
// target is the resolved --target value ("workspace", "tab", or "pane"); for
// the interactive TUI path, the model can override it via App.chosenTarget.
func (a *App) runOpen(cmd *cobra.Command, query, pathFlag, targetFlag string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	a.chosenTarget = ""
	a.chosenAction = tui.RowActionOpen
	a.rankingMu.Lock()
	a.rankingStore = nil
	a.rankingData = ranking.Snapshot{}
	a.rankingMu.Unlock()

	defer func() {
		a.rankingMu.Lock()
		store := a.rankingStore
		a.rankingStore = nil
		a.rankingMu.Unlock()
		if store != nil {
			_ = store.Close()
		}
	}()

	cfg := a.Config()
	// Use synchronous resolution when direct path, '.', test overrides cascade,
	// when fzf is explicitly configured, or when a CLI query was supplied.
	if cmd.Flags().Changed("path") || query == "." || a.selectorBuilder != nil || cfg.General.Selector == config.SelectorFzf || query != "" {
		if cfg.Ranking.Enabled {
			openRanking := a.rankingOpen
			if openRanking == nil {
				openRanking = func() (rankingStore, error) { return ranking.Open() }
			}
			if store, err := openRanking(); err == nil {
				a.rankingMu.Lock()
				a.rankingStore = store
				snap := store.Snapshot(cmd.Context(), "")
				if mru, err := a.loadWorkspaceMRU(cmd.Context()); err == nil && len(mru) > 0 {
					snap = snap.WithWorkspaceMRU(mru)
				}
				a.rankingData = snap
				a.rankingMu.Unlock()
			}
		}

		if err := a.hydrateStartupSnapshot(cmd.Context()); err != nil {
			fmt.Fprintf(errOut, "warning: herdr snapshot unavailable: %v\n", err)
		}
		if a.startupSnapshot != nil {
			a.rankingMu.Lock()
			if len(a.startupSnapshot.Workspaces) > 0 {
				a.rankingData = a.rankingData.WithFilteredWorkspaceMRU(a.startupSnapshot.Workspaces)
			}
			if a.startupSnapshot.FocusedWorkspaceID != "" {
				a.rankingData = a.rankingData.WithCurrentExact(ranking.Identity(source.Candidate{
					Source: config.SourceHerdr,
					Meta:   map[string]string{"workspace_id": a.startupSnapshot.FocusedWorkspaceID},
				}))
			}
			a.rankingMu.Unlock()
		}

		cand, ok, err := a.resolveCandidate(cmd, query, pathFlag, out, errOut)
		if err != nil {
			return err
		}
		if !ok {
			return errExitOne
		}

		target := targetFlag
		if a.chosenTarget != "" {
			target = a.chosenTarget
		}

		outcome, err := a.launch(cmd.Context(), cand, a.chosenAction, target, a.currentPane, out, errOut)
		if err != nil {
			return err
		}
		a.rankingMu.Lock()
		rankingReady := a.rankingStore != nil
		a.rankingMu.Unlock()
		if outcome == launchOutcomeCompleted && rankingReady && cfg.Ranking.Enabled {
			a.recordRankingSuccess(cand)
		}
		return nil
	}

	// Interactive startup with no query: enter Bubble Tea immediately with streaming producers!
	layout := layoutFromConfigWithIntegrations(cfg.TUI, cfg.General.SourceOrder, cfg.Integrations, cfg.Sources)
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	if err := a.openRankingForPins(cmd.Context()); err != nil {
		fmt.Fprintf(errOut, "warning: pin storage unavailable: %v\n", err)
	}

	producers := a.buildStreamingProducers(cmd.Context())
	cand, action, chosenTarget, currentPane, ok, selErr := a.runAsyncTUI(cmd.Context(), producers, query, layout)
	if selErr != nil {
		if errors.Is(selErr, tui.ErrCancelled) {
			return nil
		}
		fmt.Fprintf(errOut, "selector unavailable: %v\n", selErr)
		return errExitOne
	}
	if !ok {
		return errExitOne
	}

	if currentPane != nil {
		a.currentPane = currentPane
	}

	if cand.Path != "" && cand.Source != config.SourceSessions && action != tui.RowActionFocusTab {
		if _, err := os.Stat(cand.Path); err != nil {
			cand.Missing = true
		}
	}

	if chosenTarget != "" {
		a.chosenTarget = chosenTarget
	}
	a.chosenAction = action

	if cand.Meta["group"] == "true" {
		groupSources := splitNonEmpty(cand.Meta["group_sources"], ",")
		groupWorkspace, hasWorkspace := workspaceConfigForCandidate(cfg.Workspaces, cand)
		nestedTemplate := cand.Meta["group_template"]
		var nested *source.Registry
		nestedOrder := effectiveGroupSourceOrder(cfg, groupWorkspace, hasWorkspace, groupSources)
		if hasWorkspace {
			nested = a.withStartupSnapshot(source.NewScopedRegistryForWorkspaceWithOrder(cfg, a.Probes(), a.Driver(), groupWorkspace, nestedOrder, cand.Path))
		} else {
			nested = a.withStartupSnapshot(source.NewScopedRegistryWithOrder(cfg, a.Probes(), a.Driver(), groupSources, nestedOrder, cand.Path))
		}
		nestedCand, nestedOk, err := a.resolveFromRegistry(cmd, nested, "", nestedTemplate, nestedOrder, out, errOut)
		if err != nil {
			return err
		}
		if !nestedOk {
			return errExitOne
		}
		cand = nestedCand
	}

	target := targetFlag
	if a.chosenTarget != "" {
		target = a.chosenTarget
	}

	outcome, err := a.launch(cmd.Context(), cand, a.chosenAction, target, a.currentPane, out, errOut)
	if err != nil {
		return err
	}
	a.rankingMu.Lock()
	rankingReady := a.rankingStore != nil
	a.rankingMu.Unlock()
	if outcome == launchOutcomeCompleted && rankingReady && cfg.Ranking.Enabled {
		a.recordRankingSuccess(cand)
	}
	return nil
}

// hydrateStartupSnapshot captures the one full Herdr state generation used by
// this open invocation. It intentionally replaces the fragmented current/list
// probes: focus is resolved from focused_pane_id in the same generation.
const rankingRecordTimeout = 1 * time.Second

func (a *App) recordRankingSuccess(cand source.Candidate) {
	a.rankingMu.Lock()
	store := a.rankingStore
	snapshot := a.rankingData
	a.rankingMu.Unlock()
	if store == nil || !a.Config().Ranking.Enabled {
		return
	}
	exact, resource := ranking.CandidateKeyParts(cand)
	keys := ranking.Keys{Exact: exact, Resource: resource, CurrentExact: snapshot.CurrentExact()}
	ctx, cancel := context.WithTimeout(context.Background(), rankingRecordTimeout)
	defer cancel()
	_ = store.RecordSuccess(ctx, keys)
}

func (a *App) hydrateStartupSnapshot(ctx context.Context) error {
	a.currentPane = nil
	a.startupSnapshot = nil
	a.startupSnapshotAttempted = false
	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		return nil
	}
	a.startupSnapshotAttempted = true
	snapshotCtx, cancel := context.WithTimeout(ctx, source.SnapshotTimeout)
	defer cancel()
	snapshot, err := driver.Snapshot(snapshotCtx)
	if err != nil {
		return err
	}
	a.startupSnapshot = &snapshot
	if pane, ok := source.ResolveFocusedPane(snapshot); ok {
		copy := *pane
		a.currentPane = &copy
	}
	return nil
}

// resolveCandidate produces the candidate to launch, honouring --path first
// then the resolution + selector cascade, drilling into a nested picker when
// the resolved candidate is a group workspace. ok=false means a user-facing
// reason was already printed and the caller should exit 1.
func (a *App) resolveCandidate(cmd *cobra.Command, query, pathFlag string, out, errOut io.Writer) (source.Candidate, bool, error) {
	if cmd.Flags().Changed("path") {
		cand, err := candidateFromPath(pathFlag)
		if err != nil {
			fmt.Fprintf(errOut, "resolve --path: %v\n", err)
			return source.Candidate{}, false, errExitOne
		}
		return cand, true, nil
	}
	// A bare "." always means "the current directory", even though cwd is
	// not exposed as a picker source: it resolves the same way --path does,
	// bypassing candidate resolution entirely.
	if query == "." {
		cand, err := candidateFromPath(".")
		if err != nil {
			fmt.Fprintf(errOut, "resolve .: %v\n", err)
			return source.Candidate{}, false, errExitOne
		}
		return cand, true, nil
	}

	registry := a.withStartupSnapshot(source.NewRegistry(a.Config(), a.Probes(), a.Driver()))
	return a.resolveFromRegistry(cmd, registry, query, "", nil, out, errOut)
}

func (a *App) withStartupSnapshot(registry *source.Registry) *source.Registry {
	if !a.startupSnapshotAttempted {
		return registry
	}
	if a.startupSnapshot == nil {
		return registry.DisableHerdr()
	}
	return registry.WithHerdrSnapshot(*a.startupSnapshot)
}

// resolveFromRegistry runs the collect/dedup/select pipeline against
// registry. When the resolved pick is a group workspace marker, it recurses
// into a scoped nested registry rooted at the group's own path/sources,
// threading the group's own template (if any, else the caller's
// parentTemplate) forward as Meta["parent_template"] on the eventually
// launched candidate (template resolution precedence tier 5).
//
// order is the effective source order for THIS resolution pass: nil means
// "use cfg.General.SourceOrder" (the top-level pass), while a non-nil slice is
// the group's effective order computed at the group-recursion boundary (R3-1).
// The same order drives both ranking.SortBySourceOrder and the picker layout so
// the nested cascade's row order and block layout never diverge.
func (a *App) resolveFromRegistry(cmd *cobra.Command, registry *source.Registry, query, parentTemplate string, order []string, out, errOut io.Writer) (source.Candidate, bool, error) {
	effectiveOrder := order
	if effectiveOrder == nil {
		effectiveOrder = a.Config().General.SourceOrder
	}
	all, matches, err := resolver.ResolveFromSources(cmd.Context(), registry, query)
	if err != nil {
		// Partial collect errors are non-fatal for resolution itself; the
		// matches slice is still authoritative. We only surface hard errors
		// when there are zero matches as well.
		fmt.Fprintf(errOut, "warning: a source failed: %v\n", err)
	}

	var pick source.Candidate
	switch len(matches) {
	case 0:
		fmt.Fprintf(errOut, "no match: %s\n", query)
		return source.Candidate{}, false, errExitOne
	case 1:
		pick = matches[0]
	default:
		var cascade *selector.Cascade
		if order == nil {
			cascade = a.selectorFactory(matches)
		} else {
			cascade = a.selectorFactoryForOrder(effectiveOrder, matches)
		}
		if cascade == nil {
			printCandidates(out, all)
			fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
			return source.Candidate{}, false, errExitOne
		}
		ranked := ranking.SortBySourceOrder(matches, query, effectiveOrder, a.rankingSnapshot(matches))
		got, ok, selErr := cascade.Select(cmd.Context(), ranked, query)
		if selErr != nil {
			if errors.Is(selErr, tui.ErrCancelled) {
				// The user cancelled interactively (esc/ctrl+c/ctrl+g). This
				// is a normal, quiet outcome: no candidate list, no
				// ambiguous/selector-unavailable noise.
				return source.Candidate{}, false, errExitOne
			}
			printCandidates(out, all)
			fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
			fmt.Fprintf(errOut, "selector unavailable: %v\n", selErr)
			return source.Candidate{}, false, errExitOne
		}
		if !ok {
			printCandidates(out, all)
			fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
			return source.Candidate{}, false, errExitOne
		}
		pick = got
	}

	if pick.Meta["group"] == "true" {
		groupSources := splitNonEmpty(pick.Meta["group_sources"], ",")
		groupWorkspace, hasWorkspace := workspaceConfigForCandidate(a.Config().Workspaces, pick)
		nestedTemplate := pick.Meta["group_template"]
		if nestedTemplate == "" {
			nestedTemplate = parentTemplate
		}
		// R3-1: compute the single effective order ONCE at the group boundary
		// and thread it into both the scoped registry and nested recursion so
		// ranking, provider execution, and layout honor the same order.
		nestedOrder := effectiveGroupSourceOrder(a.Config(), groupWorkspace, hasWorkspace, groupSources)
		var nested *source.Registry
		if hasWorkspace {
			nested = a.withStartupSnapshot(source.NewScopedRegistryForWorkspaceWithOrder(a.Config(), a.Probes(), a.Driver(), groupWorkspace, nestedOrder, pick.Path))
		} else {
			nested = a.withStartupSnapshot(source.NewScopedRegistryWithOrder(a.Config(), a.Probes(), a.Driver(), groupSources, nestedOrder, pick.Path))
		}
		return a.resolveFromRegistry(cmd, nested, "", nestedTemplate, nestedOrder, out, errOut)
	}

	if parentTemplate != "" && pick.Meta["template"] == "" && pick.Meta["command"] == "" {
		if pick.Meta == nil {
			pick.Meta = map[string]string{}
		}
		pick.Meta["parent_template"] = parentTemplate
	}
	return pick, true, nil
}

func workspaceConfigForCandidate(workspaces []config.WorkspaceConfig, cand source.Candidate) (config.WorkspaceConfig, bool) {
	candPath := cand.Path
	if cand.NormalizedPath != "" {
		candPath = cand.NormalizedPath
	}
	isGroup := cand.Meta != nil && cand.Meta["group"] == "true"
	candName := cand.Label
	if cand.Meta != nil && cand.Meta["workspace_name"] != "" {
		candName = cand.Meta["workspace_name"]
	}
	candEntryID := ""
	if cand.Meta != nil {
		candEntryID = cand.Meta["entry_id"]
	}

	// Tier 1: Path matches + Type matches (if group) + (Name matches OR EntryID matches)
	for _, ws := range workspaces {
		if !workspacePathMatches(ws.Path, candPath) {
			continue
		}
		if isGroup && ws.Type != config.WorkspaceTypeGroup {
			continue
		}
		if (candName != "" && ws.Name == candName) || (candEntryID != "" && source.WorkspaceEntryIdentity(ws.Path, ws) == candEntryID) {
			return ws, true
		}
	}

	// Tier 2: Path matches + Type matches (if group)
	if isGroup {
		for _, ws := range workspaces {
			if ws.Type == config.WorkspaceTypeGroup && workspacePathMatches(ws.Path, candPath) {
				return ws, true
			}
		}
	}

	// Tier 3: Path matches + (Name matches OR EntryID matches)
	for _, ws := range workspaces {
		if !workspacePathMatches(ws.Path, candPath) {
			continue
		}
		if (candName != "" && ws.Name == candName) || (candEntryID != "" && source.WorkspaceEntryIdentity(ws.Path, ws) == candEntryID) {
			return ws, true
		}
	}

	// Tier 4: Fallback to first path match
	for _, ws := range workspaces {
		if workspacePathMatches(ws.Path, candPath) {
			return ws, true
		}
	}

	return config.WorkspaceConfig{}, false
}

func workspacePathMatches(wsPath, candPath string) bool {
	resolved := wsPath
	if expanded, err := pathutil.ExpandTilde(resolved); err == nil {
		resolved = expanded
	}
	if resolved == candPath {
		return true
	}
	if canonical, err := pathutil.Normalize(resolved); err == nil && canonical != "" {
		candNorm := candPath
		if cn, err := pathutil.Normalize(candPath); err == nil && cn != "" {
			candNorm = cn
		}
		if canonical == candNorm {
			return true
		}
	}
	return false
}

// effectiveGroupSourceOrder returns the single source order used for both
// candidate ranking and picker layout inside a group's nested picker (R3-1).
// Precedence: an explicit ws.SourceOrder when hasWorkspace && non-empty; else
// the parsed group_sources list; else cfg.General.SourceOrder. The same slice
// is handed to ranking.SortBySourceOrder and layoutFromConfig so the nested
// cascade's row order and block layout can never diverge.
func effectiveGroupSourceOrder(cfg *config.Config, ws config.WorkspaceConfig, hasWorkspace bool, groupSources []string) []string {
	if hasWorkspace && len(ws.SourceOrder) > 0 {
		return ws.SourceOrder
	}
	if len(groupSources) > 0 {
		return groupSources
	}
	return cfg.General.SourceOrder
}

// splitNonEmpty splits s on sep, dropping empty fields; an empty s yields nil.
func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// launchOutcome classifies the result of App.launch so runOpen can distinguish
// a genuinely completed launch (open/focus/attach) from a degraded path-print
// fallback (R3-2). It is package-private: no other package needs to know about
// launch completion semantics.
type launchOutcome int

const (
	// launchOutcomeNone means failure, cancellation, or an unresolved branch —
	// runOpen records zero history entries for it.
	launchOutcomeNone launchOutcome = iota
	// launchOutcomeCompleted means a real open/focus/attach happened — runOpen
	// records exactly one history entry when err is also nil.
	launchOutcomeCompleted
	// launchOutcomePathOnly means the resolved path was printed because Herdr
	// was unavailable or FocusOrCreate failed — runOpen records zero entries.
	launchOutcomePathOnly
)

// launch asks Herdr to focus/create a workspace for the candidate, applying
// the resolved template when a new workspace was created, or prints the
// resolved path when Herdr is unavailable. A candidate whose configured path
// does not exist on disk (Missing) fails clearly here instead of silently
// falling back to "/", $HOME, or cwd, and shep never creates the directory.
//
// The returned launchOutcome distinguishes a genuinely completed launch
// (Completed) from the two degraded path-print fallbacks (PathOnly) so runOpen
// records adaptive-ranking history only for real navigation (R3-2).
// Failure/cancellation branches return (None, err).
//
// target selects where the candidate opens:
//   - "workspace" (the default and historical behaviour): FocusOrCreate a
//     standalone Herdr workspace and Apply the resolved template on creation.
//   - "tab": open the candidate as a NEW TAB inside the Herdr workspace shep
//     is currently running in (currentPane), then run its command (if any) in
//     that tab's root pane. Requires a target-supported entry and a non-nil
//     currentPane.
//   - "pane": split a NEW PANE off the current one and run the command there.
//     Same requirements as "tab".
//
// tab and pane only support entries that can target the current workspace
// (source.SupportsCurrentWorkspaceTarget: command workspaces, zoxide,
// projects): they open a single new tab/pane and have no way to materialise a
// multi-tab/multi-pane template inside someone else's workspace. Already-open
// herdr workspaces, group/template entries, and plain paths surface a clear
// error instead.
func (a *App) launch(ctx context.Context, cand source.Candidate, action tui.RowAction, target string, currentPane *source.Pane, out, errOut io.Writer) (launchOutcome, error) {
	if cand.Source == config.SourceSessions {
		if target == "tab" || target == "pane" {
			if currentPane == nil {
				fmt.Fprintf(errOut, "--target=%s requires shep to be running inside a herdr workspace pane\n", target)
				return launchOutcomeNone, errExitOne
			}
			if reason := disallowTarget(cand, target); reason != "" {
				fmt.Fprintln(errOut, reason)
				return launchOutcomeNone, errExitOne
			}
		}
		return a.launchSessionAttach(ctx, cand, errOut)
	}
	if cand.Missing {
		fmt.Fprintf(errOut, "path does not exist: %s\n", displayPath(cand))
		return launchOutcomeNone, errExitOne
	}
	if target == "workspace" && cand.Meta["integration"] == "true" && strings.TrimSpace(cand.Path) == "" {
		fmt.Fprintln(errOut, "--target=workspace requires an integration row path")
		return launchOutcomeNone, errExitOne
	}

	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		fmt.Fprintln(out, displayPath(cand))
		return launchOutcomePathOnly, nil
	}

	// A synthesized tree-expand child row (RowActionFocusTab) identifies an
	// ALREADY-OPEN tab (or a pane inside one) in an ALREADY-OPEN workspace:
	// it routes straight to FocusTab and bypasses the --target switch
	// entirely (there is no "workspace"/"tab"/"pane" choice for a candidate
	// that is itself a tab/pane). A pane row reuses the exact same FocusTab
	// call as a tab row: Herdr has no "focus this exact pane" command (see
	// internal/herdr.Driver.FocusTab's own doc comment), so the safest
	// truthful action for Enter on a pane is focusing its containing tab —
	// launchChildTab only reads Meta["tab_id"], which every synthesized pane
	// candidate carries alongside its own pane_id (see internal/tui/tree.go's
	// synthesizeWorkspaceChildren). Checked before the switch below so it can
	// never fall through to launchWorkspace/launchInCurrentWorkspace. The
	// decision is the TUI-owned typed RowAction, not the candidate's Source
	// string.
	if action == tui.RowActionFocusTab {
		return a.launchChildTab(ctx, driver, cand, errOut)
	}

	switch target {
	case "tab", "pane":
		return a.launchInCurrentWorkspace(ctx, driver, cand, target, currentPane, errOut)
	default:
		// "workspace" (and any unexpected value, which validateTarget already
		// guards at the cobra layer): the historical FocusOrCreate + Apply path.
		return a.launchWorkspace(ctx, driver, cand, out, errOut)
	}
}

// launchSessionAttach runs the session CLI after the picker has restored the
// terminal. Session candidates are daemon identities, not paths, so this
// dispatch intentionally precedes the generic Missing and driver fallbacks.
// It returns launchOutcomeCompleted on a successful attach (R3-2) so runOpen
// records the navigation; failure/cancellation return (None, err).
func (a *App) launchSessionAttach(ctx context.Context, cand source.Candidate, errOut io.Writer) (launchOutcome, error) {
	name := cand.Meta["session_name"]
	if name == "" {
		fmt.Fprintln(errOut, "warning: herdr session attach: missing session name")
		return launchOutcomeNone, errExitOne
	}
	attach := a.sessionAttach
	if attach == nil {
		attach = runSessionAttach
	}
	if err := attach(ctx, a.Config().HerdrBinary(), name, stripHerdrEnv(os.Environ())); err != nil {
		fmt.Fprintf(errOut, "warning: herdr session attach failed: %v\n", err)
		return launchOutcomeNone, errExitOne
	}
	return launchOutcomeCompleted, nil
}

// runSessionAttach invokes the only foreground child in the sessions flow.
// argv is fixed, stdio is inherited, and Run waits for the attach client to
// finish before shep exits.
func runSessionAttach(ctx context.Context, binary, name string, env []string) error {
	cmd := exec.CommandContext(ctx, binary, "session", "attach", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return cmd.Run()
}

// stripHerdrEnv removes every Herdr context variable from a child environment
// while preserving unrelated entries and their original order exactly.
func stripHerdrEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "HERDR_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// launchChildTab routes Enter on a RowActionFocusTab row (a synthesized
// tree-expand tab or pane child) to driver.FocusTab, never touching
// FocusOrCreate: the child row already identifies an open tab in an open
// workspace, so there is nothing to focus-or-create at the workspace level.
// There is no rollback on failure — unlike launchInCurrentWorkspace's
// CreateTab/SplitPane, no resource is created here, so a warning plus
// errExitOne is the complete failure contract. Returns launchOutcomeCompleted
// on success (R3-2) so runOpen records the focused-tab navigation.
func (a *App) launchChildTab(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, errOut io.Writer) (launchOutcome, error) {
	tabID := cand.Meta["tab_id"]
	if tabID == "" {
		fmt.Fprintln(errOut, "warning: herdr tab focus: missing tab id")
		return launchOutcomeNone, errExitOne
	}
	if err := driver.FocusTab(ctx, tabID); err != nil {
		fmt.Fprintf(errOut, "warning: herdr tab focus failed: %v\n", err)
		return launchOutcomeNone, errExitOne
	}
	return launchOutcomeCompleted, nil
}

func (a *App) workspaceLaunchRequest(cand source.Candidate) (source.WorkspaceLaunchRequest, error) {
	cfg := a.Config()
	if cand.Source == config.SourceHerdr {
		return source.WorkspaceLaunchRequest{Candidate: cand}, nil
	}
	if cand.Source == config.SourceWorkspaces || cand.Meta["integration"] == "true" {
		return source.WorkspaceLaunchRequest{Candidate: cand, WorkspaceName: workspacename.Name(cand.Label)}, nil
	}
	normalized := cand.NormalizedPath
	if normalized == "" {
		var err error
		normalized, err = resolver.Normalize(cand.Path)
		if err != nil {
			return source.WorkspaceLaunchRequest{}, fmt.Errorf("workspace name: normalize path: %w", err)
		}
	}
	format := ""
	if wildcard, ok := config.FirstMatchingWildcard(cfg.Wildcards, normalized); ok {
		format = wildcard.WorkspaceName
	}
	if format == "" {
		format = cfg.General.WorkspaceName
	}
	context := workspacename.NewContext(cand.Path, normalized, cand.Label, cand.Source, cand.Meta)
	if format == "" && !context.IsWorktree {
		return source.WorkspaceLaunchRequest{Candidate: cand, WorkspaceName: workspacename.Name(normalized)}, nil
	}
	name, err := workspacename.Render("workspace name", format, context)
	if err != nil {
		return source.WorkspaceLaunchRequest{}, err
	}
	return source.WorkspaceLaunchRequest{Candidate: cand, WorkspaceName: name}, nil
}

// launchWorkspace is the historical "workspace" target: focus-or-create a
// standalone Herdr workspace and apply the resolved template on creation. It
// is the pre-target behaviour, factored out so the tab/pane branch reads at
// the same level. Returns launchOutcomeCompleted on a successful
// focus/create (R3-2); the FocusOrCreate-error path-print fallback returns
// (PathOnly, nil) so runOpen does not record it as a completed navigation.
func (a *App) launchWorkspace(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, out, errOut io.Writer) (launchOutcome, error) {
	request, err := a.workspaceLaunchRequest(cand)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return launchOutcomeNone, errExitOne
	}
	res, err := driver.FocusOrCreate(ctx, request)
	if err != nil {
		fmt.Fprintf(errOut, "warning: herdr unavailable: %v\n", err)
		fmt.Fprintln(out, displayPath(cand))
		return launchOutcomePathOnly, nil
	}

	if res.Action == source.HerdrActionCreated {
		tpl := resolveTemplate(cand, a.Config())
		target := templates.Target{
			WorkspaceID: res.WorkspaceID,
			RootTabID:   res.RootTabID,
			CWD:         cand.Path,
			SocketPath:  currentHerdrSocketPath(),
			Shell:       os.Getenv("SHELL"),
			PathEnv:     os.Getenv("PATH"),
		}
		if applyErr := templates.Apply(ctx, a.LayoutApplier(), target, tpl); applyErr != nil {
			fmt.Fprintf(errOut, "warning: template failed: %v\n", applyErr)
			return launchOutcomeNone, applyErr
		}
	}
	return launchOutcomeCompleted, nil
}

// launchInCurrentWorkspace realises the "tab" and "pane" targets: open the
// candidate inside a new tab or a new pane of the Herdr workspace shep is
// currently running in (currentPane), instead of creating a brand-new
// standalone workspace. Only entries that support a current-workspace target
// (source.SupportsCurrentWorkspaceTarget — command workspaces, zoxide,
// projects) reach here; already-open herdr workspaces, group/template
// entries, and plain paths surface a clear error via disallowTarget.
//
// The command (if any) is typed into the freshly created container pane via
// templates.RunCommand, which owns the close_on_exit shell-chaining wrap.
// This path deliberately does NOT use layout.apply: the container lives
// inside a workspace the user is already using, and applying a layout there
// would replace the surrounding tab rather than fill the new pane. A
// command-less candidate (zoxide/projects) leaves a plain shell — no RunPane
// call at all. Returns launchOutcomeCompleted on success (R3-2) so runOpen
// records the tab/pane navigation; failures return (None, errExitOne).
func (a *App) launchInCurrentWorkspace(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, target string, currentPane *source.Pane, errOut io.Writer) (launchOutcome, error) {
	if currentPane == nil {
		fmt.Fprintf(errOut, "--target=%s requires shep to be running inside a herdr workspace pane\n", target)
		return launchOutcomeNone, errExitOne
	}
	if reason := disallowTarget(cand, target); reason != "" {
		fmt.Fprintln(errOut, reason)
		return launchOutcomeNone, errExitOne
	}

	cmd := cand.Meta["command"]
	closeOnExit := cand.Meta["close_on_exit"] == "true"
	tpl := config.TemplateConfig{Command: cmd, CloseOnExit: closeOnExit}
	binary := a.Config().HerdrBinary()
	cwd := currentPane.CWD

	var containerPaneID string
	switch target {
	case "tab":
		_, pane, err := driver.CreateTab(ctx, currentPane.WorkspaceID, cwd, cand.Label, true)
		if err != nil {
			fmt.Fprintf(errOut, "warning: herdr tab create failed: %v\n", err)
			return launchOutcomeNone, errExitOne
		}
		containerPaneID = pane.ID
	case "pane":
		pane, err := driver.SplitPane(ctx, currentPane.ID, "right", 0.5, cwd, true)
		if err != nil {
			fmt.Fprintf(errOut, "warning: herdr pane split failed: %v\n", err)
			return launchOutcomeNone, errExitOne
		}
		containerPaneID = pane.ID
	default:
		// Unreachable: launch only routes "tab"/"pane" here. Defensive guard.
		fmt.Fprintf(errOut, "--target=%s is not supported inside the current workspace\n", target)
		return launchOutcomeNone, errExitOne
	}

	if applyErr := templates.RunCommand(ctx, driver, containerPaneID, binary, tpl); applyErr != nil {
		fmt.Fprintf(errOut, "warning: launch failed: %v\n", applyErr)
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = driver.RunPane(rollbackCtx, containerPaneID, templates.ShellCommand(binary, "pane", "close", containerPaneID))
		cancel()
		return launchOutcomeNone, errExitOne
	}

	return launchOutcomeCompleted, nil
}

// disallowTarget returns a non-empty user-facing error string when the
// candidate cannot be opened via the tab/pane targets, or "" when it is
// allowed. The allowed set is exactly source.SupportsCurrentWorkspaceTarget
// (command workspaces, zoxide, projects); each disallowed shape gets a
// distinct, specific message so the user knows exactly what to fix: an
// already-open herdr workspace (resume it via --target=workspace instead), a
// template entry, a group workspace, or an entry with no command (and no
// zoxide/projects path to fall back on).
func disallowTarget(cand source.Candidate, target string) string {
	if source.SupportsCurrentWorkspaceTarget(cand) {
		return ""
	}
	name := cand.Label
	if name == "" {
		name = displayPath(cand)
	}
	switch {
	case cand.Source == config.SourceHerdr:
		return fmt.Sprintf("--target=%s cannot open workspace %s: it is already open (use --target=workspace to focus it)", target, cand.Meta["workspace_id"])
	case cand.Meta["template"] != "":
		return fmt.Sprintf("--target=%s only supports command-only entries; entry %q uses a template", target, name)
	case cand.Meta["group"] == "true":
		return fmt.Sprintf("--target=%s requires an entry with a command, got group workspace %q", target, name)
	default:
		return fmt.Sprintf("--target=%s requires an entry with a command (or a zoxide/projects path), entry %q has no command", target, name)
	}
}

// candidateFromPath builds a candidate for the --path flag (and the bare "."
// query) and normalises it so Dedup has a canonical key to compare against
// other candidates' paths. The path is stat'd here so a non-existent target
// is marked Missing exactly like a configured workspace whose path vanished:
// launch() then fails clearly for that selection instead of silently
// printing the path (a false success) or falling back to a herdr warning.
func candidateFromPath(p string) (source.Candidate, error) {
	if strings.TrimSpace(p) == "" {
		return source.Candidate{}, fmt.Errorf("empty --path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return source.Candidate{}, fmt.Errorf("absolutize path: %w", err)
	}
	np, err := resolver.Normalize(abs)
	if err != nil {
		return source.Candidate{}, fmt.Errorf("normalize path: %w", err)
	}
	cand := source.Candidate{
		Path:           abs,
		NormalizedPath: np,
		Label:          baseLabelOpen(abs),
		Source:         "path",
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		cand.Missing = true
	}
	return cand, nil
}

// resolveTemplate resolves the template applied to a freshly created
// workspace, per the documented precedence:
//  1. exact [[workspaces]] entry with explicit template (Meta["template"])
//  2. exact [[workspaces]] entry with explicit command (Meta["command"])
//  3. first matching [[wildcards]] entry's template
//  4. template inherited from parent group picker (Meta["parent_template"])
//  5. [defaults].template
//
// An unresolved name (should not happen post-validation) or no match at any
// tier yields an empty TemplateConfig{} (a plain shell), never a crash.
func resolveTemplate(cand source.Candidate, cfg *config.Config) config.TemplateConfig {
	if cfg == nil {
		return config.TemplateConfig{}
	}
	if name := cand.Meta["template"]; name != "" {
		if t, ok := cfg.Templates[name]; ok {
			return t
		}
	}
	if cmd := cand.Meta["command"]; cmd != "" {
		tpl := config.TemplateConfig{Command: cmd}
		// Forward close_on_exit from the workspace Meta into the synthetic
		// template so the simple-Command Apply branch honors it. The only
		// writer (workspacesProvider.List) emits the literal "true", so this
		// is a strict equality contract — not strconv.ParseBool — to keep a
		// future provider from silently flipping close-on-exit on via "1"/"T".
		tpl.CloseOnExit = cand.Meta["close_on_exit"] == "true"
		return tpl
	}
	if name := matchWildcardTemplate(cand, cfg); name != "" {
		if t, ok := cfg.Templates[name]; ok {
			return t
		}
	}
	if name := cand.Meta["parent_template"]; name != "" {
		if t, ok := cfg.Templates[name]; ok {
			return t
		}
	}
	if cfg.Defaults.Template != "" {
		if t, ok := cfg.Templates[cfg.Defaults.Template]; ok {
			return t
		}
	}
	return config.TemplateConfig{}
}

// matchWildcardTemplate returns the template name for the first
// [[wildcards]] entry whose pattern matches the candidate's normalised path
// or base name, scanned in declaration order. A nil/empty config, no match,
// or a match whose own template is unset all yield "" so the caller falls
// through to the next precedence tier.
func matchWildcardTemplate(cand source.Candidate, cfg *config.Config) string {
	if cfg == nil || len(cfg.Wildcards) == 0 {
		return ""
	}
	np := cand.NormalizedPath
	if np == "" {
		np = cand.Path
	}
	base := filepath.Base(np)
	for _, w := range cfg.Wildcards {
		if config.MatchWildcard(w.Pattern, np) || config.MatchWildcard(w.Pattern, base) {
			return w.Template
		}
	}
	return ""
}

// printCandidates writes the candidate list to stdout so the user can see what
// was ambiguous. Uses the same tsv-ish shape as `shep list` for parity.
// Candidates whose configured path is missing are marked so an ambiguous
// list never hides a broken entry.
func printCandidates(out io.Writer, cands []source.Candidate) {
	for _, c := range cands {
		suffix := ""
		if c.Missing {
			suffix = " (missing)"
		}
		fmt.Fprintf(out, "%s\t%s%s\n", displayPath(c), c.Label, suffix)
	}
}

// displayPath prefers the normalised path (post-dedup, post-symlink) and falls
// back to the raw path so output is never empty.
func displayPath(c source.Candidate) string {
	if c.NormalizedPath != "" {
		return c.NormalizedPath
	}
	return c.Path
}

// baseLabelOpen mirrors source's baseLabel without an import cycle; open
// constructs candidates directly from --path.
func baseLabelOpen(p string) string {
	base := filepath.Base(p)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return p
	}
	return base
}

// errExitOne is a sentinel returned purely to drive exit code 1 from main.
// It is never printed (cobra Silences errors); the user-facing reason was
// already written to stderr by the caller.
var errExitOne = fmt.Errorf("exit 1")
