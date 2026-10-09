package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
)

// popupTimeout bounds the one request that opens the popup.
const popupTimeout = 5 * time.Second

// popupCmd opens a plugin pane as a Herdr popup: the plugin's open action
// (scripts/open-picker.sh) runs it instead of `herdr plugin pane open`.
// shep asks the server over HERDR_SOCKET_PATH, so the shortcut does not wait
// for the herdr CLI to start, which takes about 200 ms once the system has
// evicted it. It skips the config preload: nothing here reads the config.
func (a *App) popupCmd() *cobra.Command {
	var pluginID, entrypoint string
	cmd := &cobra.Command{
		Use:         "popup",
		Short:       "Open a plugin pane as a Herdr popup (the plugin's open action)",
		Hidden:      true,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"shep/skip-preload": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			socket := currentHerdrSocketPath()
			if socket == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "popup: no herdr socket for this session (HERDR_SOCKET_PATH unset)")
				return markReported(errors.New("HERDR_SOCKET_PATH unset"))
			}
			driver := herdr.New(config.HerdrBinaryWithEnv(nil, os.LookupEnv), herdr.WithSocketPath(socket))
			ctx, cancel := context.WithTimeout(cmd.Context(), popupTimeout)
			defer cancel()
			if err := driver.OpenPluginPane(ctx, pluginID, entrypoint, "popup"); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "popup: %v\n", err)
				return markReported(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&pluginID, "plugin", "", "plugin id")
	cmd.Flags().StringVar(&entrypoint, "entrypoint", "", "plugin pane id")
	_ = cmd.MarkFlagRequired("plugin")
	_ = cmd.MarkFlagRequired("entrypoint")
	return cmd
}
