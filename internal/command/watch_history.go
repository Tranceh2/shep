package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
)

// watchHistoryDeps carries everything the collector run needs. Keeping the
// dependencies explicit lets the command wire the real store, snapshot adapter,
// and stream dialer while tests inject hermetic fakes.
type watchHistoryDeps struct {
	Paths       sessionPaths
	Store       herdrwatch.HistoryStore
	Snapshotter herdrwatch.Snapshotter
	Dialer      herdrwatch.StreamDialer
	Waiter      herdrwatch.Waiter
	// ErrOut receives the operator-visible exit diagnostic. A nil ErrOut keeps
	// the raw sentinel error for callers that assert on it directly.
	ErrOut io.Writer
}

// herdrSocketDialer dials the Herdr API socket for the raw event stream. It is
// the production StreamDialer; the collector owns exactly one such connection.
type herdrSocketDialer struct{ path string }

func (d herdrSocketDialer) Dial(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", d.path)
}

// watchHistoryCmd is the hidden long-lived collector. It is hidden because it
// is a lifecycle entrypoint for the Herdr plugin, not a user-facing verb.
func (a *App) watchHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "watch-history",
		Short:  "Run the per-socket workspace focus-history collector (internal)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			// The root command sets SilenceErrors, so a startup failure would
			// otherwise exit non-zero printing nothing. A plugin startup hook
			// that fails silently is undiagnosable, so every refusal below
			// prints its own sanitized reason first.
			fail := func(reason string, err error) error {
				fmt.Fprintf(cmd.ErrOrStderr(), "watch-history: %s\n", reason)
				return err
			}

			socket := currentHerdrSocketPath()
			root, err := defaultStateRoot()
			if err != nil {
				return fail("cannot resolve the shep state directory", err)
			}
			paths, err := resolveSessionPaths(socket, root)
			if err != nil {
				return fail("no herdr socket for this session (HERDR_SOCKET_PATH unset)", err)
			}

			store, err := history.OpenPath(paths.DBPath)
			if err != nil {
				return fail("cannot open the history store", err)
			}
			// The store is the durable write path for focus history: a failed
			// Close (e.g. a lost WAL checkpoint) must surface rather than be
			// silently dropped, but only when the run itself did not already
			// fail with a more specific error.
			defer func() {
				if closeErr := store.Close(); closeErr != nil && runErr == nil {
					runErr = fmt.Errorf("close history store: %w", closeErr)
				}
			}()

			// The snapshot adapter reuses the existing CLI driver, so bootstrap
			// membership comes from the same authoritative `herdr api snapshot`
			// path the rest of shep uses. It is called once per bootstrap and
			// never per focus event.
			driver := herdr.New(config.HerdrBinaryWithEnv(a.Config(), os.LookupEnv))

			// Ctrl-C / SIGTERM cancel the run context; Run then closes owned
			// I/O, joins its workers, releases the flock, and removes only its
			// own endpoint.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			runErr = runWatchHistory(ctx, watchHistoryDeps{
				Paths:       paths,
				Store:       store,
				Snapshotter: herdrwatch.NewSnapshotAdapter(driver),
				Dialer:      herdrSocketDialer{path: socket},
				ErrOut:      cmd.ErrOrStderr(),
			})
			return runErr
		},
	}
	return cmd
}

// runWatchHistory runs the collector and maps its outcome onto the CLI's
// error contract. A duplicate start against a live owner is a benign no-op:
// it never kills, signals, or guesses the incumbent's PID, and it never clears
// the incumbent's endpoint.
func runWatchHistory(ctx context.Context, deps watchHistoryDeps) error {
	err := herdrwatch.Run(ctx, herdrwatch.Config{
		SessionKey:  deps.Paths.SessionKey,
		LockPath:    deps.Paths.LockPath,
		ControlPath: deps.Paths.ControlPath,
		Store:       deps.Store,
		Snapshotter: deps.Snapshotter,
		Dialer:      deps.Dialer,
		Waiter:      deps.Waiter,
	})
	if errors.Is(err, herdrwatch.ErrAlreadyRunning) && deps.ErrOut == nil {
		// Test callers without a stream assert on the raw sentinel.
		return err
	}
	out := deps.ErrOut
	if out == nil {
		out = io.Discard
	}
	return reportCollectorExit(err, out)
}

// reportCollectorExit turns a collector outcome into an operator-visible line
// plus the CLI's error contract. The root command silences errors, so without
// this a duplicate start or an exhausted reconnect budget would terminate with
// no explanation at all — unacceptable for a plugin startup hook whose only
// diagnostic surface is the Herdr plugin command log.
func reportCollectorExit(err error, errOut io.Writer) error {
	classified := watchHistoryExitError(err)
	switch {
	case errors.Is(err, herdrwatch.ErrAlreadyRunning):
		// R2.S1 / R6: refuse, log, leave the incumbent completely alone.
		fmt.Fprintln(errOut, "watch-history: a collector is already running for this socket; leaving it untouched")
	case classified != nil:
		fmt.Fprintf(errOut, "watch-history: %v\n", classified)
		return markReported(classified)
	}
	return classified
}

// watchHistoryExitError classifies a collector outcome.
//
//   - nil (graceful cancellation) stays nil.
//   - ErrAlreadyRunning is a no-op: another owner holds this socket, and
//     refusing is the correct, non-destructive outcome.
//   - ErrReconnectExhausted stays a classified non-zero failure. Spending the
//     bounded reconnect budget does NOT prove the Herdr host died, so it is
//     never reported as a clean exit and never labelled host death.
type sanitizedCollectorError struct {
	message string
	cause   error
}

func (e *sanitizedCollectorError) Error() string { return e.message }
func (e *sanitizedCollectorError) Unwrap() error { return e.cause }

func watchHistoryExitError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, herdrwatch.ErrAlreadyRunning):
		return nil
	case errors.Is(err, context.Canceled):
		return nil
	case errors.Is(err, herdrwatch.ErrReconnectExhausted):
		message := "watch-history: reconnect budget exhausted; collector stopped without a verified stream"
		switch {
		case errors.Is(err, herdrwatch.ErrBootstrapUnavailable):
			message = "watch-history: reconnect budget exhausted; bootstrap snapshot unavailable"
		case errors.Is(err, herdrwatch.ErrSubscribeUnavailable):
			message = "watch-history: reconnect budget exhausted; subscription unavailable"
		case errors.Is(err, herdrwatch.ErrStreamRead):
			message = "watch-history: reconnect budget exhausted; stream read failed"
		case errors.Is(err, herdrwatch.ErrStreamHandling):
			message = "watch-history: reconnect budget exhausted; stream event handling failed"
		}
		return &ExitCodeError{
			Code: exitNotReady,
			Err:  &sanitizedCollectorError{message: message, cause: err},
		}
	default:
		return err
	}
}
