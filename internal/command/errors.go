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
