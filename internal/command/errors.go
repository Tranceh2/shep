package command

import "errors"

// ExitCoder is implemented by errors that specify an OS exit status code.
type ExitCoder interface {
	ExitCode() int
}

// ExitCodeError wraps an underlying error with a specific exit code. It is a
// pure exit-code transport: it does NOT mark itself as already reported to
// the user. A command that constructs an ExitCodeError without first
// printing its own diagnostic relies on Execute's fallback stderr print, and
// that fallback only fires when reportedToUser() is absent from the error
// chain. Coupling exit-code plumbing to reporting state here would let a
// future ExitCodeError that never printed anything be silently swallowed.
type ExitCodeError struct {
	Code int
	Err  error
}

// reportedExitError wraps an error whose command path has already emitted a
// sanitized, user-facing diagnostic to stderr. Only this explicit wrapper
// opts an error out of Execute's fallback print — never ExitCodeError's type
// itself — so "already reported" stays a per-call-site decision instead of a
// blanket property of every exit-code error.
type reportedExitError struct {
	err error
}

// markReported wraps err (which must be non-nil) to record that the caller
// already printed its user-facing diagnostic. Use this at every return site
// immediately after writing that diagnostic to stderr, never as a blanket
// wrapper applied later or far from the print call.
func markReported(err error) error {
	if err == nil {
		return nil
	}
	return &reportedExitError{err: err}
}

func (e *reportedExitError) Error() string { return e.err.Error() }

func (e *reportedExitError) Unwrap() error { return e.err }

// ExitCode delegates to the package-level ExitCode helper on the wrapped
// error, so both a direct err.(ExitCoder) assertion and unwrap-aware
// consumers (errors.As via ExitCode, below) observe the same code through
// the reportedExitError wrapper. Without this method, a bare type assertion
// on a markReported error would miss ExitCoder entirely: errors.As only
// walks Unwrap chains, but a caller that type-asserts err.(ExitCoder)
// directly (bypassing errors.As) would see no ExitCode() method on
// *reportedExitError and silently fall through to a wrong/default code.
//
// Delegating to ExitCode(e.err) — rather than returning 0 when e.err is not
// itself an ExitCoder — matters because reportedExitError always wraps a
// non-nil error (markReported short-circuits nil). A wrapped plain error is
// still an ordinary failure and must report the package's non-zero failure
// code (currently 1), never 0: returning 0 here would make a reported
// failure look like success to any caller that stops at this wrapper.
func (e *reportedExitError) ExitCode() int {
	return ExitCode(e.err)
}

// reportedToUser marks errors whose command path already emitted a
// sanitized, user-facing diagnostic. The root execution wrapper must not
// print the raw error again or change the command's documented output
// contract.
func (*reportedExitError) reportedToUser() {}

func (e *ExitCodeError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return ""
}

func (e *ExitCodeError) ExitCode() int {
	return e.Code
}

func (e *ExitCodeError) Unwrap() error {
	return e.Err
}

// ExitCode derives the exit status code from err.
// Returns 0 if err is nil.
// If err satisfies ExitCoder and returns a non-zero code, that code is returned.
// Otherwise returns 1 for non-nil errors (preserving generic exit 1 contract).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec ExitCoder
	if errors.As(err, &ec) {
		code := ec.ExitCode()
		if code != 0 {
			return code
		}
	}
	return 1
}
