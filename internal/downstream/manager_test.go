package downstream

import (
	"errors"
	"testing"
)

func TestIsAuthError_True(t *testing.T) {
	if !isAuthError(ErrAuthRequired) {
		t.Error("expected isAuthError(ErrAuthRequired) to return true")
	}
}

func TestIsAuthError_Wrapped(t *testing.T) {
	wrapped := errors.Join(errors.New("outer"), ErrAuthRequired)
	if !isAuthError(wrapped) {
		t.Error("expected isAuthError to detect wrapped ErrAuthRequired")
	}
}

func TestIsAuthError_False(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"nil error", nil},
		{"generic error", errors.New("something went wrong")},
		{"different sentinel", errors.New("connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isAuthError(tt.err) {
				t.Errorf("isAuthError(%v) = true, want false", tt.err)
			}
		})
	}
}
