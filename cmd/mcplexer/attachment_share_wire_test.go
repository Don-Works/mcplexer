// attachment_share_wire_test.go — coverage for the per-workspace
// authorization on the cross-peer attachment provider. An attachment id is
// not an authorization: a paired peer may only fetch attachments in a
// workspace it is bound to, and a denial must be indistinguishable from a
// missing id (ErrAttachmentNotFound).
package main

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// attBindingStore implements only the store method peerBoundToWorkspace calls;
// the embedded nil store.Store satisfies the rest of the interface at compile
// time (any other call would panic, which the test never triggers).
type attBindingStore struct {
	store.Store
	bindings map[string][]string
}

func (f attBindingStore) ListLocalWorkspaceIDsForPeer(_ context.Context, peerID string) ([]string, error) {
	return f.bindings[peerID], nil
}

func TestAttachmentProviderPeerBoundToWorkspace(t *testing.T) {
	p := &attachmentShareProvider{store: attBindingStore{bindings: map[string][]string{
		"peer-a": {"ws-shared", "ws-other"},
	}}}

	cases := []struct {
		name      string
		peerID    string
		workspace string
		want      bool
	}{
		{"bound workspace", "peer-a", "ws-shared", true},
		{"second bound workspace", "peer-a", "ws-other", true},
		{"unbound workspace", "peer-a", "ws-private", false},
		{"unknown peer", "peer-b", "ws-shared", false},
		{"empty workspace never matches", "peer-a", "", false},
		{"empty peer never matches", "", "ws-shared", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.peerBoundToWorkspace(context.Background(), tc.peerID, tc.workspace); got != tc.want {
				t.Fatalf("peerBoundToWorkspace(%q, %q) = %v, want %v", tc.peerID, tc.workspace, got, tc.want)
			}
		})
	}
}
