// Package memory implements the cross-harness memory subsystem:
// agents call memory__save/__recall/__search/__forget over MCP, the
// dashboard manages records, workers can pin a memory scope per row,
// peers can offer + request memories over /mcplexer/memory/1.0.0.
//
// # Two-tier model
//
// Records share one table (migration 058) but split on kind:
//   - fact: atomic key/value, unique per (workspace, worker, name).
//     Updates invalidate (stamp t_valid_end) and insert a new active
//     row, preserving the bi-temporal trail. Use for "user prefers
//     dark mode", "production DB password rotation = monthly", etc.
//   - note: longer markdown blob, no uniqueness. Use for "what we
//     learned debugging the payment flow last sprint", consolidated
//     summaries, etc.
//
// # Scoping
//
// Every record has up to four scope axes (workspace, user, worker,
// run). Reads use store.SkillScope (workspace ∪ global) plus optional
// further narrowing by worker/user/run via MemoryFilter.
//
// # Provenance + redaction
//
// SourceKind is always populated. The optional source_session_id +
// source_peer_id + source_tool_call_id columns let
// memory__forget_by_source surgically purge everything a poisoned
// session/peer/tool-call wrote.
//
// # Embedding (default OFF)
//
// FTS5 is always on. Vector search is only meaningful once an embedding
// provider is configured: WriteMemory does NOT call out; the consumer
// (or a background worker) calls UpsertEmbedding once the vector is
// computed. EmbedProvider is pluggable; v1 ships noop + openai. Mismatched
// embed_model on read silently excludes that row rather than returning
// garbage.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// Service is the thin facade the gateway, REST API, workers, and the
// p2p memory protocol talk to. Wraps store.MemoryStore + an optional
// EmbedProvider (default noop) + an optional Digester (default nil =
// no CLAUDE.md write-through).
//
// Recall logging (AR4) is OFF by default — the buffered channel exists
// always so the call sites don't branch, but the consumer goroutine
// only persists events when MCPLEXER_RECALL_TRACKING=1. Off-state =
// drain-to-noop, so logging adds zero DB cost when disabled.
type Service struct {
	store    store.MemoryStore
	embedder EmbedProvider
	reranker RerankProvider
	digest   Digester

	// rrfWFTS / rrfWVec are the per-arm weights for the reciprocal rank
	// fusion in Recall. Package-level tunable (NOT per-workspace config) —
	// default 1.0/1.0 = classic unweighted RRF. Override via
	// SetFusionWeights at construction-adjacent wiring time.
	rrfWFTS float64
	rrfWVec float64

	// Notify is invoked AFTER a successful write/invalidate/delete with
	// a short descriptor so the gateway can push an SSE signal + OS
	// notification. nil = no-op.
	Notify func(ctx context.Context, ev Event)

	// SessionIDFunc resolves the current session id at recall time so
	// the log row carries who triggered the recall. nil = sessions are
	// not surfaced (logging still works, just with NULL session_id).
	SessionIDFunc func(ctx context.Context) string

	// recallEventCh is a buffered channel feeding the background flusher.
	// Buffer size is deliberately small + non-blocking: recall must
	// never wait on logging. Overflows are dropped + counted.
	recallEventCh   chan store.MemoryRecallEvent
	recallEnabled   bool // mirrors MCPLEXER_RECALL_TRACKING — cached at construct time
	recallSync      bool // test-only: persist recall events synchronously (see EnableRecallTrackingForTest)
	recallDroppedCt uint64

	// brainHook dual-writes a memory to its canonical .md file on every
	// mutation. Wired by the daemon only when the brain is enabled; nil =
	// today's behaviour (no file write). All methods are best-effort and
	// MUST NOT fail the mutation. The brain.Serializer satisfies this.
	brainHook BrainHook

	// mu guards the digest debounce state below. Digest writes are
	// coalesced off the save path: scheduleDigest marks the scope dirty
	// and arms a single timer; flushDigests drains every pending scope
	// when it fires. See digest_debounce.go.
	mu            sync.Mutex
	digestPending map[string]struct{}
	digestTimer   *time.Timer
	digestDelay   time.Duration

	// stopCh signals the recall flush loop to drain + persist a final
	// batch and exit. Closed exactly once by Close (guarded by closeOnce).
	// We NEVER close recallEventCh — producers non-block-send onto it, so
	// closing it would risk a send-on-closed-channel panic.
	stopCh    chan struct{}
	closeOnce sync.Once
}

// SetReranker installs a cross-encoder rerank provider post-construction.
// Nil-safe: a nil provider falls back to the no-op reranker so Recall's
// rerank stage stays disabled. Call from the daemon wiring once the
// MCPLEXER_RERANK_* env is resolved.
func (s *Service) SetReranker(r RerankProvider) {
	if s == nil {
		return
	}
	if r == nil {
		r = NoopReranker{}
	}
	s.reranker = r
}

// SetFusionWeights overrides the per-arm reciprocal-rank-fusion weights
// used in Recall (FTS arm, vector arm). Non-positive values are ignored
// (the existing weight is kept) so a partial override can't zero an arm
// by accident. Package-level tunable — NOT per-workspace config.
func (s *Service) SetFusionWeights(wFTS, wVec float64) {
	if s == nil {
		return
	}
	if wFTS > 0 {
		s.rrfWFTS = wFTS
	}
	if wVec > 0 {
		s.rrfWVec = wVec
	}
}

// BrainHook is the optional dual-write sink the MCPlexer Brain registers so
// a memory mutation also serializes the memory's canonical .md file. The
// service calls OnMemoryWrite after a write/pin/invalidate/entity-link
// change (the file's frontmatter or body changed), and OnMemoryDelete on a
// soft-delete. Both are best-effort.
type BrainHook interface {
	OnMemoryWrite(ctx context.Context, id string)
	OnMemoryDelete(ctx context.Context, id string)
}

// SetBrainHook installs the brain dual-write hook post-construction. Nil is
// safe (the default) — the service never writes files.
func (s *Service) SetBrainHook(h BrainHook) { s.brainHook = h }

// fireBrainWrite re-serializes one memory's canonical .md file via the
// brain hook (best-effort, nil-safe).
func (s *Service) fireBrainWrite(ctx context.Context, id string) {
	if s.brainHook == nil || id == "" {
		return
	}
	s.brainHook.OnMemoryWrite(ctx, id)
}

// fireBrainDelete removes one memory's .md file via the brain hook.
func (s *Service) fireBrainDelete(ctx context.Context, id string) {
	if s.brainHook == nil || id == "" {
		return
	}
	s.brainHook.OnMemoryDelete(ctx, id)
}

// NewService constructs a Service. Pass nil for embedder + digest to
// run with the FTS5-only floor + no CLAUDE.md write-through.
//
// Recall tracking is gated on MCPLEXER_RECALL_TRACKING=1 read at
// construction time. Restart the daemon to flip the flag.
func NewService(s store.MemoryStore, embedder EmbedProvider, digest Digester) *Service {
	if embedder == nil {
		embedder = NoopEmbedder{}
	}
	svc := &Service{
		store:         s,
		embedder:      embedder,
		reranker:      NoopReranker{},
		digest:        digest,
		rrfWFTS:       1.0,
		rrfWVec:       1.0,
		digestDelay:   defaultDigestDebounce,
		recallEventCh: make(chan store.MemoryRecallEvent, 256),
		recallEnabled: os.Getenv("MCPLEXER_RECALL_TRACKING") == "1",
		stopCh:        make(chan struct{}),
	}
	if svc.recallEnabled {
		go svc.recallFlushLoop(svc.stopCh)
	} else {
		// Drain in noop mode so producers never block when the channel
		// fills (which it shouldn't, since they non-block-send, but
		// belt-and-braces against a future hot-reload toggling the env
		// without restarting).
		go svc.recallDrainLoop(svc.stopCh)
	}
	return svc
}

