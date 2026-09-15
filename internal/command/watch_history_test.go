package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
)

// openTestHistoryStore opens a real SQLite history store under a temp path, so
// the collector wiring is exercised against the production store rather than a
// double.
func openTestHistoryStore(t *testing.T, path string) *history.Store {
	t.Helper()
	store, err := history.OpenPath(path)
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// holdingWaiter is a fake clock that parks in the reconnect backoff until the
// run context is cancelled. It keeps the collector alive for the ownership
// assertions without ever sleeping the real 30s budget or busy-looping.
type holdingWaiter struct{}

func (holdingWaiter) Wait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// waitForSocket blocks until the collector's control endpoint exists.
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control endpoint %q never appeared", path)
}

// shortStateRoot returns a short temp directory. Darwin's AF_UNIX sun_path
// limit is 104 bytes, and the default TMPDIR is long enough to blow it, so
// socket-bearing tests need a short root.
func shortStateRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "shepjb")
	if err != nil {
		t.Fatalf("create short temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestWatchHistoryCmd_HiddenAndArgless(t *testing.T) {
	app := New()
	cmd := app.watchHistoryCmd()

	if cmd.Use != "watch-history" {
		t.Fatalf("command use = %q, want watch-history", cmd.Use)
	}
	if !cmd.Hidden {
		t.Fatal("watch-history must be hidden from the public command list")
	}
	if cmd.Short == "" {
		t.Fatal("watch-history needs a short description for the hidden help output")
	}
	if err := cmd.Args(cmd, []string{"unexpected"}); err == nil {
		t.Fatal("watch-history must reject positional arguments")
	}
}

func TestRootCmd_RegistersJumpBackPublicAndWatchHistoryHidden(t *testing.T) {
	app := New()
	root := app.rootCmd()

	var jumpBack, watchHistory bool
	for _, sub := range root.Commands() {
		switch sub.Name() {
		case "jump-back":
			jumpBack = true
			if sub.Hidden {
				t.Fatal("jump-back must be a public command")
			}
		case "watch-history":
			watchHistory = true
			if !sub.Hidden {
				t.Fatal("watch-history must stay hidden")
			}
		}
	}
	if !jumpBack {
		t.Fatal("root command tree is missing jump-back")
	}
	if !watchHistory {
		t.Fatal("root command tree is missing watch-history")
	}

	// The pre-existing baseline commands must still be registered.
	for _, name := range []string{"init", "list", "open", "preview", "doctor", "ranking"} {
		found := false
		for _, sub := range root.Commands() {
			if sub.Name() == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("root registration dropped the existing %q command", name)
		}
	}
}

// fakeSnapshotter satisfies herdrwatch.Snapshotter without a live daemon.
type fakeSnapshotter struct{ calls int }

func (f *fakeSnapshotter) Snapshot(context.Context) (herdrwatch.Membership, error) {
	f.calls++
	return herdrwatch.Membership{Present: []string{"ws-a"}, FocusedWorkspaceID: "ws-a"}, nil
}

// blockedDialer never yields a stream; it keeps the collector alive in the
// reconnect loop so the ownership assertions are about the owner, not the wire.
type blockedDialer struct{}

func (blockedDialer) Dial(ctx context.Context) (net.Conn, error) {
	return nil, errors.New("stream unavailable in test")
}

func TestRunWatchHistory_SecondOwnerRefusesWithoutDisturbingIncumbent(t *testing.T) {
	root := shortStateRoot(t)
	paths, err := resolveSessionPaths("/tmp/herdr-incumbent.sock", root)
	if err != nil {
		t.Fatalf("resolveSessionPaths: %v", err)
	}

	store := openTestHistoryStore(t, paths.DBPath)
	incumbentCtx, stopIncumbent := context.WithCancel(context.Background())
	defer stopIncumbent()

	incumbentDone := make(chan error, 1)
	go func() {
		incumbentDone <- herdrwatch.Run(incumbentCtx, herdrwatch.Config{
			SessionKey:  paths.SessionKey,
			LockPath:    paths.LockPath,
			ControlPath: paths.ControlPath,
			Store:       store,
			Snapshotter: &fakeSnapshotter{},
			Dialer:      blockedDialer{},
			Waiter:      holdingWaiter{},
		})
	}()

	waitForSocket(t, paths.ControlPath)

	// A duplicate start must refuse without clearing the incumbent's endpoint.
	dupErr := runWatchHistory(context.Background(), watchHistoryDeps{
		Paths:       paths,
		Store:       store,
		Snapshotter: &fakeSnapshotter{},
		Dialer:      blockedDialer{},
		Waiter:      holdingWaiter{},
	})
	if !errors.Is(dupErr, herdrwatch.ErrAlreadyRunning) {
		t.Fatalf("duplicate start error = %v, want ErrAlreadyRunning", dupErr)
	}
	if _, statErr := os.Stat(paths.ControlPath); statErr != nil {
		t.Fatalf("duplicate start removed the incumbent endpoint: %v", statErr)
	}

	stopIncumbent()
	select {
	case err := <-incumbentDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("incumbent exited with %v, want a graceful nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("incumbent did not exit after cancellation")
	}
}

// TestWatchHistoryCmd_ReportsStartupFailureToStderr guards a real defect found
// by the runtime harness: the root command sets SilenceErrors, so a collector
// that cannot start (no HERDR_SOCKET_PATH) exited 1 printing nothing at all.
// A plugin startup hook that fails silently is undiagnosable, so the command
// must print its own sanitized reason.
func TestWatchHistoryCmd_ReportsStartupFailureToStderr(t *testing.T) {
	var errOut bytes.Buffer
	app := New(WithStreams(&bytes.Buffer{}, &errOut))
	cmd := app.watchHistoryCmd()
	cmd.SetErr(&errOut)

	t.Setenv("HERDR_SOCKET_PATH", "")

	err := cmd.RunE(cmd, nil)

	if err == nil {
		t.Fatal("expected a non-nil error when no Herdr socket is available")
	}
	if errOut.Len() == 0 {
		t.Fatal("watch-history must print why it could not start; root silences errors")
	}
	if !strings.Contains(errOut.String(), "watch-history:") {
		t.Fatalf("stderr = %q, want a watch-history-prefixed diagnostic", errOut.String())
	}
}

// TestReportCollectorExit_DuplicateStartLogsRejectionAndExitsZero guards the
// second defect the runtime harness found: a duplicate start exited 1 printing
// nothing. R2.S1 requires the second collector to exit WITHOUT disturbing the
// incumbent and to log the rejection, and R6 requires repeated starts against
// a live owner to be no-ops.
func TestReportCollectorExit_ClassifiesBootstrapFailureWithoutLeakingCause(t *testing.T) {
	var errOut bytes.Buffer
	internal := errors.New("snapshot command exposed socket=/private/session secret=token")
	err := reportCollectorExit(fmt.Errorf("%w: %w", herdrwatch.ErrReconnectExhausted, fmt.Errorf("%w: %w", herdrwatch.ErrBootstrapUnavailable, internal)), &errOut)
	if err == nil || ExitCode(err) == 0 {
		t.Fatalf("bootstrap exhaustion must be non-zero, got %v", err)
	}
	if !strings.Contains(errOut.String(), "reconnect budget exhausted") {
		t.Fatalf("stderr = %q, want bounded reconnect classification", errOut.String())
	}
	if strings.Contains(errOut.String(), "socket=") || strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr leaked internal bootstrap details: %q", errOut.String())
	}
}

func TestReportCollectorExit_DuplicateStartLogsRejectionAndExitsZero(t *testing.T) {
	var errOut bytes.Buffer

	err := reportCollectorExit(herdrwatch.ErrAlreadyRunning, &errOut)

	if err != nil {
		t.Fatalf("duplicate start must be a no-op, got %v", err)
	}
	if ExitCode(err) != 0 {
		t.Fatalf("duplicate start exit code = %d, want 0", ExitCode(err))
	}
	if !strings.Contains(errOut.String(), "already running") {
		t.Fatalf("stderr = %q, want a logged rejection naming the live owner", errOut.String())
	}

	// A clean shutdown stays silent and successful.
	var quiet bytes.Buffer
	if err := reportCollectorExit(nil, &quiet); err != nil || quiet.Len() != 0 {
		t.Fatalf("clean shutdown: err=%v stderr=%q, want nil and silence", err, quiet.String())
	}

	// Reconnect exhaustion still reports non-zero and never claims host death.
	var loud bytes.Buffer
	exhausted := reportCollectorExit(herdrwatch.ErrReconnectExhausted, &loud)
	if ExitCode(exhausted) == 0 {
		t.Fatal("reconnect exhaustion must stay non-zero")
	}
	if loud.Len() == 0 {
		t.Fatal("reconnect exhaustion must be reported to stderr")
	}
	if strings.Contains(loud.String(), "host death") {
		t.Fatalf("must not claim host death: %q", loud.String())
	}
}

func TestWatchHistoryPaths_MatchJumpBackDerivation(t *testing.T) {
	root := shortStateRoot(t)
	socket := filepath.Join(root, "herdr.sock")

	cli, err := resolveSessionPaths(socket, root)
	if err != nil {
		t.Fatalf("cli paths: %v", err)
	}
	collector, err := resolveSessionPaths(socket, root)
	if err != nil {
		t.Fatalf("collector paths: %v", err)
	}

	if cli != collector {
		t.Fatalf("CLI and collector derived different paths:\n cli=%+v\n col=%+v", cli, collector)
	}
	if len(cli.ControlPath) > 104 {
		t.Fatalf("control path is %d bytes, exceeding the AF_UNIX limit", len(cli.ControlPath))
	}
}
