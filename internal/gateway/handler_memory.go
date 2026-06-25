// handler_memory.go — implements the universal memory__* MCP tools.
// All handlers are workspace-scoped via the session's resolved scope;
// admin operations (cross-workspace browse, offer accept/decline) live
// in internal/control under mcplexer__memory_*.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/memory/harnessimport"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// parseValidAt converts an optional RFC3339 timestamp argument into the
// bi-temporal as-of pointer threaded onto MemoryFilter.ValidAt. An empty
// string is the no-op case (nil, no error → "current beliefs only"). A
// non-empty value that fails to parse yields a clear RPC error so the LLM
// gets actionable feedback instead of a silently-ignored filter.
func parseValidAt(raw string) (*time.Time, *RPCError) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, &RPCError{
			Code: CodeInvalidParams,
			Message: fmt.Sprintf(
				"invalid valid_at %q: must be an RFC3339 timestamp "+
					"(e.g. 2026-01-15T09:00:00Z): %v", raw, err),
		}
	}
	return &t, nil
}

// deriveMemoryName builds a stable kebab-case name from the content's
// leading words plus a short content hash, so memory__save({content}) works
// without an explicit name. Deterministic: the same content re-saves the
// same row (WriteMemory upserts by name); different content sharing a word
// prefix stays distinct via the hash suffix.
func deriveMemoryName(content string) string {
	var parts []string
	for _, w := range strings.Fields(strings.ToLower(content)) {
		w = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, w)
		if w == "" {
			continue
		}
		parts = append(parts, w)
		if len(parts) == 6 {
			break
		}
	}
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(content))
	suffix := fmt.Sprintf("%08x", sum.Sum32())
	if len(parts) == 0 {
		return "memory-" + suffix
	}
	name := strings.Join(parts, "-")
	if len(name) > 48 {
		name = name[:48]
	}
	return name + "-" + suffix
}

