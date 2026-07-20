package mesh

// Ingest parity coverage: inbound libp2p envelopes must pass the same
// content gates as a local Send (validULID, validKind, non-blank, size
// ceiling), and the sender's priority must be carried through with unknown
// values clamped to "normal" (it used to be dropped — every cross-peer
// message landed as normal/2h TTL).
//
// Envelope ids are peer-controlled, so the stored row id is re-minted
// locally at ingest and these tests look messages up by content, never by
// env.ID. See ingestEnvelope and p2p_ingest_cursor_test.go.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// findIngestedByContent returns the stored mesh message whose body equals
// content, or nil when the envelope was dropped.
func findIngestedByContent(t *testing.T, db store.MeshStore, content string) *store.MeshMessage {
	t.Helper()
	msgs, err := db.QueryMeshMessages(context.Background(), store.MeshMessageFilter{Limit: 500})
	if err != nil {
		t.Fatalf("QueryMeshMessages: %v", err)
	}
	for i := range msgs {
		if msgs[i].Content == content {
			return &msgs[i]
		}
	}
	return nil
}

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
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "finding", Content: "remote insight", Priority: "high",
			},
			wantStored:   true,
			wantPriority: "high",
		},
		{
			name: "missing priority defaults to normal (legacy peers)",
			env: p2p.MeshEnvelope{
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "alert", Content: "remote alert",
			},
			wantStored:   true,
			wantPriority: "normal",
		},
		{
			name: "unknown priority clamps to normal",
			env: p2p.MeshEnvelope{
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "result", Content: "remote result", Priority: "apocalyptic",
			},
			wantStored:   true,
			wantPriority: "normal",
		},
		{
			name: "invalid kind is dropped",
			env: p2p.MeshEnvelope{
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "file_claim", Content: "claiming foo.go", Priority: "normal",
			},
		},
		{
			name: "blank content is dropped",
			env: p2p.MeshEnvelope{
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "finding", Content: "  \n\t ", Priority: "normal",
			},
		},
		{
			name: "oversize content is dropped",
			env: p2p.MeshEnvelope{
				ID: newULID(), SenderPeerID: "peerA",
				Kind: "finding", Content: strings.Repeat("x", MaxSendContentBytes+1),
			},
		},
		{
			name: "non-ULID id is dropped",
			env: p2p.MeshEnvelope{
				ID: "zzz", SenderPeerID: "peerA",
				Kind: "finding", Content: "hostile id", Priority: "normal",
			},
		},
		{
			name: "empty id is dropped",
			env: p2p.MeshEnvelope{
				ID: "", SenderPeerID: "peerA",
				Kind: "finding", Content: "blank id", Priority: "normal",
			},
		},
		{
			name: "ULID-length non-base32 id is dropped",
			env: p2p.MeshEnvelope{
				ID: strings.Repeat("!", 26), SenderPeerID: "peerA",
				Kind: "finding", Content: "bad charset id", Priority: "normal",
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
			got := findIngestedByContent(t, db, tc.env.Content)
			if !tc.wantStored {
				if got != nil {
					t.Fatalf("envelope %q must be dropped, but was stored: %+v", tc.env.ID, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected stored message for envelope %q", tc.env.ID)
			}
			if got.Priority != tc.wantPriority {
				t.Errorf("priority = %q, want %q", got.Priority, tc.wantPriority)
			}
			if got.ActorKind != "peer-import" {
				t.Errorf("actor_kind = %q, want peer-import", got.ActorKind)
			}
			// The peer's id must never become the local row id — that is
			// what let a peer steer the receive cursor.
			if got.ID == tc.env.ID {
				t.Errorf("row id = env.ID (%q); inbound ids must be re-minted locally", got.ID)
			}
			if !validULID(got.ID) {
				t.Errorf("row id %q is not a ULID", got.ID)
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
		ID: newULID(), SenderPeerID: "peerA",
		Kind: "alert", Content: "urgent", Priority: "critical",
		TS: time.Now().UnixMilli(),
	}
	if err := mgr.ingestEnvelope(ctx, env); err != nil {
		t.Fatalf("ingestEnvelope: %v", err)
	}
	got := findIngestedByContent(t, db, env.Content)
	if got == nil {
		t.Fatal("expected the ingested message to be stored")
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
			ID:           newULID(),
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

	// Broadcast ingest lands in the global namespace (""), the bucket a
	// local to_workspace:"*" send resolves to — not the literal "global".
	count, err := db.CountLiveMessages(ctx, "")
	if err != nil {
		t.Fatalf("CountLiveMessages: %v", err)
	}
	if count > mgr.liveCeiling {
		t.Fatalf("p2p ingest live count = %d, want <= ceiling %d", count, mgr.liveCeiling)
	}
}
