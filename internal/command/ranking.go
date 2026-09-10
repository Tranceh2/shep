package command

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/ranking"
)

func (a *App) rankingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ranking",
		Short: "Manage local adaptive ranking history",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear all local ranking history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := ranking.Open()
			if err != nil {
				return fmt.Errorf("open ranking state: %w", err)
			}
			defer func() { _ = store.Close() }()
			if err := store.Clear(cmd.Context()); err != nil {
				return fmt.Errorf("clear ranking state: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ranking history cleared")
			return nil
		},
	})
	return cmd
}