// dispatchMemoryTool routes memory__* tool names. The caller (the main
// tools/call dispatcher) is responsible for everything else.
func (h *handler) dispatchMemoryTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.memorySvc == nil {
		switch name {
		case "memory__save", "memory__recall", "memory__recall_about",
			"memory__list", "memory__list_entities",
			"memory__related_entities", "memory__spreading_activation",
			"memory__co_recalled", "memory__suggestions",
			"memory__link_entity", "memory__unlink_entity",
			"memory__get", "memory__invalidate", "memory__pin", "memory__unpin",
			"memory__forget", "memory__forget_by_source",
			"memory__offer_memory", "memory__request_memory":
			return marshalErrorResult("Memory subsystem is not enabled."), nil, true
		}
		return nil, nil, false
	}
	switch name {
	case "memory__save":
		resp, err := h.handleMemorySave(ctx, raw)
		return resp, err, true
	case "memory__get":
		resp, err := h.handleMemoryGet(ctx, raw)
		return resp, err, true
	case "memory__invalidate":
		resp, err := h.handleMemoryInvalidate(ctx, raw)
		return resp, err, true
	case "memory__pin":
		resp, err := h.handleMemoryPin(ctx, raw, true)
		return resp, err, true
	case "memory__unpin":
		resp, err := h.handleMemoryPin(ctx, raw, false)
		return resp, err, true
	case "memory__recall":
		resp, err := h.handleMemoryRecall(ctx, raw)
		return resp, err, true
	case "memory__recall_about":
		resp, err := h.handleMemoryRecallAbout(ctx, raw)
		return resp, err, true
	case "memory__list":
		resp, err := h.handleMemoryList(ctx, raw)
		return resp, err, true
	case "memory__list_entities":
		resp, err := h.handleMemoryListEntities(ctx, raw)
		return resp, err, true
	case "memory__related_entities":
		resp, err := h.handleMemoryRelatedEntities(ctx, raw)
		return resp, err, true
	case "memory__spreading_activation":
		resp, err := h.handleMemorySpreadingActivation(ctx, raw)
		return resp, err, true
	case "memory__co_recalled":
		resp, err := h.handleMemoryCoRecalled(ctx, raw)
		return resp, err, true
	case "memory__suggestions":
		resp, err := h.handleMemorySuggestions(ctx, raw)
		return resp, err, true
	case "memory__link_entity":
		resp, err := h.handleMemoryLinkEntity(ctx, raw)
		return resp, err, true
	case "memory__unlink_entity":
		resp, err := h.handleMemoryUnlinkEntity(ctx, raw)
		return resp, err, true
	case "memory__forget":
		resp, err := h.handleMemoryForget(ctx, raw)
		return resp, err, true
	case "memory__forget_by_source":
		resp, err := h.handleMemoryForgetBySource(ctx, raw)
		return resp, err, true
	case "memory__offer_memory":
		resp, err := h.handleMemoryOffer(ctx, raw)
		return resp, err, true
	case "memory__request_memory":
		resp, err := h.handleMemoryRequest(ctx, raw)
		return resp, err, true
	case "memory__import_harness":
		resp, err := h.handleMemoryImportHarness(ctx, raw)
		return resp, err, true
	case "memory__sync_status":
		resp, err := h.handleMemorySyncStatus(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

// entityArg is the JSON arg shape every memory__* tool uses for entity
// refs. Mirrors store.EntityRef. Role is optional (defaults to "subject"
// on write, "any role" on filter).
type entityArg struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

func toEntityRefs(args []entityArg) []store.EntityRef {
	if len(args) == 0 {
		return nil
	}
	out := make([]store.EntityRef, 0, len(args))
	for _, a := range args {
		if strings.TrimSpace(a.Kind) == "" || strings.TrimSpace(a.ID) == "" {
			continue
		}
		out = append(out, store.EntityRef{Kind: a.Kind, ID: a.ID, Role: a.Role})
	}
	return out
}

func (h *handler) handleMemorySave(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Name     string         `json:"name"`
		Content  string         `json:"content"`
		Kind     string         `json:"kind"`
		Tags     flexStrings    `json:"tags"`
		Scope    string         `json:"scope"`
		Pinned   bool           `json:"pinned"`
		Meta     map[string]any `json:"metadata"`
		Entities []entityArg    `json:"entities"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	// Aggregate validation — surface every missing required field in
	// one response so the LLM converges in one retry instead of N.
	v := newValidator()
	v.requireStringWithHint("content", args.Content,
		"the actual fact / preference / decision to persist")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if strings.TrimSpace(args.Name) == "" {
		args.Name = deriveMemoryName(args.Content)
	}
	wsID, err := h.resolvePublishScope(ctx, args.Scope)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if wsID != nil {
		if rpc := h.requireWorkspaceWrite(ctx, *wsID); rpc != nil {
			return nil, rpc
		}
	}
	// WriteWithResult (not the back-compat Write facade) so the note-write
	// neighbour scan's surfaced candidate ids reach the caller. The ~40 other
	// callers keep the (string,error) Write facade; only the save tool needs
	// the advisory candidates in its response.
	res, err := h.memorySvc.WriteWithResult(ctx, memory.WriteOptions{
		Name:            args.Name,
		Kind:            args.Kind,
		Content:         args.Content,
		Tags:            args.Tags,
		Metadata:        args.Meta,
		WorkspaceID:     wsID,
		Pinned:          args.Pinned,
		SourceSessionID: h.sessions.sessionID(),
		SourceKind:      store.MemorySourceAgent,
		Entities:        toEntityRefs(args.Entities),
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Save failed: %v", err)), nil
	}
	scopeLbl := "global"
	if wsID != nil {
		scopeLbl = *wsID
	}
	entityLbl := ""
	if n := len(args.Entities); n > 0 {
		entityLbl = fmt.Sprintf(" Linked %d entity ref(s).", n)
	}
	return marshalMemorySaveResult(args.Name, res, scopeLbl, entityLbl), nil
}

// marshalMemorySaveResult renders the save-tool response. When the post-write
// neighbour scan surfaced near-duplicate / potentially-conflicting memories it
// appends an ENRICHED, ACTIONABLE block: each candidate's name + kind
// (duplicate|related) + reason + preview, plus the resolution affordance
// (memory__invalidate to supersede, or keep both). The structured block keeps
// possible_duplicates (ids, back-compat) and adds conflicts (the rich form)
// so the dashboard + agent don't need N follow-up memory__get calls.
func marshalMemorySaveResult(
	name string, res memory.WriteResult, scopeLbl, entityLbl string,
) json.RawMessage {
	var b strings.Builder
	fmt.Fprintf(&b, "Saved memory %s (%s) in scope=%s.%s", name, res.ID, scopeLbl, entityLbl)
	if n := len(res.Candidates); n > 0 {
		fmt.Fprintf(&b,
			" ⚠ %d possibly-related memor%s already exist%s — review for duplicates/conflicts:",
			n, plural(n, "y", "ies"), plural(n, "s", ""))
		for _, c := range res.Candidates {
			fmt.Fprintf(&b, "\n  • [%s] %q — %s", c.Kind, c.Name, c.Reason)
			if c.Preview != "" {
				fmt.Fprintf(&b, " — “%s”", c.Preview)
			}
		}
		b.WriteString("\n  Resolve: memory__invalidate(id, superseded_by_id=" + res.ID +
			") to supersede the old one, or leave both if they're complementary.")
	}
	result := CallToolResult{
		Content: []ToolContent{{Type: "text", Text: b.String()}},
	}
	if len(res.Candidates) > 0 {
		ids := make([]string, len(res.Candidates))
		for i, c := range res.Candidates {
			ids[i] = c.ID
		}
		result.StructuredContent = map[string]any{
			"id":                  res.ID,
			"possible_duplicates": ids,
			"conflicts":           res.Candidates,
		}
	}
	data, _ := json.Marshal(result)
	return data
}

// plural picks the singular or plural suffix for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (h *handler) handleMemoryRecall(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Query          string      `json:"query"`
		Limit          int         `json:"limit"`
		Kind           string      `json:"kind"`
		Tags           flexStrings `json:"tags"`
		IncludeInvalid bool        `json:"include_invalid"`
		ValidAt        string      `json:"valid_at"`
		Entities       []entityArg `json:"entities"`
		EntitiesAny    []entityArg `json:"entities_any"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	validAt, rpcErr := parseValidAt(args.ValidAt)
	if rpcErr != nil {
		return nil, rpcErr
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	entAnd := toEntityRefs(args.Entities)
	entAny := toEntityRefs(args.EntitiesAny)
	// memory__recall lifts the workspace filter only when EVERY ref the
	// caller passed (across whichever side(s) are non-empty) is global-
	// kind + subject/derived_from role. A single peer-local kind or
	// mentioned role keeps current scope semantics. An empty side does
	// NOT veto — only refs that were actually supplied must qualify. See
	// store.EntityRecallCanEscapeScope.
	escape := false
	if len(entAnd)+len(entAny) > 0 {
		okAnd := len(entAnd) == 0 || store.EntityRecallCanEscapeScope(entAnd)
		okAny := len(entAny) == 0 || store.EntityRecallCanEscapeScope(entAny)
		escape = okAnd && okAny
	}
	hits, err := h.memorySvc.Recall(ctx, store.MemoryFilter{
		Scope:                    h.sessionScope(ctx),
		Kind:                     args.Kind,
		Tags:                     args.Tags,
		IncludeInvalid:           args.IncludeInvalid,
		ValidAt:                  validAt,
		Entities:                 entAnd,
		EntitiesAny:              entAny,
		EntityDrivenIgnoresScope: escape,
	}, args.Query, limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Recall failed: %v", err)), nil
	}
	if len(hits) == 0 {
		b, _ := json.Marshal(struct {
			Count int               `json:"count"`
			Hits  []memoryRecallHit `json:"hits"`
		}{Count: 0, Hits: []memoryRecallHit{}})
		return marshalToolResult(string(b)), nil
	}
	hitItems := make([]memoryRecallHit, 0, len(hits))
	for _, h := range hits {
		sc := "global"
		if h.Entry.WorkspaceID != nil {
			sc = *h.Entry.WorkspaceID
		}
		tags := readTagsList(h.Entry.TagsJSON)
		hitItems = append(hitItems, memoryRecallHit{
			ID:      h.Entry.ID,
			Name:    h.Entry.Name,
			Kind:    h.Entry.Kind,
			Content: h.Entry.Content,
			Tags:    tags,
			Scope:   sc,
			Score:   h.Score,
			Source:  h.Source,
		})
	}
	b, _ := json.Marshal(struct {
		Count int               `json:"count"`
		Hits  []memoryRecallHit `json:"hits"`
	}{Count: len(hitItems), Hits: hitItems})
	return marshalToolResult(string(b)), nil
}

// handleMemoryRecallAbout is the "tell me everything about X" surface.
// Convenience over memory__recall with entities=[{kind,id}] — the named
// shape is load-bearing for agents because the verb "recall about"
// matches the conceptual model better than a kwargs blob does.
func (h *handler) handleMemoryRecallAbout(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Kind    string   `json:"kind"`
		ID      string   `json:"id"`
		Role    string   `json:"role"`
		Query   string      `json:"query"`
		Limit   int         `json:"limit"`
		MemKind string      `json:"memory_kind"`
		Tags    flexStrings `json:"tags"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Kind) == "" || strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "kind and id are required"}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	ref := store.EntityRef{Kind: args.Kind, ID: args.ID, Role: args.Role}
	refs := []store.EntityRef{ref}
	// "Tell me everything about X" is the canonical entity-driven path.
	// When X is a globally-identifiable kind (person, task, skill, …)
	// and the role is subject / derived_from / unspecified, lift the
	// workspace filter so facts about X surface from wherever they were
	// originally saved. Role=mentioned keeps current scope semantics
	// because passing references are contextual, not definitional.
	escape := store.EntityRecallCanEscapeScope(refs)
	hits, err := h.memorySvc.Recall(ctx, store.MemoryFilter{
		Scope:                    h.sessionScope(ctx),
		Kind:                     args.MemKind,
		Tags:                     args.Tags,
		Entities:                 refs,
		EntityDrivenIgnoresScope: escape,
	}, args.Query, limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Recall failed: %v", err)), nil
	}
	if len(hits) == 0 {
		scopeLbl := "this scope"
		if escape {
			scopeLbl = "any workspace"
		}
		return marshalToolResult(fmt.Sprintf(
			"No memories about %s:%s in %s yet.",
			args.Kind, args.ID, scopeLbl)), nil
	}
	return marshalToolResult(renderRecallHits(hits)), nil
}

func (h *handler) handleMemoryListEntities(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Kind   string `json:"kind"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	if args.Limit > 200 {
		args.Limit = 200
	}
	entities, err := h.memorySvc.Entities(ctx, store.EntityFilter{
		Scope:  h.sessionScope(ctx),
		Kind:   args.Kind,
		Limit:  args.Limit,
		Offset: args.Offset,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("List entities failed: %v", err)), nil
	}
	if len(entities) == 0 {
		b, _ := json.Marshal(struct {
			Count    int                `json:"count"`
			Entities []memoryEntityItem `json:"entities"`
		}{Count: 0, Entities: []memoryEntityItem{}})
		return marshalToolResult(string(b)), nil
	}
	items := make([]memoryEntityItem, 0, len(entities))
	for _, e := range entities {
		items = append(items, memoryEntityItem{
			Kind:         e.Kind,
			ID:           e.ID,
			MemoryCount:  e.MemoryCount,
			LastLinkedAt: e.LastLinkedAt.Format("2006-01-02 15:04"),
		})
	}
	b, _ := json.Marshal(struct {
		Count    int                `json:"count"`
		Entities []memoryEntityItem `json:"entities"`
	}{Count: len(items), Entities: items})
	return marshalToolResult(string(b)), nil
}

func (h *handler) handleMemoryRelatedEntities(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Kind) == "" || strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "kind and id are required"}
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if args.Limit > 100 {
		args.Limit = 100
	}
	related, err := h.memorySvc.RelatedEntities(ctx,
		store.EntityRef{Kind: args.Kind, ID: args.ID},
		h.sessionScope(ctx), args.Limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Related failed: %v", err)), nil
	}
	if len(related) == 0 {
		return marshalToolResult(fmt.Sprintf(
			"No entities co-link with %s:%s yet.", args.Kind, args.ID)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entit(ies) co-linked with %s:%s.\n\n",
		len(related), args.Kind, args.ID)
	for i, r := range related {
		fmt.Fprintf(&b, "%d. **%s:%s** — shared with %d memor%s, last seen %s\n",
			i+1, r.Kind, r.ID, r.SharedCount,
			pluralY(r.SharedCount), r.LastSeenAt.Format("2006-01-02"))
	}
	return marshalToolResult(b.String()), nil
}

func (h *handler) handleMemorySpreadingActivation(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Kind) == "" || strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "kind and id are required"}
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	spread, err := h.memorySvc.SpreadingActivation(ctx,
		store.EntityRef{Kind: args.Kind, ID: args.ID},
		h.sessionScope(ctx), args.Limit, 20, 8)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Spread failed: %v", err)), nil
	}
	if len(spread) == 0 {
		return marshalToolResult(
			"No semantic neighbours surfaced for that entity. Either there " +
				"are no embedded memories about it yet, or no embedding " +
				"provider is configured — without one, spreading activation " +
				"degrades to an empty set.",
		), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d adjacent entit(ies) via spreading activation from %s:%s.\n\n",
		len(spread), args.Kind, args.ID)
	for i, r := range spread {
		fmt.Fprintf(&b, "%d. **%s:%s** — activation score %d (vec-neighbour aggregate)\n",
			i+1, r.Kind, r.ID, r.SharedCount)
	}
	return marshalToolResult(b.String()), nil
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func (h *handler) handleMemoryCoRecalled(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		MemoryID string `json:"memory_id"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.MemoryID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "memory_id is required"}
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	rows, err := h.memorySvc.CoRecalled(ctx, args.MemoryID,
		h.sessionScope(ctx), args.Limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Co-recall failed: %v", err)), nil
	}
	if len(rows) == 0 {
		return marshalToolResult(
			"No co-recall signal yet. Either no recall events have been " +
				"logged for this memory, or MCPLEXER_RECALL_TRACKING=1 is " +
				"not set on the daemon (recall tracking is off by default).",
		), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d co-recalled memor(ies).\n\n", len(rows))
	for i, r := range rows {
		fmt.Fprintf(&b, "%d. **%s** — co-occurred %d×, score %.3f, last seen %s\n   id: %s\n",
			i+1, r.Name, r.CoOccurrences, r.Score,
			r.LastSeenAt.Format("2006-01-02 15:04"), r.MemoryID)
	}
	return marshalToolResult(b.String()), nil
}

func (h *handler) handleMemorySuggestions(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		MemoryID string `json:"memory_id"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.MemoryID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "memory_id is required"}
	}
	if args.Limit <= 0 {
		args.Limit = 12
	}
	if args.Limit > 50 {
		args.Limit = 50
	}
	rows, err := h.memorySvc.SuggestionsFor(ctx, args.MemoryID,
		h.sessionScope(ctx), args.Limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Suggestions failed: %v", err)), nil
	}
	if len(rows) == 0 {
		return marshalToolResult(
			"No suggestions surfaced. The three signal sources (co-recall, " +
				"related-entity, semantic) all came up empty — either this " +
				"memory has no entity links yet, no embedder is configured, " +
				"and no recall events have built up co-occurrence history.",
		), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d suggestion(s).\n\n", len(rows))
	for i, r := range rows {
		fmt.Fprintf(&b, "%d. **%s** — via %s (score %.3f)\n   id: %s\n   %s\n",
			i+1, r.Name, r.Source, r.Score, r.MemoryID, r.Reason)
	}
	return marshalToolResult(b.String()), nil
}

