package command

import "errors"

// ExitCoder is implemented by errors that specify an OS exit status code.
type ExitCoder interface {
	ExitCode() int
}

// ExitCodeError wraps an underlying error with a specific exit code.
type ExitCodeError struct {
	Code int
	Err  error
}

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
