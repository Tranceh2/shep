// Package command builds the cobra command tree for the shep CLI.
//
// The package exposes an App type whose New constructor returns a fresh,
// self-contained command tree. Tests must call New per case because cobra
// accumulates flag state across Execute calls on a single command instance;
// rebuilding the tree from App each time keeps tests isolated.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/effective"
	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/selector"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/templates"
	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
	"github.com/tranceh2/shep/internal/tui"
)

// App wires the shep command tree and holds runtime state shared across
// subcommands (config, probes, output streams) and per-invocation open-run
// state (currentPane, chosenTarget). Construct one with New per process or
// test invocation.
type rankingStore interface {
	Snapshot(context.Context, string) ranking.Snapshot
	RecordSuccess(context.Context, ranking.Keys) error
	RecordAcknowledgement(context.Context, string, string) error
	ClearAcknowledgement(context.Context, string) error
	TogglePin(context.Context, string) (bool, error)
	Clear(context.Context) error
	Close() error
}

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
	// sessionAttach runs the foreground `herdr session attach` child. It stays
	// separate from HerdrDriver because attach must inherit terminal stdio rather
	// than use the driver's captured-output CommandRunner.
	sessionAttach sessionAttachFunc
	// asyncTUIRun overrides the interactive input-first TUI runner for tests.
	asyncTUIRun asyncTUIRunFunc
	// chosenTarget records a target override chosen by the interactive TUI
	// picker (ctrl+t => "tab", ctrl+p => "pane"). Empty means "no override":
	// runOpenWithView then uses the --target flag value (default "workspace"). It is
	// populated only when the TUI is the selecting selector and the user
	// pressed a target binding; Direct/fzf never set it.
	chosenTarget string
	// chosenAction records the typed tui.RowAction of the row the interactive
	// TUI picker selected (RowActionFocusTab for a synthesized tab/pane row,
	// RowActionOpen otherwise). The zero value (RowActionOpen) is the default
	// for every non-TUI path (Direct, fzf, --path, "."). It is populated only
	// when the TUI is the selecting selector; runOpenWithView threads it into launch,
	// which dispatches on the typed action instead of the candidate's Source
	// string.
	chosenAction tui.RowAction
	// currentPane is resolved from the startup snapshot's focused_pane_id. nil
	// means "not inside a Herdr pane" (or snapshot hydration failed); the
	// tab/pane launch targets are disabled in that case.
	currentPane *source.Pane
	// startupSnapshot is the one full state generation captured by runOpenWithView.
	// attempted prevents a failed initial hydration from triggering a second
	// provider-level state call during candidate resolution.
	startupSnapshot          *source.Snapshot
	startupSnapshotAttempted bool
	statusDialer             tui.StatusDialer
	historyMRUReader         func(context.Context) ([]string, error)
	rankingStore             rankingStore
	rankingData              ranking.Snapshot
	rankingOpen              func() (rankingStore, error)
	rankingMu                sync.Mutex
	// layoutApplier dispatches compiled layouts over the Herdr socket. It is
	// separate from herdrDriver because layout.apply is a socket transport,
	// not a CLI subprocess; nil means "build the default client on demand".
	layoutApplier templates.LayoutApplier
	// linkFS provides filesystem operations for shep link and unlink.
	// Production uses realLinkFS; tests inject an in-memory double.
	linkFS linkFS
	// userHomeDir resolves the current user's home directory for link/unlink.
	userHomeDir func() (string, error)
	// linkEnv reads environment variables for link and unlink.
	linkEnv linkEnv
	// executable resolves the current process executable path for link/unlink.
	executable func() (string, error)
	// evalSymlinks resolves symbolic links in an executable path for link/unlink.
	evalSymlinks func(string) (string, error)
	// templates is the process's single template engine (row labels,
	// workspace names, preview commands), built on first use by
	// templateEngine from the user's home directory.
	templates     *tmpl.Engine
	templatesOnce sync.Once
	// resolver is the one effective.Resolver for resolverCfg (the loaded
	// configuration; nil for the defaults), built on first use by settings.
	resolver    *effective.Resolver
	resolverCfg *config.Config
	resolverMu  sync.Mutex
	// pickerTheme is the picker's color theme, selected once by
	// selectedTheme. themeGetenv reads NO_COLOR, SHEP_THEME and the location
	// of Herdr's configuration for it (nil: os.Getenv); darkBackground
	// reports the terminal's appearance when an auto-switching Herdr theme
	// needs it (nil: ask the terminal). Tests set both.
	pickerTheme    theme.Theme
	themeOnce      sync.Once
	themeGetenv    func(string) string
	darkBackground func() bool
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