func (h *handler) handleMemoryLinkEntity(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		MemoryID string `json:"memory_id"`
		Kind     string `json:"kind"`
		ID       string `json:"id"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.MemoryID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "memory_id is required"}
	}
	if strings.TrimSpace(args.Kind) == "" || strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "kind and id are required"}
	}
	entry, err := h.memorySvc.Get(ctx, args.MemoryID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, true); rpc != nil {
		return nil, rpc
	}
	ref := store.EntityRef{Kind: args.Kind, ID: args.ID, Role: args.Role}
	if err := h.memorySvc.LinkEntity(ctx, args.MemoryID, ref,
		h.sessions.sessionID()); err != nil {
		return marshalErrorResult(fmt.Sprintf("Link failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Linked memory %s to %s:%s (role=%s).",
		args.MemoryID, args.Kind, args.ID,
		defaultRole(args.Role))), nil
}

func (h *handler) handleMemoryUnlinkEntity(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		MemoryID string `json:"memory_id"`
		Kind     string `json:"kind"`
		ID       string `json:"id"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.MemoryID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "memory_id is required"}
	}
	if strings.TrimSpace(args.Kind) == "" || strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams,
			Message: "kind and id are required"}
	}
	entry, err := h.memorySvc.Get(ctx, args.MemoryID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, true); rpc != nil {
		return nil, rpc
	}
	ref := store.EntityRef{Kind: args.Kind, ID: args.ID, Role: args.Role}
	if err := h.memorySvc.UnlinkEntity(ctx, args.MemoryID, ref); err != nil {
		return marshalErrorResult(fmt.Sprintf("Unlink failed: %v", err)), nil
	}
	roleLbl := args.Role
	if roleLbl == "" {
		roleLbl = "any"
	}
	return marshalToolResult(fmt.Sprintf(
		"Unlinked memory %s from %s:%s (role=%s).",
		args.MemoryID, args.Kind, args.ID, roleLbl)), nil
}