// EnableRecallTrackingForTest forces AR4 recall logging on and switches it
// to synchronous persistence so tests can assert a recall event row landed
// without racing the async flush loop. This is a test seam ONLY — the
// production path is env-gated (MCPLEXER_RECALL_TRACKING) + async. Calling
// it outside tests defeats the "recall never waits on logging" guarantee.
func (s *Service) EnableRecallTrackingForTest() {
	if s == nil {
		return
	}
	s.recallEnabled = true
	s.recallSync = true
}

// EnableAsyncRecallTrackingForTest turns AR4 recall logging on using the
// PRODUCTION async path (buffered channel + flush loop), so a test can
// exercise the real enqueue→drain→persist pipeline (including Close's
// final drain). Unlike EnableRecallTrackingForTest it does NOT switch to
// synchronous persistence. Starts the flush loop. Test seam ONLY.
func (s *Service) EnableAsyncRecallTrackingForTest() {
	if s == nil {
		return
	}
	// Stop the noop drain loop started by NewService (tests construct with
	// MCPLEXER_RECALL_TRACKING unset) so it doesn't compete with the flush
	// loop for events, then install a fresh stopCh for Close to signal.
	// Guard the stopCh swap with the same mutex Close takes when it reads +
	// closes stopCh, so the reassignment can't race a concurrent Close (-race).
	s.mu.Lock()
	close(s.stopCh)
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.mu.Unlock()
	s.recallEnabled = true
	s.recallSync = false
	go s.recallFlushLoop(stop)
}

// Event is the metadata the Service hands to Notify on every CUD op.
// Consumers (gateway signal stream) render it into a UI signal +
// optional OS notification.
//
// Kind taxonomy (load-bearing — the dashboard's /memory page filters on
// the "memory_<kind>" prefix derived from these values, see
// MemoryLandingPage.isMemoryEvent + memory_notify.go):
//
//   - write           — new row inserted (or fact row replaced)
//   - invalidate      — row stamped t_valid_end
//   - delete          — soft-deleted (or forget-by-source purge)
//   - link_entity     — entity link added to an existing memory
//   - unlink_entity   — entity link removed
//   - pin             — pinned flag set to true
//   - unpin           — pinned flag set to false
//   - offer_received  — peer offered a memory (recorded into memory_offers)
//   - offer_accepted  — local user accepted a peer offer (imports complete)
//   - offer_declined  — local user declined a peer offer
//   - possible_contradiction — a NOTE write surfaced existing near-duplicate
//     / potentially-conflicting memories (a cheap bounded neighbour scan).
//     ADVISORY ONLY: nothing is auto-invalidated — notes can be
//     complementary, so adjudication stays with the agent/consolidator. The
//     candidate ids ride in Event.Candidates so the dashboard / consolidator
//     can offer a "review these" affordance.
type Event struct {
	Kind        string `json:"kind"`
	MemoryID    string `json:"memory_id,omitempty"`
	MemoryName  string `json:"memory_name,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	OfferID     string `json:"offer_id,omitempty"`
	PeerID      string `json:"peer_id,omitempty"`
	PeerName    string `json:"peer_name,omitempty"`
	EntityKind  string `json:"entity_kind,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
	Source      string `json:"source,omitempty"`
	// Candidates carries the surfaced near-duplicate/conflict memory ids on a
	// possible_contradiction event. Empty for every other kind.
	Candidates []string `json:"candidates,omitempty"`
}

// EventKindPossibleContradiction is emitted after a note write whose cheap
// neighbour scan surfaced existing near-duplicate / potentially-related
// memories. Advisory only — never auto-invalidates.
//
// NAMING CAVEAT: the detector is a LEXICAL near-duplicate scan (FTS5 token
// overlap, gated on contradictionMinSharedTokens) on the no-embedder default
// path — it does NOT prove a semantic contradiction. The vector arm
// (surfaceContradictions, embedder-only) catches more, but even that is
// "similar", not "conflicting". User-facing copy therefore says
// "possibly-related" / "duplicate", never asserts a true contradiction; the
// agent/consolidator adjudicates. The kind string is kept for wire/back-compat
// stability — treat it as "possible_duplicate".
const EventKindPossibleContradiction = "possible_contradiction"

// WriteResult is the extended return shape of WriteWithResult: the new
// memory's id plus any near-duplicate / potential-conflict candidate ids the
// post-write neighbour scan surfaced (notes only; facts auto-supersede via
// the unique index). Candidates is advisory — nothing is auto-invalidated.
type WriteResult struct {
	ID         string
	Candidates []string
}

// contradictionScanCap bounds the neighbour scan to a few candidates so the
// surfacing stays cheap. A handful is enough to flag a likely duplicate;
// the consolidator does the heavy adjudication.
const contradictionScanCap = 3

// contradictionMinTokenLen is the minimum rune length for a token to count
// toward the overlap gate — filters out short stopwords ("the", "is", "to",
// "a") that FTS5's OR-join would otherwise let match incidentally.
const contradictionMinTokenLen = 4

// contradictionMinSharedTokens is the minimum count of DISTINCT significant
// tokens a candidate must share with the new note to be surfaced. Two is
// enough to distinguish a genuine topical overlap from a one-word
// coincidence while staying permissive enough to catch real near-duplicates.
const contradictionMinSharedTokens = 2

// WriteOptions is the arg payload for Write. Mirrors MemoryEntry but
// hides bookkeeping fields the caller never sets.
type WriteOptions struct {
	Name             string
	Kind             string // "" defaults to note
	Content          string
	Tags             []string
	Metadata         map[string]any
	WorkspaceID      *string
	UserID           string
	WorkerID         string
	RunID            string
	SourceKind       string
	SourceSessionID  string
	SourcePeerID     string
	SourceToolCallID string
	OriginPeerID     string
	Pinned           bool
	// Entities is the optional "what is this memory about" link set
	// (migration 076). Each ref is linked on the new row in the same
	// call. Empty Role on each ref defaults to "subject" at the store
	// layer. Invalid refs (missing Kind or ID) are skipped silently —
	// matches the "best-effort tagging" model.
	Entities []store.EntityRef
}

// Write persists a memory. Defaults: Kind=note, SourceKind=agent. For
// kind=fact the store atomically invalidates any prior active row in
// the same (workspace, worker, name) bucket. Returns the ID assigned.
// Notify is called after the write so subscribers can render the
// "new memory" signal.
//
// If an embedder is wired AND it is non-noop, embedding is computed
// asynchronously after the write so the call latency stays bounded —
// failures are logged inside Embed, never surfaced here.
//
// Write is the back-compat facade returning just the id; WriteWithResult
// exposes the extended shape (id + contradiction candidates).
func (s *Service) Write(ctx context.Context, opts WriteOptions) (string, error) {
	res, err := s.WriteWithResult(ctx, opts)
	return res.ID, err
}

