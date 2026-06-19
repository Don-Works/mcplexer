package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	dataDefaultTTLMinutes = 24 * 60
	dataMaxItems          = 5000
	dataMaxPayloadBytes   = 5 << 20
)

func (h *handler) dispatchDataTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	switch name {
	case "data__ingest":
		resp, err := h.handleDataIngest(ctx, raw)
		return resp, err, true
	case "data__list":
		resp, err := h.handleDataList(ctx, raw)
		return resp, err, true
	case "data__describe":
		resp, err := h.handleDataDescribe(ctx, raw)
		return resp, err, true
	case "data__query":
		resp, err := h.handleDataQuery(ctx, raw)
		return resp, err, true
	case "data__search":
		resp, err := h.handleDataSearch(ctx, raw)
		return resp, err, true
	case "data__drop":
		resp, err := h.handleDataDrop(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

func (h *handler) handleDataIngest(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name        string            `json:"name"`
		Kind        string            `json:"kind"`
		Rows        []json.RawMessage `json:"rows"`
		Documents   []json.RawMessage `json:"documents"`
		Text        string            `json:"text"`
		Tags        []string          `json:"tags"`
		TTLMinutes  *int              `json:"ttl_minutes"`
		Pinned      bool              `json:"pinned"`
		WorkspaceID string            `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	items, schema, kind, err := buildDataItems(args.Kind, args.Rows, args.Documents, args.Text)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	if len(items) > dataMaxItems {
		return marshalErrorResult(fmt.Sprintf("too many items: %d > %d", len(items), dataMaxItems)), nil
	}
	if payloadSize(items) > dataMaxPayloadBytes {
		return marshalErrorResult("payload is too large for scratch ingest"), nil
	}
	tagsJSON, _ := json.Marshal(args.Tags)
	schemaJSON, _ := json.Marshal(schema)
	c := &store.DataCollection{
		WorkspaceID:     workspaceID,
		Name:            strings.TrimSpace(args.Name),
		Kind:            kind,
		TagsJSON:        tagsJSON,
		SchemaJSON:      schemaJSON,
		Pinned:          args.Pinned,
		SourceSessionID: h.sessions.sessionID(),
	}
	if c.Name == "" {
		return marshalErrorResult("name is required"), nil
	}
	c.TTLExpiresAt = dataTTL(args.TTLMinutes, args.Pinned)
	if err := h.store.IngestDataCollection(ctx, c, items); err != nil {
		return marshalErrorResult(fmt.Sprintf("ingest failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{"ok": true, "collection": dataCollectionView(*c)})
}

func (h *handler) handleDataList(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Tags           []string `json:"tags"`
		IncludeExpired bool     `json:"include_expired"`
		Limit          int      `json:"limit"`
		Offset         int      `json:"offset"`
		WorkspaceID    string   `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	rows, err := h.store.ListDataCollections(ctx, store.DataCollectionFilter{
		WorkspaceID: workspaceID, Tags: args.Tags, IncludeExpired: args.IncludeExpired,
		Limit: args.Limit, Offset: args.Offset,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("list failed: %v", err)), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, dataCollectionView(row))
	}
	return marshalJSONResult(map[string]any{"count": len(out), "collections": out})
}

func (h *handler) handleDataDescribe(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct{ Name, WorkspaceID string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	c, err := h.store.GetDataCollection(ctx, workspaceID, strings.TrimSpace(args.Name))
	if err != nil {
		return dataStoreError("describe", err), nil
	}
	return marshalJSONResult(map[string]any{"collection": dataCollectionView(*c)})
}

func (h *handler) handleDataQuery(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name, SQL, WorkspaceID string
		Limit                  int
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	rows, err := h.store.QueryDataCollection(ctx, store.DataQuery{
		WorkspaceID: workspaceID, Name: strings.TrimSpace(args.Name),
		SQL: args.SQL, Limit: args.Limit,
	})
	if err != nil {
		return dataStoreError("query", err), nil
	}
	return marshalJSONResult(map[string]any{"count": len(rows), "rows": rows})
}

func (h *handler) handleDataSearch(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name, Query, WorkspaceID string
		Limit                    int
		Semantic                 bool `json:"semantic"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	hits, err := h.store.SearchDataCollection(ctx, store.DataSearch{
		WorkspaceID: workspaceID, Name: strings.TrimSpace(args.Name),
		Query: args.Query, Limit: args.Limit,
	})
	if err != nil {
		return dataStoreError("search", err), nil
	}
	return marshalJSONResult(map[string]any{
		"count": len(hits), "hits": hits, "semantic": false,
		"note": "semantic retrieval is staged; FTS5 lexical search was used",
	})
}

func (h *handler) handleDataDrop(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct{ Name, WorkspaceID string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	if err := h.store.DropDataCollection(ctx, workspaceID, strings.TrimSpace(args.Name)); err != nil {
		return dataStoreError("drop", err), nil
	}
	return marshalJSONResult(map[string]any{"ok": true, "dropped": args.Name})
}

func (h *handler) dataWorkspace(ctx context.Context, override string, write bool) (string, *RPCError) {
	workspaceID := strings.TrimSpace(override)
	if workspaceID == "" {
		workspaceID = h.currentWorkspaceID(ctx)
	}
	if workspaceID == "" {
		return "", &RPCError{Code: CodeInvalidRequest, Message: "workspace_id required"}
	}
	if write {
		return workspaceID, h.requireWorkspaceWrite(ctx, workspaceID)
	}
	return workspaceID, h.requireWorkspaceRead(ctx, workspaceID)
}

func dataCollectionView(c store.DataCollection) map[string]any {
	return map[string]any{
		"id": c.ID, "workspace_id": c.WorkspaceID, "name": c.Name, "kind": c.Kind,
		"tags": jsonRaw(c.TagsJSON, []any{}), "schema": jsonRaw(c.SchemaJSON, map[string]any{}),
		"row_count": c.RowCount, "doc_count": c.DocCount, "pinned": c.Pinned,
		"ttl_expires_at": c.TTLExpiresAt, "source_session_id": c.SourceSessionID,
		"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
	}
}

func jsonRaw(raw json.RawMessage, fallback any) any {
	if len(raw) == 0 {
		return fallback
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return fallback
	}
	return out
}

func dataStoreError(op string, err error) json.RawMessage {
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("data collection not found")
	}
	return marshalErrorResult(fmt.Sprintf("%s failed: %v", op, err))
}

func rpcResult(rpc *RPCError) json.RawMessage {
	return marshalErrorResult(rpc.Message)
}