// templateEngine returns the one template engine this process renders every
// user template with. It is built on first use, resolving the home directory
// its tilde helper abbreviates exactly once; an unresolvable home only
// disables that abbreviation.
func (a *App) templateEngine() *tmpl.Engine {
	a.templatesOnce.Do(func() {
		home, _ := a.getUserHomeDir()()
		a.templates = tmpl.New(home)
	})
	return a.templates
}

// settings returns the resolver every per-candidate setting comes from:
// row presentations attached by the producers, preview sections, the
// template and workspace name of a launch. It is built once per loaded
// configuration and reads the filesystem, so it runs in the command layer
// and in the picker's background commands, never in its Update or View.
func (a *App) settings() *effective.Resolver {
	a.resolverMu.Lock()
	defer a.resolverMu.Unlock()
	if a.resolver == nil || a.resolverCfg != a.cfg {
		a.resolver = effective.New(a.Config())
		a.resolverCfg = a.cfg
	}
	return a.resolver
}

// selectedTheme selects the picker's color theme once per process (see
// selectTheme). A selection error (the configuration was validated, so none
// is expected) is reported on stderr and the picker keeps Herdr's default
// theme.
func (a *App) selectedTheme(cfg *config.Config) theme.Theme {
	a.themeOnce.Do(func() {
		t, err := a.selectTheme(cfg)
		if err != nil {
			fmt.Fprintf(a.err, "shep: %v; using the %s theme\n", err, theme.NameDefault)
			t = theme.Theme{}
		}
		a.pickerTheme = t
	})
	return a.pickerTheme
}

// selectTheme selects the color theme with theme.Select: NO_COLOR, then
// SHEP_THEME, then [tui].theme and its [themes.<name>] tables, inheriting
// Herdr's own theme (its config.toml, [theme.custom] included) by default.
// The terminal's appearance is asked only when Herdr's auto_switch needs it,
// and reads as dark when the terminal does not answer.
func (a *App) selectTheme(cfg *config.Config) (theme.Theme, error) {
	getenv := a.themeGetenv
	if getenv == nil {
		getenv = os.Getenv
	}
	dark := a.darkBackground
	if dark == nil {
		dark = lipgloss.HasDarkBackground
	}
	customs, err := cfg.CustomThemes()
	if err != nil {
		return theme.Theme{}, err
	}
	return theme.Select(theme.Options{
		ConfigTheme:     cfg.TUI.Theme,
		Customs:         customs,
		Getenv:          getenv,
		HerdrConfigPath: theme.DefaultHerdrConfigPath(getenv, a.templateEngine().Home()),
		Dark:            dark,
	})
}

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

// WithLayoutApplier injects the layout.apply socket boundary (intended for
// tests, which capture dispatched layouts instead of reaching a daemon).
func WithLayoutApplier(applier templates.LayoutApplier) Option {
	return func(a *App) { a.layoutApplier = applier }
}

// WithSessionAttach injects the blocking session-attach boundary for tests.
func WithSessionAttach(attach sessionAttachFunc) Option {
	return func(a *App) { a.sessionAttach = attach }
}

// WithStatusDialer overrides the live status dialer for tests.
func WithStatusDialer(dialer tui.StatusDialer) Option {
	return func(a *App) { a.statusDialer = dialer }
}

// WithHistoryMRUReader overrides the workspace focus MRU reader for tests.
func WithHistoryMRUReader(reader func(context.Context) ([]string, error)) Option {
	return func(a *App) { a.historyMRUReader = reader }
}

// WithAsyncTUIRunner overrides the interactive input-first TUI runner for tests.
func WithAsyncTUIRunner(runner asyncTUIRunFunc) Option {
	return func(a *App) { a.asyncTUIRun = runner }
}

// WithLinkFS injects the filesystem boundary used by link and unlink (intended for tests).
func WithLinkFS(fs linkFS) Option {
	return func(a *App) { a.linkFS = fs }
}

// WithLinkEnv injects the environment variable lookup used by link and unlink.
func WithLinkEnv(env linkEnv) Option {
	return func(a *App) { a.linkEnv = env }
}

// WithUserHomeDir injects the user home directory resolver used by link and unlink.
func WithUserHomeDir(fn func() (string, error)) Option {
	return func(a *App) { a.userHomeDir = fn }
}

// WithExecutable injects the binary executable path resolver used by link and unlink.
func WithExecutable(fn func() (string, error)) Option {
	return func(a *App) { a.executable = fn }
}

// WithEvalSymlinks injects the symlink evaluator used by link and unlink.
func WithEvalSymlinks(fn func(string) (string, error)) Option {
	return func(a *App) { a.evalSymlinks = fn }
}

// setChosenTarget records a target override chosen by the interactive TUI
// picker (ctrl+t => "tab", ctrl+p => "pane"; "" means no override). It is
// threaded into the TUI selector as a callback (see newTUISelector) because
// the shared selector.Selector interface's Select signature is fixed across
// every cascade member and cannot itself grow a target return value.
func (a *App) setChosenTarget(target string) {
	a.chosenTarget = target
}

