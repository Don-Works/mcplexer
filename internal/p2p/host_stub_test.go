//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"testing"
)

// TestNewHostStubDisabled verifies that the disabled path is the same in both
// build modes: NewHost(Enabled=false) is a no-op.
func TestNewHostStubDisabled(t *testing.T) {
	t.Parallel()
	h, err := NewHost(context.Background(), Config{Enabled: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewHost(disabled): %v", err)
	}
	if h != nil {
		t.Fatalf("NewHost(disabled) = %v, want nil", h)
	}
}

// TestNewHostStubEnabledReturnsSentinel verifies that asking for p2p in a
// non-p2p build returns ErrP2PNotBuiltIn — the sentinel callers can branch
// on to surface a useful error instead of silently doing nothing.
func TestNewHostStubEnabledReturnsSentinel(t *testing.T) {
	t.Parallel()
	h, err := NewHost(context.Background(), Config{Enabled: true}, nil, nil)
	if h != nil {
		t.Fatalf("NewHost(enabled, stub) = %v, want nil", h)
	}
	if !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("NewHost(enabled, stub) err = %v, want ErrP2PNotBuiltIn", err)
	}
}

// TestStubAccessorsSafe checks that the stub Host's accessor methods don't
// panic on a nil receiver — defensive guard for callers that hold a *Host
// returned from a successful NewHost(disabled) (which is nil).
func TestStubAccessorsSafe(t *testing.T) {
	t.Parallel()
	var h *Host
	if got := h.PeerID(); got != "" {
		t.Fatalf("nil.PeerID() = %q, want \"\"", got)
	}
	if got := h.Addrs(); got != nil {
		t.Fatalf("nil.Addrs() = %v, want nil", got)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("nil.Close() = %v, want nil", err)
	}
}
