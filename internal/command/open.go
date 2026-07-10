package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/templates"
	"github.com/tranceh2/shep/internal/tui"
)

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

// currentPaneTimeout bounds the best-effort CurrentPane probe runOpen issues
// before candidate resolution. Without a deadline, a hung Herdr daemon could
// block shep startup indefinitely; 2s is a short, user-imperceptible budget
// for a single local CLI round-trip.
const currentPaneTimeout = 2 * time.Second

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
func (a *App) selectorFactory() *selector.Cascade {
	if a.selectorBuilder != nil {
		return a.selectorBuilder()
	}
	cfg := a.Config()
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), a.currentPane, a.setChosenTarget, layoutFromConfig(cfg.TUI))
}

// layoutFromConfig builds the tui.Layout consumed by the picker from the
// loaded [tui] config, threading list_width/preview_width and the layout
// orientation through the same way. This is the user's configured DEFAULT
// orientation for the session — the live ctrl+l keybinding may flip it
// in-memory afterwards without ever writing back to cfg.
func layoutFromConfig(t config.TUIConfig) tui.Layout {
	return tui.Layout{
		ListWidth:    t.ListWidth,
		PreviewWidth: t.PreviewWidth,
		Orientation:  t.Layout,
	}
}

// buildPreviewRenderer wires the production preview.Renderer from the loaded
// config and binary probes so the TUI's preview pane and the `shep preview`
// command share identical rendering behaviour. A CommandRunner is always
// constructed (it powers both the "dir" built-in and any declared
// preview.commands); the active Herdr driver (if any) is threaded in via
// WithHerdrDriver so the workspace/active_pane preview sections can
// enumerate tabs/panes and read the active pane.
func (a *App) buildPreviewRenderer() preview.Renderer {
	cfg := a.Config()
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	runner := preview.NewCommandRunner()
	var opts []preview.RendererOption
	if driver := a.Driver(); driver != nil {
		opts = append(opts, preview.WithHerdrDriver(driver))
	}
	return preview.NewRenderer(cfg, a.Probes(), git, runner, opts...)
}

// cascadeFor builds the selector cascade for a [general].selector value.
// builtin skips fzf and uses the Bubble Tea TUI; fzf and auto include fzf
// (Fzf.Select no-ops when the binary is absent, so both fall back to the TUI).
// Direct is always first so exact / single matches short-circuit. currentPane
// and onTarget are threaded into the TUI selector (see newTUISelector); layout
// is optional (variadic so existing callers keep compiling) and configures
// the TUI's list/preview pane widths.
func cascadeFor(sel string, renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), layout ...tui.Layout) *selector.Cascade {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	direct := selector.Direct{}
	tuiSel := newTUISelector(renderer, currentPane, onTarget, l)
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
type tuiRunFunc func(ctx context.Context, candidates []source.Candidate, query string, renderer preview.Renderer, currentPane *source.Pane, layout ...tui.Layout) (source.Candidate, string, bool, error)

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
	// run defaults to tui.Run; tests substitute a fake to simulate a
	// ctrl+t/ctrl+p pick without driving a real Bubble Tea program.
	run tuiRunFunc
}

// newTUISelector builds a tuiSelector carrying the given Renderer (nil is
// valid in tests and degrades to the picker's built-in candidate summary),
// the Herdr pane shep is currently running inside (nil when not running
// inside one), a callback receiving the chosen target after a successful
// pick, and pane-width Layout (zero value falls back to the built-in
// heuristic).
func newTUISelector(renderer preview.Renderer, currentPane *source.Pane, onTarget func(string), layout ...tui.Layout) *tuiSelector {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	return &tuiSelector{renderer: renderer, layout: l, currentPane: currentPane, onTarget: onTarget, run: tui.Run}
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
	cand, target, ok, err := run(ctx, candidates, query, s.renderer, s.currentPane, s.layout)
	if ok && s.onTarget != nil {
		s.onTarget(target)
	}
	return cand, ok, err
}

