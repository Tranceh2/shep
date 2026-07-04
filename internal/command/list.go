package command

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
)

// format is the output rendering mode for `shep list`.
type format string

const (
	formatHuman format = "human"
	formatTSV   format = "tsv"
	formatJSON  format = "json"
)

func parseFormat(s string) (format, error) {
	switch s {
	case "human", "tsv", "json", "":
		if s == "" {
			return formatHuman, nil
		}
		return format(s), nil
	default:
		return "", fmt.Errorf("invalid format %q: want human|tsv|json", s)
	}
}

// listCandidate is the JSON projection of a candidate. Fields are stable and
// lowercase so cable/tooling integrations can rely on the schema.
type listCandidate struct {
	Path           string `json:"path"`
	NormalizedPath string `json:"normalized_path"`
	Label          string `json:"label"`
	Source         string `json:"source"`
}

// listCmd builds `shep list` which enumerates candidates from all enabled
// sources, deduplicates them and renders in the requested format.
func (a *App) listCmd() *cobra.Command {
	var outFormat string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered project candidates from all enabled sources",
		Long: `shep list enumerates candidates from Herdr workspaces, zoxide, the current
directory and any configured roots, deduplicates by normalised path, and
prints them as a table (human), tab-separated path	label lines (tsv) for
Television, or structured JSON.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := parseFormat(outFormat)
			if err != nil {
				return err
			}
			return a.runList(cmd, f)
		},
	}
	cmd.Flags().StringVar(&outFormat, "format", "human", "output format: human|tsv|json")
	return cmd
}

// runList owns the collect -> dedup -> render pipeline so tests can call it
// directly against a fresh App without rebuilding a cobra command.
func (a *App) runList(cmd *cobra.Command, f format) error {
	cfg := a.Config()
	probes := a.Probes()
	// The real Herdr driver powers the herdr source provider; when Herdr is
	// not installed Driver() returns nil and the provider stays inert, so
	// list still surfaces cwd/zoxide/roots candidates.
	registry := source.NewRegistry(cfg, probes, a.Driver())

	candidates, collectErr := registry.Collect(cmd.Context())
	deduped := resolver.Dedup(candidates)
	// Stable presentation order: by label then path, deterministic across runs.
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Label != deduped[j].Label {
			return deduped[i].Label < deduped[j].Label
		}
		return deduped[i].NormalizedPath < deduped[j].NormalizedPath
	})

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	if collectErr != nil {
		// Partial-provider failures are non-fatal for list: warn on stderr and
		// still print whatever the other providers produced (HI-6 philosophy).
		fmt.Fprintf(errOut, "warning: a source failed: %v\n", collectErr)
	}
	return render(out, deduped, f)
}

// render writes candidates in the chosen format. Splitting it from runList
// keeps the formatting logic independently testable.
func render(out io.Writer, cands []candidate, f format) error {
	switch f {
	case formatTSV:
		return renderTSV(out, cands)
	case formatJSON:
		return renderJSON(out, cands)
	default:
		return renderHuman(out, cands)
	}
}

// candidate is the local rendering alias for source.Candidate so render is
// easy to test without pulling the source package into tests that build
// synthetic slices. We keep the conversion in runList.
type candidate = source.Candidate

func renderHuman(out io.Writer, cands []source.Candidate) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tLABEL\tSOURCE")
	for _, c := range cands {
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.NormalizedPath, c.Label, c.Source)
	}
	return w.Flush()
}

func renderTSV(out io.Writer, cands []source.Candidate) error {
	for _, c := range cands {
		// PL-5: each line is path\tlabel\n, no ANSI, no source column.
		fmt.Fprintf(out, "%s\t%s\n", c.NormalizedPath, c.Label)
	}
	return nil
}

func renderJSON(out io.Writer, cands []source.Candidate) error {
	out2 := make([]listCandidate, 0, len(cands))
	for _, c := range cands {
		out2 = append(out2, listCandidate{
			Path:           c.Path,
			NormalizedPath: c.NormalizedPath,
			Label:          c.Label,
			Source:         c.Source,
		})
	}
	if out2 == nil {
		out2 = []listCandidate{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(out2)
}