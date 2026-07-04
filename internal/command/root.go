// Package command builds the cobra command tree for the shep CLI.
//
// The package exposes an App type whose New constructor returns a fresh,
// self-contained command tree. Tests must call New per case because cobra
// accumulates flag state across Execute calls on a single command instance;
// rebuilding the tree from App each time keeps tests isolated.
package command

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
)

// App wires the shep command tree and holds runtime state shared across
// subcommands (config, probes, output streams). Construct one with New per
// process or test invocation.
type App struct {
	// versionInfo holds build metadata shown by `shep --version`.
	versionInfo versionInfo
	// configPath is the --config override; empty means "discover".
	configPath string
	// cfg is the loaded configuration, populated by PersistentPreRunE. It is
	// nil until the first command runs; subcommands must read it through the
	// Config() accessor.
	cfg *config.Config
	// probes records which optional binaries are present at startup.
	probes config.Probes
	// out and err are the streams commands write to. They default to os.Std*
	// but may be overridden via WithStreams for tests.
	out io.Writer
	err io.Writer
	// herdrDriver is the Herdr bridge. It is built lazily from cfg+probes in
	// PersistentPreRunE unless a test injects one via WithHerdrDriver; the
	// latter keeps tests from shelling out to a real Herdr binary.
	herdrDriver source.HerdrDriver
	// herdrDriverInjected guards against PersistentPreRunE overwriting a
	// test-injected driver.
	herdrDriverInjected bool
	// selectorBuilder overrides the `shep open` selector cascade for tests.
	// nil falls back to the default [Direct, Fzf] cascade.
	selectorBuilder func() *selector.Cascade
}

// Config returns the loaded configuration, defaulting to path-agnostic
// Defaults when nothing has been loaded yet (e.g. commands invoked before
// PersistentPreRunE in tests, or when config discovery itself is skipped).
func (a *App) Config() *config.Config {
	if a.cfg == nil {
		return config.Defaults()
	}
	return a.cfg
}

// Probes returns the binary availability snapshot.
func (a *App) Probes() config.Probes { return a.probes }

// versionInfo bundles injected build metadata.
type versionInfo struct {
	version string
	commit  string
}

// Option configures an App at construction time.
type Option func(*App)

// WithVersion injects build metadata (set via -ldflags in the Makefile).
func WithVersion(version, commit string) Option {
	return func(a *App) {
		a.versionInfo = versionInfo{version: version, commit: commit}
	}
}

// WithStreams overrides stdout/stderr (intended for tests).
func WithStreams(stdout, stderr io.Writer) Option {
	return func(a *App) {
		a.out = stdout
		a.err = stderr
	}
}

// WithHerdrDriver injects a HerdrDriver (intended for tests). When supplied,
// PersistentPreRunE keeps it instead of building a real exec-backed driver.
func WithHerdrDriver(d source.HerdrDriver) Option {
	return func(a *App) {
		a.herdrDriver = d
		a.herdrDriverInjected = true
	}
}

// Driver returns the active Herdr driver, lazily building a real one from the
// loaded config when one was not injected. Returns nil when Herdr is not on
// PATH (the probe failed); callers treat nil as "fall back to path-print".
func (a *App) Driver() source.HerdrDriver {
	if a.herdrDriver != nil {
		return a.herdrDriver
	}
	cfg := a.Config()
	if !a.Probes().Herdr {
		return nil
	}
	a.herdrDriver = herdr.New(cfg.HerdrBinary())
	return a.herdrDriver
}

// New constructs a fresh App with sensible defaults. Apply options to inject
// build metadata or redirect output streams.
func New(opts ...Option) *App {
	a := &App{
		out: os.Stdout,
		err: os.Stderr,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Execute builds the command tree and runs it against os.Args. A non-nil
// error indicates a runtime failure.
func (a *App) Execute() error {
	return a.rootCmd().Execute()
}

// rootCmd builds the root command. It is constructed per Execute so flag
// state never leaks across invocations.
func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "shep",
		Short: "Herdr-first project launcher",
		Long: "shep enumerates project workspaces from Herdr, zoxide and the\n" +
			"current directory, then opens the selected one with Herdr.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Stream redirection is centralised on the root so subcommands inherit it
	// through cobra's parent walk in OutOrStdout/ErrOrStderr.
	root.SetOut(a.out)
	root.SetErr(a.err)

	// cobra renders `shep --version` from this field automatically.
	if a.versionInfo.version != "" {
		root.Version = fmt.Sprintf("%s (commit: %s)", a.versionInfo.version, a.versionInfo.commit)
	}

	root.PersistentFlags().StringVar(&a.configPath, "config", "",
		"path to shep config.toml (default: discovered via os.UserConfigDir)")

	// PersistentPreRunE runs before every subcommand: it loads the config
	// (from --config or the discovered path, falling back to Defaults when
	// the file is absent) and probes optional binaries. `init` opts out via a
	// sentinel annotation so it can write the file even when none exists.
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Annotations != nil && cmd.Annotations["shep/skip-preload"] == "true" {
			return nil
		}
		// Tests inject cfg + probes directly before Execute; honour them by
		// loading only when nothing has been set yet. Real invocations leave
		// a.cfg nil, so they always resolve --config / discover + probe.
		if a.cfg == nil {
			cfg, err := config.Load(a.configPath)
			if err != nil {
				return err
			}
			a.cfg = cfg
			a.probes = config.ProbesFor(cfg)
		}
		return nil
	}

	root.AddCommand(a.initCmd())
	root.AddCommand(a.listCmd())
	root.AddCommand(a.openCmd())

	return root
}
