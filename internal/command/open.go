package command

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
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

// selectorFactory builds the cascade for `shep open`. The default is
// [Direct, Fzf]; the TUI selector is appended in commit "feat(tui)". Tests
// override via the selectorBuilder field without depending on the TUI package.
func (a *App) selectorFactory() *selector.Cascade {
	if a.selectorBuilder != nil {
		return a.selectorBuilder()
	}
	return selector.New(selector.Direct{}, selector.NewFzf())
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
		fmt.Fprintf(errOut, "selector: %v\n", selErr)
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
		if startup := matchLayout(cand, a.Config()); startup != "" {
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

// matchLayout returns the startup command for the first [layouts.<glob>]
// whose pattern matches the candidate's normalised path or base name.
// A nil/empty result means no layout applied (no startup).
func matchLayout(cand source.Candidate, cfg *config.Config) string {
	if cfg == nil || len(cfg.Layouts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cfg.Layouts))
	for k := range cfg.Layouts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic first-match
	np := cand.NormalizedPath
	if np == "" {
		np = cand.Path
	}
	base := filepath.Base(np)
	for _, k := range keys {
		if matchGlob(k, np) || matchGlob(k, base) {
			return cfg.Layouts[k].Startup
		}
	}
	return ""
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