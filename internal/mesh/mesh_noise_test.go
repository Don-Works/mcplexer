package mesh

// Signal-to-noise coverage: blank-content rejection, default task_event
// exclusion (receive + pending count), kind/actor-kind filters, the
// PendingCount/Receive global-namespace scope alignment, and the live
// ceiling applying to the global namespace.

import (
	"context"
	"strings"
	"testing"
)

func TestSendRejectsBlankContent(t *testing.T) {
	t.Parallel()
	mgr := NewManager(nil)
	for _, content := range []string{"", "   ", "\n\t  \n"} {
		_, err := mgr.Send(context.Background(), SessionMeta{
			SessionID: "s", WorkspaceIDs: []string{"ws"}, ClientType: "test",
		}, SendRequest{Kind: "event", Content: content})
		if err == nil {
			t.Fatalf("expected blank content %q to be rejected", content)
		}
		if !strings.Contains(err.Error(), "content is required") {
			t.Fatalf("unexpected error for %q: %v", content, err)
		}
	}
}

func TestResolveKindFilters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		req          ReceiveRequest
		wantKinds    []string
		wantExcludes []string
		wantExcluded bool
	}{
		{
			name:         "default excludes task_event",
			req:          ReceiveRequest{},
			wantExcludes: []string{KindTaskEvent},
			wantExcluded: true,
		},
		{
			name:         "kinds including task_event opts in",
			req:          ReceiveRequest{Kinds: "task_event, finding"},
			wantKinds:    []string{"task_event", "finding"},
			wantExcluded: false,
		},
		{
			name:         "explicit whitelist without task_event keeps it out",
			req:          ReceiveRequest{Kinds: "finding"},
			wantKinds:    []string{"finding"},
			wantExcluded: true,
		},
		{
			name:         "explicit exclude is not duplicated",
			req:          ReceiveRequest{ExcludeKinds: "task_event,alert"},
			wantExcludes: []string{"task_event", "alert"},
			wantExcluded: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kinds, excludes, excluded := resolveKindFilters(tc.req)
			if !equalStrings(kinds, tc.wantKinds) {
				t.Errorf("kinds = %v, want %v", kinds, tc.wantKinds)
			}
			if !equalStrings(excludes, tc.wantExcludes) {
				t.Errorf("excludes = %v, want %v", excludes, tc.wantExcludes)
			}
			if excluded != tc.wantExcluded {
				t.Errorf("taskEventsExcluded = %v, want %v", excluded, tc.wantExcluded)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReceiveExcludesTaskEventsByDefault(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{SessionID: "rcv", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "rcv"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	peer := SessionMeta{SessionID: "peer", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	for _, kind := range []string{"task_event", "finding", "alert"} {
		if _, err := mgr.Send(ctx, peer, SendRequest{
			Kind: kind, Content: "body for " + kind, Audience: "*",
		}); err != nil {
			t.Fatalf("send %s: %v", kind, err)
		}
	}

	res, err := mgr.Receive(ctx, receiver, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("receive new: %v", err)
	}
	if !res.TaskEventsExcluded {
		t.Error("TaskEventsExcluded = false, want true on a default receive")
	}
	if len(res.Messages) != 2 {
		t.Fatalf("filter=new delivered %d messages, want 2 (task_event hidden)", len(res.Messages))
	}
	for _, m := range res.Messages {
		if m.Kind == KindTaskEvent {
			t.Fatalf("task_event leaked into default receive: %+v", m)
		}
	}

	optIn, err := mgr.Receive(ctx, receiver, ReceiveRequest{
		Filter: "all", SinceMinutes: 10, Kinds: "task_event",
	})
	if err != nil {
		t.Fatalf("receive opt-in: %v", err)
	}
	if optIn.TaskEventsExcluded {
		t.Error("TaskEventsExcluded = true on an explicit task_event opt-in")
	}
	if len(optIn.Messages) != 1 || optIn.Messages[0].Kind != KindTaskEvent {
		t.Fatalf("kinds=task_event delivered %+v, want exactly the task_event", optIn.Messages)
	}

	custom, err := mgr.Receive(ctx, receiver, ReceiveRequest{
		Filter: "all", SinceMinutes: 10, ExcludeKinds: "alert",
	})
	if err != nil {
		t.Fatalf("receive exclude alert: %v", err)
	}
	if len(custom.Messages) != 1 || custom.Messages[0].Kind != "finding" {
		t.Fatalf("exclude_kinds=alert delivered %+v, want only the finding", custom.Messages)
	}
}

func TestReceiveFiltersByActorKind(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{SessionID: "rcv", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Receive(ctx, receiver, ReceiveRequest{Name: "rcv"}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	peer := SessionMeta{SessionID: "peer", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	sends := []struct{ actor, content string }{
		{"worker", "worker chatter"},
		{"", "agent default"},
	}
	for _, s := range sends {
		if _, err := mgr.Send(ctx, peer, SendRequest{
			Kind: "finding", Content: s.content, Audience: "*", ActorKind: s.actor,
		}); err != nil {
			t.Fatalf("send %q: %v", s.content, err)
		}
	}

	hidden, err := mgr.Receive(ctx, receiver, ReceiveRequest{
		Filter: "all", SinceMinutes: 10, ExcludeActorKinds: "worker",
	})
	if err != nil {
		t.Fatalf("receive exclude worker: %v", err)
	}
	if len(hidden.Messages) != 1 || hidden.Messages[0].ActorKind != "agent" {
		t.Fatalf("exclude_actor_kinds=worker delivered %+v, want only the agent message", hidden.Messages)
	}

	only, err := mgr.Receive(ctx, receiver, ReceiveRequest{
		Filter: "all", SinceMinutes: 10, ActorKinds: "worker",
	})
	if err != nil {
		t.Fatalf("receive only worker: %v", err)
	}
	if len(only.Messages) != 1 || only.Messages[0].ActorKind != "worker" {
		t.Fatalf("actor_kinds=worker delivered %+v, want only the worker message", only.Messages)
	}
}

func TestPendingCountExcludesTaskEvents(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{SessionID: "rcv", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if err := mgr.RegisterAgent(ctx, receiver); err != nil {
		t.Fatalf("register: %v", err)
	}
	peer := SessionMeta{SessionID: "peer", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, peer, SendRequest{
		Kind: "task_event", Content: "Task created: plumbing", Audience: "*",
	}); err != nil {
		t.Fatalf("send task_event: %v", err)
	}

	if n, err := mgr.PendingCount(ctx, receiver); err != nil || n != 0 {
		t.Fatalf("PendingCount with only task_events = (%d, %v), want (0, nil)", n, err)
	}

	if _, err := mgr.Send(ctx, peer, SendRequest{
		Kind: "finding", Content: "real signal", Audience: "*",
	}); err != nil {
		t.Fatalf("send finding: %v", err)
	}
	if n, err := mgr.PendingCount(ctx, receiver); err != nil || n != 1 {
		t.Fatalf("PendingCount = (%d, %v), want (1, nil) — task_event must not count", n, err)
	}
}

// TestPendingCountSeesGlobalNamespace locks the PendingCount/Receive scope
// alignment: a to_workspace:"*" broadcast is visible to Receive (which always
// appends the "" namespace), so it must count toward the pending nag too.
func TestPendingCountSeesGlobalNamespace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	receiver := SessionMeta{SessionID: "rcv", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	if err := mgr.RegisterAgent(ctx, receiver); err != nil {
		t.Fatalf("register: %v", err)
	}
	peer := SessionMeta{SessionID: "peer", WorkspaceIDs: []string{"ws-other"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, peer, SendRequest{
		Kind: "alert", Content: "global broadcast", Audience: "*", ToWorkspace: "*",
	}); err != nil {
		t.Fatalf("send global: %v", err)
	}

	n, err := mgr.PendingCount(ctx, receiver)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("PendingCount = %d, want 1 — the global namespace Receive reads must be counted", n)
	}
}

// TestSendEnforcesCeilingInGlobalNamespace: the live-message ceiling used to
// skip wsID=="" entirely, letting the one namespace every session reads grow
// without bound.
func TestSendEnforcesCeilingInGlobalNamespace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)
	mgr.liveCeiling = 2

	sender := SessionMeta{SessionID: "s", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	for i := 0; i < 5; i++ {
		if _, err := mgr.Send(ctx, sender, SendRequest{
			Kind: "event", Content: "global flood", ToWorkspace: "*",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	count, err := db.CountLiveMessages(ctx, "")
	if err != nil {
		t.Fatalf("CountLiveMessages: %v", err)
	}
	if count > mgr.liveCeiling {
		t.Fatalf("global namespace live count = %d, want <= ceiling %d", count, mgr.liveCeiling)
	}
}
