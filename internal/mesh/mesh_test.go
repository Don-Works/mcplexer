package mesh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestAgentDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		clientType string
		want       string
	}{
		{"empty falls back to unknown", "", "unknown"},
		{"rest is pretty-printed", "rest", "REST API"},
		{"claude-code passes through", "claude-code", "claude-code"},
		{"custom caller name passes through", "MyCIBot", "MyCIBot"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := agentDisplayName(SessionMeta{ClientType: tc.clientType})
			if got != tc.want {
				t.Errorf("agentDisplayName(%q) = %q, want %q",
					tc.clientType, got, tc.want)
			}
		})
	}
}

func TestSendRejectsOverlargeContent(t *testing.T) {
	mgr := NewManager(nil)
	_, err := mgr.Send(context.Background(), SessionMeta{
		SessionID:    "sender",
		WorkspaceIDs: []string{"ws"},
		ClientType:   "test",
	}, SendRequest{
		Kind:    "event",
		Content: strings.Repeat("x", MaxSendContentBytes+1),
	})
	if err == nil {
		t.Fatal("expected overlarge content to be rejected")
	}
	if !strings.Contains(err.Error(), "content too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReceiveClampsMaxResults(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{SessionID: "receiver", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "receiver"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	for i := 0; i < MaxReceiveResults+10; i++ {
		if _, err := mgr.Send(ctx, sender, SendRequest{
			Kind:     "event",
			Content:  "msg",
			Audience: receiver.SessionID,
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	res, err := mgr.Receive(ctx, receiver, ReceiveRequest{Filter: "new", MaxResults: MaxReceiveResults + 100})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(res.Messages) != MaxReceiveResults {
		t.Fatalf("received %d messages, want hard cap %d", len(res.Messages), MaxReceiveResults)
	}
}

// TestReceiveNewExcludesOwnMessages is the regression test for the phantom
// pending-message nag: an agent's own sends (e.g. the task_event broadcasts
// fired by its own task mutations) must never count as "new for you" or be
// delivered by filter=new — they perpetually re-triggered the piggyback
// notice. filter=all stays inclusive for explicit catch-up reads.
func TestReceiveNewExcludesOwnMessages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	self := SessionMeta{SessionID: "self", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, self, ReceiveRequest{Name: "self"}); err != nil {
		t.Fatalf("register self: %v", err)
	}
	if _, err := mgr.Send(ctx, self, SendRequest{
		Kind: "task_event", Content: "Task created: my own work", Audience: "*",
	}); err != nil {
		t.Fatalf("self send: %v", err)
	}
	peer := SessionMeta{SessionID: "peer", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, peer, SendRequest{
		Kind: "finding", Content: "from the peer", Audience: "*",
	}); err != nil {
		t.Fatalf("peer send: %v", err)
	}

	if count, err := mgr.PendingCount(ctx, self); err != nil {
		t.Fatalf("pending count: %v", err)
	} else if count != 1 {
		t.Fatalf("pending count = %d, want 1 (own send must not count)", count)
	}

	res, err := mgr.Receive(ctx, self, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("receive new: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].SessionID != "peer" {
		t.Fatalf("filter=new delivered %d message(s) %+v, want only the peer's", len(res.Messages), res.Messages)
	}

	// filter=all stays inclusive of own sends for catch-up. task_event is
	// kind-excluded by default, so opt in explicitly to see the own send.
	all, err := mgr.Receive(ctx, self, ReceiveRequest{
		Filter: "all", SinceMinutes: 10, Kinds: "task_event,finding",
	})
	if err != nil {
		t.Fatalf("receive all: %v", err)
	}
	if len(all.Messages) != 2 {
		t.Fatalf("filter=all delivered %d message(s), want 2 (inclusive catch-up)", len(all.Messages))
	}
}

// TestSendAttributionPrefersRegisteredAgentName is the regression test for
// "from unknown": a sender with a registered mesh-agent name must stamp that
// name on outbound messages, and a bare SessionMeta (no ClientType — the
// task_event emitter shape) must fall back to a short session id, never the
// literal "unknown".
func TestSendAttributionPrefersRegisteredAgentName(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	named := SessionMeta{SessionID: "sess-named", WorkspaceIDs: []string{"ws"}, ClientType: "claude-code"}
	if _, err := mgr.Receive(ctx, named, ReceiveRequest{Name: "fable-reviewer"}); err != nil {
		t.Fatalf("register named: %v", err)
	}
	msg, err := mgr.Send(ctx, SessionMeta{SessionID: "sess-named", WorkspaceIDs: []string{"ws"}},
		SendRequest{Kind: "task_event", Content: "Task claimed: x", Audience: "*"})
	if err != nil {
		t.Fatalf("send named: %v", err)
	}
	if msg.AgentName != "fable-reviewer" {
		t.Fatalf("AgentName = %q, want registered name fable-reviewer", msg.AgentName)
	}

	anon, err := mgr.Send(ctx, SessionMeta{SessionID: "sess-anonymous", WorkspaceIDs: []string{"ws"}},
		SendRequest{Kind: "task_event", Content: "Task claimed: y", Audience: "*"})
	if err != nil {
		t.Fatalf("send anon: %v", err)
	}
	if anon.AgentName == "unknown" || anon.AgentName == "" {
		t.Fatalf("AgentName = %q, want short session id fallback", anon.AgentName)
	}
}

func TestHydrateScopesToVisibleMessages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"ws-a"}, ClientType: "test"}
	msg, err := mgr.Send(ctx, sender, SendRequest{
		Kind:     "event",
		Content:  "visible in ws-a only",
		Audience: "*",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if _, err := mgr.Hydrate(ctx, SessionMeta{
		SessionID: "reader-a", WorkspaceIDs: []string{"ws-a"}, ClientType: "test",
	}, msg.ID); err != nil {
		t.Fatalf("hydrate visible message: %v", err)
	}
	if _, err := mgr.Hydrate(ctx, SessionMeta{
		SessionID: "reader-b", WorkspaceIDs: []string{"ws-b"}, ClientType: "test",
	}, msg.ID); err == nil {
		t.Fatal("expected hydrate to reject a message outside the reader workspace")
	}
}

func TestAgentDirectoryScopesToWorkspaces(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)
	now := time.Now().UTC()
	for _, agent := range []store.MeshAgent{
		{
			SessionID: "agent-a", WorkspaceID: "ws-a", Name: "visible",
			ClientType: "test", Origin: store.MeshAgentOriginLocal,
			LastSeenAt: now, CreatedAt: now,
		},
		{
			SessionID: "agent-b", WorkspaceID: "ws-b", Name: "hidden",
			ClientType: "test", Origin: store.MeshAgentOriginLocal,
			LastSeenAt: now, CreatedAt: now,
		},
	} {
		if err := db.UpsertMeshAgent(ctx, &agent); err != nil {
			t.Fatalf("upsert agent %s: %v", agent.SessionID, err)
		}
	}

	agents, err := mgr.ListAgentsInWorkspaces(ctx, []string{"ws-a"})
	if err != nil {
		t.Fatalf("ListAgentsInWorkspaces: %v", err)
	}
	if len(agents) != 1 || agents[0].SessionID != "agent-a" {
		t.Fatalf("visible agents = %#v, want only agent-a", agents)
	}
	if _, err := mgr.ResolveAgentNameInWorkspaces(ctx, "hidden", []string{"ws-a"}); err == nil {
		t.Fatal("expected hidden workspace agent to be unresolvable")
	}
	got, err := mgr.ResolveAgentNameInWorkspaces(ctx, "visible", []string{"ws-a"})
	if err != nil {
		t.Fatalf("resolve visible: %v", err)
	}
	if got.SessionID != "agent-a" {
		t.Fatalf("resolved %s, want agent-a", got.SessionID)
	}
}
