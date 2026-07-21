package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/harnesscontext"
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
	case "data__harvest_harness_context":
		resp, err := h.handleDataHarvestHarnessContext(ctx, raw)
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

func (h *handler) handleDataHarvestHarnessContext(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name          string   `json:"name"`
		Harnesses     []string `json:"harnesses"`
		WorkspaceID   string   `json:"workspace_id"`
		HomeDir       string   `json:"home_dir"`
		WorkDir       string   `json:"work_dir"`
		MaxFiles      int      `json:"max_files"`
		MaxFileBytes  int      `json:"max_bytes_per_file"`
		MaxTotalBytes int      `json:"max_total_bytes"`
		TTLMinutes    *int     `json:"ttl_minutes"`
		Pinned        bool     `json:"pinned"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	var harnesses []harnesscontext.Harness
	for _, hs := range args.Harnesses {
		harnesses = append(harnesses, harnesscontext.Harness(hs))
	}
	collectionName := dataHarvestCollectionName(args.Name)
	batchID := "harvest-" + time.Now().UTC().Format("20060102T150405Z")
	result, err := harnesscontext.Harvest(harnesscontext.Options{
		Harnesses:      harnesses,
		HomeDir:        args.HomeDir,
		WorkDir:        h.dataHarvestWorkDir(ctx, args.WorkDir),
		MaxFiles:       args.MaxFiles,
		MaxFileBytes:   args.MaxFileBytes,
		MaxTotalBytes:  args.MaxTotalBytes,
		HarvestBatchID: batchID,
		CollectionName: collectionName,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("harvest failed: %v", err)), nil
	}
	items, err := harnesscontext.BuildDataItems(result.Documents)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("harvest failed: %v", err)), nil
	}
	if len(items) > dataMaxItems {
		return marshalErrorResult(fmt.Sprintf("too many items: %d > %d", len(items), dataMaxItems)), nil
	}
	if payloadSize(items) > dataMaxPayloadBytes {
		return marshalErrorResult("payload is too large for scratch ingest"), nil
	}
	collection, rpc := h.ingestDataHarvest(ctx, workspaceID, collectionName, batchID, args.TTLMinutes, args.Pinned, items)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	return marshalJSONResult(dataHarvestResponse(collectionName, batchID, result, collection))
}

func dataHarvestCollectionName(name string) string {
	if strings.TrimSpace(name) == "" {
		return harnesscontext.DefaultCollectionName
	}
	return strings.TrimSpace(name)
}

func (h *handler) dataHarvestWorkDir(ctx context.Context, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	ancestors := h.routingWorkspaceAncestors(ctx)
	if len(ancestors) > 0 {
		return ancestors[0].RootPath
	}
	return ""
}

func (h *handler) ingestDataHarvest(
	ctx context.Context, workspaceID, name, batchID string, ttl *int, pinned bool,
	items []store.DataItem,
) (map[string]any, *RPCError) {
	if len(items) == 0 {
		return nil, nil
	}
	tagsJSON, _ := json.Marshal([]string{"harvest", "harness-context", batchID})
	schemaJSON, _ := json.Marshal(map[string]any{
		"documents": len(items),
		"columns": map[string]string{
			"harness":          "string",
			"source_path":      "string",
			"source_kind":      "string",
			"title":            "string",
			"content":          "string",
			"source_hash":      "string",
			"size_bytes":       "number",
			"modified_at":      "string",
			"harvest_batch_id": "string",
		},
	})
	metadataJSON, _ := json.Marshal(map[string]any{
		"source":           "data__harvest_harness_context",
		"harvest_batch_id": batchID,
	})
	c := &store.DataCollection{
		WorkspaceID:     workspaceID,
		Name:            name,
		Kind:            store.DataWorkbenchKindDocs,
		TagsJSON:        tagsJSON,
		SchemaJSON:      schemaJSON,
		MetadataJSON:    metadataJSON,
		Pinned:          pinned,
		SourceSessionID: h.sessions.sessionID(),
	}
	c.TTLExpiresAt = dataTTL(ttl, pinned)
	if err := h.store.IngestDataCollection(ctx, c, items); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("ingest failed: %v", err)}
	}
	return dataCollectionView(*c), nil
}

func dataHarvestResponse(
	collectionName, batchID string, result *harnesscontext.Result, collection map[string]any,
) map[string]any {
	return map[string]any{
		"ok":               true,
		"collection_name":  collectionName,
		"collection":       collection,
		"harvest_batch_id": batchID,
		"total_found":      result.Found,
		"total_ingested":   result.Ingested,
		"total_skipped":    result.Skipped,
		"total_excluded":   result.Excluded,
		"files":            result.Files,
		"errors":           result.Errors,
	}
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