func defaultRole(r string) string {
	if strings.TrimSpace(r) == "" {
		return "subject"
	}
	return r
}

func (h *handler) requireMemoryEntryAccess(
	ctx context.Context,
	entry *store.MemoryEntry,
	write bool,
) *RPCError {
	if entry == nil || entry.WorkspaceID == nil || strings.TrimSpace(*entry.WorkspaceID) == "" {
		// GLOBAL (NULL-workspace) row. Reads stay open (out of scope here);
		// destructive/mutating writes (forget/invalidate/pin/unpin) need a
		// provenance check so a low-trust worker on the tool allowlist can't
		// drop a global memory it never authored.
		if write {
			return h.requireGlobalMemoryWrite(ctx, entry)
		}
		return nil
	}
	if write {
		return h.requireWorkspaceWrite(ctx, *entry.WorkspaceID)
	}
	return h.requireWorkspaceRead(ctx, *entry.WorkspaceID)
}

// requireGlobalMemoryWrite guards mutating ops on a GLOBAL (NULL-workspace)
// memory row. There's no workspace grant to lean on for global rows, so the
// decision rides on provenance stamped at write time plus the trust level of
// the caller's dispatch path:
//
//   - ALLOW when the row's SourceSessionID matches this session — the author
//     can always mutate what it wrote (same-origin).
//   - ALLOW when ctx is marked as a trusted in-process worker dispatch
//     (IsInProcessWorkerCall) — this is the memory consolidator's path. The
//     consolidator runs AS an in-process worker (nil session, so same-origin
//     can never match) and its core job is invalidating/pruning foreign-
//     authored GLOBAL rows; the in-process marker is set only by the daemon's
//     worker runner, never on an external JSON-RPC call.
//   - ALLOW when there is no worker context on ctx at all — ordinary
//     (non-worker) interactive / in-process sessions are trusted to curate the
//     global store. (This tightens only the EXTERNAL worker path; interactive
//     non-worker clients remain unguarded on global destructive ops, as before.)
//   - DENY only when a worker context IS present AND the call is NOT a trusted
//     in-process dispatch AND it is not same-origin — i.e. a bounded EXTERNAL
//     worker on the tool allowlist must not be able to forget/invalidate/pin/
//     unpin a global memory another session authored.
func (h *handler) requireGlobalMemoryWrite(
	ctx context.Context, entry *store.MemoryEntry,
) *RPCError {
	if entry != nil {
		if sid := h.sessions.sessionID(); sid != "" &&
			entry.SourceSessionID == sid {
			return nil
		}
	}
	if IsInProcessWorkerCall(ctx) {
		return nil
	}
	if _, isWorker := workerWorkspaceAccessFromContext(ctx); !isWorker {
		return nil
	}
	return &RPCError{
		Code: CodeInvalidRequest,
		Message: "global memories may only be mutated by the session that " +
			"authored them or a trusted in-process session; a worker cannot " +
			"forget/invalidate/pin/unpin a global memory it did not create",
	}
}

