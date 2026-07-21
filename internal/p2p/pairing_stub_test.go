//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"testing"
)

// TestPairingStubStartReturnsSentinel confirms the stub propagates
// ErrP2PNotBuiltIn so callers can branch on the sentinel and return a
// useful error to the user.
func TestPairingStubStartReturnsSentinel(t *testing.T) {
	t.Parallel()
	s := NewPairingService(nil, nil)
	res, err := s.StartPair(context.Background())
	if res != nil {
		t.Fatalf("StartPair stub result = %v, want nil", res)
	}
	if !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("StartPair stub err = %v, want ErrP2PNotBuiltIn", err)
	}
}

// TestPairingStubCompleteReturnsSentinel mirrors the start-side check for
// CompletePair so both REST endpoints are guaranteed-501 in stub builds.
func TestPairingStubCompleteReturnsSentinel(t *testing.T) {
	t.Parallel()
	s := NewPairingService(nil, nil)
	err := s.CompletePair(context.Background(), "123456", "12D3KooWfake", nil)
	if !errors.Is(err, ErrP2PNotBuiltIn) {
		t.Fatalf("CompletePair stub err = %v, want ErrP2PNotBuiltIn", err)
	}
}
