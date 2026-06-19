package replication_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/replication"
)

// PushTask lets recordingPusher (defined in replication_test.go) also
// satisfy replication.TaskPusher. Records under kind "task" so the
// existing snapshot/assert helpers work unchanged.
func (p *recordingPusher) PushTask(_ context.Context, peerID, _ /*workspaceID*/, taskID string) error {
	return p.record("task", peerID, taskID)
}

// fakeLinkLister returns canned linked-peer sets per local workspace.
type fakeLinkLister struct {
	links map[string][]string // workspaceID -> linked peer ids
}

func (f *fakeLinkLister) LinkedPeersForWorkspace(_ context.Context, workspaceID string) ([]string, error) {
	return f.links[workspaceID], nil
}

// countKind tallies push calls of a given kind from a snapshot.
func countKind(calls []pushCall, kind string) int {
	n := 0
	for _, c := range calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// TestTaskReplicatesOnlyToLinkedPeers is the headline test for the
// linked-workspace task replication path. A task mutation in a LINKED
// workspace pushes to the linked peer; a mutation in an UNLINKED
// workspace pushes to nobody.
func TestTaskReplicatesOnlyToLinkedPeers(t *testing.T) {
	peers := []replication.PeerInfo{{PeerID: "peer-B"}, {PeerID: "peer-C"}}
	c, pusher, _ := makeCoordinator(t, peers, map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
		"peer-C": consent.TierSameUser,
	})
	c.SetTaskReplication(pusher, &fakeLinkLister{links: map[string][]string{
		// ws-gateway is linked to peer-B only; peer-C is paired but NOT linked.
		"ws-gateway": {"peer-B"},
	}})

	// Mutation in the linked workspace → queues for peer-B only.
	c.OnTaskEvent(context.Background(), "ws-gateway", "task-1", "agent")
	if got := c.QueueDepth("peer-B"); got != 1 {
		t.Fatalf("peer-B queue depth = %d, want 1", got)
	}
	if got := c.QueueDepth("peer-C"); got != 0 {
		t.Fatalf("peer-C (paired, unlinked) queue depth = %d, want 0", got)
	}

	// Mutation in a workspace with no link → nobody.
	c.OnTaskEvent(context.Background(), "ws-personal", "task-2", "agent")
	if got := c.QueueDepth("peer-B"); got != 1 {
		t.Fatalf("unlinked-workspace mutation changed peer-B depth to %d, want 1", got)
	}

	c.DrainOnce(context.Background())
	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 task push after drain, got %d", len(pusher.snapshot()))
	}
	calls := pusher.snapshot()
	if n := countKind(calls, "task"); n != 1 {
		t.Fatalf("task push count = %d, want 1 (calls=%+v)", n, calls)
	}
	if calls[0].Peer != "peer-B" || calls[0].ID != "task-1" {
		t.Fatalf("unexpected push target: %+v", calls[0])
	}
}

// TestTaskReplicationEchoGuard drops peer-origin mutations so a task
// received from a peer is never replicated back out.
func TestTaskReplicationEchoGuard(t *testing.T) {
	peers := []replication.PeerInfo{{PeerID: "peer-B"}}
	c, _, _ := makeCoordinator(t, peers, map[string]consent.Tier{"peer-B": consent.TierSameUser})
	c.SetTaskReplication(&recordingPusher{}, &fakeLinkLister{links: map[string][]string{
		"ws-gateway": {"peer-B"},
	}})

	c.OnTaskEvent(context.Background(), "ws-gateway", "task-1", "peer")
	if got := c.QueueDepth("peer-B"); got != 0 {
		t.Fatalf("peer-origin mutation enqueued (depth=%d), echo guard failed", got)
	}
}

// TestTaskReplicationOptOut honours the per-peer opt-out scope even for
// an explicitly linked workspace.
func TestTaskReplicationOptOut(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B", Scopes: []string{replication.ReplicationOptOutScope}},
	}
	c, _, _ := makeCoordinator(t, peers, map[string]consent.Tier{"peer-B": consent.TierSameUser})
	c.SetTaskReplication(&recordingPusher{}, &fakeLinkLister{links: map[string][]string{
		"ws-gateway": {"peer-B"},
	}})

	c.OnTaskEvent(context.Background(), "ws-gateway", "task-1", "agent")
	if got := c.QueueDepth("peer-B"); got != 0 {
		t.Fatalf("opted-out peer enqueued (depth=%d)", got)
	}
}

// TestTaskReplicationDisabledWithoutWiring keeps OnTaskEvent a no-op
// when SetTaskReplication was never called (slim build / no task svc).
func TestTaskReplicationDisabledWithoutWiring(t *testing.T) {
	peers := []replication.PeerInfo{{PeerID: "peer-B"}}
	c, _, _ := makeCoordinator(t, peers, map[string]consent.Tier{"peer-B": consent.TierSameUser})
	// No SetTaskReplication call.
	c.OnTaskEvent(context.Background(), "ws-gateway", "task-1", "agent")
	if got := c.QueueDepth("peer-B"); got != 0 {
		t.Fatalf("task enqueued without wiring (depth=%d)", got)
	}
}
