package api

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMergeMeshCounts(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	ts := []store.TimeSeriesPoint{
		{Bucket: now},
		{Bucket: now.Add(time.Minute)},
		{Bucket: now.Add(2 * time.Minute)},
	}
	counts := map[int64]int{
		ts[0].Bucket.Unix(): 3,
		ts[2].Bucket.Unix(): 7,
		// no entry for ts[1] — should stay at zero
	}
	mergeMeshCounts(ts, counts)
	if ts[0].MeshMessages != 3 {
		t.Errorf("ts[0].MeshMessages = %d, want 3", ts[0].MeshMessages)
	}
	if ts[1].MeshMessages != 0 {
		t.Errorf("ts[1].MeshMessages = %d, want 0", ts[1].MeshMessages)
	}
	if ts[2].MeshMessages != 7 {
		t.Errorf("ts[2].MeshMessages = %d, want 7", ts[2].MeshMessages)
	}
}

func TestMergeMeshCounts_EmptyMap(t *testing.T) {
	ts := []store.TimeSeriesPoint{
		{MeshMessages: 5}, // pre-existing value
	}
	mergeMeshCounts(ts, nil)
	if ts[0].MeshMessages != 5 {
		t.Errorf("empty counts shouldn't reset existing values, got %d", ts[0].MeshMessages)
	}
}

func TestCountPeerStates(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second)
	stale := now.Add(-1 * time.Hour)
	veryStale := now.Add(-24 * time.Hour)

	peers := []store.P2PPeer{
		{PeerID: "online-1", LastSeen: &recent},
		{PeerID: "online-2", LastSeen: &recent},
		{PeerID: "offline-1", LastSeen: &stale},
		{PeerID: "offline-2", LastSeen: &veryStale},
		{PeerID: "never-seen"}, // LastSeen nil — counts as offline
	}

	online, total := countPeerStates(peers, now)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if online != 2 {
		t.Errorf("online = %d, want 2 (only peers within %s window)", online, peerOnlineWindow)
	}
}

func TestCountPeerStates_Empty(t *testing.T) {
	online, total := countPeerStates(nil, time.Now())
	if online != 0 || total != 0 {
		t.Errorf("empty peers: got online=%d total=%d, want 0/0", online, total)
	}
}
