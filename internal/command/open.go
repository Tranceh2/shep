package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	cmd := &cobra.Command{
		Use:   "open [query]",
		Short: "Open a project with Herdr (or print its path when Herdr is absent)",
		Long: `shep open resolves a query to a single project candidate and asks Herdr
to focus an existing workspace whose pane cwd matches, or to create a new
focused workspace. When Herdr is not installed or its daemon is unreachable,
shep prints the resolved absolute path and exits 0 so the caller can still
reach the project through any shell cd / file manager.

A selector cascade short-circuits an exact match, accelerates with fzf when
installed, and falls back to an interactive TUI. With multiple candidates and
no selection, shep prints the candidates and exits 1.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			return a.runOpen(cmd, query, pathFlag)
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"open the given absolute path directly, bypassing query resolution (used by the Television cable)")
	return cmd
}

// selectorFactory builds the cascade for `shep open` honouring
// [general].selector. Tests override the cascade via the selectorBuilder
// field. Direct always runs first regardless of selector value.
func (a *App) selectorFactory() *selector.Cascade {
	if a.selectorBuilder != nil {
		return a.selectorBuilder()
	}
	cfg := a.Config()
	layout := tui.Layout{ListWidth: cfg.TUI.ListWidth, PreviewWidth: cfg.TUI.PreviewWidth}
	return cascadeFor(cfg.General.Selector, a.buildPreviewRenderer(), layout)
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
// Direct is always first so exact / single matches short-circuit. layout is
// optional (variadic so existing single-arg callers keep compiling) and
// configures the TUI's list/preview pane widths.
func cascadeFor(sel string, renderer preview.Renderer, layout ...tui.Layout) *selector.Cascade {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	direct := selector.Direct{}
	tuiSel := newTUISelector(renderer, l)
	switch sel {
	case config.SelectorFzf, config.SelectorAuto:
		return selector.New(direct, selector.NewFzf(), tuiSel)
	default: // SelectorBuiltin, empty, or unknown
		return selector.New(direct, tuiSel)
	}
}

// tuiSelector is the universal interactive fallback: it runs the embedded
// Bubble Tea picker over the candidates, threading through the shared
// preview.Renderer so the picker's preview pane matches `shep preview`
// output. If the interactive selector cannot run, the open command falls back
// to printing the ambiguous candidate list and exits 1.
type tuiSelector struct {
	renderer preview.Renderer
	layout   tui.Layout
}

// newTUISelector builds a tuiSelector carrying the given Renderer (nil is
// valid in tests and degrades to the picker's built-in candidate summary)
// and pane-width Layout (zero value falls back to the built-in heuristic).
func newTUISelector(renderer preview.Renderer, layout ...tui.Layout) *tuiSelector {
	var l tui.Layout
	if len(layout) > 0 {
		l = layout[0]
	}
	return &tuiSelector{renderer: renderer, layout: l}
}

func (tuiSelector) Name() string { return "tui" }

func (s tuiSelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	return tui.Run(ctx, candidates, query, s.renderer, s.layout)
}

// runOpen is the pipeline so tests can call it directly against a fresh App.
func (a *App) runOpen(cmd *cobra.Command, query, pathFlag string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	cand, ok, err := a.resolveCandidate(cmd, query, pathFlag, out, errOut)
	if err != nil {
		return err
	}
	if !ok {
		return errExitOne // no candidate: resolveCandidate already printed why
	}

	return a.launch(cmd.Context(), cand, out, errOut)
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
func (a *App) launch(ctx context.Context, cand source.Candidate, out, errOut io.Writer) error {
	if cand.Missing {
		fmt.Fprintf(errOut, "path does not exist: %s\n", displayPath(cand))
		return errExitOne
	}

	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		fmt.Fprintln(out, displayPath(cand))
		return nil
	}

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

// candidateFromPath builds a candidate for the --path flag (and the bare "."
// query) and normalises it so the Herdr focus-by-cwd match has a canonical
// key to compare against. The path is stat'd here so a non-existent target
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
		return config.TemplateConfig{Command: cmd}
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
