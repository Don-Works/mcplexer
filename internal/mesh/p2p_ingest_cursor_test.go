package mesh

// Cursor-poisoning regressions for the p2p ingest path.
//
// mesh_messages.id doubles as the receive cursor and the cursor filter is a
// lexicographic string compare (`id > ?`). Ingest used to store the peer's
// envelope id verbatim, so any peer-supplied id sorting above the local id
// space advanced the cursor past every future local message and killed the
// inbox permanently — silently, because PendingCount reads the same cursor.
//
// The fix re-mints the row id locally at ingest. These tests pin that a
// hostile id and a forward-skewed clock both leave the inbox working.

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/p2p"
)

// futureULID mints a well-formed ULID whose embedded timestamp is `ahead`
// in the future — exactly what a paired peer with a fast clock produces.
func futureULID(ahead time.Duration) string {
	return ulid.MustNew(ulid.Timestamp(time.Now().Add(ahead)), rand.Reader).String()
}

// receiveContents drains one filter=new poll and returns the message bodies.
func receiveContents(t *testing.T, mgr *Manager, meta SessionMeta) []string {
	t.Helper()
	res, err := mgr.Receive(context.Background(), meta, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	out := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		out = append(out, m.Content)
	}
	return out
}

func containsContent(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// TestNewULIDIsMonotonicWithinMillisecond pins the ordering invariant that
// the whole cursor design rests on (see selectOldestBatch): ids minted in
// the same millisecond must still sort by creation order. With plain random
// entropy roughly half of same-millisecond pairs sorted BACKWARDS, which
// silently lost any message minted in the same millisecond as a
// just-delivered one.
func TestNewULIDIsMonotonicWithinMillisecond(t *testing.T) {
	const mints = 5000
	ids := make([]string, mints)
	for i := range ids {
		ids[i] = newULID()
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("id %d (%q) does not sort above its predecessor (%q)",
				i, ids[i], ids[i-1])
		}
	}
}

// TestSendCursorSurvivesSameMillisecondBurst is the behavioural half: a
// receive poll landing between two same-millisecond sends must not swallow
// the second one.
func TestSendCursorSurvivesSameMillisecondBurst(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	const ws = "ws-burst"
	receiver := SessionMeta{SessionID: "receiver", WorkspaceIDs: []string{ws}, ClientType: "test"}
	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	// Send, drain (advancing the cursor), then send again immediately. The
	// two sends land in the same millisecond on any reasonable machine.
	for round := 0; round < 25; round++ {
		first := fmt.Sprintf("burst %d first", round)
		second := fmt.Sprintf("burst %d second", round)
		if _, err := mgr.Send(ctx, sender, SendRequest{Kind: "finding", Content: first}); err != nil {
			t.Fatalf("Send first: %v", err)
		}
		receiveContents(t, mgr, receiver)
		if _, err := mgr.Send(ctx, sender, SendRequest{Kind: "finding", Content: second}); err != nil {
			t.Fatalf("Send second: %v", err)
		}
		if got := receiveContents(t, mgr, receiver); !containsContent(got, second) {
			t.Fatalf("round %d: message %q lost to a same-millisecond cursor advance; got %v",
				round, second, got)
		}
	}
}

// TestIngestDoesNotPoisonReceiveCursor is the headline regression: an
// inbound envelope carrying an id that sorts above every ULID must not
// stop the NEXT legitimate message from being delivered.
//
// Before the fix this test failed on the second poll with 0 messages, and
// stayed at 0 forever.
func TestIngestDoesNotPoisonReceiveCursor(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	const ws = "ws-cursor"
	receiver := SessionMeta{SessionID: "receiver", WorkspaceIDs: []string{ws}, ClientType: "test"}
	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}

	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	// A peer-supplied id that sorts above every 26-char ULID. It must be
	// rejected outright — but even if a future change accepts it, the row
	// id it produces must not be able to steer the cursor.
	hostile := p2p.MeshEnvelope{
		ID: "zzz", SenderPeerID: "peerA", Kind: "finding",
		Content: "hostile id", Priority: "normal", TS: time.Now().UnixMilli(),
	}
	if err := mgr.ingestEnvelope(ctx, hostile); err != nil {
		t.Fatalf("ingestEnvelope(hostile): %v", err)
	}
	receiveContents(t, mgr, receiver) // drain whatever the hostile ingest produced

	// The legitimate message every agent depends on seeing.
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "finding", Content: "legit after hostile",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := receiveContents(t, mgr, receiver)
	if !containsContent(got, "legit after hostile") {
		t.Fatalf("inbox is dead: legitimate message not delivered after hostile ingest; got %v", got)
	}

	pending, err := mgr.PendingCount(ctx, receiver)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after draining, want 0", pending)
	}
}

