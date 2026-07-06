package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
		path := expandTildeDoctor(ws.Path)
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

// expandTildeDoctor mirrors source's expandTilde without importing it
// directly (kept local to avoid a needless command->source coupling beyond
// what open.go already needs for candidates).
func expandTildeDoctor(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if len(p) >= 2 && p[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return home + p[1:]
	}
	return p
}
