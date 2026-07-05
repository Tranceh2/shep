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
	"github.com/tranceh2/shep/internal/tui"
)

// openCmd builds `shep open [query]`. The command resolves the query (or the
// --path override) to a single candidate, invokes Herdr to focus/create a
// workspace for it, and falls back to printing the resolved absolute path
// when Herdr is absent or unavailable (HI-5, HI-6).
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
installed, and falls back to an interactive TUI (see commit "feat(tui)").
With multiple candidates and no selection, shep prints the candidates and
exits 1.`,
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
	return cascadeFor(a.Config().General.Selector, a.buildPreviewRenderer())
}

// buildPreviewRenderer wires the production preview.Renderer from the loaded
// config and binary probes so the TUI's preview pane and the future `shep
// preview <path>` command share identical rendering behaviour (design:
// "same Renderer backs both views").
func (a *App) buildPreviewRenderer() preview.Renderer {
	cfg := a.Config()
	var git preview.GitProvider
	if a.Probes().Git {
		git = preview.NewGitProvider()
	}
	var runner preview.CommandRunner
	if cfg.Preview.Command != "" {
		runner = preview.NewCommandRunner()
	}
	return preview.NewRenderer(cfg.Preview, a.Probes(), git, runner)
}

// cascadeFor builds the selector cascade for a [general].selector value.
// builtin skips fzf and uses the Bubble Tea TUI; fzf and auto include fzf
// (Fzf.Select no-ops when the binary is absent, so both fall back to the TUI).
// Direct is always first so exact / single matches short-circuit. An unknown
// or empty value degrades to the builtin shape rather than blocking open.
// renderer backs the TUI selector's async preview pane (nil is valid, e.g. in
// tests, and degrades to a built-in candidate summary).
func cascadeFor(sel string, renderer preview.Renderer) *selector.Cascade {
	direct := selector.Direct{}
	tuiSel := newTUISelector(renderer)
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
}

// newTUISelector builds a tuiSelector carrying the given Renderer (nil is
// valid in tests and degrades to the picker's built-in candidate summary).
func newTUISelector(renderer preview.Renderer) *tuiSelector {
	return &tuiSelector{renderer: renderer}
}

func (tuiSelector) Name() string { return "tui" }

func (s tuiSelector) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	return tui.Run(ctx, candidates, query, s.renderer)
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
// then the resolution + selector cascade. ok=false means a user-facing reason
// was already printed and the caller should exit 1.
func (a *App) resolveCandidate(cmd *cobra.Command, query, pathFlag string, out, errOut io.Writer) (source.Candidate, bool, error) {
	if cmd.Flags().Changed("path") {
		cand, err := candidateFromPath(pathFlag)
		if err != nil {
			fmt.Fprintf(errOut, "resolve --path: %v\n", err)
			return source.Candidate{}, false, errExitOne
		}
		return cand, true, nil
	}

	cfg := a.Config()
	registry := source.NewRegistry(cfg, a.Probes(), a.Driver())
	all, matches, err := resolver.ResolveFromSources(cmd.Context(), registry, query)
	if err != nil {
		// Partial collect errors are non-fatal for resolution itself; the
		// matches slice is still authoritative. We only surface hard errors
		// when there are zero matches as well.
		fmt.Fprintf(errOut, "warning: a source failed: %v\n", err)
	}

	switch len(matches) {
	case 0:
		fmt.Fprintf(errOut, "no match: %s\n", query)
		return source.Candidate{}, false, errExitOne
	case 1:
		return matches[0], true, nil
	}

	cascade := a.selectorFactory()
	if cascade == nil {
		// No selector available: behave like PL-8 (print candidates, exit 1).
		printCandidates(out, all)
		fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
		return source.Candidate{}, false, errExitOne
	}
	pick, ok, selErr := cascade.Select(cmd.Context(), matches, query)
	if selErr != nil {
		if errors.Is(selErr, tui.ErrCancelled) {
			// The user cancelled interactively (esc/ctrl+c/ctrl+g). This is a
			// normal, quiet outcome, not an error to surface: no candidate
			// list, no ambiguous/selector-unavailable noise.
			return source.Candidate{}, false, errExitOne
		}
		printCandidates(out, all)
		fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
		fmt.Fprintf(errOut, "selector unavailable: %v\n", selErr)
		return source.Candidate{}, false, errExitOne
	}
	if ok {
		return pick, true, nil
	}

	// PL-8: multiple candidates and no selection -> print candidates, exit 1.
	printCandidates(out, all)
	fmt.Fprintf(errOut, "ambiguous: %s (%d matches)\n", query, len(matches))
	return source.Candidate{}, false, errExitOne
}