// WriteWithResult persists a memory and, for NOTE writes, runs a cheap
// bounded neighbour scan that SURFACES (never auto-invalidates) existing
// near-duplicate / potentially-conflicting memories. Returns the new id plus
// any surfaced candidate ids in WriteResult.Candidates and emits a
// possible_contradiction Event when the set is non-empty. Facts are left
// alone: they already auto-supersede via the unique (workspace,worker,name)
// index, so a contradicting fact write invalidates the prior row in the
// store and needs no advisory surfacing.
func (s *Service) WriteWithResult(ctx context.Context, opts WriteOptions) (WriteResult, error) {
	if s == nil || s.store == nil {
		return WriteResult{}, errors.New("memory: service not initialised")
	}
	if strings.TrimSpace(opts.Name) == "" {
		return WriteResult{}, errors.New("memory: name required")
	}
	trimmed := strings.TrimSpace(opts.Content)
	if trimmed == "" {
		return WriteResult{}, errors.New("memory: content required")
	}
	// Junk guard: tiny bodies ("x", "ok", "v1") carry no recall value and
	// pollute ranking. Pinning is the deliberate override escape hatch.
	if n := len([]rune(trimmed)); n < minSaveContentChars && !opts.Pinned {
		return WriteResult{}, fmt.Errorf(
			"memory: content too short (%d chars after trimming; minimum %d); "+
				"save something substantive, or set pinned:true to keep a short memory deliberately",
			n, minSaveContentChars)
	}
	tagsJSON, err := toTagsJSON(opts.Tags)
	if err != nil {
		return WriteResult{}, err
	}
	metaJSON, err := toMetadataJSON(opts.Metadata)
	if err != nil {
		return WriteResult{}, err
	}
	e := &store.MemoryEntry{
		Name:             opts.Name,
		Kind:             opts.Kind,
		Content:          opts.Content,
		TagsJSON:         tagsJSON,
		MetadataJSON:     metaJSON,
		WorkspaceID:      opts.WorkspaceID,
		UserID:           opts.UserID,
		WorkerID:         opts.WorkerID,
		RunID:            opts.RunID,
		SourceKind:       opts.SourceKind,
		SourceSessionID:  opts.SourceSessionID,
		SourcePeerID:     opts.SourcePeerID,
		SourceToolCallID: opts.SourceToolCallID,
		OriginPeerID:     opts.OriginPeerID,
		Pinned:           opts.Pinned,
	}
	if err := s.store.WriteMemory(ctx, e); err != nil {
		return WriteResult{}, err
	}
	for _, ref := range opts.Entities {
		if ref.Kind == "" || ref.ID == "" {
			continue
		}
		if err := s.store.LinkMemoryEntity(ctx, e.ID, ref, opts.SourceSessionID); err != nil {
			return WriteResult{ID: e.ID}, fmt.Errorf("memory: link entity %s:%s: %w", ref.Kind, ref.ID, err)
		}
	}
	s.maybeEmbedAsync(ctx, e)
	s.scheduleDigest(e.WorkspaceID)
	s.fireBrainWrite(ctx, e.ID)
	s.notify(ctx, Event{
		Kind:        "write",
		MemoryID:    e.ID,
		MemoryName:  e.Name,
		WorkspaceID: ptrOr(e.WorkspaceID, ""),
		Source:      e.SourceKind,
	})
	// Contradiction surfacing (notes only). Cheap + bounded; emits its own
	// advisory event when candidates land. Facts auto-supersede via the
	// unique index, so they skip this.
	candidates := s.surfaceContradictions(ctx, e, opts.Entities)
	return WriteResult{ID: e.ID, Candidates: candidates}, nil
}

// surfaceContradictions runs a cheap bounded neighbour scan after a NOTE
// write and returns the ids of existing memories that may be near-duplicates
// or conflicts. It NEVER auto-invalidates — notes can be complementary, so
// adjudication stays with the agent/consolidator. Scan shape:
//
//   - FTS5 SearchMemories ALWAYS (one bounded query; the only cost on the
//     no-embedder default path).
//   - VectorSearchMemories additionally ONLY when an embedder HasModel — and
//     even then it reuses a synchronous query embed of the note content so
//     it doesn't depend on the async UpsertMemoryEmbedding having landed yet.
//
// Scope is the note's own scope, narrowed by its linked entities when it has
// any (so the scan stays focused on the same subject rather than the whole
// corpus). Returns nil (and emits nothing) for facts, empty content, or when
// nothing is surfaced. Best-effort: a store error degrades to "no
// candidates", never failing the write.
func (s *Service) surfaceContradictions(
	ctx context.Context, e *store.MemoryEntry, entities []store.EntityRef,
) []string {
	if e == nil || e.Kind == store.MemoryKindFact {
		return nil
	}
	if strings.TrimSpace(e.Content) == "" {
		return nil
	}
	f := store.MemoryFilter{Limit: contradictionScanCap + 1}
	if e.WorkspaceID != nil && *e.WorkspaceID != "" {
		f.Scope = store.SkillScope{WorkspaceIDs: []string{*e.WorkspaceID}}
	}
	for _, ref := range entities {
		if ref.Kind == "" || ref.ID == "" {
			continue
		}
		f.Entities = append(f.Entities, store.EntityRef{Kind: ref.Kind, ID: ref.ID})
	}
	// FTS5 OR-joins every query term, so a raw content search surfaces hits
	// that merely share a common word. Gate each candidate on a minimum
	// distinct-significant-token overlap with the new note so an incidental
	// stopword match doesn't read as a contradiction. Cheap: O(candidates ·
	// tokens), and candidates are capped at the scan limit.
	newTokens := significantTokens(e.Content)
	seen := map[string]struct{}{e.ID: {}}
	var out []string
	addHit := func(h store.MemoryHit) {
		if len(out) >= contradictionScanCap {
			return
		}
		if _, dup := seen[h.Entry.ID]; dup {
			return
		}
		if sharedTokenCount(newTokens, significantTokens(h.Entry.Content)) < contradictionMinSharedTokens {
			return
		}
		seen[h.Entry.ID] = struct{}{}
		out = append(out, h.Entry.ID)
	}
	if ftsHits, err := s.store.SearchMemories(ctx, f, e.Content); err == nil {
		for _, h := range ftsHits {
			addHit(h)
		}
	}
	// Vector arm only when an embedder is configured AND we still have room.
	if len(out) < contradictionScanCap && s.embedder.HasModel() {
		if vec, model, err := s.embedder.Embed(ctx, []string{e.Content}); err == nil && len(vec) > 0 {
			if vecHits, verr := s.store.VectorSearchMemories(
				ctx, f, model, vec[0], contradictionScanCap+1,
			); verr == nil {
				for _, h := range vecHits {
					addHit(h)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	s.notify(ctx, Event{
		Kind:        EventKindPossibleContradiction,
		MemoryID:    e.ID,
		MemoryName:  e.Name,
		WorkspaceID: ptrOr(e.WorkspaceID, ""),
		Candidates:  out,
	})
	return out
}

// significantTokens lowercases content and returns the SET of distinct
// tokens at least contradictionMinTokenLen runes long — the "meaningful"
// vocabulary used by the contradiction overlap gate. Punctuation and short
// stopwords are dropped. Returns nil for empty content.
func significantTokens(content string) map[string]struct{} {
	if content == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, tok := range strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(tok)) >= contradictionMinTokenLen {
			out[tok] = struct{}{}
		}
	}
	return out
}

// sharedTokenCount counts how many significant tokens two token sets share.
func sharedTokenCount(a, b map[string]struct{}) int {
	// Iterate the smaller set for cheapness.
	if len(b) < len(a) {
		a, b = b, a
	}
	n := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			n++
		}
	}
	return n
}