// runOpen is the pipeline so tests can call it directly against a fresh App.
// target is the resolved --target value ("workspace", "tab", or "pane"); for
// the interactive TUI path, the model can override it via App.chosenTarget.
func (a *App) runOpen(cmd *cobra.Command, query, pathFlag, targetFlag string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// a.currentPane is best-effort and queried once per invocation, BEFORE
	// candidate resolution, so both the interactive TUI (footer hints and
	// ctrl+t/ctrl+p bindings, threaded in via selectorFactory) and the launch
	// path below share a single CurrentPane call. A nil driver or any error
	// (including source.ErrNoFocusedPane) just means the tab/pane targets —
	// and the TUI bindings — stay disabled; the workspace target ignores it
	// entirely. The call is bounded by currentPaneTimeout so a hung Herdr
	// daemon can never block shep startup indefinitely: a timeout is just
	// another CurrentPane error and degrades the same way.
	if driver := a.Driver(); driver != nil && driver.Detect(cmd.Context()) {
		paneCtx, cancel := context.WithTimeout(cmd.Context(), currentPaneTimeout)
		pane, perr := driver.CurrentPane(paneCtx)
		cancel()
		if perr == nil {
			a.currentPane = &pane
		}
	}

	cand, ok, err := a.resolveCandidate(cmd, query, pathFlag, out, errOut)
	if err != nil {
		return err
	}
	if !ok {
		return errExitOne // no candidate: resolveCandidate already printed why
	}

	// The TUI picker may have overridden the target (ctrl+t / ctrl+p). When it
	// did not (Enter, or any non-TUI selector), the --target flag value wins.
	target := targetFlag
	if a.chosenTarget != "" {
		target = a.chosenTarget
	}

	return a.launch(cmd.Context(), cand, target, a.currentPane, out, errOut)
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

	registry := source.NewRegistry(a.Config(), a.Probes(), a.Driver())
	return a.resolveFromRegistry(cmd, registry, query, "", out, errOut)
}

// resolveFromRegistry runs the collect/dedup/select pipeline against
// registry. When the resolved pick is a group workspace marker, it recurses
// into a scoped nested registry rooted at the group's own path/sources,
// threading the group's own template (if any, else the caller's
// parentTemplate) forward as Meta["parent_template"] on the eventually
// launched candidate (template resolution precedence tier 5).
func (a *App) resolveFromRegistry(cmd *cobra.Command, registry *source.Registry, query, parentTemplate string, out, errOut io.Writer) (source.Candidate, bool, error) {
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
		cascade := a.selectorFactory()
		if cascade == nil {
			printCandidates(out, all)
			fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
			return source.Candidate{}, false, errExitOne
		}
		got, ok, selErr := cascade.Select(cmd.Context(), matches, query)
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
		nestedTemplate := pick.Meta["group_template"]
		if nestedTemplate == "" {
			nestedTemplate = parentTemplate
		}
		nested := source.NewScopedRegistry(a.Config(), a.Probes(), a.Driver(), groupSources, pick.Path)
		return a.resolveFromRegistry(cmd, nested, "", nestedTemplate, out, errOut)
	}

	if parentTemplate != "" && pick.Meta["template"] == "" && pick.Meta["command"] == "" {
		if pick.Meta == nil {
			pick.Meta = map[string]string{}
		}
		pick.Meta["parent_template"] = parentTemplate
	}
	return pick, true, nil
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

