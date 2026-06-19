// handler_memory_consolidator_test.go — gateway-level coverage for the
// new memory__get / memory__invalidate / memory__pin / memory__unpin MCP
// tools that back the consolidator. We construct a real memory.Service
// backed by an in-memory SQLite store so the dispatcher exercises the
// full path (handler → service → store).
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestHandlerWithMemory(t *testing.T) (*handler, *memory.Service) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)

	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.memorySvc = svc
	return h, svc
}

func TestMemoryConsolidatorTools_RoundTrip(t *testing.T) {
	ctx := context.Background()
	h, svc := newTestHandlerWithMemory(t)

	// Seed two memories.
	idA, err := svc.Write(ctx, memory.WriteOptions{
		Name:    "note-a",
		Content: "First note about the consolidator.",
	})
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	idB, err := svc.Write(ctx, memory.WriteOptions{
		Name:    "note-b",
		Content: "Second note that supersedes the first.",
	})
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}

	// memory__get — must surface the full content + id.
	getResp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__get",
		json.RawMessage(`{"id":"`+idA+`"}`))
	if !handled || rpcErr != nil {
		t.Fatalf("memory__get not handled: handled=%v rpcErr=%v", handled, rpcErr)
	}
	if !strings.Contains(string(getResp), "First note about the consolidator.") {
		t.Errorf("memory__get response missing content: %s", string(getResp))
	}
	if !strings.Contains(string(getResp), idA) {
		t.Errorf("memory__get response missing id: %s", string(getResp))
	}

	// memory__pin — flips the row.
	pinResp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__pin",
		json.RawMessage(`{"id":"`+idA+`"}`))
	if !handled || rpcErr != nil {
		t.Fatalf("memory__pin not handled: handled=%v rpcErr=%v", handled, rpcErr)
	}
	if !strings.Contains(string(pinResp), "Pinned memory") {
		t.Errorf("memory__pin response unexpected: %s", string(pinResp))
	}
	entry, err := svc.Get(ctx, idA)
	if err != nil {
		t.Fatalf("re-fetch after pin: %v", err)
	}
	if !entry.Pinned {
		t.Errorf("expected entry.Pinned=true after memory__pin")
	}

	// memory__unpin — flips it back.
	_, rpcErr, handled = h.dispatchMemoryTool(ctx, "memory__unpin",
		json.RawMessage(`{"id":"`+idA+`"}`))
	if !handled || rpcErr != nil {
		t.Fatalf("memory__unpin not handled: handled=%v rpcErr=%v", handled, rpcErr)
	}
	entry, err = svc.Get(ctx, idA)
	if err != nil {
		t.Fatalf("re-fetch after unpin: %v", err)
	}
	if entry.Pinned {
		t.Errorf("expected entry.Pinned=false after memory__unpin")
	}

	// memory__invalidate — stamps t_valid_end + records superseded_by.
	invResp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__invalidate",
		json.RawMessage(`{"id":"`+idA+`","superseded_by_id":"`+idB+`"}`))
	if !handled || rpcErr != nil {
		t.Fatalf("memory__invalidate not handled: handled=%v rpcErr=%v", handled, rpcErr)
	}
	if !strings.Contains(string(invResp), "Invalidated memory") {
		t.Errorf("memory__invalidate response unexpected: %s", string(invResp))
	}
	if !strings.Contains(string(invResp), idB) {
		t.Errorf("memory__invalidate response missing superseded_by: %s", string(invResp))
	}
	entry, err = svc.Get(ctx, idA)
	if err != nil {
		t.Fatalf("re-fetch after invalidate: %v", err)
	}
	if entry.TValidEnd == nil {
		t.Errorf("expected TValidEnd to be set after invalidate")
	}
	if entry.InvalidatedBy != idB {
		t.Errorf("expected InvalidatedBy=%s, got %q", idB, entry.InvalidatedBy)
	}
}

func TestMemoryConsolidatorTools_MissingId(t *testing.T) {
	ctx := context.Background()
	h, _ := newTestHandlerWithMemory(t)

	for _, name := range []string{"memory__get", "memory__invalidate", "memory__pin", "memory__unpin"} {
		_, rpcErr, _ := h.dispatchMemoryTool(ctx, name, json.RawMessage(`{}`))
		if rpcErr == nil {
			t.Errorf("%s with empty args should return RPC error, got nil", name)
		}
	}
}

func TestMemoryConsolidatorTools_DispatchedWhenMemoryServiceDisabled(t *testing.T) {
	ctx := context.Background()
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.memorySvc = nil

	// With memorySvc==nil the dispatcher returns a "memory disabled" tool
	// result rather than falling through — the slim-surface tools/list
	// otherwise wouldn't have advertised these tools at all.
	resp, _, handled := h.dispatchMemoryTool(ctx, "memory__invalidate",
		json.RawMessage(`{"id":"foo"}`))
	if !handled {
		t.Fatalf("memory__invalidate not handled when service nil")
	}
	if !strings.Contains(string(resp), "Memory subsystem is not enabled") {
		t.Errorf("expected disabled hint, got %s", string(resp))
	}
}