func (h *handler) requireWorkspaceRead(ctx context.Context, workspaceID string) *RPCError {
	if rpc := h.requireWorkerWorkspaceAccess(ctx, workspaceID, false); rpc != nil {
		return rpc
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok {
		return nil
	}
	for _, id := range h.readableWorkspaceIDs(ctx) {
		if id == workspaceID {
			return nil
		}
	}
	return &RPCError{
		Code:    CodeInvalidRequest,
		Message: fmt.Sprintf("workspace %q is outside this session's readable scope", workspaceID),
	}
}

func (h *handler) requireWorkspaceWrite(ctx context.Context, workspaceID string) *RPCError {
	if rpc := h.requireWorkerWorkspaceAccess(ctx, workspaceID, true); rpc != nil {
		return rpc
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok {
		return nil
	}
	for _, id := range h.readableWorkspaceIDs(ctx) {
		if id == workspaceID {
			return nil
		}
	}
	return &RPCError{
		Code:    CodeInvalidRequest,
		Message: fmt.Sprintf("workspace %q is outside this session's writable scope", workspaceID),
	}
}

func (h *handler) memoryForgetBySourceScope(ctx context.Context) (store.SkillScope, *RPCError) {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok {
		ids := make([]string, 0, len(c.Grants))
		for _, g := range c.Grants {
			if grantCanWrite(g.Access) && strings.TrimSpace(g.WorkspaceID) != "" {
				ids = append(ids, g.WorkspaceID)
			}
		}
		if len(ids) == 0 {
			return store.SkillScope{}, &RPCError{
				Code:    CodeInvalidRequest,
				Message: "memory__forget_by_source requires a writable workspace grant",
			}
		}
		return store.SkillScope{WorkspaceIDs: ids}, nil
	}
	return store.SkillScope{WorkspaceIDs: h.readableWorkspaceIDs(ctx)}, nil
}

func renderRecallHits(hits []store.MemoryHit) string {
	// Structured JSON so execute_code / workers get parseable objects
	// (count + hits) instead of prose. Auto-unwrap in codemode yields
	// usable {count, hits: [...] }.
	hitItems := make([]memoryRecallHit, 0, len(hits))
	for _, h := range hits {
		sc := "global"
		if h.Entry.WorkspaceID != nil {
			sc = *h.Entry.WorkspaceID
		}
		tags := readTagsList(h.Entry.TagsJSON)
		hitItems = append(hitItems, memoryRecallHit{
			ID:      h.Entry.ID,
			Name:    h.Entry.Name,
			Kind:    h.Entry.Kind,
			Content: h.Entry.Content,
			Tags:    tags,
			Scope:   sc,
			Score:   h.Score,
			Source:  h.Source,
		})
	}
	b, _ := json.Marshal(struct {
		Count int               `json:"count"`
		Hits  []memoryRecallHit `json:"hits"`
	}{Count: len(hitItems), Hits: hitItems})
	return string(b)
}

func (h *handler) handleMemoryList(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Kind           string      `json:"kind"`
		Tags           flexStrings `json:"tags"`
		Limit          int         `json:"limit"`
		Offset         int         `json:"offset"`
		IncludeInvalid bool        `json:"include_invalid"`
		ValidAt        string      `json:"valid_at"`
		Scope          string      `json:"scope"` // "" | "any" | "workspace_only" | "global_only"
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	validAt, rpcErr := parseValidAt(args.ValidAt)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	if args.Limit > 200 {
		args.Limit = 200
	}
	scopeFilter := ""
	switch strings.TrimSpace(args.Scope) {
	case "", "any":
		// default: workspaces ∪ global
	case "workspace_only", "global_only":
		scopeFilter = args.Scope
	default:
		return marshalErrorResult(fmt.Sprintf("invalid scope %q — use \"any\" (default), \"workspace_only\", or \"global_only\"", args.Scope)), nil
	}
	entries, err := h.memorySvc.List(ctx, store.MemoryFilter{
		Scope:          h.sessionScope(ctx),
		Kind:           args.Kind,
		Tags:           args.Tags,
		Limit:          args.Limit,
		Offset:         args.Offset,
		IncludeInvalid: args.IncludeInvalid,
		ValidAt:        validAt,
		ScopeFilter:    scopeFilter,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("List failed: %v", err)), nil
	}
	if len(entries) == 0 {
		b, _ := json.Marshal(struct {
			Count    int              `json:"count"`
			Memories []memoryListItem `json:"memories"`
		}{Count: 0, Memories: []memoryListItem{}})
		return marshalToolResult(string(b)), nil
	}
	items := make([]memoryListItem, 0, len(entries))
	for _, e := range entries {
		sc := "global"
		if e.WorkspaceID != nil {
			sc = *e.WorkspaceID
		}
		tags := readTagsList(e.TagsJSON)
		items = append(items, memoryListItem{
			ID:        e.ID,
			Name:      e.Name,
			Kind:      e.Kind,
			Content:   e.Content,
			Tags:      tags,
			UpdatedAt: e.UpdatedAt.Format("2006-01-02 15:04"),
			Scope:     sc,
		})
	}
	b, _ := json.Marshal(struct {
		Count    int              `json:"count"`
		Memories []memoryListItem `json:"memories"`
	}{Count: len(items), Memories: items})
	return marshalToolResult(string(b)), nil
}

func readTagsList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// memoryListItem / memoryListResult etc provide the structured shape for
// memory__list / recall / list_entities so that mcpx__execute_code (and
// delegated workers) receive parsed objects instead of prose text.
// This fixes the "returns 0 rows" symptom for JS consumers: the tool
// result text is now valid JSON that the codemode auto-unwrap parses.
type memoryListItem struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	UpdatedAt string   `json:"updated_at"`
	Scope     string   `json:"scope"`
}

type memoryRecallHit struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Scope   string   `json:"scope"`
	Score   float64  `json:"score"`
	Source  string   `json:"source"`
}