// setChosenAction records the typed RowAction of the row the interactive TUI
// picker selected, threaded in as a callback for the same reason as
// setChosenTarget (Select's signature is fixed across Direct/Fzf/TUI).
func (a *App) setChosenAction(action tui.RowAction) {
	a.chosenAction = action
}

// Driver returns the active Herdr driver, lazily building a real one from the
// loaded config when one was not injected. A valid HERDR_BIN_PATH is accepted
// independently of PATH, matching the Herdr plugin runtime contract.
func (a *App) Driver() source.HerdrDriver {
	return a.driverWithOptions()
}

// pluginDriver returns the Herdr bridge for plugin-invoked commands. Unlike
// the general source path, it constructs a real driver even when probing says
// the binary is unavailable so command execution reports the failure.
func (a *App) pluginDriver() source.HerdrDriver {
	if a.herdrDriverInjected {
		return a.herdrDriver
	}
	cfg := a.Config()
	a.herdrDriver = herdr.New(config.HerdrBinaryWithEnv(cfg, os.LookupEnv))
	return a.herdrDriver
}

func (a *App) driverWithOptions() source.HerdrDriver {
	if a.herdrDriver != nil {
		return a.herdrDriver
	}
	cfg := a.Config()
	if !a.Probes().Herdr {
		return nil
	}
	a.herdrDriver = herdr.New(config.HerdrBinaryWithEnv(cfg, os.LookupEnv))
	return a.herdrDriver
}

// LayoutApplier returns the layout.apply socket client, building the default
// one on first use. Unlike Driver it never returns nil: an unreachable daemon
// is reported by the client as a fail-closed error at dispatch time, which is
// where the caller already prints its degradation warning.
func (a *App) LayoutApplier() templates.LayoutApplier {
	if a.layoutApplier == nil {
		a.layoutApplier = herdr.NewLayoutClient()
	}
	return a.layoutApplier
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
// error indicates a runtime failure. Errors that have not already been
// reported by a command are written once to the configured stderr stream.
func (a *App) Execute() error {
	return a.executeArgs(os.Args[1:])
}

func (a *App) executeArgs(args []string) error {
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.Execute()
	a.reportUnhandledError(err)
	return err
}

// reportUnhandledError prints err to stderr exactly once when no command in
// the path has already reported it (see markReported / isUserReportedError).
// It is split out from executeArgs so the "print exactly once, only when
// unreported" boundary is directly unit-testable against a hand-built
// ExitCodeError, independent of any specific subcommand's wiring.
func (a *App) reportUnhandledError(err error) {
	if err != nil && !isUserReportedError(err) {
		fmt.Fprintf(a.err, "error: %s\n", actionableError(err))
	}
}

func isUserReportedError(err error) bool {
	var reported interface{ reportedToUser() }
	return errors.As(err, &reported)
}

func actionableError(err error) string {
	message := err.Error()
	if strings.HasPrefix(message, "unknown command ") || strings.HasPrefix(message, "unknown flag:") {
		return message + "\nRun 'shep --help' for usage."
	}
	return message
}

// rootCmd builds the root command. It is constructed per Execute so flag
// state never leaks across invocations.
func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "shep",
		Short: "Herdr-first project launcher",
		Long: "shep enumerates project workspaces from Herdr, predefined workspaces,\n" +
			"zoxide and discovered projects, then opens the selected one with Herdr.",
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
		"path to shep config.toml (default: $XDG_CONFIG_HOME/shep/config.toml, or ~/.config/shep/config.toml when XDG_CONFIG_HOME is unset)")

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
				// root has SilenceErrors:true (subcommands print their own
				// user-facing messages and return errExitOne so main.go's
				// blanket os.Exit(1) never double-prints). PersistentPreRunE
				// runs before any subcommand body, so nothing else ever
				// prints this specific failure — without this line a bad
				// config silently exits 1 with zero output.
				fmt.Fprintf(cmd.ErrOrStderr(), "config error: %v\n", err)
				return errExitOne
			}
			a.cfg = cfg
			a.probes = config.ProbesFor(cfg)
		}
		return nil
	}

	root.AddCommand(a.initCmd())
	root.AddCommand(a.linkCmd())
	root.AddCommand(a.unlinkCmd())
	root.AddCommand(a.listCmd())
	root.AddCommand(a.openCmd())
	root.AddCommand(a.previewCmd())
	root.AddCommand(a.doctorCmd())
	root.AddCommand(a.rankingCmd())
	root.AddCommand(a.jumpBackCmd())
	root.AddCommand(a.watchHistoryCmd())

	return root
}
