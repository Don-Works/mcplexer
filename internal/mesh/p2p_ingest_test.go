package mesh

// Ingest parity coverage: inbound libp2p envelopes must pass the same
// content gates as a local Send (validKind, non-blank, size ceiling), and
// the sender's priority must be carried through with unknown values clamped
// to "normal" (it used to be dropped — every cross-peer message landed as
// normal/2h TTL).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

func TestIngestEnvelopeAppliesSendGates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	cases := []struct {
		name         string
		env          p2p.MeshEnvelope
		wantStored   bool
		wantPriority string
	}{
		{
			name: "valid finding with high priority carries through",
			env: p2p.MeshEnvelope{
				ID: "01INGESThi", SenderPeerID: "peerA",
				Kind: "finding", Content: "remote insight", Priority: "high",
			},
			wantStored:   true,
			wantPriority: "high",
		},
		{
			name: "missing priority defaults to normal (legacy peers)",
			env: p2p.MeshEnvelope{
				ID: "01INGESTno", SenderPeerID: "peerA",
				Kind: "alert", Content: "remote alert",
			},
			wantStored:   true,
			wantPriority: "normal",
		},
		{
			name: "unknown priority clamps to normal",
			env: p2p.MeshEnvelope{
				ID: "01INGESTbg", SenderPeerID: "peerA",
				Kind: "result", Content: "remote result", Priority: "apocalyptic",
			},
			wantStored:   true,
			wantPriority: "normal",
		},
		{
			name: "invalid kind is dropped",
			env: p2p.MeshEnvelope{
				ID: "01INGESTik", SenderPeerID: "peerA",
				Kind: "file_claim", Content: "claiming foo.go", Priority: "normal",
			},
		},
		{
			name: "blank content is dropped",
			env: p2p.MeshEnvelope{
				ID: "01INGESTbl", SenderPeerID: "peerA",
				Kind: "finding", Content: "  \n\t ", Priority: "normal",
			},
		},
		{
			name: "oversize content is dropped",
			env: p2p.MeshEnvelope{
				ID: "01INGESTov", SenderPeerID: "peerA",
				Kind: "finding", Content: strings.Repeat("x", MaxSendContentBytes+1),
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tc.env.TS = time.Now().UnixMilli()
			if err := mgr.ingestEnvelope(ctx, tc.env); err != nil {
				t.Fatalf("ingestEnvelope: %v", err)
			}
			got, err := db.GetMeshMessage(ctx, tc.env.ID)
			if !tc.wantStored {
				if err == nil {
					t.Fatalf("envelope %q must be dropped, but was stored: %+v", tc.env.ID, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected stored message %q: %v", tc.env.ID, err)
			}
			if got.Priority != tc.wantPriority {
				t.Errorf("priority = %q, want %q", got.Priority, tc.wantPriority)
			}
			if got.ActorKind != "peer-import" {
				t.Errorf("actor_kind = %q, want peer-import", got.ActorKind)
			}
		})
	}
}

// TestIngestEnvelopePriorityDrivesTTL confirms the carried-through priority
// also drives the TTL bucket, not just the stored label.
func TestIngestEnvelopePriorityDrivesTTL(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	env := p2p.MeshEnvelope{
		ID: "01INGESTtt", SenderPeerID: "peerA",
		Kind: "alert", Content: "urgent", Priority: "critical",
		TS: time.Now().UnixMilli(),
	}
	if err := mgr.ingestEnvelope(ctx, env); err != nil {
		t.Fatalf("ingestEnvelope: %v", err)
	}
	got, err := db.GetMeshMessage(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetMeshMessage: %v", err)
	}
	wantMin := time.Now().UTC().Add(priorityTTL["critical"] - time.Minute)
	if got.ExpiresAt.Before(wantMin) {
		t.Fatalf("expires_at = %v, want >= %v (critical TTL)", got.ExpiresAt, wantMin)
	}
}

func TestClampPriority(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"critical": "critical",
		"high":     "high",
		"normal":   "normal",
		"low":      "low",
		"":         "normal",
		"bogus":    "normal",
	}
	for in, want := range cases {
		if got := clampPriority(in); got != want {
			t.Errorf("clampPriority(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIngestEnvelopeEnforcesCeiling verifies that inbound p2p messages
// obey the same live-message ceiling as local sends. A remote peer
// must not be able to grow live messages beyond the cap.
func TestIngestEnvelopeEnforcesCeiling(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)
	mgr.liveCeiling = 3

	for i := 0; i < 6; i++ {
		env := p2p.MeshEnvelope{
			ID:           fmt.Sprintf("01CEIL%02d", i),
			SenderPeerID: "peerA",
			Kind:         "event",
			Content:      fmt.Sprintf("remote msg %d", i),
			Priority:     "normal",
			TS:           time.Now().UnixMilli(),
		}
		if err := mgr.ingestEnvelope(ctx, env); err != nil {
			t.Fatalf("ingestEnvelope %d: %v", i, err)
		}
	}

	count, err := db.CountLiveMessages(ctx, "global")
	if err != nil {
		t.Fatalf("CountLiveMessages: %v", err)
	}
	if count > mgr.liveCeiling {
		t.Fatalf("p2p ingest live count = %d, want <= ceiling %d", count, mgr.liveCeiling)
	}
}
