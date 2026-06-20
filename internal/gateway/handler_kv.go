package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	kvDefaultTTLMinutes   = 120      // 2h; hotter scratch than the 24h data workbench
	kvMaxKeyLen           = 256      // bytes
	kvMaxValueBytes       = 1 << 20  // 1 MiB per value
	kvMaxKeysPerWorkspace = 256      // keys per workspace
	kvMaxTotalBytes       = 16 << 20 // 16 MiB per workspace
)

func (h *handler) dispatchKVTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	switch name {
	case "kv__set":
		resp, err := h.handleKVSet(ctx, raw)
		return resp, err, true
	case "kv__get":
		resp, err := h.handleKVGet(ctx, raw)
		return resp, err, true
	case "kv__list":
		resp, err := h.handleKVList(ctx, raw)
		return resp, err, true
	case "kv__delete":
		resp, err := h.handleKVDelete(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

func (h *handler) handleKVSet(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Key         string          `json:"key"`
		Value       json.RawMessage `json:"value"`
		TTLMinutes  *int            `json:"ttl_minutes"`
		Pinned      bool            `json:"pinned"`
		WorkspaceID string          `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	key := strings.TrimSpace(args.Key)
	if key == "" {
		return marshalErrorResult("key is required"), nil
	}
	if len(key) > kvMaxKeyLen {
		return marshalErrorResult(fmt.Sprintf("key too long: %d > %d bytes", len(key), kvMaxKeyLen)), nil
	}
	if len(args.Value) == 0 {
		return marshalErrorResult("value is required (use kv__delete to remove a key)"), nil
	}
	if len(args.Value) > kvMaxValueBytes {
		return marshalErrorResult(fmt.Sprintf("value too large: %d > %d bytes", len(args.Value), kvMaxValueBytes)), nil
	}

	// Enforce per-workspace key-count and total-byte caps. The key being
	// overwritten does not count against either ceiling.
	existing, err := h.store.ListCodeState(ctx, store.CodeStateFilter{
		WorkspaceID: workspaceID, Limit: kvMaxKeysPerWorkspace + 1,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("set failed: %v", err)), nil
	}
	count, total := 0, 0
	for _, e := range existing {
		if e.Key == key {
			continue
		}
		count++
		total += e.Bytes
	}
	if count+1 > kvMaxKeysPerWorkspace {
		return marshalErrorResult(fmt.Sprintf(
			"workspace kv store has too many keys (%d, max %d) — kv__delete some first",
			count+1, kvMaxKeysPerWorkspace)), nil
	}
	if total+len(args.Value) > kvMaxTotalBytes {
		return marshalErrorResult(fmt.Sprintf(
			"workspace kv store is full (%d > %d bytes) — kv__delete some keys first",
			total+len(args.Value), kvMaxTotalBytes)), nil
	}

	e := &store.CodeStateEntry{
		WorkspaceID:     workspaceID,
		Key:             key,
		ValueJSON:       args.Value,
		Pinned:          args.Pinned,
		SourceSessionID: h.sessions.sessionID(),
		TTLExpiresAt:    kvTTL(args.TTLMinutes, args.Pinned),
	}
	if err := h.store.SetCodeState(ctx, e); err != nil {
		return marshalErrorResult(fmt.Sprintf("set failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{
		"ok": true, "key": key, "bytes": len(args.Value),
		"pinned": e.Pinned, "ttl_expires_at": e.TTLExpiresAt,
	})
}

func (h *handler) handleKVGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Key         string `json:"key"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	key := strings.TrimSpace(args.Key)
	if key == "" {
		return marshalErrorResult("key is required"), nil
	}
	e, err := h.store.GetCodeState(ctx, workspaceID, key)
	if errors.Is(err, store.ErrNotFound) {
		// Missing or expired keys return null so callers can branch on it
		// (e.g. `const d = kv.get({key}) || buildExpensiveDataset()`).
		return marshalJSONResult(nil)
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("get failed: %v", err)), nil
	}
	// Return the stored value verbatim so the sandbox auto-unwraps it back to
	// the original JS value.
	return marshalJSONResult(json.RawMessage(e.ValueJSON))
}

func (h *handler) handleKVList(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Prefix         string `json:"prefix"`
		IncludeExpired bool   `json:"include_expired"`
		Limit          int    `json:"limit"`
		Offset         int    `json:"offset"`
		WorkspaceID    string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	rows, err := h.store.ListCodeState(ctx, store.CodeStateFilter{
		WorkspaceID: workspaceID, Prefix: args.Prefix,
		IncludeExpired: args.IncludeExpired, Limit: args.Limit, Offset: args.Offset,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("list failed: %v", err)), nil
	}
	out := make([]map[string]any, 0, len(rows))
	total := 0
	for _, e := range rows {
		total += e.Bytes
		out = append(out, kvEntryView(e))
	}
	return marshalJSONResult(map[string]any{
		"count": len(out), "total_bytes": total, "keys": out,
	})
}

func (h *handler) handleKVDelete(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Key         string `json:"key"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	key := strings.TrimSpace(args.Key)
	if key == "" {
		return marshalErrorResult("key is required"), nil
	}
	err := h.store.DeleteCodeState(ctx, workspaceID, key)
	if errors.Is(err, store.ErrNotFound) {
		// Delete is idempotent — absent keys are not an error.
		return marshalJSONResult(map[string]any{"ok": true, "deleted": false, "key": key})
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("delete failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{"ok": true, "deleted": true, "key": key})
}

func kvEntryView(e store.CodeStateEntry) map[string]any {
	return map[string]any{
		"key": e.Key, "bytes": e.Bytes, "pinned": e.Pinned,
		"ttl_expires_at": e.TTLExpiresAt, "source_session_id": e.SourceSessionID,
		"created_at": e.CreatedAt, "updated_at": e.UpdatedAt,
	}
}

// kvTTL mirrors dataTTL but defaults to the shorter kv scratch window. A nil ttl
// with pinned=true means no expiry; ttl<=0 also means no expiry.
func kvTTL(ttl *int, pinned bool) *time.Time {
	minutes := kvDefaultTTLMinutes
	if ttl != nil {
		minutes = *ttl
	}
	if minutes <= 0 || pinned && ttl == nil {
		return nil
	}
	t := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	return &t
}
