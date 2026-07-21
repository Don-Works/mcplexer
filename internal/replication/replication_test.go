package replication_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/replication"
)

// fakeTierResolver returns canned tiers per peer-id. Unknown peers
// default to cross-org (matches the production NopResolver posture).
type fakeTierResolver struct {
	tiers map[string]consent.Tier
}

func (f *fakeTierResolver) TierFor(_ context.Context, peerID string) consent.Tier {
	if t, ok := f.tiers[peerID]; ok {
		return t
	}
	return consent.TierCrossOrg
}

// fakePeerLister returns a static peer list. Tests mutate Peers
// between events to model pair/unpair.
type fakePeerLister struct {
	mu    sync.Mutex
	Peers []replication.PeerInfo
}

func (f *fakePeerLister) ListActivePairedPeers(_ context.Context) ([]replication.PeerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]replication.PeerInfo, len(f.Peers))
	copy(out, f.Peers)
	return out, nil
}

// recordingPusher captures every push request so the test can assert
// who got what.
type recordingPusher struct {
	mu    sync.Mutex
	calls []pushCall
	// errFor lets a test inject an error for a specific (peerID,id).
	errFor map[string]error
}

type pushCall struct {
	Peer string
	Kind string
	ID   string
}

func (p *recordingPusher) PushMemory(_ context.Context, peerID, memoryID string) error {
	return p.record("memory", peerID, memoryID)
}

func (p *recordingPusher) PushSkill(_ context.Context, peerID, skillName string) error {
	return p.record("skill", peerID, skillName)
}

func (p *recordingPusher) record(kind, peerID, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, pushCall{Peer: peerID, Kind: kind, ID: id})
	if p.errFor != nil {
		if err, ok := p.errFor[peerID+":"+id]; ok {
			return err
		}
	}
	return nil
}

func (p *recordingPusher) snapshot() []pushCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pushCall, len(p.calls))
	copy(out, p.calls)
	return out
}

// makeCoordinator wires a coordinator with two-peer test fixtures.
// Returns the recorder so the test can assert on push calls.
func makeCoordinator(t *testing.T, peers []replication.PeerInfo, tiers map[string]consent.Tier) (
	*replication.Coordinator, *recordingPusher, *fakePeerLister,
) {
	t.Helper()
	resolver := &fakeTierResolver{tiers: tiers}
	lister := &fakePeerLister{Peers: peers}
	pusher := &recordingPusher{}
	c := replication.NewCoordinator(
		resolver, lister, pusher, pusher, replication.Config{
			// Long interval so the test drives drains explicitly via
			// DrainOnce — the ticker doesn't interfere.
			BatchInterval: 1 * time.Hour,
		},
	)
	if c == nil {
		t.Fatal("NewCoordinator returned nil")
	}
	t.Cleanup(c.Stop)
	return c, pusher, lister
}

// TestMemoryWriteReplicatesToTier1Peer is the headline test for task
// 01KSM6D2FEWEMZHDA6VS64WPMS. A local write on machine A queues a
// push to the paired same-user peer B; the next drain dispatches it
// via the (mocked) libp2p pusher.
func TestMemoryWriteReplicatesToTier1Peer(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	c.OnMemoryEvent(context.Background(), "write", "mem-001", "agent")

	if depth := c.QueueDepth("peer-B"); depth != 1 {
		t.Fatalf("expected queue depth 1 before drain, got %d", depth)
	}
	c.DrainOnce(context.Background())
	// dispatch goroutine is async; poll briefly.
	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 push after drain, got %d", len(pusher.snapshot()))
	}
	calls := pusher.snapshot()
	if calls[0].Peer != "peer-B" || calls[0].Kind != "memory" || calls[0].ID != "mem-001" {
		t.Fatalf("wrong push: %+v", calls[0])
	}
}