type memoryEntityItem struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	MemoryCount  int    `json:"memory_count"`
	LastLinkedAt string `json:"last_linked_at"`
}

func (h *handler) handleMemoryGet(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	entry, err := h.memorySvc.Get(ctx, args.ID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, false); rpc != nil {
		return nil, rpc
	}
	scope := "global"
	if entry.WorkspaceID != nil {
		scope = *entry.WorkspaceID
	}
	tags := readTagsList(entry.TagsJSON)
	pinned := ""
	if entry.Pinned {
		pinned = " · pinned"
	}
	invalidated := ""
	if entry.TValidEnd != nil {
		invalidated = fmt.Sprintf(" · invalidated %s", entry.TValidEnd.Format("2006-01-02 15:04"))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%s · %s%s%s)\n", entry.Name, entry.Kind, scope, pinned, invalidated)
	fmt.Fprintf(&b, "id: %s · updated: %s\n",
		entry.ID, entry.UpdatedAt.Format("2006-01-02 15:04"))
	if len(tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(tags, ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", entry.Content)
	return marshalToolResult(b.String()), nil
}

func (h *handler) handleMemoryInvalidate(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID             string `json:"id"`
		SupersededByID string `json:"superseded_by_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	entry, err := h.memorySvc.Get(ctx, args.ID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, true); rpc != nil {
		return nil, rpc
	}
	if err := h.memorySvc.Invalidate(ctx, args.ID, args.SupersededByID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Invalidate failed: %v", err)), nil
	}
	supersedeLbl := ""
	if strings.TrimSpace(args.SupersededByID) != "" {
		supersedeLbl = fmt.Sprintf(" (superseded by %s)", args.SupersededByID)
	}
	return marshalToolResult(fmt.Sprintf(
		"Invalidated memory %s%s.", args.ID, supersedeLbl)), nil
}

func (h *handler) handleMemoryPin(
	ctx context.Context, raw json.RawMessage, pinned bool,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.ID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "id is required"}
	}
	entry, err := h.memorySvc.Get(ctx, args.ID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, true); rpc != nil {
		return nil, rpc
	}
	if err := h.memorySvc.SetPinned(ctx, args.ID, pinned); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
		}
		verb := "pin"
		if !pinned {
			verb = "unpin"
		}
		return marshalErrorResult(fmt.Sprintf("%s failed: %v", verb, err)), nil
	}
	verb := "Pinned"
	if !pinned {
		verb = "Unpinned"
	}
	return marshalToolResult(fmt.Sprintf("%s memory %s.", verb, args.ID)), nil
}

func (h *handler) handleMemoryForget(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("id", args.ID,
		"pass the memory id returned from memory__save (call memory__list to see ids)")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	entry, err := h.memorySvc.Get(ctx, args.ID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Get failed: %v", err)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, true); rpc != nil {
		return nil, rpc
	}
	if err := h.memorySvc.Forget(ctx, args.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("No memory with that id. Use memory__list or memory__recall to find valid ids; ids are 26-char ULIDs returned by memory__save."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Forget failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf("Forgot memory %s.", args.ID)), nil
}

func (h *handler) handleMemoryForgetBySource(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		SourceSessionID string `json:"source_session_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("source_session_id", args.SourceSessionID,
		"the session id whose memories you want to bulk-purge")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	scope, rpc := h.memoryForgetBySourceScope(ctx)
	if rpc != nil {
		return nil, rpc
	}
	n, err := h.memorySvc.ForgetBySource(ctx, args.SourceSessionID, scope)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Forget failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Purged %d in-scope memory row(s) from source_session_id=%s.",
		n, args.SourceSessionID)), nil
}

