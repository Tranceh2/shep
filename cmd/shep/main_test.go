package main

import (
	"errors"
	"fmt"
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
