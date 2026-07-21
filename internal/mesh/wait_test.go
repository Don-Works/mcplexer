package mesh_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

const waitWS = "global"

// newWaitMgr spins up an in-memory-ish sqlite Manager and registers a waiter
// agent named `name` with `role` so resolveLocalAgent can find it. Returns the
// manager, the waiter's session meta, and the resolved session_id.
func newWaitMgr(t *testing.T, name, role string) (*mesh.Manager, mesh.SessionMeta) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "wait.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	meta := mesh.SessionMeta{
		SessionID:    "sess-" + name,
		WorkspaceIDs: []string{waitWS},
		ClientType:   "test",
	}
	// Register the waiter (mirrors mesh__receive(name:...)) so it resolves.
	if _, err := mgr.Receive(ctx, meta, mesh.ReceiveRequest{Name: name, Role: role}); err != nil {
		t.Fatalf("register waiter: %v", err)
	}
	return mgr, meta
}

// send is a tiny helper that pushes a message from a distinct sender session.
func sendMsg(t *testing.T, mgr *mesh.Manager, req mesh.SendRequest) *store.MeshMessage {
	t.Helper()
	sender := mesh.SessionMeta{
		SessionID:    "sender-1",
		WorkspaceIDs: []string{waitWS},
		ClientType:   "sender",
	}
	msg, err := mgr.Send(context.Background(), sender, req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return msg
}

// TestWaitImmediateMatch: a message already past the cursor returns at once.
func TestWaitImmediateMatch(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "alice", "dev")
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "hi alice", Audience: meta.SessionID})

	crit := mesh.WaitCriteria{AgentName: "alice", WorkspaceID: waitWS}
	got, err := mgr.WaitForMessage(context.Background(), crit, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForMessage: %v", err)
	}
	if len(got) != 1 || got[0].Content != "hi alice" {
		t.Fatalf("want 1 matched message, got %d (%+v)", len(got), got)
	}
}

// TestWaitBlocksThenWakes: wait starts with nothing pending, then a targeted
// Send wakes it. Asserts it did NOT return before the Send.
func TestWaitBlocksThenWakes(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "bob", "dev")

	type res struct {
		msgs []*store.MeshMessage
		err  error
	}
	done := make(chan res, 1)
	go func() {
		m, e := mgr.WaitForMessage(context.Background(),
			mesh.WaitCriteria{AgentName: "bob", WorkspaceID: waitWS}, 5*time.Second)
		done <- res{m, e}
	}()

	// It must still be blocked shortly after start (nothing pending).
	select {
	case r := <-done:
		t.Fatalf("wait returned before any Send: %d msgs err=%v", len(r.msgs), r.err)
	case <-time.After(150 * time.Millisecond):
	}

	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "wake bob", Audience: meta.SessionID})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("wait err: %v", r.err)
		}
		if len(r.msgs) != 1 || r.msgs[0].Content != "wake bob" {
			t.Fatalf("want wake bob, got %+v", r.msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not wake within deadline")
	}
}

// TestWaitTagFilter: a non-matching tag must NOT wake; the right tag wakes.
func TestWaitTagFilter(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "carol", "dev")

	// Non-matching tag: should time out.
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "nope", Audience: meta.SessionID, Tags: "other"})
	crit := mesh.WaitCriteria{AgentName: "carol", WorkspaceID: waitWS, Tags: []string{"urgent"}}
	got, err := mgr.WaitForMessage(context.Background(), crit, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tag filter should have excluded; got %d", len(got))
	}

	// Matching tag now present.
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "yes", Audience: meta.SessionID, Tags: "urgent,foo"})
	got, err = mgr.WaitForMessage(context.Background(), crit, 2*time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "yes" {
		t.Fatalf("want tagged match, got %+v", got)
	}
}

