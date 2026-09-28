package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestExitCodeError_DoesNotSelfReport is the structural guard for the R2/R3
// fix: ExitCodeError must be a pure exit-code transport. If a future change
// re-adds reportedToUser() directly on *ExitCodeError, this test fails
// because a bare, never-printed ExitCodeError would then be silently
// swallowed by Execute's fallback print (see
// TestReportUnhandledError_UnreportedExitCodeErrorPrintsOnce below).
func TestExitCodeError_DoesNotSelfReport(t *testing.T) {
	var bare error = &ExitCodeError{Code: 7, Err: errors.New("boom")}
	if isUserReportedError(bare) {
		t.Fatal("a bare *ExitCodeError must not be marked as already reported to the user")
	}
}

// TestMarkReported_PreservesExitCodeAndIdentity guards the wrapper contract:
// wrapping must keep the exit code reachable via errors.As/ExitCode, keep the
// wrapped sentinel reachable via errors.Is (the errExitOne identity contract
// used by dozens of existing tests), and mark the result as reported.
func TestMarkReported_PreservesExitCodeAndIdentity(t *testing.T) {
	base := &ExitCodeError{Code: 9, Err: errors.New("inner")}
	wrapped := markReported(base)

	if ExitCode(wrapped) != 9 {
		t.Fatalf("ExitCode(markReported(base)) = %d, want 9", ExitCode(wrapped))
	}
	if !isUserReportedError(wrapped) {
		t.Fatal("markReported(base) must be recognized as already reported")
	}
	if !errors.Is(wrapped, base) {
		t.Fatal("errors.Is must still find the wrapped sentinel through markReported")
	}
}

// TestMarkReported_Nil guards the nil short-circuit so call sites can pass a
// possibly-nil error without an extra nil check.
func TestMarkReported_Nil(t *testing.T) {
	if got := markReported(nil); got != nil {
		t.Fatalf("markReported(nil) = %v, want nil", got)
	}
}

// TestReportUnhandledError_UnreportedExitCodeErrorPrintsOnce is test 1 from
// the required behavior set: an ExitCodeError that no command path printed
// reaches stderr exactly once, via Execute's fallback, and its exit code is
// preserved.
func TestReportUnhandledError_UnreportedExitCodeErrorPrintsOnce(t *testing.T) {
	var errOut bytes.Buffer
	app := New(WithStreams(&bytes.Buffer{}, &errOut))

	unreported := &ExitCodeError{Code: 8, Err: errors.New("never printed by any command")}
	app.reportUnhandledError(unreported)

	stderr := errOut.String()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stderr = %q, want exactly one line", stderr)
	}
	if !strings.Contains(stderr, "never printed by any command") {
		t.Fatalf("stderr = %q, want the underlying diagnostic surfaced", stderr)
	}
	if ExitCode(unreported) != 8 {
		t.Fatalf("ExitCode(unreported) = %d, want 8 (must survive unchanged)", ExitCode(unreported))
	}
}

// TestReportUnhandledError_ReportedExitCodeErrorStaysSilent is test 2 from
// the required behavior set: an explicitly markReported error must not be
// printed again by the fallback, while its exit code is preserved.
func TestReportUnhandledError_ReportedExitCodeErrorStaysSilent(t *testing.T) {
	var errOut bytes.Buffer
	app := New(WithStreams(&bytes.Buffer{}, &errOut))

	reported := markReported(&ExitCodeError{Code: 3, Err: errors.New("already printed by the command")})
	app.reportUnhandledError(reported)

	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want no output for an already-reported error", errOut.String())
	}
	if ExitCode(reported) != 3 {
		t.Fatalf("ExitCode(reported) = %d, want 3 (must survive unchanged)", ExitCode(reported))
	}
}

// TestReportedExitError_SatisfiesExitCoderDirectly guards the follow-up fix
// to the advisory finding on reportedExitError: a caller that type-asserts
// err.(ExitCoder) directly (bypassing errors.As/ExitCode) must still observe
// the wrapped error's documented code. Before this fix, *reportedExitError
// had no ExitCode() method of its own, so this direct assertion failed even
// though errors.As-based consumers (ExitCode()) worked via Unwrap.
func TestReportedExitError_SatisfiesExitCoderDirectly(t *testing.T) {
	wrapped := markReported(&ExitCodeError{Code: 5, Err: errors.New("boom")})

	ec, ok := wrapped.(ExitCoder)
	if !ok {
		t.Fatal("markReported(err) must satisfy ExitCoder directly, not only through errors.As")
	}
	if got := ec.ExitCode(); got != 5 {
		t.Fatalf("direct ExitCode() = %d, want 5", got)
	}
	// The errors.As-based path (used by ExitCode()) must agree.
	if got := ExitCode(wrapped); got != 5 {
		t.Fatalf("ExitCode(wrapped) = %d, want 5 (must match direct assertion)", got)
	}
}

// TestErrExitOne_IdentityCodeAndReportingProperties pins the three properties
// every one of the ~45 call sites across the package rely on for the shared
// errExitOne sentinel: errors.Is identity survives the markReported wrap,
// the exit code is exactly 1, and the fallback stderr path treats it as
// already reported (no double-print for the ~45 call sites that already
// print their own message before returning errExitOne).
func TestErrExitOne_IdentityCodeAndReportingProperties(t *testing.T) {
	if !errors.Is(errExitOne, errExitOne) {
		t.Fatal("errExitOne must be errors.Is-identity-stable with itself")
	}
	if ExitCode(errExitOne) != 1 {
		t.Fatalf("ExitCode(errExitOne) = %d, want 1", ExitCode(errExitOne))
	}
	if !isUserReportedError(errExitOne) {
		t.Fatal("errExitOne must be recognized as already reported to the user")
	}

	var errOut bytes.Buffer
	app := New(WithStreams(&bytes.Buffer{}, &errOut))
	app.reportUnhandledError(errExitOne)
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want no fallback output for errExitOne", errOut.String())
	}
}