// LinkEntity adds a "this memory is about X" link to an existing memory.
// Idempotent on (memory, kind, id, role). Emits a link_entity Event so
// the dashboard renders the change live.
func (s *Service) LinkEntity(
	ctx context.Context, memoryID string, e store.EntityRef, createdBy string,
) error {
	if s == nil || s.store == nil {
		return errors.New("memory: not initialised")
	}
	if err := s.store.LinkMemoryEntity(ctx, memoryID, e, createdBy); err != nil {
		return err
	}
	s.fireBrainWrite(ctx, memoryID)
	s.notify(ctx, Event{
		Kind: "link_entity", MemoryID: memoryID,
		EntityKind: e.Kind, EntityID: e.ID,
	})
	return nil
}

// UnlinkEntity removes a "this memory is about X" link. Empty Role on
// the ref removes every role flavour for that (memory, kind, id) triple.
// Emits an unlink_entity Event for dashboard live updates.
func (s *Service) UnlinkEntity(
	ctx context.Context, memoryID string, e store.EntityRef,
) error {
	if s == nil || s.store == nil {
		return errors.New("memory: not initialised")
	}
	if err := s.store.UnlinkMemoryEntity(ctx, memoryID, e); err != nil {
		return err
	}
	s.fireBrainWrite(ctx, memoryID)
	s.notify(ctx, Event{
		Kind: "unlink_entity", MemoryID: memoryID,
		EntityKind: e.Kind, EntityID: e.ID,
	})
	return nil
}

// MemoryEntities returns every entity link for one memory.
func (s *Service) MemoryEntities(
	ctx context.Context, memoryID string,
) ([]store.MemoryEntityRow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.ListMemoryEntities(ctx, memoryID)
}

// Entities returns distinct entities in scope, ranked by memory_count
// DESC. Powers the "Top entities" UI tile + the agent-facing
// memory__list_entities tool.
func (s *Service) Entities(
	ctx context.Context, f store.EntityFilter,
) ([]store.EntitySummary, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.ListEntities(ctx, f)
}

// RelatedEntities returns entities that co-link with the given entity
// in at least one memory (AR1 — co-occurrence axis of associative
// recall). Self is excluded. Ranked by shared_count DESC. Backs the
// memory__related_entities MCP tool + the "Related entities" section
// on the dashboard's MemoryAboutPage.
func (s *Service) RelatedEntities(
	ctx context.Context, x store.EntityRef, scope store.SkillScope, limit int,
) ([]store.EntityCoLink, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.RelatedEntities(ctx, x, scope, limit)
}

// EntityGraph constructs the entity-to-entity graph in scope (AR3 —
// the graph view). Pass nodeCap=0 to use the default; minWeight=0
// retains every co-link edge.
func (s *Service) EntityGraph(
	ctx context.Context, scope store.SkillScope, nodeCap, minWeight int,
) (store.EntityGraph, error) {
	if s == nil || s.store == nil {
		return store.EntityGraph{}, errors.New("memory: not initialised")
	}
	return s.store.BuildEntityGraph(ctx, scope, nodeCap, minWeight)
}