func TestWaitAllTagsRequiresEveryTag(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "cora", "dev")

	crit := mesh.WaitCriteria{
		AgentName:   "cora",
		WorkspaceID: waitWS,
		AllTags:     []string{"task_event:status_changed", "status_to:review"},
	}
	sendMsg(t, mgr, mesh.SendRequest{
		Kind: "task_event", Content: "not enough", Audience: meta.SessionID,
		Tags: "task_event:status_changed,status_to:doing",
	})
	got, err := mgr.WaitForMessage(context.Background(), crit, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if got != nil {
		t.Fatalf("all_tags should have excluded partial match, got %+v", got)
	}

	sendMsg(t, mgr, mesh.SendRequest{
		Kind: "task_event", Content: "ready", Audience: meta.SessionID,
		Tags: "task_event:status_changed,task_id:t1,status_to:review",
	})
	got, err = mgr.WaitForMessage(context.Background(), crit, time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "ready" {
		t.Fatalf("want ready match, got %+v", got)
	}
}

// TestWaitKindFilter: kind not in set must not wake.
func TestWaitKindFilter(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "dave", "dev")
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "evt", Audience: meta.SessionID})

	crit := mesh.WaitCriteria{AgentName: "dave", WorkspaceID: waitWS, Kinds: []string{"alert"}}
	got, err := mgr.WaitForMessage(context.Background(), crit, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("kind filter should have excluded; got %d", len(got))
	}
}

func TestWaitStatusTransitionRequiresStatusChangedEvent(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "dina", "reviewer")

	crit := mesh.WaitCriteria{
		AgentName: "dina", WorkspaceID: waitWS,
		Kinds: []string{"task_event"}, StatusTo: "review",
	}
	sendMsg(t, mgr, mesh.SendRequest{
		Kind: "task_event", Content: "spoofed", Audience: meta.SessionID,
		Tags: "task_event:created,task_id:t1,status_to:review",
	})
	got, err := mgr.WaitForMessage(context.Background(), crit, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if got != nil {
		t.Fatalf("status_to without status_changed marker should not match, got %+v", got)
	}

	sendMsg(t, mgr, mesh.SendRequest{
		Kind: "task_event", Content: "real transition", Audience: meta.SessionID,
		Tags: "task_event:status_changed,task_id:t1,status_from:doing,status_to:review",
	})
	got, err = mgr.WaitForMessage(context.Background(), crit, time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "real transition" {
		t.Fatalf("want real transition match, got %+v", got)
	}
}

// TestWaitBroadcastGate: "*" messages only wake when IncludeBroadcast.
func TestWaitBroadcastGate(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "erin", "dev")
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "broadcast", Audience: "*"})

	// Default (no broadcast): should not match.
	got, err := mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "erin", WorkspaceID: waitWS}, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("broadcast leaked without include_broadcast; got %d", len(got))
	}

	// With include_broadcast: matches.
	got, err = mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "erin", WorkspaceID: waitWS, IncludeBroadcast: true}, 2*time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "broadcast" {
		t.Fatalf("want broadcast match, got %+v", got)
	}
}

func TestWaitScopedWorkspaceIncludesGlobalBroadcast(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "gail", "dev")
	sender := mesh.SessionMeta{
		SessionID:    "sender-global",
		WorkspaceIDs: []string{""},
		ClientType:   "sender",
	}
	if _, err := mgr.Send(context.Background(), sender, mesh.SendRequest{
		Kind: "event", Content: "global", Audience: "*", ToWorkspace: "*",
	}); err != nil {
		t.Fatalf("Send global: %v", err)
	}

	got, err := mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "gail", WorkspaceID: waitWS, IncludeBroadcast: true}, time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "global" {
		t.Fatalf("want global broadcast match, got %+v", got)
	}
}

// TestWaitRoleGate: role-addressed messages only wake when IncludeRole.
func TestWaitRoleGate(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "frank", "reviewer")
	sendMsg(t, mgr, mesh.SendRequest{Kind: "event", Content: "for reviewers", Audience: "reviewer"})

	got, err := mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "frank", WorkspaceID: waitWS}, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("role msg leaked without include_role; got %d", len(got))
	}

	got, err = mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "frank", WorkspaceID: waitWS, IncludeRole: true}, 2*time.Second)
	if err != nil {
		t.Fatalf("wait err: %v", err)
	}
	if len(got) != 1 || got[0].Content != "for reviewers" {
		t.Fatalf("want role match, got %+v", got)
	}
}

// TestWaitTimeout: no message => (nil, nil) after the deadline.
func TestWaitTimeout(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "grace", "dev")
	got, err := mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "grace", WorkspaceID: waitWS}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("want nil err on timeout, got %v", err)
	}
	if got != nil {
		t.Fatalf("want nil msgs on timeout, got %+v", got)
	}
}