// TestForwardSkewedPeerCannotWedgeInbox covers the accidental case, which
// needs no malice at all: a paired peer whose clock is a year fast mints
// VALID ULIDs above every local id. ULID validation alone does not catch
// this — only re-minting the row id does.
func TestForwardSkewedPeerCannotWedgeInbox(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	const ws = "ws-skew"
	receiver := SessionMeta{SessionID: "receiver", WorkspaceIDs: []string{ws}, ClientType: "test"}
	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{ws}, ClientType: "test"}

	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	skewed := p2p.MeshEnvelope{
		ID: futureULID(365 * 24 * time.Hour), SenderPeerID: "peerA",
		Kind: "finding", Content: "from a peer a year fast",
		Priority: "normal", TS: time.Now().UnixMilli(),
	}
	if !validULID(skewed.ID) {
		t.Fatalf("test bug: %q should be a valid ULID", skewed.ID)
	}
	if err := mgr.ingestEnvelope(ctx, skewed); err != nil {
		t.Fatalf("ingestEnvelope(skewed): %v", err)
	}

	// The skewed message itself is legitimate traffic and must arrive.
	first := receiveContents(t, mgr, receiver)
	if !containsContent(first, "from a peer a year fast") {
		t.Fatalf("skewed-peer message not delivered; got %v", first)
	}

	// And it must not have taken the inbox with it.
	for i, body := range []string{"local one", "local two"} {
		if _, err := mgr.Send(ctx, sender, SendRequest{Kind: "finding", Content: body}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	got := receiveContents(t, mgr, receiver)
	for _, want := range []string{"local one", "local two"} {
		if !containsContent(got, want) {
			t.Errorf("message %q lost after skewed-peer ingest; got %v", want, got)
		}
	}

	// The cursor must be a locally-minted ULID, not the peer's future id.
	agent, err := db.GetMeshAgent(ctx, receiver.SessionID)
	if err != nil {
		t.Fatalf("GetMeshAgent: %v", err)
	}
	if agent.Cursor >= skewed.ID {
		t.Errorf("cursor %q was advanced to/past the peer's future id %q",
			agent.Cursor, skewed.ID)
	}
	if !validULID(agent.Cursor) {
		t.Errorf("cursor %q is not a locally-minted ULID", agent.Cursor)
	}
}

// TestIngestedBroadcastReachesReceive is the A2 regression: a cross-machine
// broadcast used to land in the literal "global" workspace, which Receive's
// readable set never includes — durably stored, visible on the dashboard,
// invisible to every agent on the receiving machine.
//
// The existing ingest coverage only asserted the row was STORED, which is
// why this survived. Drive it through Receive.
func TestIngestedBroadcastReachesReceive(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{
		SessionID: "receiver", WorkspaceIDs: []string{"dir:/repo/acme"}, ClientType: "test",
	}
	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	// WorkspaceID:"" on the wire == explicit broadcast (or a legacy peer).
	broadcast := p2p.MeshEnvelope{
		ID: newULID(), SenderPeerID: "peerA", Kind: "alert",
		Content: "cross-machine broadcast", Priority: "high",
		TS: time.Now().UnixMilli(),
	}
	if err := mgr.ingestEnvelope(ctx, broadcast); err != nil {
		t.Fatalf("ingestEnvelope: %v", err)
	}

	got := receiveContents(t, mgr, receiver)
	if !containsContent(got, "cross-machine broadcast") {
		t.Fatalf("ingested broadcast is invisible to a normal session; got %v", got)
	}
}

// TestIngestedBroadcastUsesSameBucketAsLocalGlobalSend pins the two
// sentinels to one value: the bucket ingest writes to must be the bucket a
// local to_workspace:"*" send resolves to. Two sentinels for one concept is
// what produced the write-only bucket in the first place.
func TestIngestedBroadcastUsesSameBucketAsLocalGlobalSend(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"ws-a"}, ClientType: "test"}
	local, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "finding", Content: "local global send", ToWorkspace: "*",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	for _, wireValue := range []string{"", "global"} {
		env := p2p.MeshEnvelope{
			ID: newULID(), SenderPeerID: "peerA", Kind: "finding",
			Content: "ingested " + wireValue, Priority: "normal",
			WorkspaceID: wireValue, TS: time.Now().UnixMilli(),
		}
		if err := mgr.ingestEnvelope(ctx, env); err != nil {
			t.Fatalf("ingestEnvelope(%q): %v", wireValue, err)
		}
		got := findIngestedByContent(t, db, env.Content)
		if got == nil {
			t.Fatalf("envelope with WorkspaceID=%q was not stored", wireValue)
		}
		if got.WorkspaceID != local.WorkspaceID {
			t.Errorf("WorkspaceID=%q ingested into %q, want %q (same bucket as a local global send)",
				wireValue, got.WorkspaceID, local.WorkspaceID)
		}
	}
}

