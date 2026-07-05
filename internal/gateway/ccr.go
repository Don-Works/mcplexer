package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	// ccrWorkspace is the reserved store bucket for compression originals so
	// they never collide with real per-workspace code-state.
	ccrWorkspace = "__ccr__"
	// ccrTTLMinutes bounds how long a stashed original outlives its marker.
	ccrTTLMinutes = 120
)

// ccrPut stashes an original payload in the CCR cache keyed by its content
// address, returning the key and whether the write succeeded. Best-effort for
// pipeline stashes (the kill-switch catches an unresolvable marker); callers
// that mint a marker OUTSIDE the pipeline must check ok before emitting it.
func (h *handler) ccrPut(ctx context.Context, original []byte) (string, bool) {
	key := compression.CCRKey(original)
	if h == nil || h.store == nil || len(original) == 0 {
		return key, false
	}
	exp := time.Now().Add(ccrTTLMinutes * time.Minute)
	if err := h.store.SetCodeState(ctx, &store.CodeStateEntry{
		WorkspaceID:  ccrWorkspace,
		Key:          key,
		ValueJSON:    json.RawMessage(original),
		TTLExpiresAt: &exp,
	}); err != nil {
		slog.Debug("ccr put", "err", err)
		return key, false
	}
	return key, true
}

// ccrGet returns the stashed original for a key, if present and unexpired.
// Touch-on-read: a marker being expanded is a marker still live in some
// model's context (possibly a long or resumed session), so each hit renews
// the TTL — best-effort, a failed touch never blocks the read.
func (h *handler) ccrGet(ctx context.Context, key string) ([]byte, bool) {
	if h == nil || h.store == nil {
		return nil, false
	}
	e, err := h.store.GetCodeState(ctx, ccrWorkspace, key)
	if err != nil || e == nil || len(e.ValueJSON) == 0 {
		return nil, false
	}
	exp := time.Now().Add(ccrTTLMinutes * time.Minute)
	e.TTLExpiresAt = &exp
	if err := h.store.SetCodeState(ctx, e); err != nil {
		slog.Debug("ccr touch", "err", err)
	}
	return []byte(e.ValueJSON), true
}

// persistCCR stores every original a stashing transform dropped, so the markers
// left in the compressed result can be expanded.
func (h *handler) persistCCR(ctx context.Context, obs []compression.Observation) {
	for _, o := range obs {
		for _, blob := range o.Stash {
			_, _ = h.ccrPut(ctx, blob)
		}
	}
}

// ccrMarkersResolve reports whether every CCR marker in result can be expanded
// from the cache. This is the verify-after-compress kill-switch: if it returns
// false, the caller MUST return the original result so the model never sees a
// marker it can't retrieve.
func (h *handler) ccrMarkersResolve(ctx context.Context, result json.RawMessage) bool {
	for _, key := range compression.ParseCCRKeys(string(result)) {
		if _, ok := h.ccrGet(ctx, key); !ok {
			return false
		}
	}
	return true
}

// retrieveToolDefinition is the synthetic tool the model calls to expand a CCR
// marker back to the exact original bytes.
func retrieveToolDefinition() Tool {
	return Tool{
		Name:        "mcpx__retrieve",
		Description: "Expand a compression marker. Given the key from a `[[ccr key=...]]` marker in a tool result, returns the exact original content that was omitted to save tokens. Call this whenever you need the full content behind such a marker.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"The 24-character key from a [[ccr key=...]] marker."}},"required":["key"]}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Retrieve Original",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

// handleRetrieve expands a CCR marker key into the original content, or a
// graceful "re-run to regenerate" message on a cache miss (never a bare error).
func (h *handler) handleRetrieve(ctx context.Context, rawArgs json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Key string `json:"key"`
	}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	key := strings.TrimSpace(args.Key)
	if key == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "key is required"}
	}
	if original, ok := h.ccrGet(ctx, key); ok {
		return ccrTextResult(string(original)), nil
	}
	return ccrTextResult("The original content for key " + key +
		" has expired or was evicted from the compression cache. Re-run the tool call that produced it to regenerate the full content."), nil
}

func ccrTextResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return b
}