// launch asks Herdr to focus/create a workspace for the candidate, applying
// the resolved template when a new workspace was created, or prints the
// resolved path when Herdr is unavailable. A candidate whose configured path
// does not exist on disk (Missing) fails clearly here instead of silently
// falling back to "/", $HOME, or cwd, and shep never creates the directory.
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
func (a *App) launch(ctx context.Context, cand source.Candidate, target string, currentPane *source.Pane, out, errOut io.Writer) error {
	if cand.Missing {
		fmt.Fprintf(errOut, "path does not exist: %s\n", displayPath(cand))
		return errExitOne
	}

	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		fmt.Fprintln(out, displayPath(cand))
		return nil
	}

	// A synthesized tree-expand child row (SourceHerdrTab) identifies an
	// ALREADY-OPEN tab inside an ALREADY-OPEN workspace: it routes straight
	// to FocusTab and bypasses the --target switch entirely (there is no
	// "workspace"/"tab"/"pane" choice for a candidate that is itself a tab).
	// Checked before the switch below so it can never fall through to
	// launchWorkspace/launchInCurrentWorkspace.
	if cand.Source == config.SourceHerdrTab {
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

// launchChildTab routes Enter on a synthesized SourceHerdrTab child row
// (tree-expand, R4) to driver.FocusTab, never touching FocusOrCreate: the
// child row already identifies an open tab in an open workspace, so there is
// nothing to focus-or-create at the workspace level. There is no rollback on
// failure — unlike launchInCurrentWorkspace's CreateTab/SplitPane, no
// resource is created here, so a warning plus errExitOne is the complete
// failure contract.
func (a *App) launchChildTab(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, errOut io.Writer) error {
	tabID := cand.Meta["tab_id"]
	if tabID == "" {
		fmt.Fprintln(errOut, "warning: herdr tab focus: missing tab id")
		return errExitOne
	}
	if err := driver.FocusTab(ctx, tabID); err != nil {
		fmt.Fprintf(errOut, "warning: herdr tab focus failed: %v\n", err)
		return errExitOne
	}
	return nil
}

// launchWorkspace is the historical "workspace" target: focus-or-create a
// standalone Herdr workspace and apply the resolved template on creation. It
// is the pre-target behaviour, factored out so the tab/pane branch reads at
// the same level.
func (a *App) launchWorkspace(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, out, errOut io.Writer) error {
	res, err := driver.FocusOrCreate(ctx, cand)
	if err != nil {
		fmt.Fprintf(errOut, "warning: herdr unavailable: %v\n", err)
		fmt.Fprintln(out, displayPath(cand))
		return nil
	}

	if res.Action == source.HerdrActionCreated {
		tpl := resolveTemplate(cand, a.Config())
		target := templates.Target{
			WorkspaceID: res.WorkspaceID,
			RootTabID:   res.RootTabID,
			RootPaneID:  res.RootPaneID,
			CWD:         cand.Path,
			Binary:      a.Config().HerdrBinary(),
		}
		if applyErr := templates.Apply(ctx, driver, target, tpl); applyErr != nil {
			fmt.Fprintf(errOut, "warning: template failed: %v\n", applyErr)
		}
	}
	return nil
}

// launchInCurrentWorkspace realises the "tab" and "pane" targets: open the
// candidate inside a new tab or a new pane of the Herdr workspace shep is
// currently running in (currentPane), instead of creating a brand-new
// standalone workspace. Only entries that support a current-workspace target
// (source.SupportsCurrentWorkspaceTarget — command workspaces, zoxide,
// projects) reach here; already-open herdr workspaces, group/template
// entries, and plain paths surface a clear error via disallowTarget.
//
// The command (if any) is run through templates.Apply with a synthetic
// template built from Meta["command"] (+ close_on_exit), so the
// shell-chaining wrap is the single tested code path shared with the
// workspace target's simple-command branch. A command-less candidate
// (zoxide/projects) applies an empty TemplateConfig — a plain shell, no
// RunPane.
func (a *App) launchInCurrentWorkspace(ctx context.Context, driver source.HerdrDriver, cand source.Candidate, target string, currentPane *source.Pane, errOut io.Writer) error {
	if currentPane == nil {
		fmt.Fprintf(errOut, "--target=%s requires shep to be running inside a herdr workspace pane\n", target)
		return errExitOne
	}
	if reason := disallowTarget(cand, target); reason != "" {
		fmt.Fprintln(errOut, reason)
		return errExitOne
	}

	cmd := cand.Meta["command"]
	closeOnExit := cand.Meta["close_on_exit"] == "true"
	tpl := config.TemplateConfig{Command: cmd, CloseOnExit: closeOnExit}
	binary := a.Config().HerdrBinary()
	cwd := currentPane.CWD

	var containerTabID, containerPaneID string
	switch target {
	case "tab":
		tab, pane, err := driver.CreateTab(ctx, currentPane.WorkspaceID, cwd, cand.Label, true)
		if err != nil {
			fmt.Fprintf(errOut, "warning: herdr tab create failed: %v\n", err)
			return nil
		}
		containerTabID, containerPaneID = tab.ID, pane.ID
	case "pane":
		pane, err := driver.SplitPane(ctx, currentPane.ID, "right", 0.5, cwd, true)
		if err != nil {
			fmt.Fprintf(errOut, "warning: herdr pane split failed: %v\n", err)
			return nil
		}
		containerTabID, containerPaneID = currentPane.TabID, pane.ID
	default:
		// Unreachable: launch only routes "tab"/"pane" here. Defensive guard.
		fmt.Fprintf(errOut, "--target=%s is not supported inside the current workspace\n", target)
		return errExitOne
	}

	applyTarget := templates.Target{
		WorkspaceID: currentPane.WorkspaceID,
		RootTabID:   containerTabID,
		RootPaneID:  containerPaneID,
		CWD:         cwd,
		Binary:      binary,
	}
	if applyErr := templates.Apply(ctx, driver, applyTarget, tpl); applyErr != nil {
		fmt.Fprintf(errOut, "warning: launch failed: %v\n", applyErr)
		// Apply failed after CreateTab/SplitPane already succeeded above,
		// leaving a ghost empty tab/pane in Herdr. Best-effort close it so
		// the user isn't left with dangling UI state; the close error (if
		// any) is intentionally swallowed since applyErr is already the
		// primary, user-facing failure.
		_ = driver.RunPane(ctx, containerPaneID, binary+" pane close "+containerPaneID)
		return errExitOne
	}
	return nil
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
//  1. exact [[workspaces]] entry with an explicit template (Meta["template"])
//  2. exact [[workspaces]] entry with an explicit command (Meta["command"])
//  3. first matching [[wildcards]] entry's template
//  4. template inherited from the parent group picker (Meta["parent_template"])
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