// TestOnlyTier1PeersReceiveReplication asserts the tier gate: a same-
// user peer gets the push, a same-org and a cross-org peer do not.
// This is the load-bearing safety check — silent replication MUST
// NOT leak data to peers outside the same-user trust boundary.
func TestOnlyTier1PeersReceiveReplication(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-tier1"},
		{PeerID: "peer-tier2"},
		{PeerID: "peer-tier3"},
		{PeerID: "peer-unknown"},
	}
	tiers := map[string]consent.Tier{
		"peer-tier1": consent.TierSameUser,
		"peer-tier2": consent.TierSameOrg,
		"peer-tier3": consent.TierCrossOrg,
		// peer-unknown intentionally absent → resolver returns cross_org
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	c.OnMemoryEvent(context.Background(), "write", "mem-001", "agent")
	c.DrainOnce(context.Background())

	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 push, got %d", len(pusher.snapshot()))
	}
	calls := pusher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 push (tier1 only), got %d: %+v", len(calls), calls)
	}
	if calls[0].Peer != "peer-tier1" {
		t.Fatalf("expected push to peer-tier1, got %s", calls[0].Peer)
	}
}

// TestPeerOriginWriteDoesNotReplicate locks in the echo-prevention
// guarantee: receiving a memory from peer A and then re-broadcasting
// it to peer B would create a flood storm. The coordinator drops
// events whose Source=="peer".
func TestPeerOriginWriteDoesNotReplicate(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	c.OnMemoryEvent(context.Background(), "write", "mem-001", "peer")
	c.DrainOnce(context.Background())

	// Give any phantom goroutine a chance to fire.
	time.Sleep(50 * time.Millisecond)
	if calls := pusher.snapshot(); len(calls) != 0 {
		t.Fatalf("peer-origin write was replicated: %+v", calls)
	}
}

// TestOnlyWriteKindReplicates locks in "we don't replicate invalidate
// / delete / link / pin events". Those are observability events; the
// next write on the originating peer covers the substantive state
// change.
func TestOnlyWriteKindReplicates(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	for _, kind := range []string{"invalidate", "delete", "link_entity", "pin", "offer_received"} {
		c.OnMemoryEvent(context.Background(), kind, "mem-001", "agent")
	}
	c.DrainOnce(context.Background())

	time.Sleep(50 * time.Millisecond)
	if calls := pusher.snapshot(); len(calls) != 0 {
		t.Fatalf("non-write event was replicated: %+v", calls)
	}
}

// TestSkillInstallReplicates covers the second task
// (01KSM6YYNJ0T6C5TK1ZDABK4EW): a local skill install fans out to
// Tier-1 paired peers. Receiving-side installs (peerOriginInstall=
// true) MUST NOT re-fan-out.
func TestSkillInstallReplicates(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
		{PeerID: "peer-C"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
		"peer-C": consent.TierSameOrg, // should NOT receive
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	c.OnSkillInstall(context.Background(), "my-skill", false)
	c.DrainOnce(context.Background())

	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 push after skill install, got %d", len(pusher.snapshot()))
	}
	calls := pusher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 push (tier1 only), got %d: %+v", len(calls), calls)
	}
	if calls[0].Kind != "skill" || calls[0].ID != "my-skill" || calls[0].Peer != "peer-B" {
		t.Fatalf("wrong push: %+v", calls[0])
	}

	// Peer-origin install should NOT replicate (echo prevention).
	c.OnSkillInstall(context.Background(), "another-skill", true)
	c.DrainOnce(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := len(pusher.snapshot()); got != 1 {
		t.Fatalf("peer-origin install was replicated: total calls=%d", got)
	}
}

// TestOptOutScopeSkipsPeer locks in the opt-out path: a same-user
// peer that has been granted ReplicationOptOutScope is silently
// skipped at enqueue time. The other Tier-1 peer still gets the push.
func TestOptOutScopeSkipsPeer(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-optedout", Scopes: []string{replication.ReplicationOptOutScope}},
		{PeerID: "peer-active"},
	}
	tiers := map[string]consent.Tier{
		"peer-optedout": consent.TierSameUser,
		"peer-active":   consent.TierSameUser,
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	c.OnMemoryEvent(context.Background(), "write", "mem-001", "agent")
	c.DrainOnce(context.Background())

	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 push after drain, got %d", len(pusher.snapshot()))
	}
	calls := pusher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 push (opt-out skipped), got %d: %+v", len(calls), calls)
	}
	if calls[0].Peer != "peer-active" {
		t.Fatalf("expected push to peer-active, got %s", calls[0].Peer)
	}
}