// TestWaitContextCancel: cancellation returns promptly with ctx.Err().
func TestWaitContextCancel(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "heidi", "dev")
	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		msgs []*store.MeshMessage
		err  error
	}
	done := make(chan res, 1)
	go func() {
		m, e := mgr.WaitForMessage(ctx, mesh.WaitCriteria{AgentName: "heidi", WorkspaceID: waitWS}, 5*time.Second)
		done <- res{m, e}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", r.err)
		}
		if r.msgs != nil {
			t.Fatalf("want nil msgs on cancel, got %+v", r.msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock wait")
	}
}

// TestWaitUnknownAgent: an unregistered name returns ErrUnknownAgent.
func TestWaitUnknownAgent(t *testing.T) {
	t.Parallel()
	mgr, _ := newWaitMgr(t, "ivan", "dev")
	_, err := mgr.WaitForMessage(context.Background(),
		mesh.WaitCriteria{AgentName: "nobody", WorkspaceID: waitWS}, time.Second)
	if !errors.Is(err, mesh.ErrUnknownAgent) {
		t.Fatalf("want ErrUnknownAgent, got %v", err)
	}
}

// TestWaitP2PIngestPath: a message inserted+notified via the same subscriber
// path the p2p ingest uses also wakes the waiter. We exercise it through the
// public Send path (which is exactly what notifySubscribers fires on for both
// local sends and p2p ingest) from a peer-flavoured sender session.
func TestWaitP2PIngestPath(t *testing.T) {
	t.Parallel()
	mgr, meta := newWaitMgr(t, "judy", "dev")

	type res struct {
		msgs []*store.MeshMessage
		err  error
	}
	done := make(chan res, 1)
	go func() {
		m, e := mgr.WaitForMessage(context.Background(),
			mesh.WaitCriteria{AgentName: "judy", WorkspaceID: waitWS}, 5*time.Second)
		done <- res{m, e}
	}()
	time.Sleep(120 * time.Millisecond)

	// Simulate a peer-imported send (actor_kind peer-import) targeting judy.
	peerSender := mesh.SessionMeta{SessionID: "peer:remote", WorkspaceIDs: []string{waitWS}, ClientType: "peer"}
	if _, err := mgr.Send(context.Background(), peerSender, mesh.SendRequest{
		Kind: "event", Content: "from peer", Audience: meta.SessionID, ActorKind: "peer-import",
	}); err != nil {
		t.Fatalf("peer Send: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("wait err: %v", r.err)
		}
		if len(r.msgs) != 1 || r.msgs[0].Content != "from peer" {
			t.Fatalf("want peer-ingest match, got %+v", r.msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("p2p-ingest path did not wake waiter")
	}
}

// TestWaitConsumeAdvancesCursor: consume=true advances the cursor; a follow-up
// wait sees nothing. consume=false leaves it so the message is still pending.
func TestWaitConsumeAdvancesCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// consume=false: cursor untouched — second wait re-sees the same message.
	mgrA, metaA := newWaitMgr(t, "ken", "dev")
	sendMsg(t, mgrA, mesh.SendRequest{Kind: "event", Content: "keep", Audience: metaA.SessionID})
	critA := mesh.WaitCriteria{AgentName: "ken", WorkspaceID: waitWS, Consume: false}
	if got, err := mgrA.WaitForMessage(ctx, critA, time.Second); err != nil || len(got) != 1 {
		t.Fatalf("first wait (no consume): got %d err=%v", len(got), err)
	}
	if got, err := mgrA.WaitForMessage(ctx, critA, 400*time.Millisecond); err != nil || len(got) != 1 {
		t.Fatalf("no-consume should leave message pending: got %d err=%v", len(got), err)
	}

	// consume=true: cursor advances — second wait times out.
	mgrB, metaB := newWaitMgr(t, "lee", "dev")
	sendMsg(t, mgrB, mesh.SendRequest{Kind: "event", Content: "eat", Audience: metaB.SessionID})
	critB := mesh.WaitCriteria{AgentName: "lee", WorkspaceID: waitWS, Consume: true}
	if got, err := mgrB.WaitForMessage(ctx, critB, time.Second); err != nil || len(got) != 1 {
		t.Fatalf("first wait (consume): got %d err=%v", len(got), err)
	}
	if got, err := mgrB.WaitForMessage(ctx, critB, 400*time.Millisecond); err != nil || got != nil {
		t.Fatalf("consume should advance cursor: got %d err=%v", len(got), err)
	}
}
