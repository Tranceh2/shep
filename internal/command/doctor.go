package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tranceh2/shep/internal/pathutil"
)

// doctorCmd builds `shep doctor`, a read-only diagnostic that reports
// configured [[workspaces]] paths that do not exist on disk. It never
// creates directories and never fails the config load; a missing path is a
// warning, not a hard error, so doctor always exits 0.
func (a *App) doctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check configured workspace paths for problems",
		Long: `shep doctor checks every [[workspaces]] entry (including group workspaces)
and reports any whose path does not exist on disk. It never creates
directories and never fails the config; missing paths are reported as
warnings so you can fix your config.toml before they surface as a confusing
"path does not exist" error from shep open.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runDoctor(cmd)
		},
	}
	return cmd
}

// runDoctor is the pipeline so tests can call it directly against a fresh App.
func (a *App) runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	cfg := a.Config()
	if len(cfg.Workspaces) == 0 {
		fmt.Fprintln(out, "no [[workspaces]] entries configured")
		return nil
	}
	missing := 0
	for _, ws := range cfg.Workspaces {
		// Fall back to the raw path on an unresolvable HOME so os.Stat reports
		// the bad path instead of crashing on expansion.
		path := ws.Path
		if expanded, err := pathutil.ExpandTilde(ws.Path); err == nil {
			path = expanded
		}
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(out, "MISSING %s: %s (configured path does not exist)\n", ws.Name, path)
			missing++
			continue
		}
		fmt.Fprintf(out, "OK      %s: %s\n", ws.Name, path)
	}
	if missing > 0 {
		fmt.Fprintf(out, "\n%d of %d configured workspace path(s) missing\n", missing, len(cfg.Workspaces))
	}
	return nil
}
