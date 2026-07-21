package mesh

// B1 regression coverage: a caller-narrowed filter=new receive is a
// NON-CONSUMING PEEK. It must return matching rows WITHOUT advancing the
// cursor, so it can never strand the broader backlog below the cursor. The
// canonical unfiltered poll still consumes and advances.

import (
	"context"
	"testing"
	"time"
)

// TestFilterNewNarrowedDoesNotStrandBacklog is the headline B1 regression. The
// task-review pattern steers agents to poll kinds:"task_event" first. If that
// narrowed read advances the cursor to the (higher-id) task_event, the normal
// finding sent earlier (lower id) falls permanently below the cursor and is
// never delivered by a later default poll — silent, permanent loss.
func TestFilterNewNarrowedDoesNotStrandBacklog(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ctx := context.Background()

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	// Normal finding lands FIRST (lower id); the task_event lands after it.
	finding, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "finding", Content: "normal backlog", ToWorkspace: "global",
	})
	if err != nil {
		t.Fatalf("send finding: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // distinct millisecond → strictly higher ULID
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "task_event", Content: "task lifecycle", ToWorkspace: "global",
	}); err != nil {
		t.Fatalf("send task_event: %v", err)
	}

	reader := SessionMeta{SessionID: "reader", WorkspaceIDs: []string{"ws-other"}, ClientType: "test"}

	// FIRST poll narrows to task_event (the pattern that used to strand the backlog).
	narrowed, err := mgr.Receive(ctx, reader, ReceiveRequest{Filter: "new", Kinds: "task_event"})
	if err != nil {
		t.Fatalf("narrowed receive: %v", err)
	}
	if len(narrowed.Messages) != 1 || narrowed.Messages[0].Kind != "task_event" {
		t.Fatalf("narrowed receive delivered %+v, want the single task_event", narrowed.Messages)
	}

	// The normal finding (lower id) MUST still be deliverable via the default poll.
	def, err := mgr.Receive(ctx, reader, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("default receive: %v", err)
	}
	if len(def.Messages) != 1 || def.Messages[0].ID != finding.ID {
		t.Fatalf("default receive delivered %+v, want the previously-stranded finding %s",
			def.Messages, finding.ID)
	}
}

// TestFilterNewNarrowedIsRepeatablePeek confirms the non-consuming contract
// from the other direction: because a narrowed read never advances the cursor,
// repeating it returns the same matching rows every time.
func TestFilterNewNarrowedIsRepeatablePeek(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ctx := context.Background()

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "task_event", Content: "tick", ToWorkspace: "global",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	reader := SessionMeta{SessionID: "reader", WorkspaceIDs: []string{"ws-other"}, ClientType: "test"}

	for i := 0; i < 2; i++ {
		res, err := mgr.Receive(ctx, reader, ReceiveRequest{Filter: "new", Kinds: "task_event"})
		if err != nil {
			t.Fatalf("peek %d: %v", i, err)
		}
		if len(res.Messages) != 1 {
			t.Fatalf("peek %d delivered %d messages, want 1 (a narrowed read is a non-consuming peek)",
				i, len(res.Messages))
		}
	}
}

// TestFilterNewDefaultStillConsumes guards the other half of option (c): the
// canonical unfiltered poll MUST keep advancing the cursor, or every default
// receive would re-deliver the whole live window forever.
func TestFilterNewDefaultStillConsumes(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ctx := context.Background()

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "finding", Content: "consume me", ToWorkspace: "global",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	reader := SessionMeta{SessionID: "reader", WorkspaceIDs: []string{"ws-other"}, ClientType: "test"}

	first, err := mgr.Receive(ctx, reader, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("first receive: %v", err)
	}
	if len(first.Messages) != 1 {
		t.Fatalf("first default receive delivered %d, want 1", len(first.Messages))
	}
	second, err := mgr.Receive(ctx, reader, ReceiveRequest{Filter: "new"})
	if err != nil {
		t.Fatalf("second receive: %v", err)
	}
	if len(second.Messages) != 0 {
		t.Fatalf("second default receive delivered %d, want 0 (the default poll must consume)",
			len(second.Messages))
	}
}

// TestReceiveIsNarrowed unit-tests the classifier that decides consume vs peek.
func TestReceiveIsNarrowed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  ReceiveRequest
		want bool
	}{
		{"empty is not narrowed", ReceiveRequest{}, false},
		{"filter+max_results only is not narrowed", ReceiveRequest{Filter: "new", MaxResults: 10}, false},
		{"kinds narrows", ReceiveRequest{Kinds: "task_event"}, true},
		{"exclude_kinds narrows", ReceiveRequest{ExcludeKinds: "alert"}, true},
		{"actor_kinds narrows", ReceiveRequest{ActorKinds: "worker"}, true},
		{"exclude_actor_kinds narrows", ReceiveRequest{ExcludeActorKinds: "worker"}, true},
		{"tags narrows", ReceiveRequest{Tags: "urgent"}, true},
		{"repo narrows", ReceiveRequest{Repo: "github.com/acme/example"}, true},
		{"branch narrows", ReceiveRequest{Branch: "main"}, true},
		{"workspace_path narrows", ReceiveRequest{WorkspacePath: "/tmp/example"}, true},
	}
	for _, tc := range cases {
		if got := receiveIsNarrowed(tc.req); got != tc.want {
			t.Errorf("%s: receiveIsNarrowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}
