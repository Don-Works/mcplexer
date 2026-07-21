package googlechat

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// fakeMeshSender records every Send for assertion.
type fakeMeshSender struct {
	mu    sync.Mutex
	calls []struct {
		Meta mesh.SessionMeta
		Req  mesh.SendRequest
	}
}

func (f *fakeMeshSender) Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		Meta mesh.SessionMeta
		Req  mesh.SendRequest
	}{meta, req})
	return &store.MeshMessage{
		ID:          "mesh-" + req.Audience,
		WorkspaceID: firstWS(meta.WorkspaceIDs),
		SessionID:   meta.SessionID,
		Kind:        req.Kind,
		Content:     req.Content,
	}, nil
}

func (f *fakeMeshSender) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeMeshSender) Last() (mesh.SessionMeta, mesh.SendRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return mesh.SessionMeta{}, mesh.SendRequest{}, false
	}
	c := f.calls[len(f.calls)-1]
	return c.Meta, c.Req, true
}

func firstWS(ws []string) string {
	if len(ws) > 0 {
		return ws[0]
	}
	return ""
}

func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.New(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestManager_InboundRoutedToMesh exercises the end-to-end inbound path:
// a paired space + a MESSAGE event → mesh.Send called with the expected
// workspace + tags.
func TestManager_InboundRoutedToMesh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newTestDB(t)
	fake := &fakeMeshSender{}
	bus := notify.NewBus()
	mgr := NewManager(db, fake, bus)

	// Insert a paired space.
	space := &store.GoogleChatSpace{
		ID:          "space-1",
		SpaceName:   "spaces/AAAA",
		SpaceType:   "dm",
		WorkspaceID: "ws-1",
		SessionID:   "googlechat-spaces/AAAA",
		MinPriority: "normal",
		ListenMode:  "mention",
		Active:      true,
		CreatedAt:   time.Now().UTC(),
	}
	if err := db.UpsertGoogleChatSpace(ctx, space); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Push a MESSAGE event onto the inbound channel.
	mgr.PushInbound(IncomingMessage{
		EventType:       EventTypeMessage,
		SpaceName:       "spaces/AAAA",
		SpaceType:       "dm",
		NativeMessageID: "MSG1",
		SenderName:      "Alice",
		SenderType:      "HUMAN",
		Text:            "hi",
	})

	// Run the manager loop briefly.
	done := make(chan struct{})
	go func() {
		_ = mgr.Run(ctx)
		close(done)
	}()
	// Poll for the dispatch (max 500ms).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fake.Count() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if fake.Count() != 1 {
		t.Fatalf("expected 1 mesh.Send call, got %d", fake.Count())
	}
	meta, req, _ := fake.Last()
	if meta.SessionID != space.SessionID {
		t.Errorf("session: want %q, got %q", space.SessionID, meta.SessionID)
	}
	if len(meta.WorkspaceIDs) != 1 || meta.WorkspaceIDs[0] != "ws-1" {
		t.Errorf("workspace: want [ws-1], got %v", meta.WorkspaceIDs)
	}
	if meta.ClientType != Platform {
		t.Errorf("client_type: want %q, got %q", Platform, meta.ClientType)
	}
	if req.Tags != "human,googlechat" {
		t.Errorf("tags: want human,googlechat, got %q", req.Tags)
	}
}

// TestManager_UnboundSpaceRejected verifies the bridge refuses messages from
// a space that hasn't been paired into a workspace.
func TestManager_UnboundSpaceRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newTestDB(t)
	fake := &fakeMeshSender{}
	mgr := NewManager(db, fake, notify.NewBus())

	mgr.PushInbound(IncomingMessage{
		EventType: EventTypeMessage,
		SpaceName: "spaces/UNKNOWN",
		SpaceType: "dm",
		Text:      "hi",
	})

	done := make(chan struct{})
	go func() { _ = mgr.Run(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if fake.Count() != 0 {
		t.Fatalf("expected zero mesh.Send for unbound space, got %d", fake.Count())
	}
}

// TestManager_PairingBindsSpace confirms a /pair <code> message creates a
// GoogleChatSpace row tied to the pairing's workspace.
func TestManager_PairingBindsSpace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newTestDB(t)
	mgr := NewManager(db, &fakeMeshSender{}, notify.NewBus())

	pairing, err := mgr.CreatePairing(ctx, "ws-42", "", 5*time.Minute)
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}

	mgr.PushInbound(IncomingMessage{
		EventType:   EventTypeMessage,
		SpaceName:   "spaces/PAIRED",
		SpaceType:   "dm",
		Text:        "/pair " + pairing.Code,
		PairingCode: pairing.Code,
		SenderName:  "Alice",
	})

	done := make(chan struct{})
	go func() { _ = mgr.Run(ctx); close(done) }()
	deadline := time.Now().Add(500 * time.Millisecond)
	var bound *store.GoogleChatSpace
	for time.Now().Before(deadline) {
		bound, _ = db.GetGoogleChatSpaceByName(ctx, "spaces/PAIRED")
		if bound != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if bound == nil {
		t.Fatal("expected space to be created after pairing")
	}
	if bound.WorkspaceID != "ws-42" {
		t.Errorf("workspace: want ws-42, got %q", bound.WorkspaceID)
	}
	if !bound.Active {
		t.Error("expected paired space to be active")
	}
}

// TestManager_GroupNoMentionSkipped — a space-mode message with neither
// mention nor reply gets dropped before mesh.Send.
func TestManager_GroupNoMentionSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := newTestDB(t)
	fake := &fakeMeshSender{}
	mgr := NewManager(db, fake, notify.NewBus())

	// Paired group space with mention-only listen mode.
	if err := db.UpsertGoogleChatSpace(ctx, &store.GoogleChatSpace{
		ID:          "space-grp",
		SpaceName:   "spaces/GRP",
		SpaceType:   "space",
		WorkspaceID: "ws-1",
		SessionID:   "googlechat-spaces/GRP",
		MinPriority: "normal",
		ListenMode:  "mention",
		Active:      true,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	mgr.PushInbound(IncomingMessage{
		EventType:  EventTypeMessage,
		SpaceName:  "spaces/GRP",
		SpaceType:  "space",
		Text:       "ambient chatter",
		SenderName: "Bob",
	})

	done := make(chan struct{})
	go func() { _ = mgr.Run(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if fake.Count() != 0 {
		t.Fatalf("expected zero mesh.Send for group-no-mention, got %d", fake.Count())
	}
}
