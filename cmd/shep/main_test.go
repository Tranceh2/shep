package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tranceh2/shep/internal/command"
)

func TestHandleAppError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "nil error -> exit 0",
			err:      nil,
			wantCode: 0,
		},
		{
			name:     "generic error -> exit 1",
			err:      errors.New("generic error"),
			wantCode: 1,
		},
		{
			name:     "wrapped ExitCodeError 4 -> exit 4",
			err:      fmt.Errorf("wrap: %w", &command.ExitCodeError{Code: 4, Err: errors.New("target missing")}),
			wantCode: 4,
		},
		{
			// Regression guard for the reportedExitError plain-error bug:
			// markReported wraps errors returned from the two reachable
			// call sites (open.go's launchWorkspace, watch_history.go's
			// startup "fail" closure) that carry a plain, non-ExitCoder
			// error. Before the fix, reportedExitError.ExitCode() returned
			// 0 for these — the exact wrapper handleAppError feeds to
			// os.Exit — turning an ordinary command failure into a
			// process-level success. The internal/command package cannot
			// export markReported, so this exercises the same shape via
			// ExitCodeError's own Unwrap chain wrapped a second time,
			// mirroring markReported's "wrap a plain error" case: the
			// package's ExitCode() helper is what markReported's
			// ExitCode() now delegates to.
			name:     "generic error wrapped twice (markReported-shaped) -> exit 1, never 0",
			err:      fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errors.New("plain, no ExitCoder"))),
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handleAppError(tt.err)
			if got != tt.wantCode {
				t.Errorf("handleAppError(%v) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

// TestWatchHistorySubcommand_StartupFailureExitsNonZero is a real,
// process-level regression guard for the reportedExitError plain-error bug
// on its `watch-history` reachable path: it builds the actual shep binary
// and runs it as a subprocess with no Herdr socket configured, exactly the
// scenario watch_history.go's "fail" closure wraps with markReported(err)
// on a plain, non-ExitCoder error. Before the fix, main's os.Exit(ExitCode
// (err)) would have received code 0 from that wrapper and the process would
// have exited successfully despite the startup failure it printed.
func TestWatchHistorySubcommand_StartupFailureExitsNonZero(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "shep")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shep binary: %v\n%s", err, out)
	}

	cmd := exec.Command(binaryPath, "watch-history")
	cmd.Env = append(os.Environ(), "HERDR_SOCKET_PATH=")
	output, err := cmd.CombinedOutput()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("watch-history with no socket: err = %v (%T), want *exec.ExitError; output=%s", err, err, output)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("watch-history with no socket exited 0, want non-zero; output=%s", output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("watch-history with no socket exit code = %d, want 1; output=%s", exitErr.ExitCode(), output)
	}
}