// launch asks Herdr to focus/create a workspace for the candidate or prints
// the resolved path when Herdr is unavailable (HI-5, HI-6).
func (a *App) launch(ctx context.Context, cand source.Candidate, out, errOut io.Writer) error {
	driver := a.Driver()
	if driver == nil || !driver.Detect(ctx) {
		// HI-5: Herdr absent -> print path, exit 0.
		fmt.Fprintln(out, displayPath(cand))
		return nil
	}

	res, err := driver.FocusOrCreate(ctx, cand)
	if err != nil {
		// HI-6: Herdr present but unreachable / bad JSON -> warn + path-print.
		fmt.Fprintf(errOut, "warning: herdr unavailable: %v\n", err)
		fmt.Fprintln(out, displayPath(cand))
		return nil
	}

	if res.Action == source.HerdrActionCreated {
		if startup := resolveStartup(cand, a.Config()); startup != "" {
			if runErr := driver.RunStartup(ctx, res.WorkspaceID, startup); runErr != nil {
				fmt.Fprintf(errOut, "warning: startup failed: %v\n", runErr)
			}
		}
	}
	return nil
}

// candidateFromPath builds a candidate for the --path flag and normalises it
// so the Herdr focus-by-cwd match has a canonical key to compare against.
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
	return source.Candidate{
		Path:           abs,
		NormalizedPath: np,
		Label:          baseLabelOpen(abs),
		Source:         "path",
	}, nil
}

// resolveStartup resolves the startup command for a freshly created workspace
// using the cascading precedence defined by the spec (task 3.5):
//  1. a predefined workspace (source "config") whose path matches the
//     candidate and carries an explicit Startup;
//  2. the first [[wildcards]] entry whose glob matches the candidate's
//     normalised path or base name (matchWildcard);
//  3. [defaults].startup as the final fallback.
//
// An empty return means no startup should run.
func resolveStartup(cand source.Candidate, cfg *config.Config) string {
	if cfg != nil && cand.Source == "config" {
		np := cand.NormalizedPath
		if np == "" {
			np = cand.Path
		}
		for _, ws := range cfg.Workspaces {
			if ws.Startup == "" {
				continue
			}
			if expanded := expandTildePath(ws.Path); expanded != "" && samePath(expanded, np) {
				return ws.Startup
			}
		}
	}
	if startup := matchWildcard(cand, cfg); startup != "" {
		return startup
	}
	if cfg != nil {
		return cfg.Defaults.Startup
	}
	return ""
}

// matchWildcard returns the startup command for the first [[wildcards]] entry
// whose pattern matches the candidate's normalised path or base name, scanned
// in declaration order. A nil/empty config or no match yields an empty string
// (no startup). It replaces the removed matchLayout helper.
func matchWildcard(cand source.Candidate, cfg *config.Config) string {
	if cfg == nil || len(cfg.Wildcards) == 0 {
		return ""
	}
	np := cand.NormalizedPath
	if np == "" {
		np = cand.Path
	}
	base := filepath.Base(np)
	for _, w := range cfg.Wildcards {
		if matchGlob(w.Pattern, np) || matchGlob(w.Pattern, base) {
			return w.Startup
		}
	}
	return ""
}

// samePath reports whether two paths are equal after symlink resolution. Used
// by resolveStartup to match a config workspace's expanded path against the
// candidate's normalised path regardless of how each was canonicalised.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}

// matchGlob wraps filepath.Match so a malformed pattern is treated as "no
// match" rather than an error (a bad glob in config should not crash open).
func matchGlob(pattern, name string) bool {
	if pattern == "" || name == "" {
		return false
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

// expandTildePath replaces a leading "~" or "~/" with the user's home dir. It
// mirrors source.expandTilde but lives in the command package to avoid an
// import cycle (source.expandTilde is unexported). An unresolvable home dir
// returns the input unchanged so resolveStartup falls back to wildcard/default.
func expandTildePath(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// printCandidates writes the candidate list to stdout so the user can see what
// was ambiguous (PL-8). Uses the same tsv-ish shape as `shep list` for parity.
func printCandidates(out io.Writer, cands []source.Candidate) {
	for _, c := range cands {
		fmt.Fprintf(out, "%s\t%s\n", displayPath(c), c.Label)
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

// baseLabelOpen mirrors source.baseLabel without an import cycle; open
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
