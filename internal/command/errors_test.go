package command_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tranceh2/shep/internal/command"
)

func TestExitCode(t *testing.T) {
	innerErr := errors.New("underlying issue")

	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "nil error returns exit 0",
			err:      nil,
			wantCode: 0,
		},
		{
			name:     "unknown generic error returns exit 1",
			err:      errors.New("unknown error"),
			wantCode: 1,
		},
		{
			name:     "ExitCodeError code 2 direct",
			err:      &command.ExitCodeError{Code: 2, Err: innerErr},
			wantCode: 2,
		},
		{
			name:     "ExitCodeError code 3 wrapped",
			err:      fmt.Errorf("wrapped context: %w", &command.ExitCodeError{Code: 3, Err: innerErr}),
			wantCode: 3,
		},
		{
			name:     "ExitCodeError code 4 wrapped",
			err:      fmt.Errorf("wrapped context: %w", &command.ExitCodeError{Code: 4, Err: innerErr}),
			wantCode: 4,
		},
		{
			name:     "ExitCodeError code 5 wrapped",
			err:      fmt.Errorf("wrapped context: %w", &command.ExitCodeError{Code: 5, Err: innerErr}),
			wantCode: 5,
		},
		{
			name:     "ExitCodeError code 6 wrapped",
			err:      fmt.Errorf("wrapped context: %w", &command.ExitCodeError{Code: 6, Err: innerErr}),
			wantCode: 6,
		},
		{
			name:     "non-nil error with Code 0 must NOT return success (falls back to 1)",
			err:      &command.ExitCodeError{Code: 0, Err: innerErr},
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := command.ExitCode(tt.err)
			if got != tt.wantCode {
				t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

func TestExitCodeError_ErrorAndUnwrap(t *testing.T) {
	base := errors.New("base error")
	ecErr := &command.ExitCodeError{Code: 4, Err: base}

	if ecErr.ExitCode() != 4 {
		t.Errorf("ExitCode() = %d, want 4", ecErr.ExitCode())
	}
	if !errors.Is(ecErr, base) {
		t.Errorf("Unwrap / errors.Is failed to find base error")
	}
	if ecErr.Error() == "" {
		t.Errorf("Error() returned empty string")
	}
}