// handleMemoryOffer routes memory__offer_memory through the libp2p
// /mcplexer/memory/1.0.0 protocol. Looks up the local memory and sends
// a thin descriptor (no content) to the paired peer; the receiver
// decides accept/decline asynchronously.
func (h *handler) handleMemoryOffer(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.memoryShare == nil {
		return marshalErrorResult("p2p memory share not enabled — start the daemon with --p2p."), nil
	}
	var args struct {
		PeerID   string `json:"peer_id"`
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", args.PeerID,
		"peer libp2p ID or display name — call mesh__list_peers to see paired peers")
	v.requireStringWithHint("memory_id", args.MemoryID,
		"id of the local memory to offer — call memory__list to see ids")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, args.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(args.PeerID, resolveErr)), nil
	}
	entry, err := h.memorySvc.Get(ctx, args.MemoryID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Memory not found: %s", args.MemoryID)), nil
	}
	if rpc := h.requireMemoryEntryAccess(ctx, entry, false); rpc != nil {
		return nil, rpc
	}
	preview := entry.Content
	if len(preview) > 512 {
		preview = preview[:512] + "…"
	}
	// EntitiesPreview rides on the offer descriptor (top 5, peer-local
	// kinds filtered) so the receiver can see what the memory is about
	// before deciding accept/decline. The full set ships on payload pull.
	links, _ := h.memorySvc.MemoryEntities(ctx, entry.ID)
	preview5 := make([]p2p.EntityLink, 0, 5)
	for _, l := range links {
		if len(preview5) >= 5 {
			break
		}
		if p2p.IsEntityKindPeerLocal(l.EntityKind) {
			continue
		}
		preview5 = append(preview5, p2p.EntityLink{
			Kind: l.EntityKind, ID: l.EntityID, Role: l.Role,
		})
	}
	offer := &p2p.MemoryOffer{
		RemoteID:        entry.ID,
		Name:            entry.Name,
		Kind:            entry.Kind,
		Preview:         preview,
		TagsJSON:        entry.TagsJSON,
		MetadataJSON:    entry.MetadataJSON,
		EmbedModel:      entry.EmbedModel,
		SizeBytes:       int64(len(entry.Content)),
		EntitiesPreview: preview5,
	}
	if err := h.memoryShare.OfferMemory(ctx, peerID, offer); err != nil {
		return marshalErrorResult(fmt.Sprintf("Offer failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Offered memory %s (\"%s\") to peer %s. They'll see it in their dashboard's /memory/shared view.",
		entry.ID, entry.Name, peerID)), nil
}

