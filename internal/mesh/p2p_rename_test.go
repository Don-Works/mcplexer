package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakePeerLister returns a fixed slice of P2PPeer rows. Used to exercise
// ResolveDeviceName without spinning up a sqlite store.
type fakePeerLister struct {
	peers []store.P2PPeer
}

func (f *fakePeerLister) ListPeers(_ context.Context) ([]store.P2PPeer, error) {
	return f.peers, nil
}

// TestResolveDeviceName_TrimsStoredDisplayName pins the fix for the bug where
// a paired-peer row carrying trailing whitespace in its display_name
// (legacy rows seeded from raw os.Hostname() pre-resolveSelfDisplayName fix —
// e.g. "peer-laptop.ts.net lan ") would silently fail to match user input
// "peer-laptop.ts.net lan" because the comparison was exact. The resolver now
// trims and lowercases both sides.
func TestResolveDeviceName_TrimsStoredDisplayName(t *testing.T) {
	t.Parallel()
	peers := []store.P2PPeer{
		{PeerID: "12D3KooWClean", DisplayName: "workstation"},
		{PeerID: "12D3KooWLegacy", DisplayName: "Mac.ts.net lan  "}, // trailing whitespace
		{PeerID: "12D3KooWUpper", DisplayName: "Elliot"},
	}
	mgr := NewManager(nil)
	mgr.SetPeerLister(&fakePeerLister{peers: peers})

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean exact match", "workstation", "12D3KooWClean"},
		{"legacy stored has trailing whitespace, input clean", "Mac.ts.net lan", "12D3KooWLegacy"},
		{"input has surrounding whitespace", "  workstation  ", "12D3KooWClean"},
		{"case-insensitive", "ELLIOT", "12D3KooWUpper"},
		{"unknown name returns empty", "ghost", ""},
		{"empty input returns empty", "", ""},
		{"peer-ID shape passes through unchanged",
			"12D3KooWArbitraryLooksLikeAPeerIDLongEnough",
			"12D3KooWArbitraryLooksLikeAPeerIDLongEnough"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mgr.ResolveDeviceName(context.Background(), tt.input)
			if got != tt.want {
				t.Fatalf("ResolveDeviceName(%q) = %q, want %q",
					tt.input, got, tt.want)
			}
		})
	}
}

// TestResolveDeviceName_SkipsRevoked guards the existing behavior that
// revoked peers cannot be addressed by name, even if their stored
// display_name matches. Pinned here because the trim change touched the
// same loop and could regress this contract.
func TestResolveDeviceName_SkipsRevoked(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	peers := []store.P2PPeer{
		{PeerID: "12D3KooWActive", DisplayName: "shared-name"},
		{PeerID: "12D3KooWRevoked", DisplayName: "shared-name", RevokedAt: &now},
	}
	mgr := NewManager(nil)
	mgr.SetPeerLister(&fakePeerLister{peers: peers})

	got := mgr.ResolveDeviceName(context.Background(), "shared-name")
	if got != "12D3KooWActive" {
		t.Fatalf("expected revoked row to be skipped; got %q (want 12D3KooWActive)", got)
	}
}