// TestQueueDedupsByID asserts that two writes to the same memory id
// within one batch collapse into a single push. This keeps a hot
// loop (e.g. an agent updating the same fact 20 times in 5s) from
// shipping 20 redundant payloads.
func TestQueueDedupsByID(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
	}
	c, pusher, _ := makeCoordinator(t, peers, tiers)

	for i := 0; i < 5; i++ {
		c.OnMemoryEvent(context.Background(), "write", "mem-001", "agent")
	}
	if depth := c.QueueDepth("peer-B"); depth != 1 {
		t.Fatalf("expected depth 1 after 5 same-id writes, got %d", depth)
	}
	c.DrainOnce(context.Background())
	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 push, got %d", len(pusher.snapshot()))
	}
	if got := len(pusher.snapshot()); got != 1 {
		t.Fatalf("expected exactly 1 push, got %d", got)
	}
}

// TestPushErrorIsLoggedNotPropagated asserts the coordinator is best-
// effort: a pusher that returns an error doesn't crash the loop and
// doesn't re-queue (matches the "next write replays state" model).
func TestPushErrorIsLoggedNotPropagated(t *testing.T) {
	peers := []replication.PeerInfo{
		{PeerID: "peer-B"},
	}
	tiers := map[string]consent.Tier{
		"peer-B": consent.TierSameUser,
	}
	pusher := &recordingPusher{
		errFor: map[string]error{
			"peer-B:mem-001": errors.New("simulated wire failure"),
		},
	}
	resolver := &fakeTierResolver{tiers: tiers}
	lister := &fakePeerLister{Peers: peers}
	c := replication.NewCoordinator(
		resolver, lister, pusher, pusher, replication.Config{
			BatchInterval: 1 * time.Hour,
		},
	)
	if c == nil {
		t.Fatal("NewCoordinator returned nil")
	}
	t.Cleanup(c.Stop)
	c.OnMemoryEvent(context.Background(), "write", "mem-001", "agent")
	c.DrainOnce(context.Background())

	if !waitForCalls(pusher, 1, time.Second) {
		t.Fatalf("expected 1 attempted push, got %d", len(pusher.snapshot()))
	}
	// Second drain must not re-attempt — we don't re-queue on error.
	c.DrainOnce(context.Background())
	time.Sleep(50 * time.Millisecond)
	if got := len(pusher.snapshot()); got != 1 {
		t.Fatalf("expected exactly 1 attempted push (no re-queue), got %d", got)
	}
}

// TestNilDependenciesReturnNilCoordinator locks in the defensive
// constructor: if the daemon hasn't wired one of the pushers (slim
// build, mis-config), NewCoordinator returns nil and life continues
// without auto-replication.
func TestNilDependenciesReturnNilCoordinator(t *testing.T) {
	resolver := &fakeTierResolver{}
	lister := &fakePeerLister{}
	pusher := &recordingPusher{}

	cases := []struct {
		name string
		c    *replication.Coordinator
	}{
		{"nil_tiers", replication.NewCoordinator(nil, lister, pusher, pusher, replication.Config{})},
		{"nil_peers", replication.NewCoordinator(resolver, nil, pusher, pusher, replication.Config{})},
		{"nil_memPush", replication.NewCoordinator(resolver, lister, nil, pusher, replication.Config{})},
		{"nil_skillPush", replication.NewCoordinator(resolver, lister, pusher, nil, replication.Config{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.c != nil {
				t.Fatalf("expected nil coordinator")
			}
			// nil-safe surface check: calls must not panic.
			tc.c.OnMemoryEvent(context.Background(), "write", "x", "agent")
			tc.c.OnSkillInstall(context.Background(), "y", false)
			tc.c.Stop()
		})
	}
}

// waitForCalls polls until pusher has accumulated >= want calls or
// timeout elapses. Returns true iff the count reached want. Used
// instead of fixed sleeps so a fast CI doesn't oversleep + a slow one
// doesn't flake.
func waitForCalls(p *recordingPusher, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(p.snapshot()) >= want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
