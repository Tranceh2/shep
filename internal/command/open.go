package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/effective"
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
	var viewFlag string
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
projects (already-open herdr workspaces, templates, and groups are rejected).

--view selects all, agents, a built-in or custom source, or a group id. It
always opens the built-in picker, even for a single match, ignoring fzf and
exact-match selection. A query filters only that view. Hidden views become a
temporary active tab alongside the configured tabs; they do not join all.`,
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
			return a.runOpenWithView(cmd, query, pathFlag, targetFlag, viewFlag)
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"open the given absolute path directly, bypassing query resolution (used by the Television cable)")
	cmd.Flags().StringVar(&targetFlag, "target", "workspace",
		"where to open an entry: workspace (default), tab, or pane")
	cmd.Flags().StringVar(&viewFlag, "view", "",
		"open a view in the built-in picker (all, agents, source name, or group id)")
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
// (queried once by runOpenWithView before candidate resolution) and a.setChosenTarget
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
		layout := a.pickerLayout(cfg.General.SourceOrder, matches)
		layout.RankingSnapshot = a.rankingSnapshot(matches)
		layout.StatusDialer = a.resolveStatusDialer()
		layout.PinToggler = a.pinToggler()
		layout.Closer = a.herdrCloser()
		layout.AckClearer = a.ackClearer()
		return snapshotCascadeFor(cfg.General.Selector, a.buildPreviewRendererForSnapshot(*a.startupSnapshot), a.currentPane, a.setChosenTarget, a.setChosenAction, *a.startupSnapshot, a.Driver(), a.buildPreviewRendererForSnapshot, matches, layout)
	}
	layout := a.pickerLayout(cfg.General.SourceOrder, matches)
	layout.RankingSnapshot = a.rankingSnapshot(matches)
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	layout.Closer = a.herdrCloser()
	layout.AckClearer = a.ackClearer()
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), a.currentPane, a.setChosenTarget, a.setChosenAction, layout)
}

func (a *App) rankingSnapshot(_ []source.Candidate) ranking.Snapshot {
	a.rankingMu.Lock()
	defer a.rankingMu.Unlock()
	return a.rankingData
}

// herdrCloser shares the active driver with snapshot collection. A missing
// Herdr binary leaves the TUI action unavailable rather than attempting it.
func (a *App) herdrCloser() tui.Closer {
	if !a.Probes().Herdr {
		return nil
	}
	driver, ok := a.Driver().(interface {
		ClosePane(context.Context, string) error
		CloseTab(context.Context, string) error
		CloseWorkspace(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context, kind, id string) tui.CloseResultMsg {
		var err error
		switch kind {
		case "pane":
			err = driver.ClosePane(ctx, id)
		case "tab":
			err = driver.CloseTab(ctx, id)
		case "workspace":
			err = driver.CloseWorkspace(ctx, id)
		default:
			err = fmt.Errorf("not an open Herdr item: %s", kind)
		}
		return tui.CloseResultMsg{Kind: kind, ID: id, Err: err}
	}
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

func (a *App) ackClearer() tui.AckClearer {
	return func(ctx context.Context, paneID string) {
		if err := a.openRankingForPins(ctx); err != nil {
			return
		}
		a.rankingMu.Lock()
		store := a.rankingStore
		a.rankingMu.Unlock()
		if store == nil {
			return
		}
		_ = store.ClearAcknowledgement(ctx, paneID)
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

// openRankingInBackground runs openRankingForPins in its own goroutine, so the
// picker's first frame never waits on the SQLite store or on Herdr's focus
// history. Every consumer that needs the store (pins, acknowledgements, the
// ranking producer) calls openRankingForPins, which waits for this open; the
// returned func waits for it too and reports its error, any number of times.
func (a *App) openRankingInBackground(ctx context.Context) func() error {
	done := make(chan error, 1)
	go func() { done <- a.openRankingForPins(ctx) }()
	return sync.OnceValue(func() error { return <-done })
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
		layout := a.pickerLayout(order, matches)
		layout.RankingSnapshot = a.rankingSnapshot(matches)
		layout.StatusDialer = a.resolveStatusDialer()
		layout.PinToggler = a.pinToggler()
		layout.Closer = a.herdrCloser()
		layout.AckClearer = a.ackClearer()
		return snapshotCascadeFor(cfg.General.Selector, a.buildPreviewRendererForSnapshot(*a.startupSnapshot), a.currentPane, a.setChosenTarget, a.setChosenAction, *a.startupSnapshot, a.Driver(), a.buildPreviewRendererForSnapshot, matches, layout)
	}
	layout := a.pickerLayout(order, matches)
	layout.RankingSnapshot = a.rankingSnapshot(matches)
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	layout.Closer = a.herdrCloser()
	layout.AckClearer = a.ackClearer()
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), a.currentPane, a.setChosenTarget, a.setChosenAction, layout)
}

// treeActiveFor reports whether the picker's tab and pane children replace
// fzf for this resolution pass: active only when there is more than one
// candidate to choose from AND at least one is an already-open Herdr
// workspace. Fzf cannot render synthesized child rows, so when this is true
// the snapshot cascade skips it (see snapshotCascadeFor).
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
// loaded config: the [tui] pane widths, orientation and icon tier, the
// configured tabs, order (the source order the picker groups rows in — the
// same order source.Registry.Enabled() collects candidates in) and the
// resolved row presentations. This is the user's configured DEFAULT layout
// for the session — the live ctrl+l keybinding may flip the orientation
// in-memory afterwards without ever writing back to cfg. The theme is
// selected separately, once per process (see App.selectedTheme).
func layoutFromConfig(cfg *config.Config, order []string) tui.Layout {
	t := cfg.TUI
	presentation := cfg.Presentations()
	layout := tui.Layout{
		ListWidth:    t.ListWidth,
		PreviewWidth: t.PreviewWidth,
		Orientation:  t.Layout,
		SourceOrder:  order,
		Icons:        t.Icons,
		Presentation: &presentation,
	}
	for _, id := range t.Tabs {
		tab := tui.TabDefinition{ID: id}
		switch id {
		case "all":
			tab.Kind = tui.TabAll
		case "agents":
			tab.Kind = tui.TabAgents
		default:
			tab.Kind = tui.TabSource
			for _, customSource := range cfg.Sources.Custom {
				if customSource.Name == id {
					tab.Kind = tui.TabCustomSource
					break
				}
			}
		}
		layout.Tabs = append(layout.Tabs, tab)
	}
	return layout
}

// pickerLayout shares configured tab definitions and scoped loaders between
// streaming startup and the synchronous ambiguous-query picker. Tab-only
// providers are loaded on activation, never added to general.source_order.
func (a *App) pickerLayout(order []string, matches []source.Candidate) tui.Layout {
	return a.pickerLayoutForConfig(a.Config(), order, matches)
}

func (a *App) pickerLayoutForConfig(cfg *config.Config, order []string, matches []source.Candidate) tui.Layout {
	layout := layoutFromConfig(cfg, order)
	layout.ConfirmClose = append([]string(nil), cfg.TUI.ConfirmClose...)
	layout.Templates = a.templateEngine()
	layout.Theme, layout.LightTheme = a.selectedTheme(cfg)
	settings := a.settings()
	layout.IconColors = settings.IconColors()
	layout.Resolve = settings.Attach
	registry := a.withStartupSnapshot(source.NewRegistry(cfg, a.Probes(), a.Driver()))
	providers := make(map[string]source.Provider)
	for _, provider := range registry.Providers() {
		providers[provider.Name()] = provider
	}
	for i := range layout.Tabs {
		tab := &layout.Tabs[i]
		for _, ws := range cfg.Workspaces {
			if ws.ID != tab.ID || ws.Type != config.WorkspaceTypeGroup {
				continue
			}
			tab.Kind = tui.TabGroup
			tab.Label = ws.Name
			groupOrder := effectiveGroupSourceOrder(cfg, ws, true, nil)
			tab.SourceOrder = groupOrder
			workspace := ws
			tab.Load = func(ctx context.Context, snapshot *source.Snapshot) ([]source.Candidate, error) {
				root, err := pathutil.ExpandTilde(workspace.Path)
				if err != nil {
					return nil, err
				}
				scoped := source.NewScopedRegistryForWorkspaceWithOrder(cfg, a.Probes(), a.Driver(), workspace, groupOrder, root)
				if snapshot != nil {
					scoped.WithHerdrSnapshot(*snapshot)
				} else if a.startupSnapshotAttempted {
					scoped.DisableHerdr()
				}
				candidates, err := scoped.Collect(ctx)
				if workspace.Template != "" {
					for i := range candidates {
						if candidates[i].Meta == nil {
							candidates[i].Meta = make(map[string]string)
						}
						if candidates[i].Meta["template"] == "" && candidates[i].Meta["command"] == "" {
							candidates[i].Meta["parent_template"] = workspace.Template
						}
					}
				}
				settings.Attach(candidates)
				return candidates, err
			}
			break
		}
		if matches == nil || tab.Kind == tui.TabGroup || tab.Kind == tui.TabAll || tab.Kind == tui.TabAgents {
			continue
		}
		// Already collected providers reuse their resolver matches. Only
		// tab-only providers need a separate, lazy collection.
		if slices.Contains(order, tab.ID) {
			continue
		}
		provider := providers[tab.ID]
		if provider == nil {
			continue
		}
		if tab.ID == config.SourceHerdr {
			tab.SourceOrder = []string{config.SourceHerdr}
		}
		tab.Load = func(ctx context.Context, snapshot *source.Snapshot) ([]source.Candidate, error) {
			var rows []source.Candidate
			var err error
			if provider.Name() == config.SourceHerdr && snapshot != nil {
				rows = source.HerdrCandidates(*snapshot)
			} else {
				rows, err = provider.List(ctx)
			}
			out := make([]source.Candidate, 0, len(rows))
			for _, row := range rows {
				out = append(out, row.Clone())
			}
			settings.Attach(out)
			return out, err
		}
	}
	return layout
}

// buildPreviewRenderer wires the production preview.Renderer from the settings
// resolver and binary probes so the TUI's preview pane and the `shep preview`
// command share identical rendering behaviour. A CommandRunner is always
// constructed (it powers both the "dir" built-in and any declared
// preview.commands); the active Herdr driver (if any) supplies only live pane
// reads. Workspace and agent-status state require a startup snapshot.
func (a *App) buildPreviewRenderer() preview.Renderer {
	if a.startupSnapshot != nil {
		return a.buildPreviewRendererForSnapshot(*a.startupSnapshot)
	}
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	runner := preview.NewCommandRunner()
	var opts []preview.RendererOption
	if driver := a.Driver(); driver != nil {
		opts = append(opts, preview.WithPaneReader(driver))
	}
	return preview.NewRenderer(a.settings(), a.templateEngine(), a.Probes(), git, runner, opts...)
}

func (a *App) buildPreviewRendererForSnapshot(snapshot source.Snapshot) preview.Renderer {
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	runner := preview.NewCommandRunner()
	opts := []preview.RendererOption{preview.WithSnapshot(snapshot)}
	if driver := a.Driver(); driver != nil {
		opts = append(opts, preview.WithPaneReader(driver))
	}
	return preview.NewRenderer(a.settings(), a.templateEngine(), a.Probes(), git, runner, opts...)
}

// cascadeFor builds the selector cascade for a [general].selector value when
// no Herdr state was captured (see snapshotCascadeFor for the picker that has
// it). builtin skips fzf and uses the Bubble Tea TUI; fzf and auto include fzf
// (Fzf.Select no-ops when the binary is absent, so both fall back to the TUI).
// Direct is always first so exact / single matches short-circuit.
// currentPane, onTarget and onAction are threaded into the TUI selector (see
// newTUISelector); layout configures the picker.
func cascadeFor(sel string, renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), layout tui.Layout) *selector.Cascade {
	direct := selector.Direct{}
	tuiSel := newTUISelector(renderer, currentPane, onTarget, onAction, layout)
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
type tuiRunFunc func(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, currentPane *source.Pane, layout tui.Layout) (source.Candidate, tui.RowAction, string, bool, error)

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
	// runOpenWithView instead reads it back via App.chosenTarget, set through
	// this callback (App.setChosenTarget).
	onTarget func(string)
	// onAction receives the typed RowAction of the picked row once Select
	// returns a successful pick — same out-of-band pattern as onTarget, for
	// the same reason (Select's signature is fixed). runOpenWithView reads it
	// back via App.chosenAction (App.setChosenAction) and passes it to
	// launch, which dispatches on the typed action instead of the
	// candidate's Source string.
	onAction func(tui.RowAction)
	// run defaults to tui.Run; tests substitute a fake to simulate a
	// ctrl+t/ctrl+p pick without driving a real Bubble Tea program.
	run tuiRunFunc
}

// newTUISelector builds a tuiSelector carrying the given Renderer (nil is
// valid in tests and degrades to the picker's built-in candidate summary),
// the Herdr pane shep is currently running inside (nil when not running
// inside one), callbacks receiving the chosen target and typed RowAction
// after a successful pick, and the picker Layout.
func newTUISelector(renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), layout tui.Layout) *tuiSelector {
	return &tuiSelector{renderer: renderer, layout: layout, currentPane: currentPane, onTarget: onTarget, onAction: onAction, run: tui.Run}
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
	onTarget            func(string)
	onAction            func(tui.RowAction)
}

func (s snapshotTUISelector) Name() string { return s.name }

func (s snapshotTUISelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	cand, action, target, ok, err := tui.RunWithSnapshot(ctx, candidates, query, s.renderer, s.snapshot, s.driver, s.rendererForSnapshot, s.currentPane, s.layout)
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

func snapshotCascadeFor(sel string, renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), onAction func(tui.RowAction), snapshot source.Snapshot, driver source.HerdrDriver, rendererForSnapshot tui.SnapshotRendererFactory, matches []source.Candidate, layout tui.Layout) *selector.Cascade {
	direct := selector.Direct{}
	picker := snapshotTUISelector{
		name:                "tui",
		renderer:            renderer,
		layout:              layout,
		currentPane:         currentPane,
		snapshot:            snapshot,
		driver:              driver,
		rendererForSnapshot: rendererForSnapshot,
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

// buildProviderProducer streams one provider's candidates, resolved (see
// App.settings) in the producer's own goroutine.
func (a *App) buildProviderProducer(p source.Provider) tui.SourceProducer {
	settings := a.settings()
	return func(ctx context.Context) tui.SourceResultMsg {
		raw, err := p.List(ctx)
		cands := make([]source.Candidate, 0, len(raw))
		for _, c := range raw {
			cands = append(cands, c.Clone())
		}
		settings.Attach(cands)
		return tui.SourceResultMsg{
			Source:          p.Name(),
			Candidates:      cands,
			NormalizedPaths: resolver.NormalizedPaths(cands),
			Err:             err,
		}
	}
}

// snapshotProducerOptions selects which candidate families the single shared
// Herdr snapshot generation feeds. Herdr workspaces, sessions, and agent panes
// all derive from one state generation, so whichever families
// general.source_order enables stream through one producer: the daemon is
// asked for the state exactly once per startup and the picker never mixes two
// generations.
type snapshotProducerOptions struct {
	herdr    bool
	sessions bool
	agents   bool
}

// buildSnapshotProducer streams the families of the shared Herdr generation,
// resolved (see App.settings) in the producer's own goroutine, together with
// the presentations of the generation's agent panes for the agents view.
func (a *App) buildSnapshotProducer(opts snapshotProducerOptions) tui.SourceProducer {
	driver := a.Driver()
	settings := a.settings()
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

		var cands []source.Candidate
		var snapshotSources []string

		if opts.herdr {
			cands = append(cands, source.HerdrCandidates(snapshot)...)
			snapshotSources = append(snapshotSources, config.SourceHerdr)
		}

		if opts.sessions {
			sessCtx, sCancel := context.WithTimeout(ctx, source.SessionsListTimeout)
			sessions, sErr := driver.ListSessions(sessCtx)
			sCancel()
			if sErr == nil {
				cands = append(cands, source.SessionCandidates(sessions, os.Getenv)...)
			}
		}
		settings.Attach(cands)

		// The agents view draws the generation's agent panes whether or not
		// the agents source is enabled.
		agents := source.AgentCandidates(snapshot)
		settings.Attach(agents)
		if opts.agents {
			// Agent rows keep their own source identity so the model files
			// them under SourceAgents even though they arrive on this shared
			// message.
			cands = append(cands, agents...)
			snapshotSources = append(snapshotSources, config.SourceAgents)
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
			AgentPresentations:  tui.AgentPresentations(agents),
			SnapshotSources:     snapshotSources,
			NormalizedPaths:     resolver.NormalizedPaths(cands),
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

func (a *App) streamingProducersForView(cmdCtx context.Context, view string) []tui.SourceProducer {
	cfg := a.Config()
	probes := a.Probes()
	registry := source.NewRegistry(cfg, probes, a.Driver())

	var producers []tui.SourceProducer

	// herdr workspaces, sessions, and agent panes all derive from one Herdr
	// state generation, so they share a single snapshot producer (see
	// App.buildSnapshotProducer) instead of issuing a Snapshot per family.
	var includeHerdr, includeAgents bool
	var standaloneSessions source.Provider
	enabled := registry.Enabled()
	requested := make(map[string]bool)
	if view != "" {
		if view == "agents" {
			requested[config.SourceAgents] = true
		} else if kind, _ := config.ResolveView(cfg, view); kind == "group" {
			for _, ws := range cfg.Workspaces {
				if ws.ID == view {
					for _, name := range effectiveGroupSourceOrder(cfg, ws, true, nil) {
						if name == config.SourceHerdr || name == config.SourceAgents {
							requested[config.SourceHerdr] = true
						}
					}
					break
				}
			}
		} else {
			requested[view] = true
		}
	} else {
		for _, tab := range cfg.TUI.Tabs {
			if tab == "all" {
				continue
			}
			isGroup := false
			for _, ws := range cfg.Workspaces {
				if ws.ID == tab && ws.Type == config.WorkspaceTypeGroup {
					isGroup = true
					order := effectiveGroupSourceOrder(cfg, ws, true, nil)
					for _, name := range order {
						if name == config.SourceHerdr || name == config.SourceAgents {
							requested[config.SourceHerdr] = true
							break
						}
					}
					break
				}
			}
			if !isGroup {
				requested[tab] = true
			}
		}
	}
	if view != "" && view != "all" {
		enabled = nil
	}
	for _, p := range registry.Providers() {
		if !requested[p.Name()] {
			continue
		}
		found := false
		for _, active := range enabled {
			if active.Name() == p.Name() {
				found = true
				break
			}
		}
		if !found {
			enabled = append(enabled, p)
		}
	}
	for _, p := range enabled {
		switch p.Name() {
		case config.SourceHerdr:
			includeHerdr = true
		case config.SourceSessions:
			standaloneSessions = p
		case config.SourceAgents:
			includeAgents = true
		default:
			// workspaces, zoxide, projects, and every declared custom source
			// share the same generic producer builder (see
			// App.buildProviderProducer): they all just call p.List(ctx) and
			// stream the result through tui.SourceResultMsg.Err on failure.
			producers = append(producers, a.buildProviderProducer(p))
		}
	}

	if includeHerdr || includeAgents {
		producers = append(producers, a.buildSnapshotProducer(
			snapshotProducerOptions{
				herdr: includeHerdr,
				// sessions ride the snapshot generation only alongside herdr
				// workspaces (the pre-agents contract); without herdr they
				// keep their standalone producer.
				sessions: includeHerdr && standaloneSessions != nil,
				agents:   includeAgents,
			},
		))
	}
	if standaloneSessions != nil && !includeHerdr {
		producers = append(producers, a.buildProviderProducer(standaloneSessions))
	}

	if cfg.Ranking.Enabled {
		producers = append(producers, a.buildRankingProducer())
	}

	return producers
}

// runOpenWithView is the open pipeline. target is the resolved --target value
// ("workspace", "tab", or "pane"); for the interactive TUI path, the model can
// override it via App.chosenTarget. view is the --view id ("" for none).
func (a *App) runOpenWithView(cmd *cobra.Command, query, pathFlag, targetFlag, view string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	a.chosenTarget = ""
	a.chosenAction = tui.RowActionOpen
	a.rankingMu.Lock()
	a.rankingStore = nil
	a.rankingData = ranking.Snapshot{}
	a.rankingMu.Unlock()

	// waitRanking waits for a background ranking store open (see
	// openRankingInBackground); the store is closed only after it settles.
	waitRanking := func() error { return nil }
	defer func() {
		_ = waitRanking()
		a.rankingMu.Lock()
		store := a.rankingStore
		a.rankingStore = nil
		a.rankingMu.Unlock()
		if store != nil {
			_ = store.Close()
		}
	}()

	cfg := a.Config()
	if cmd.Flags().Changed("view") && view == "" {
		return fmt.Errorf("--view requires a non-empty id")
	}
	if view != "" {
		if cmd.Flags().Changed("path") || query == "." {
			return fmt.Errorf("--view conflicts with --path or query '.'")
		}
		if _, err := config.ResolveView(cfg, view); err != nil {
			return err
		}
	}
	// Use synchronous resolution when direct path, '.', test overrides cascade,
	// when fzf is explicitly configured, or when a CLI query was supplied.
	if view == "" && (cmd.Flags().Changed("path") || query == "." || a.selectorBuilder != nil || cfg.General.Selector == config.SelectorFzf || query != "") {
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
		if outcome == launchOutcomeCompleted && rankingReady {
			if cfg.Ranking.Enabled {
				a.recordRankingSuccess(cand)
			}
			a.recordAcknowledgement(cand, a.chosenAction)
		}
		return nil
	}

	// Interactive startup with no query: enter Bubble Tea immediately with streaming producers!
	layout := a.pickerLayout(cfg.General.SourceOrder, nil)
	if view != "" {
		found := false
		for _, tab := range layout.Tabs {
			if tab.ID == view {
				found = true
				break
			}
		}
		if !found {
			// Append a temporary tab so keyboard navigation never loses the view.
			cfgCopy := *cfg
			cfgCopy.TUI.Tabs = []string{view}
			viewLayout := a.pickerLayoutForConfig(&cfgCopy, cfg.General.SourceOrder, nil)
			layout.Tabs = append(layout.Tabs, viewLayout.Tabs[0])
		}
		layout.InitialTab = view
	}
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	layout.Closer = a.herdrCloser()
	layout.AckClearer = a.ackClearer()
	waitRanking = a.openRankingInBackground(cmd.Context())

	producers := a.streamingProducersForView(cmd.Context(), view)
	cand, action, chosenTarget, currentPane, ok, selErr := a.runAsyncTUI(cmd.Context(), producers, query, layout)
	if err := waitRanking(); err != nil {
		fmt.Fprintf(errOut, "warning: pin storage unavailable: %v\n", err)
	}
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

	if cand.Path != "" && cand.Source != config.SourceSessions && cand.Source != config.SourceAgents && action != tui.RowActionFocusTab {
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
		groupWorkspace, hasWorkspace := a.settings().Workspace(cand)
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
	if outcome == launchOutcomeCompleted && rankingReady {
		if cfg.Ranking.Enabled {
			a.recordRankingSuccess(cand)
		}
		a.recordAcknowledgement(cand, a.chosenAction)
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

func (a *App) recordAcknowledgement(cand source.Candidate, action tui.RowAction) {
	if action != tui.RowActionFocusTab && cand.Source != config.SourceAgents {
		return
	}
	if cand.Meta == nil {
		return
	}
	paneID := cand.Meta["pane_id"]
	status := cand.Meta["agent_status"]
	if paneID == "" || (strings.ToLower(status) != "blocked" && strings.ToLower(status) != "done") {
		return
	}
	a.rankingMu.Lock()
	store := a.rankingStore
	a.rankingMu.Unlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), rankingRecordTimeout)
	defer cancel()
	_ = store.RecordAcknowledgement(ctx, paneID, status)
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
	// The matches are what a picker draws.
	a.settings().Attach(matches)

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
		groupWorkspace, hasWorkspace := a.settings().Workspace(pick)
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

// launchOutcome classifies the result of App.launch so runOpenWithView can distinguish
// a genuinely completed launch (open/focus/attach) from a degraded path-print
// fallback (R3-2). It is package-private: no other package needs to know about
// launch completion semantics.
type launchOutcome int

const (
	// launchOutcomeNone means failure, cancellation, or an unresolved branch —
	// runOpenWithView records zero history entries for it.
	launchOutcomeNone launchOutcome = iota
	// launchOutcomeCompleted means a real open/focus/attach happened — runOpenWithView
	// records exactly one history entry when err is also nil.
	launchOutcomeCompleted
	// launchOutcomePathOnly means the resolved path was printed because Herdr
	// was unavailable or FocusOrCreate failed — runOpenWithView records zero entries.
	launchOutcomePathOnly
)

// launch asks Herdr to focus/create a workspace for the candidate, applying
// the resolved template when a new workspace was created, or prints the
// resolved path when Herdr is unavailable. A candidate whose configured path
// does not exist on disk (Missing) fails clearly here instead of silently
// falling back to "/", $HOME, or cwd, and shep never creates the directory.
//
// The returned launchOutcome distinguishes a genuinely completed launch
// (Completed) from the two degraded path-print fallbacks (PathOnly) so runOpenWithView
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
	if target == "workspace" && cand.Meta["custom_source"] == "true" && strings.TrimSpace(cand.Path) == "" {
		fmt.Fprintln(errOut, "--target=workspace requires a custom source row path")
		return launchOutcomeNone, errExitOne
	}

	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		fmt.Fprintln(out, displayPath(cand))
		return launchOutcomePathOnly, nil
	}

	// A synthesized tree-expand child row (RowActionFocusTab) or an agents source
	// selection identifies an ALREADY-OPEN tab (or a pane inside one) in an
	// ALREADY-OPEN workspace: it routes straight to FocusTab and bypasses the
	// --target switch entirely (there is no "workspace"/"tab"/"pane" choice for
	// an existing pane/tab). Herdr has no "focus this exact pane" command, so
	// focusing its containing tab is the safest truthful action.
	// launchChildTab only reads Meta["tab_id"].
	if action == tui.RowActionFocusTab || cand.Source == config.SourceAgents {
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
// It returns launchOutcomeCompleted on a successful attach (R3-2) so runOpenWithView
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
	if err := attach(ctx, config.HerdrBinaryWithEnv(a.Config(), os.LookupEnv), name, stripHerdrEnv(os.Environ())); err != nil {
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
// on success (R3-2) so runOpenWithView records the focused-tab navigation.
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

// workspaceLaunchRequest names the workspace a candidate creates: an open
// Herdr workspace is focused, not created; a configured workspace or a custom
// row is named by its own label; anything else by its resolved
// workspace_name format (see internal/effective), rendered against its data.
func (a *App) workspaceLaunchRequest(cand source.Candidate) (source.WorkspaceLaunchRequest, error) {
	if cand.Source == config.SourceHerdr {
		return source.WorkspaceLaunchRequest{Candidate: cand}, nil
	}
	if cand.Source == config.SourceWorkspaces || cand.Meta["custom_source"] == "true" {
		return source.WorkspaceLaunchRequest{Candidate: cand, WorkspaceName: workspacename.Name(cand.Label)}, nil
	}
	settings := a.settings().For(cand)
	if settings.NormalizedPath == "" {
		return source.WorkspaceLaunchRequest{}, errors.New("workspace name: the candidate has no path")
	}
	data := source.TemplateData(cand)
	data.NormalizedPath = settings.NormalizedPath
	name, err := workspacename.Render(a.templateEngine(), "workspace name", settings.WorkspaceName, data)
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
// (PathOnly, nil) so runOpenWithView does not record it as a completed navigation.
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
		tpl := resolveTemplate(a.settings().For(cand), a.Config())
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
			return launchOutcomeNone, markReported(applyErr)
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
// call at all. Returns launchOutcomeCompleted on success (R3-2) so runOpenWithView
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
	binary := config.HerdrBinaryWithEnv(a.Config(), os.LookupEnv)
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

// resolveTemplate returns what a freshly created workspace runs for its
// resolved settings (see internal/effective for the precedence): their
// command as a one-pane template honouring close_on_exit, else their named
// template, else nothing (a plain shell).
func resolveTemplate(settings effective.Settings, cfg *config.Config) config.TemplateConfig {
	if settings.Command != "" {
		return config.TemplateConfig{Command: settings.Command, CloseOnExit: settings.CloseOnExit}
	}
	if t, ok := cfg.Templates[settings.Template]; ok && settings.Template != "" {
		return t
	}
	return config.TemplateConfig{}
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

// errExitOne is a sentinel returned after a command has already written its
// user-facing diagnostic to stderr. It is wrapped with markReported so the
// root execution wrapper recognizes it as already reported and preserves the
// existing output contract; the exit code itself still comes from the
// underlying ExitCodeError.
var errExitOne = markReported(&ExitCodeError{Code: 1, Err: fmt.Errorf("exit 1")})
