package mesh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Test peer IDs are full-length-ish (>40 chars) and end in distinct
// 10-char suffixes so we can exercise the short-id match without
// accidental collisions. Format mirrors real libp2p Ed25519 ids.
const (
	peerAirFull = "12D3KooWAirOneTwoThreeFourFiveSixSevpLYmq366A7"
	peerPiFull  = "12D3KooWPiOneTwoThreeFourFiveSixSevenrpynr8M1cr"
	peerDupName = "12D3KooWDupOneTwoThreeFourFiveSixSevenEighnine9"
)

// TestResolvePeer covers the canonical input shapes: full id, 10-char
// short id, device name, and the error paths (unknown, ambiguous, empty,
// no lister wired). Table-driven so adding a new case is a single line.
func TestResolvePeer(t *testing.T) {
	t.Parallel()
	peers := []store.P2PPeer{
		{PeerID: peerAirFull, DisplayName: "peer-laptop"},
		{PeerID: peerPiFull, DisplayName: "mcplexer-pi"},
		// Two rows with the SAME display_name — exercises ErrAmbiguousPeer.
		{PeerID: peerDupName, DisplayName: "twins"},
		{PeerID: "12D3KooWAnotherTwinSecondOneSomethingElseHere99", DisplayName: "Twins"},
	}
	mgr := NewManager(nil)
	mgr.SetPeerLister(&fakePeerLister{peers: peers})

	tests := []struct {
		name      string
		input     string
		want      string
		wantErr   error
		errSubstr string
	}{
		{
			name:  "full peer id hits",
			input: peerAirFull,
			want:  peerAirFull,
		},
		{
			name:  "short suffix hits — mcplexer-pi",
			input: "rpynr8M1cr",
			want:  peerPiFull,
		},
		{
			name:  "short suffix hits — peer-laptop",
			input: "pLYmq366A7",
			want:  peerAirFull,
		},
		{
			name:  "device name hits — case-insensitive",
			input: "MCPLEXER-PI",
			want:  peerPiFull,
		},
		{
			name:  "device name hits — surrounding whitespace",
			input: "  peer-laptop  ",
			want:  peerAirFull,
		},
		{
			name:    "ambiguous display_name",
			input:   "twins",
			wantErr: ErrAmbiguousPeer,
		},
		{
			name:    "unknown input",
			input:   "nope",
			wantErr: ErrPeerNotPaired,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: ErrPeerNotPaired,
		},
		{
			name:    "whitespace-only input",
			input:   "   ",
			wantErr: ErrPeerNotPaired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mgr.ResolvePeer(context.Background(), tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolvePeer(%q) err = %v, want errors.Is(%v)",
						tt.input, err, tt.wantErr)
				}
				if got != "" {
					t.Fatalf("ResolvePeer(%q) returned id %q with err %v, want empty",
						tt.input, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePeer(%q) unexpected err: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ResolvePeer(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestResolvePeer_SkipsRevoked guards that a revoked peer is never
// returned, regardless of input shape. Mirrors the contract enforced
// by ResolveDeviceName_SkipsRevoked.
func TestResolvePeer_SkipsRevoked(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	peers := []store.P2PPeer{
		{PeerID: peerAirFull, DisplayName: "peer-laptop", RevokedAt: &now},
	}
	mgr := NewManager(nil)
	mgr.SetPeerLister(&fakePeerLister{peers: peers})

	for _, input := range []string{peerAirFull, "pLYmq366A7", "peer-laptop"} {
		_, err := mgr.ResolvePeer(context.Background(), input)
		if !errors.Is(err, ErrPeerNotPaired) {
			t.Fatalf("ResolvePeer(%q) on revoked row err = %v, want ErrPeerNotPaired",
				input, err)
		}
	}
}

// TestResolvePeer_NoLister covers the slim/test wiring where the Manager
// has no PeerLister. Must fail loudly rather than silently returning the
// raw input — the bug we're fixing was a silent fall-through to a raw
// id that the downstream validator then rejected with a stale message.
func TestResolvePeer_NoLister(t *testing.T) {
	t.Parallel()
	mgr := NewManager(nil)
	_, err := mgr.ResolvePeer(context.Background(), peerAirFull)
	if !errors.Is(err, ErrPeerNotPaired) {
		t.Fatalf("ResolvePeer with no lister err = %v, want ErrPeerNotPaired", err)
	}
}

// TestFormatPeerNotPairedError pins the user-facing wording so handlers
// don't drift. The "(try mesh__list_peers ...)" suffix is the contract
// — agents reading the error decide whether to call list_peers next.
func TestFormatPeerNotPairedError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		err   error
		want  string
	}{
		{
			name:  "not paired",
			input: "ghost",
			err:   ErrPeerNotPaired,
			want:  `no paired peer matches "ghost" (try mesh__list_peers to see paired devices)`,
		},
		{
			name:  "ambiguous",
			input: "twins",
			err:   ErrAmbiguousPeer,
			want:  `"twins" matches multiple paired peers — disambiguate by passing the full peer id (try mesh__list_peers to see paired devices)`,
		},
		{
			name:  "other error passes through",
			input: "x",
			err:   errors.New("kaboom"),
			want:  "kaboom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPeerNotPairedError(tt.input, tt.err)
			if got != tt.want {
				t.Fatalf("FormatPeerNotPairedError(%q, %v) = %q, want %q",
					tt.input, tt.err, got, tt.want)
			}
		})
	}
}
