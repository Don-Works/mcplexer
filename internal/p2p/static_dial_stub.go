//go:build !p2p

package p2p

import "context"

// Stub variants for the slim (non-p2p) build. No host exists, so persisting
// or re-dialing static addresses is a no-op.

// PersistStaticDial is a no-op in stub mode.
func (h *Host) PersistStaticDial(_ string) error { return nil }

// RedialStatic is a no-op in stub mode and connects nothing.
func (h *Host) RedialStatic(_ context.Context) int { return 0 }