// handleMemoryRequest pulls a memory from a paired peer + writes it
// locally with provenance. Returns the new local memory id.
func (h *handler) handleMemoryRequest(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.memoryShare == nil {
		return marshalErrorResult("p2p memory share not enabled — start the daemon with --p2p."), nil
	}
	var args struct {
		PeerID   string `json:"peer_id"`
		RemoteID string `json:"remote_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", args.PeerID,
		"peer libp2p ID or display name — call mesh__list_peers")
	v.requireStringWithHint("remote_id", args.RemoteID,
		"id of the memory on the remote peer (from their memory__offer_memory descriptor)")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, args.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(args.PeerID, resolveErr)), nil
	}
	localID, err := h.memoryShare.RequestMemory(ctx, peerID, args.RemoteID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Request failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Pulled memory from peer %s. Stored locally as %s (origin_peer_id=%s).",
		peerID, localID, peerID)), nil
}

// handleMemoryImportHarness triggers a one-shot import of harness-native
// memory files into the mcplexer memory store.
func (h *handler) handleMemoryImportHarness(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		Harness string `json:"harness"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Cannot resolve home dir: %v", err)), nil
	}
	if args.Harness != "" {
		hKey := harnessimport.Harness(args.Harness)
		res, err := harnessimport.ImportHarness(ctx, h.store, hKey, home)
		if err != nil {
			return marshalErrorResult(fmt.Sprintf("Import failed: %v", err)), nil
		}
		if res == nil {
			return marshalToolResult("No files found for harness: " + args.Harness), nil
		}
		return marshalToolResult(fmt.Sprintf(
			"[%s] Imported %d, skipped %d, errors %d.",
			res.Harness, res.Imported, res.Skipped, len(res.Errors))), nil
	}
	results, err := harnessimport.ImportAll(ctx, h.store, home)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Import failed: %v", err)), nil
	}
	if len(results) == 0 {
		return marshalToolResult("No harness memory files found."), nil
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "[%s] Imported %d, skipped %d, errors %d.\n",
			r.Harness, r.Imported, r.Skipped, len(r.Errors))
	}
	return marshalToolResult(strings.TrimSpace(b.String())), nil
}

// handleMemorySyncStatus reports the sync scanner's status.
func (h *handler) handleMemorySyncStatus(
	_ context.Context, _ json.RawMessage,
) (json.RawMessage, *RPCError) {
	enabled := os.Getenv("MCPLEXER_SYNC_ENABLED") != "0"
	importEnabled := os.Getenv("MCPLEXER_AUTO_IMPORT_MEMORY") != "0"
	return marshalToolResult(fmt.Sprintf(
		"Sync scanner: %s\nAuto-import on startup: %s\n"+
			"Memory unification: mcplexer is the single source of truth for all harnesses.",
		boolStatus(enabled), boolStatus(importEnabled))), nil
}

func boolStatus(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