// TestUnboundSessionTrafficStaysIsolated is the guard on the fix above: the
// leak defaultMeshWorkspace (gateway/handler_mesh.go) documents must still
// be closed. A session that resolves to its own directory-scoped workspace
// must NOT have its routine traffic land in the global namespace where
// every other session reads it.
func TestUnboundSessionTrafficStaysIsolated(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	repoA := SessionMeta{SessionID: "agent-a", WorkspaceIDs: []string{"dir:/repo/a"}, ClientType: "test"}
	repoB := SessionMeta{SessionID: "agent-b", WorkspaceIDs: []string{"dir:/repo/b"}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, repoB, ReceiveRequest{Name: "b"}); err != nil {
		t.Fatalf("register b: %v", err)
	}

	// Routine send, no to_workspace — must stay in repo A's own bucket.
	msg, err := mgr.Send(ctx, repoA, SendRequest{Kind: "finding", Content: "repo A internal"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.WorkspaceID == "" {
		t.Fatal("routine send leaked into the global namespace ('') — every session would read it")
	}

	if got := receiveContents(t, mgr, repoB); containsContent(got, "repo A internal") {
		t.Fatalf("cross-workspace leak: repo B saw repo A's traffic; got %v", got)
	}

	// A session with NO workspace at all must also stay isolated.
	unbound := SessionMeta{SessionID: "agent-unbound", ClientType: "test"}
	orphan, err := mgr.Send(ctx, unbound, SendRequest{Kind: "finding", Content: "unbound traffic"})
	if err != nil {
		t.Fatalf("Send(unbound): %v", err)
	}
	if orphan.WorkspaceID == "" {
		t.Fatal("unbound session's send leaked into the global namespace ('')")
	}
	if got := receiveContents(t, mgr, repoB); containsContent(got, "unbound traffic") {
		t.Fatalf("cross-workspace leak: repo B saw an unbound session's traffic; got %v", got)
	}
}

// TestWorkspaceScopedEnvelopeStillRequiresBinding pins the other half of the
// ingest isolation contract: normalizing the broadcast bucket must not let an
// unbound peer pick a target workspace.
func TestWorkspaceScopedEnvelopeStillRequiresBinding(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	env := p2p.MeshEnvelope{
		ID: newULID(), SenderPeerID: "peerA", Kind: "finding",
		Content: "targeted at a workspace we never bound", Priority: "normal",
		WorkspaceID: "dir:/repo/victim", TS: time.Now().UnixMilli(),
	}
	if err := mgr.ingestEnvelope(ctx, env); err != nil {
		t.Fatalf("ingestEnvelope: %v", err)
	}
	if got := findIngestedByContent(t, db, env.Content); got != nil {
		t.Fatalf("unbound workspace-scoped envelope was stored in %q; want dropped", got.WorkspaceID)
	}
}