// SpreadingActivation implements AR2: given a query entity, find the
// memories about it, find vec-neighbours of those memories, and return
// the entities those neighbours are about — ranked by accumulated
// proximity. Composes the existing entity-filter + vec search; does NOT
// require a new schema migration.
//
// Gracefully degrades when no embedder is configured by returning an
// empty slice (the caller can surface "configure an embedding provider
// to enable spreading activation"). Bounds the work: at most maxSeed
// seed memories, k vec-neighbours each. Excludes entity links that
// already point at the query entity (those are the structural recall —
// AR1 handles that surface).
//
// Scoring: each adjacent entity's score is the sum over surfaced
// neighbours of (1 / (1 + distance)) — closer neighbours contribute
// more. Final ranking is by score DESC.
func (s *Service) SpreadingActivation(
	ctx context.Context, x store.EntityRef, scope store.SkillScope,
	limit, maxSeed, neighboursPerSeed int,
) ([]store.EntityCoLink, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	if !s.embedder.HasModel() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if maxSeed <= 0 {
		maxSeed = 20
	}
	if neighboursPerSeed <= 0 {
		neighboursPerSeed = 8
	}
	// Step 1: seed memories — those linked to the query entity.
	seedFilter := store.MemoryFilter{
		Scope:    scope,
		Entities: []store.EntityRef{x},
		Limit:    maxSeed,
	}
	seeds, err := s.store.ListMemories(ctx, seedFilter)
	if err != nil {
		return nil, fmt.Errorf("spread seed: %w", err)
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	// Step 2: for each seed with an embedding, run a KNN. Aggregate scores
	// per neighbour memory across all seeds. Skip seeds without vectors —
	// they can't seed activation.
	type neighborInfo struct {
		entry store.MemoryEntry
		score float64
	}
	neighbourScores := make(map[string]*neighborInfo)
	for _, seed := range seeds {
		if seed.EmbedModel == "" {
			continue
		}
		// Use the seed's STORED vector instead of re-embedding its content
		// — removes one synchronous embed round-trip per seed (up to maxSeed
		// per call). Skip seeds whose stored model no longer matches the
		// active embedder (mixed-model corpus) or that have no vector yet.
		model, vecSeed, err := s.store.GetMemoryEmbedding(ctx, seed.ID)
		if err != nil || len(vecSeed) == 0 || model != seed.EmbedModel {
			continue
		}
		neighbours, err := s.store.VectorSearchMemories(ctx,
			store.MemoryFilter{Scope: scope}, model, vecSeed, neighboursPerSeed+1)
		if err != nil {
			continue
		}
		for _, n := range neighbours {
			if n.Entry.ID == seed.ID {
				continue // self
			}
			// distance → similarity (cosine returns 0..2; smaller=closer)
			weight := 1.0 / (1.0 + n.Score)
			if existing, ok := neighbourScores[n.Entry.ID]; ok {
				existing.score += weight
			} else {
				neighbourScores[n.Entry.ID] = &neighborInfo{
					entry: n.Entry, score: weight,
				}
			}
		}
	}
	if len(neighbourScores) == 0 {
		return nil, nil
	}
	// Step 3: for each surfaced neighbour, fetch its entity links and
	// aggregate by (kind, id). Skip links to the query entity (caller
	// already has those via the structural recall).
	type entityAccum struct {
		kind, id string
		score    float64
		lastSeen time.Time
	}
	entAgg := make(map[string]*entityAccum)
	queryKey := strings.ToLower(x.Kind + ":" + x.ID)
	for _, info := range neighbourScores {
		links, err := s.store.ListMemoryEntities(ctx, info.entry.ID)
		if err != nil {
			continue
		}
		for _, l := range links {
			k := l.EntityKind + ":" + l.EntityID
			if strings.ToLower(k) == queryKey {
				continue
			}
			a, ok := entAgg[k]
			if !ok {
				a = &entityAccum{kind: l.EntityKind, id: l.EntityID}
				entAgg[k] = a
			}
			a.score += info.score
			if l.CreatedAt.After(a.lastSeen) {
				a.lastSeen = l.CreatedAt
			}
		}
	}
	// Step 4: sort by score DESC, truncate.
	out := make([]store.EntityCoLink, 0, len(entAgg))
	for _, a := range entAgg {
		// SharedCount slot is repurposed as an integer score-proxy
		// (multiply by 1000 to keep ordering when JSON-cast to int).
		// The wire shape stays EntityCoLink so the UI doesn't need a
		// second type.
		out = append(out, store.EntityCoLink{
			Kind: a.kind, ID: a.id,
			SharedCount: int(a.score * 1000),
			LastSeenAt:  a.lastSeen,
		})
	}
	sortEntityCoLinks(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortEntityCoLinks(in []store.EntityCoLink) {
	for i := 1; i < len(in); i++ {
		j := i
		for j > 0 && in[j].SharedCount > in[j-1].SharedCount {
			in[j], in[j-1] = in[j-1], in[j]
			j--
		}
	}
}

// Get returns one memory by ID.
func (s *Service) Get(ctx context.Context, id string) (*store.MemoryEntry, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.GetMemory(ctx, id)
}

// List returns matching memories. The filter's scope governs visibility.
func (s *Service) List(ctx context.Context, f store.MemoryFilter) ([]store.MemoryEntry, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.ListMemories(ctx, f)
}

// Recall is the high-level retrieval entry point: agentic + scoped.
// Behaviour:
//   - When query is empty → returns the newest matching memories from
//     ListMemories (callable for "show me my recent memories").
//   - When query is non-empty and an embedder is configured → runs
//     FTS5 + vector KNN, fuses with RRF (k=60), and returns the
//     top-N. Otherwise falls back to FTS5 alone.
//   - Tag filtering, scope filtering, and source filtering are honored
//     through the filter argument.
//
// Returns ranked MemoryHits. The caller decides whether to render
// content directly or just metadata pointers.
func (s *Service) Recall(ctx context.Context, f store.MemoryFilter, query string, k int) ([]store.MemoryHit, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	if k <= 0 {
		k = 20
	}
	if strings.TrimSpace(query) == "" {
		f.Limit = k
		entries, err := s.store.ListMemories(ctx, f)
		if err != nil {
			return nil, err
		}
		hits := make([]store.MemoryHit, 0, len(entries))
		for _, e := range entries {
			hits = append(hits, store.MemoryHit{Entry: e, Source: "list"})
		}
		s.maybeLogRecall(ctx, hits, f, query)
		return hits, nil
	}
	// Pass the raw query straight through: SearchMemories sanitizes it
	// internally. Pre-quoting here double-sanitized the expression and
	// injected the literal term "or" into every multi-word recall.
	ftsHits, err := s.store.SearchMemories(ctx, f, query)
	if err != nil {
		return nil, fmt.Errorf("fts recall: %w", err)
	}
	// AR4 recall tracking must fire on whichever ranked list is actually
	// returned — not only the full vector-fusion success path — otherwise
	// installs without an embedder (or where any vector step fails) never
	// populate the recall log and the co_recall axis stays permanently
	// empty. capHits is computed once so logging and the return value
	// always agree on the FTS-only path.
	now := time.Now().UTC()
	// FTS-only fallback result, computed LAZILY. The primary fused path never
	// returns this, so its recall-stats query (recallStatsFor) is wasted work
	// there — only the early-return branches below (no embedder / embed fails /
	// vector search fails) need it. Building it on demand keeps the primary
	// path to ONE recall-stats query (the fused poolRecall) while every
	// fallback branch gets a byte-identical result to the eager version.
	// Recall-driven nudge (AR4): recallStatsFor returns nil when tracking is
	// off or the log is empty → recallSignal contributes zero → ranking is
	// byte-identical to the pre-AR4 behaviour.
	//
	// Takes the hit slice explicitly so it always reranks the ORIGINAL
	// f.Limit=k FTS pool (the eager version did the same), even on the
	// post-fusion vector-error branch where ftsHits has been reassigned to the
	// wider k*2 pool — keeping every fallback byte-identical to the old code.
	ftsFallback := func(hits []store.MemoryHit) []store.MemoryHit {
		res := capHits(rerankHits(hits, now, s.recallStatsFor(ctx, hits)), k)
		s.maybeLogRecall(ctx, res, f, query)
		return res
	}
	if !s.embedder.HasModel() {
		return ftsFallback(ftsHits), nil
	}
	vec, model, err := s.embedder.Embed(ctx, []string{query})
	if err != nil || len(vec) == 0 {
		return ftsFallback(ftsHits), nil
	}
	// Snapshot the original k-pool so the vector-error fallback below reranks
	// it (not the wider k*2 pool ftsHits is about to become) — preserving the
	// eager version's exact fallback result.
	origFTSHits := ftsHits
	// Symmetric pools: fetch the FTS arm at k*2 too so both arms feed the
	// fusion with the same candidate depth. f.Limit drives SearchMemories'
	// LIMIT; restore the caller's value afterwards (defensive — f is a copy
	// here but keep the contract clear).
	ftsF := f
	ftsF.Limit = k * 2
	if poolHits, perr := s.store.SearchMemories(ctx, ftsF, query); perr == nil {
		ftsHits = poolHits
	}
	vecHits, err := s.store.VectorSearchMemories(ctx, f, model, vec[0], k*2)
	if err != nil {
		return ftsFallback(origFTSHits), nil
	}
	// Fuse the two arms with weighted RRF, keep a 2k candidate pool so MMR
	// + the cross-encoder have room to reorder before we cap to k.
	pool := capHits(rrfFuse(ftsHits, vecHits, 0, 60, s.rrfWFTS, s.rrfWVec), k*2)
	// Recall stats for the fused candidate pool (one batched call, nil when
	// tracking is off / log empty → zero nudge → unchanged ranking).
	poolRecall := s.recallStatsFor(ctx, pool)
	// Cross-encoder rerank (biggest precision lever) — only when a model is
	// configured. When it fires we fold ONLY the recency + pinned
	// multipliers onto the cross-encoder order (foldRecencyPin) as a bounded
	// tie-breaker, so the cross-encoder's joint relevance ranking is the
	// primary key rather than being re-sorted by the bi-encoder magnitude
	// blend in rerankHits. When the reranker is absent (or its response is
	// unusable → ok=false) we fall back to the standard rerankHits
	// (position + magnitude + recency + pin) over the PRE-rerank order.
	if s.reranker.HasModel() {
		// NOTE: deliberately SKIP mmrPool on the cross-encoder path. A
		// cross-encoder scores each (query, doc) pair INDEPENDENTLY of input
		// order, so reordering the pool for diversity before it has zero
		// effect on the final ranking — while mmrPool costs up to k*2 (=40)
		// single-row GetMemoryEmbedding round-trips + O(n^2) cosine every
		// Recall. MMR only pays off when its order is the FINAL order, i.e.
		// the no-cross-encoder vector path below.
		if reordered, ok := s.crossEncoderReorder(ctx, query, pool); ok {
			fused := capHits(foldRecencyPin(reordered, now, poolRecall), k)
			s.maybeLogRecall(ctx, fused, f, query)
			return fused, nil
		}
	}
	// No (usable) cross-encoder: MMR diversity matters here because this
	// order IS final. Reorder for diversity before the magnitude-blend
	// rerank. Degrades to identity when stored vectors are unavailable.
	// (Follow-up: mmrPool still does k*2 single-row GetMemoryEmbedding
	// round-trips; a batched store fetch would cut that to one query — left
	// as a follow-up rather than adding a new store method here.)
	pool = s.mmrPool(ctx, pool)
	fused := capHits(rerankHits(pool, now, poolRecall), k)
	s.maybeLogRecall(ctx, fused, f, query)
	return fused, nil
}

// recallStatsFor fetches the per-memory recall aggregate for a candidate
// hit pool in ONE batched store call (AR4). Returns nil — contributing a
// zero recall nudge in ranking — when recall tracking is disabled
// (MCPLEXER_RECALL_TRACKING off), the pool is empty, or the store errors.
// Gating on s.recallEnabled means an install without tracking pays no query
// cost and ranking stays byte-identical to the pre-AR4 behaviour. The
// returned map is keyed by memory id; ids with no recall history are simply
// absent (recallSignal reads a missing id as zero).
func (s *Service) recallStatsFor(
	ctx context.Context, hits []store.MemoryHit,
) map[string]store.MemoryRecallStat {
	if !s.recallEnabled || len(hits) == 0 {
		return nil
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Entry.ID != "" {
			ids = append(ids, h.Entry.ID)
		}
	}
	stats, err := s.store.GetMemoryRecallStats(ctx, ids)
	if err != nil || len(stats) == 0 {
		return nil
	}
	return stats
}

// mmrPool reorders a fused candidate pool for diversity via MMR. It
// fetches each hit's STORED embedding (no re-embed round-trip) and calls
// mmrReorder. Degrades to identity when no embedder is configured or no
// stored vectors are available.
func (s *Service) mmrPool(ctx context.Context, pool []store.MemoryHit) []store.MemoryHit {
	if len(pool) <= 1 || !s.embedder.HasModel() {
		return pool
	}
	vecs := make([][]float32, len(pool))
	for i, h := range pool {
		if _, vec, err := s.store.GetMemoryEmbedding(ctx, h.Entry.ID); err == nil {
			vecs[i] = vec
		}
	}
	return mmrReorder(pool, vecs, mmrLambda)
}

// crossEncoderReorder re-scores the pool with the cross-encoder and
// returns the pool sorted by the joint (query, doc) relevance score
// descending. ok is false (and the caller falls back to the magnitude-blend
// rerankHits over the PRE-rerank order) when the rerank call errors OR the
// provider returns a score slice that does not cover every doc exactly once.
// The provider (HTTPReranker.Rerank) now ERRORS on partial/empty/duplicate
// coverage rather than zero-filling, so a degraded response surfaces as
// ok=false here instead of silently corrupting the order — the len check is
// kept as a defence-in-depth backstop for any future provider.
func (s *Service) crossEncoderReorder(
	ctx context.Context, query string, pool []store.MemoryHit,
) ([]store.MemoryHit, bool) {
	if len(pool) <= 1 {
		return pool, false
	}
	docs := make([]string, len(pool))
	for i, h := range pool {
		docs[i] = h.Entry.Content
	}
	scores, err := s.reranker.Rerank(ctx, query, docs)
	if err != nil || len(scores) != len(pool) {
		return pool, false
	}
	type sh struct {
		hit   store.MemoryHit
		score float64
	}
	all := make([]sh, len(pool))
	for i := range pool {
		all[i] = sh{hit: pool[i], score: scores[i]}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	out := make([]store.MemoryHit, len(all))
	for i := range all {
		out[i] = all[i].hit
	}
	return out, true
}

// recallTopK is the cap on how many surfaced rows get logged per recall
// (AR4 anti-noise: tail rows aren't signal). Cheap to bump later.
const recallTopK = 10

// recallFlushBatch is how many events the flusher persists per write.
// Keeps each transaction small + the channel drained.
const recallFlushBatch = 32

// maybeLogRecall enqueues recall events to the async flusher when
// tracking is enabled. Non-blocking — drops + increments
// recallDroppedCt if the channel is full, so a slow store never stalls
// recall. Only the top recallTopK hits are logged.
func (s *Service) maybeLogRecall(
	ctx context.Context, hits []store.MemoryHit, f store.MemoryFilter, query string,
) {
	if s == nil || !s.recallEnabled || len(hits) == 0 {
		return
	}
	resultSetID := ulid.Make().String()
	now := time.Now().UTC()
	sessionID := ""
	if s.SessionIDFunc != nil {
		sessionID = s.SessionIDFunc(ctx)
	}
	workspaceID := ""
	if len(f.Scope.WorkspaceIDs) == 1 {
		workspaceID = f.Scope.WorkspaceIDs[0]
	}
	entityFilter := ""
	if len(f.Entities) > 0 {
		entityFilter = f.Entities[0].Kind + ":" + f.Entities[0].ID
	}
	cap := recallTopK
	if len(hits) < cap {
		cap = len(hits)
	}
	// Test-only: persist synchronously so a test can assert the row landed
	// without racing the async flush loop. Never set in production.
	if s.recallSync {
		batch := make([]store.MemoryRecallEvent, 0, cap)
		for i := 0; i < cap; i++ {
			batch = append(batch, store.MemoryRecallEvent{
				ID:           ulid.Make().String(),
				MemoryID:     hits[i].Entry.ID,
				SessionID:    sessionID,
				WorkspaceID:  workspaceID,
				Query:        query,
				EntityFilter: entityFilter,
				RankPosition: i + 1,
				ResultSetID:  resultSetID,
				Source:       hits[i].Source,
				CreatedAt:    now,
			})
		}
		_ = s.store.LogMemoryRecallEvents(ctx, batch)
		return
	}
	for i := 0; i < cap; i++ {
		ev := store.MemoryRecallEvent{
			ID:           ulid.Make().String(),
			MemoryID:     hits[i].Entry.ID,
			SessionID:    sessionID,
			WorkspaceID:  workspaceID,
			Query:        query,
			EntityFilter: entityFilter,
			RankPosition: i + 1,
			ResultSetID:  resultSetID,
			Source:       hits[i].Source,
			CreatedAt:    now,
		}
		select {
		case s.recallEventCh <- ev:
		default:
			atomic.AddUint64(&s.recallDroppedCt, 1)
			return
		}
	}
}

// recallFlushLoop batches events from the channel and persists them.
// Flushes when the batch fills OR after a short interval, whichever comes
// first. On a Close() signal (stopCh closed) it drains whatever is still
// buffered in recallEventCh (non-blocking), persists a final batch, and
// returns. It NEVER closes recallEventCh — producers non-block-send onto
// it, so closing would risk a send-on-closed-channel panic.
func (s *Service) recallFlushLoop(stop <-chan struct{}) {
	batch := make([]store.MemoryRecallEvent, 0, recallFlushBatch)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Use a fresh context — never inherit a request context here.
		_ = s.store.LogMemoryRecallEvents(context.Background(), batch)
		batch = batch[:0]
	}
	for {
		select {
		case ev := <-s.recallEventCh:
			batch = append(batch, ev)
			if len(batch) >= recallFlushBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-stop:
			// Drain remaining buffered events without blocking, then persist
			// a final batch and exit.
			for {
				select {
				case ev := <-s.recallEventCh:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

// Close performs a graceful shutdown of the Service's background workers:
// it signals the recall flush loop to drain + persist a final batch and
// exit. Idempotent (guarded by sync.Once) and nil-safe. Wire it as a
// daemon-shutdown defer after NewService. Close NEVER closes
// recallEventCh — producers non-block-send onto it.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		// Take s.mu so the read+close can't race the stopCh swap in
		// EnableAsyncRecallTrackingForTest (test seam) under -race.
		s.mu.Lock()
		stop := s.stopCh
		s.mu.Unlock()
		if stop != nil {
			close(stop)
		}
	})
	return nil
}

// recallDrainLoop runs when tracking is disabled. It empties the channel
// to noop so a hot-reload (future) toggle never blocks producers. Exits on
// the stop signal (Close, or the async-tracking test seam swapping in the
// flush loop) or when the channel is closed (never, today).
func (s *Service) recallDrainLoop(stop <-chan struct{}) {
	for {
		select {
		case _, ok := <-s.recallEventCh:
			if !ok {
				return
			}
		case <-stop:
			return
		}
	}
}

// CoRecalled returns memories that frequently co-surface with memoryID
// in the recall log (AR4). Empty when tracking has produced no events
// yet — the caller can render a hint to enable MCPLEXER_RECALL_TRACKING.
func (s *Service) CoRecalled(
	ctx context.Context, memoryID string, scope store.SkillScope, limit int,
) ([]store.CoRecalledMemory, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	return s.store.CoRecalledMemories(ctx, memoryID, scope, limit)
}

// SuggestionsFor returns a unified "you might also remember" bundle for
// memoryID (AR5). Composes three signal sources:
//
//  1. **co_recall** — memories that co-surface in the recall log (AR4)
//  2. **related_entity** — memories linked to entities this one is linked to
//  3. **semantic** — vec-neighbours of this memory's embedding
//
// All three feed a single ranked output, deduplicated by memory id.
// Empty when none of the sources have signal — e.g. a fresh install
// with no recall events + no entity links + no embedder configured.
func (s *Service) SuggestionsFor(
	ctx context.Context, memoryID string, scope store.SkillScope, limit int,
) ([]store.MemorySuggestion, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("memory: not initialised")
	}
	if strings.TrimSpace(memoryID) == "" {
		return nil, errors.New("memoryID required")
	}
	if limit <= 0 {
		limit = 12
	}

	// Accumulator keyed by memory_id so the three sources don't double-count.
	type accum struct {
		s store.MemorySuggestion
	}
	out := make(map[string]*accum)
	add := func(id, name, source, reason string, score float64) {
		if id == memoryID {
			return
		}
		if existing, ok := out[id]; ok {
			if score > existing.s.Score {
				existing.s.Score = score
				existing.s.Source = source
				existing.s.Reason = reason
			}
			return
		}
		out[id] = &accum{s: store.MemorySuggestion{
			MemoryID: id, Name: name, Score: score,
			Source: source, Reason: reason,
		}}
	}

	// 1) co-recall axis (AR4)
	if coRecalled, err := s.store.CoRecalledMemories(ctx, memoryID, scope, limit); err == nil {
		for _, c := range coRecalled {
			add(c.MemoryID, c.Name, "co_recall",
				fmt.Sprintf("surfaced with this %d time(s)", c.CoOccurrences),
				c.Score)
		}
	}

	// 2) related-entity axis (076) — pull this memory's entity links,
	// then for each link gather memories that share it.
	links, _ := s.store.ListMemoryEntities(ctx, memoryID)
	for _, l := range links {
		f := store.MemoryFilter{
			Scope: scope,
			Entities: []store.EntityRef{
				{Kind: l.EntityKind, ID: l.EntityID},
			},
			Limit: 5,
		}
		sharers, err := s.store.ListMemories(ctx, f)
		if err != nil {
			continue
		}
		for _, m := range sharers {
			add(m.ID, m.Name, "related_entity",
				fmt.Sprintf("co-linked via %s:%s", l.EntityKind, l.EntityID),
				0.5,
			)
		}
	}

	// 3) semantic axis — vec-neighbours of this memory. Only fires when
	// an embedder is configured AND the memory has an embedding row.
	if s.embedder.HasModel() {
		entry, err := s.store.GetMemory(ctx, memoryID)
		if err == nil && entry.EmbedModel != "" {
			// Use the stored embedding rather than re-embedding the content —
			// removes one synchronous embed round-trip per call. Degrade
			// gracefully (skip the semantic axis) when no vector is stored or
			// the stored model no longer matches the row's embed_model.
			model, vec, gerr := s.store.GetMemoryEmbedding(ctx, memoryID)
			if gerr == nil && len(vec) > 0 && model == entry.EmbedModel {
				neighbours, err := s.store.VectorSearchMemories(ctx,
					store.MemoryFilter{Scope: scope}, model, vec, limit+1)
				if err == nil {
					for _, n := range neighbours {
						if n.Entry.ID == memoryID {
							continue
						}
						sim := 1.0 / (1.0 + n.Score)
						add(n.Entry.ID, n.Entry.Name, "semantic",
							fmt.Sprintf("vec-neighbour (distance %.3f)", n.Score),
							sim,
						)
					}
				}
			}
		}
	}

	// Sort by score DESC and cap.
	list := make([]store.MemorySuggestion, 0, len(out))
	for _, a := range out {
		list = append(list, a.s)
	}
	for i := 1; i < len(list); i++ {
		j := i
		for j > 0 && list[j].Score > list[j-1].Score {
			list[j], list[j-1] = list[j-1], list[j]
			j--
		}
	}
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

// Invalidate marks one memory as superseded. Optional supersededByID
// records the new active row's ID; pass "" if there's no replacement.
func (s *Service) Invalidate(ctx context.Context, id, supersededByID string) error {
	if s == nil || s.store == nil {
		return errors.New("memory: not initialised")
	}
	if err := s.store.InvalidateMemory(ctx, id, supersededByID); err != nil {
		return err
	}
	// Both the invalidated row and the superseding row changed on disk
	// (t_valid_end / invalidated_by frontmatter); re-serialize both.
	s.fireBrainWrite(ctx, id)
	if supersededByID != "" {
		s.fireBrainWrite(ctx, supersededByID)
	}
	s.notify(ctx, Event{Kind: "invalidate", MemoryID: id})
	return nil
}

// SetPinned flips the pinned flag on the row. Pinned rows are excluded
// from the consolidator's auto-prune and surface with a star in the UI.
// Idempotent. Emits a pin/unpin Event so the dashboard can render the
// star toggle live.
func (s *Service) SetPinned(ctx context.Context, id string, pinned bool) error {
	if s == nil || s.store == nil {
		return errors.New("memory: not initialised")
	}
	if err := s.store.SetMemoryPinned(ctx, id, pinned); err != nil {
		return err
	}
	kind := "unpin"
	if pinned {
		kind = "pin"
	}
	s.fireBrainWrite(ctx, id)
	s.notify(ctx, Event{Kind: kind, MemoryID: id})
	return nil
}

// NotifyOfferReceived emits an offer_received Event. Called by the p2p
// memory-share recorder after the offer row has been persisted, so the
// dashboard lights up the moment a peer announces a new memory.
// Best-effort — never errors out the caller.
func (s *Service) NotifyOfferReceived(
	ctx context.Context, offerID, peerID, peerName, memoryName string,
) {
	if s == nil {
		return
	}
	s.notify(ctx, Event{
		Kind:       "offer_received",
		OfferID:    offerID,
		PeerID:     peerID,
		PeerName:   peerName,
		MemoryName: memoryName,
	})
}

// NotifyOfferAccepted emits an offer_accepted Event. The acceptOffer
// REST + MCP paths call this after AcceptMemoryOffer succeeds so the
// dashboard's "incoming offers" tile drops the row and the activity
// stream surfaces the import.
func (s *Service) NotifyOfferAccepted(
	ctx context.Context, offerID, peerID, localMemoryID string,
) {
	if s == nil {
		return
	}
	s.notify(ctx, Event{
		Kind:     "offer_accepted",
		OfferID:  offerID,
		PeerID:   peerID,
		MemoryID: localMemoryID,
	})
}

// NotifyOfferDeclined emits an offer_declined Event so the dashboard
// drops the offer from the pending list without a manual refresh.
func (s *Service) NotifyOfferDeclined(ctx context.Context, offerID, peerID string) {
	if s == nil {
		return
	}
	s.notify(ctx, Event{Kind: "offer_declined", OfferID: offerID, PeerID: peerID})
}

// Forget soft-deletes one memory. The vector row is also dropped so KNN
// no longer surfaces the entry.
func (s *Service) Forget(ctx context.Context, id string) error {
	if s == nil || s.store == nil {
		return errors.New("memory: not initialised")
	}
	if err := s.store.SoftDeleteMemory(ctx, id); err != nil {
		return err
	}
	s.fireBrainDelete(ctx, id)
	s.notify(ctx, Event{Kind: "delete", MemoryID: id})
	return nil
}

// ForgetBySource hard-purges every row written by sourceSessionID inside
// scope. The matching vector rows go too. Recall events tagged with the same
// session are also dropped in the same scope (AR4 forensic redaction parity).
// Returns the memory-row count only — recall-event purges are best-effort.
func (s *Service) ForgetBySource(
	ctx context.Context, sourceSessionID string, scope store.SkillScope,
) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("memory: not initialised")
	}
	// Capture the affected ids BEFORE the purge so the brain can drop the
	// matching .md files (best-effort — a list failure just skips file
	// cleanup; the next reindex/verify reconciles the orphans).
	var purgedIDs []string
	if s.brainHook != nil {
		if rows, lErr := s.store.ListMemories(ctx, store.MemoryFilter{
			Scope:           scope,
			SourceSessionID: sourceSessionID,
			IncludeInvalid:  true,
			Limit:           10000,
		}); lErr == nil {
			for _, r := range rows {
				purgedIDs = append(purgedIDs, r.ID)
			}
		}
	}
	n, err := s.store.ForgetMemoryBySource(ctx, sourceSessionID, scope)
	if err != nil {
		return 0, err
	}
	// Best-effort: drop the session's recall trail too. Errors do not
	// fail the operation since the memories are the primary target.
	_, _ = s.store.ForgetRecallEventsBySource(ctx, sourceSessionID, scope)
	for _, id := range purgedIDs {
		s.fireBrainDelete(ctx, id)
	}
	if n > 0 {
		s.notify(ctx, Event{Kind: "delete", Source: sourceSessionID})
	}
	return n, nil
}

// Count returns (facts, notes) in scope for the dashboard vitals card.
func (s *Service) Count(ctx context.Context, scope store.SkillScope) (int, int, error) {
	if s == nil || s.store == nil {
		return 0, 0, errors.New("memory: not initialised")
	}
	return s.store.CountMemories(ctx, scope)
}

// Stats returns the aggregate "shape of the brain" snapshot powering the
// memory landing header (totals, type mix, recency, sparkline, top tags,
// decay pressure). See store.MemoryStats for field semantics.
func (s *Service) Stats(ctx context.Context, scope store.SkillScope) (store.MemoryStats, error) {
	if s == nil || s.store == nil {
		return store.MemoryStats{}, errors.New("memory: not initialised")
	}
	return s.store.GetMemoryStats(ctx, scope)
}

// ReEmbedAfterUpdate schedules an async re-embed for a memory whose content
// was just changed through a store path the Service did NOT mediate (the
// brain Editor/Indexer persist via store.UpdateMemory directly so FTS +
// bi-temporal triggers fire unchanged). The brain wires this as its ReEmbed
// hook: after it rewrites a memory row from an edited .md file, it calls this
// with the fresh row so the dropped vector is rebuilt from the new content.
// No-ops gracefully when no embedder is configured.
func (s *Service) ReEmbedAfterUpdate(ctx context.Context, e *store.MemoryEntry) {
	if s == nil || e == nil {
		return
	}
	s.maybeEmbedAsync(ctx, e)
}

// maybeEmbedAsync fires off an embedding computation in the background
// when a non-noop embedder is wired. Failures are swallowed (logged
// inside the embedder) — embedding is a "nice to have" we never block
// the write on.
func (s *Service) maybeEmbedAsync(ctx context.Context, e *store.MemoryEntry) {
	if !s.embedder.HasModel() {
		return
	}
	// Detach from the request context: ctx may be cancelled when the
	// MCP call returns. Use a fresh background context but inherit the
	// cancellation deadline of any parent that has one.
	go func(id, content string) {
		bg := context.Background()
		vecs, model, err := s.embedder.Embed(bg, []string{content})
		if err != nil || len(vecs) == 0 {
			return
		}
		_ = s.store.UpsertMemoryEmbedding(bg, id, model, 1, vecs[0])
	}(e.ID, e.Content)
}

func (s *Service) notify(ctx context.Context, ev Event) {
	if s.Notify == nil {
		return
	}
	s.Notify(ctx, ev)
}

// toTagsJSON converts a string slice to a canonical JSON array for the
// memories.tags_json column.
func toTagsJSON(tags []string) (json.RawMessage, error) {
	if len(tags) == 0 {
		return json.RawMessage("[]"), nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	return b, nil
}

func toMetadataJSON(m map[string]any) (json.RawMessage, error) {
	if len(m) == 0 {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	return b, nil
}

func capHits(hits []store.MemoryHit, k int) []store.MemoryHit {
	if len(hits) <= k {
		return hits
	}
	return hits[:k]
}

// rrfFuse merges two ranked lists with weighted reciprocal rank fusion.
// score(d) = sum over lists L of (w_L / (k + rank_L(d))).
// k=60 is the standard default — robust across ranking sources. wFTS and
// wVec weight the FTS and vector arms respectively; pass 1.0/1.0 for the
// classic unweighted RRF (no behaviour change).
func rrfFuse(a, b []store.MemoryHit, topN, k int, wFTS, wVec float64) []store.MemoryHit {
	score := make(map[string]float64)
	rec := make(map[string]store.MemoryHit)
	for i, h := range a {
		score[h.Entry.ID] += wFTS / float64(k+i+1)
		rec[h.Entry.ID] = h
	}
	for i, h := range b {
		score[h.Entry.ID] += wVec / float64(k+i+1)
		if _, ok := rec[h.Entry.ID]; !ok {
			rec[h.Entry.ID] = h
		}
	}
	type sh struct {
		hit   store.MemoryHit
		score float64
	}
	all := make([]sh, 0, len(rec))
	for id, h := range rec {
		all = append(all, sh{hit: h, score: score[id]})
	}
	// In-place insertion sort — fine for the small N we cap at.
	for i := 1; i < len(all); i++ {
		j := i
		for j > 0 && all[j].score > all[j-1].score {
			all[j], all[j-1] = all[j-1], all[j]
			j--
		}
	}
	if topN > 0 && len(all) > topN {
		all = all[:topN]
	}
	out := make([]store.MemoryHit, 0, len(all))
	for _, x := range all {
		h := x.hit
		h.Source = "rrf"
		h.Score = x.score
		out = append(out, h)
	}
	return out
}

func ptrOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

const digestMaxEntries = 50

// guard against unused imports when service.go is read alone.
var _ = time.Now
