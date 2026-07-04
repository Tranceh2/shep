package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
)

// initCmd builds `shep init` which materialises a commented, path-agnostic
// example config so the user has a starting point to edit. It bails out if a
// config already exists unless --force is passed.
func (a *App) initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented example shep config to the default path",
		Long: `shep init writes a path-agnostic example configuration to your user
config directory. The example contains no hardcoded user paths; edit it to
point at your own project roots.`,
		Args: cobra.NoArgs,
		// init must be able to create the config file even when one does not
		// yet exist, so it skips the config/probe preload in PersistentPreRunE.
		Annotations: map[string]string{"shep/skip-preload": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			path := a.configPath
			if path == "" {
				p, err := config.DiscoverPath()
				if err != nil {
					return err
				}
				path = p
			}

			if !force {
				if _, statErr := os.Stat(path); statErr == nil {
					return fmt.Errorf("config already exists at %q (use --force to overwrite)", path)
				}
			}

			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create config dir %q: %w", dir, err)
			}
			if err := os.WriteFile(path, []byte(config.ExampleTOML()), 0o644); err != nil {
				return fmt.Errorf("write config %q: %w", path, err)
			}
			fmt.Fprintf(out, "wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}
