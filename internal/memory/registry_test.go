// registry_test.go — service-layer coverage. Uses the real SQLite store
// (modernc.org/sqlite + vec0) via :memory: so the FTS/vector paths are
// exercised end-to-end without docker.
package memory_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newSvc(t *testing.T) (*memory.Service, *sqlite.DB) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := memory.NewService(d, memory.NoopEmbedder{}, nil)
	return s, d
}

func TestWriteRecallRoundtrip(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	id, err := svc.Write(ctx, memory.WriteOptions{
		Name:    "preferred-editor",
		Content: "neovim with telescope and treesitter",
		Tags:    []string{"editor", "preference"},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id == "" {
		t.Fatal("expected id")
	}
	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "editor", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if hits[0].Entry.ID != id {
		t.Fatalf("expected id=%s, got %s", id, hits[0].Entry.ID)
	}
}

// fakeBrainHook records the ids the memory service dual-writes/deletes.
type fakeBrainHook struct {
	writes  []string
	deletes []string
}

func (f *fakeBrainHook) OnMemoryWrite(_ context.Context, id string) { f.writes = append(f.writes, id) }
func (f *fakeBrainHook) OnMemoryDelete(_ context.Context, id string) {
	f.deletes = append(f.deletes, id)
}

func TestBrainHook_FiresOnMutators(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	hook := &fakeBrainHook{}
	svc.SetBrainHook(hook)

	id, err := svc.Write(ctx, memory.WriteOptions{Name: "h1", Content: "brain hook body"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(hook.writes) != 1 || hook.writes[0] != id {
		t.Fatalf("write hook not fired: %+v", hook.writes)
	}

	if err := svc.SetPinned(ctx, id, true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	if len(hook.writes) != 2 {
		t.Fatalf("pin should fire a write hook: %+v", hook.writes)
	}

	if err := svc.LinkEntity(ctx, id, store.EntityRef{Kind: "task", ID: "01X"}, ""); err != nil {
		t.Fatalf("LinkEntity: %v", err)
	}
	if len(hook.writes) != 3 {
		t.Fatalf("link should fire a write hook: %+v", hook.writes)
	}

	if err := svc.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if len(hook.deletes) != 1 || hook.deletes[0] != id {
		t.Fatalf("delete hook not fired: %+v", hook.deletes)
	}
}

func TestRecallEmptyQueryReturnsRecent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	for _, n := range []string{"a", "b", "c"} {
		_, _ = svc.Write(ctx, memory.WriteOptions{Name: n, Content: n + " memory body"})
	}
	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(hits))
	}
}

func TestForgetBySourceClearsBoth(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	for i, n := range []string{"x", "y", "z"} {
		sess := "poison"
		if i == 2 {
			sess = "clean"
		}
		_, _ = svc.Write(ctx, memory.WriteOptions{
			Name: n, Content: "content " + n,
			SourceSessionID: sess,
		})
	}
	n, err := svc.ForgetBySource(ctx, "poison", store.SkillScope{IncludeAll: true})
	if err != nil {
		t.Fatalf("ForgetBySource: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 forgotten, got %d", n)
	}
	hits, _ := svc.Recall(ctx, store.MemoryFilter{}, "", 10)
	if len(hits) != 1 {
		t.Fatalf("expected 1 surviving memory, got %d", len(hits))
	}
}

func TestForgetBySourceHonorsScope(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	source := "poison"
	wsA := "ws-a"
	wsB := "ws-b"
	var ids []struct {
		id     string
		purged bool
	}
	for _, row := range []struct {
		name string
		ws   *string
	}{
		{name: "global"},
		{name: "a", ws: &wsA},
		{name: "b", ws: &wsB},
	} {
		id, err := svc.Write(ctx, memory.WriteOptions{
			Name: row.name, Content: "content " + row.name,
			WorkspaceID: row.ws, SourceSessionID: source,
		})
		if err != nil {
			t.Fatalf("write %s: %v", row.name, err)
		}
		ids = append(ids, struct {
			id     string
			purged bool
		}{id: id, purged: row.name != "b"})
	}

	n, err := svc.ForgetBySource(ctx, source, store.SkillScope{WorkspaceIDs: []string{wsA}})
	if err != nil {
		t.Fatalf("ForgetBySource scoped: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 in-scope rows forgotten, got %d", n)
	}
	for _, row := range ids {
		_, err := svc.Get(ctx, row.id)
		if row.purged && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected %s purged, err=%v", row.id, err)
		}
		if !row.purged && err != nil {
			t.Fatalf("expected %s to survive, err=%v", row.id, err)
		}
	}
}

func TestNotifyHookFires(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	var mu sync.Mutex
	var events []memory.Event
	svc.Notify = func(_ context.Context, ev memory.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}
	id, err := svc.Write(ctx, memory.WriteOptions{Name: "n", Content: "notify content"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := svc.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Kind != "write" || events[1].Kind != "delete" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

// TestNotifyHookEmitsFullKindSet exercises every Event.Kind the
// dashboard's /memory page expects to render live. Pre-fix only
// write/invalidate/delete/(write-from-pin) were emitted; the page's
// activity widget therefore missed entity links, pin toggles, and
// offer transitions. Table-driven so a future kind is one row to add.
func TestNotifyHookEmitsFullKindSet(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	var mu sync.Mutex
	var events []memory.Event
	svc.Notify = func(_ context.Context, ev memory.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}

	id, err := svc.Write(ctx, memory.WriteOptions{Name: "n", Content: "notify kind-set content"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	ent := store.EntityRef{Kind: "task", ID: "T-1", Role: "subject"}
	if err := svc.LinkEntity(ctx, id, ent, ""); err != nil {
		t.Fatalf("LinkEntity: %v", err)
	}
	if err := svc.UnlinkEntity(ctx, id, ent); err != nil {
		t.Fatalf("UnlinkEntity: %v", err)
	}
	if err := svc.SetPinned(ctx, id, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := svc.SetPinned(ctx, id, false); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if err := svc.Invalidate(ctx, id, ""); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	svc.NotifyOfferReceived(ctx, "off-1", "peer-1", "Alice", "memory-name")
	svc.NotifyOfferAccepted(ctx, "off-1", "peer-1", id)
	svc.NotifyOfferDeclined(ctx, "off-2", "peer-2")

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"write", "link_entity", "unlink_entity",
		"pin", "unpin", "invalidate",
		"offer_received", "offer_accepted", "offer_declined",
	}
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %+v", len(want), len(events), events)
	}
	for i, k := range want {
		if events[i].Kind != k {
			t.Errorf("event[%d]: want kind=%q, got %q (%+v)", i, k, events[i].Kind, events[i])
		}
	}

	// Spot-check that link_entity carries the entity ref so the
	// dashboard can render "memory → task:T-1".
	if events[1].EntityKind != "task" || events[1].EntityID != "T-1" {
		t.Errorf("link_entity missing entity ref: %+v", events[1])
	}
	// Offer events carry peer + offer id so the dashboard can link
	// the activity row through to /memory/shared.
	if events[6].OfferID != "off-1" || events[6].PeerName != "Alice" {
		t.Errorf("offer_received missing peer fields: %+v", events[6])
	}
}

func TestDigestWriteThrough(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	digest := memory.NewFileDigester(dir)
	svc := memory.NewService(d, memory.NoopEmbedder{}, digest)
	wsID := "ws-1"
	_, err = svc.Write(ctx, memory.WriteOptions{
		Name:        "preferred-editor",
		Content:     "neovim with telescope",
		Kind:        store.MemoryKindFact,
		WorkspaceID: &wsID,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	path := digest.Path(wsID)
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("digest path outside dir: %s", path)
	}
	if filepath.Base(path) != "workspace-ws-1.md" {
		t.Fatalf("unexpected digest filename: %s", filepath.Base(path))
	}
}

// TestRecallLogsOnFTSOnlyPath is the regression guard for the AR4
// coupling bug: Recall only logged recall events on the full
// vector-fusion success path, so an install with no embedder (the
// default NoopEmbedder, HasModel()==false) never populated the recall
// log even with tracking enabled. That left the co_recall axis
// permanently empty. The fix hoists maybeLogRecall onto every ranked
// return path (FTS-only, embed-failure, vector-failure, empty-query).
//
// Verified indirectly via CoRecalledMemories: two memories surfaced in
// one recall share a result_set_id, so co-recall must link them — which
// is only possible if the FTS-only path actually wrote the rows.
func TestRecallLogsOnFTSOnlyPath(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		query string // non-empty exercises the FTS path; "" the empty-query path
	}{
		{name: "fts_path", query: "language"},
		{name: "empty_query_path", query: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t) // NoopEmbedder => HasModel()==false => FTS-only
			svc.EnableRecallTrackingForTest()

			idA, err := svc.Write(ctx, memory.WriteOptions{
				Name: "go-lang", Content: "go is a great language",
			})
			if err != nil {
				t.Fatalf("Write A: %v", err)
			}
			idB, err := svc.Write(ctx, memory.WriteOptions{
				Name: "rust-lang", Content: "rust is a great language",
			})
			if err != nil {
				t.Fatalf("Write B: %v", err)
			}

			hits, err := svc.Recall(ctx, store.MemoryFilter{}, tc.query, 10)
			if err != nil {
				t.Fatalf("Recall: %v", err)
			}
			if len(hits) < 2 {
				t.Fatalf("expected >=2 hits in one result set, got %d", len(hits))
			}

			// The recall event rows are what make co-recall possible. If the
			// FTS-only / empty-query path skipped maybeLogRecall (the bug),
			// CoRecalled returns nothing.
			co, err := svc.CoRecalled(ctx, idA, store.SkillScope{}, 10)
			if err != nil {
				t.Fatalf("CoRecalled: %v", err)
			}
			if len(co) == 0 {
				t.Fatalf("recall event not logged on the no-embedder path: "+
					"co-recall for %s is empty", idA)
			}
			var found bool
			for _, c := range co {
				if c.MemoryID == idB {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s in co-recall of %s, got %+v", idB, idA, co)
			}
		})
	}
}

// TestWriteFactBiTemporalInvalidation guards the headline correctness
// contract of the package: writing the same fact name twice in one
// (workspace, worker) bucket must leave exactly one active row (the new
// value) plus one invalidated row whose t_valid_end is stamped and whose
// invalidated_by points at the new id.
func TestWriteFactBiTemporalInvalidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	wsID := "ws-bt"
	const worker = "worker-bt"
	const factName = "preferred-db"

	write := func(val string) string {
		t.Helper()
		id, err := svc.Write(ctx, memory.WriteOptions{
			Name:        factName,
			Content:     val,
			Kind:        store.MemoryKindFact,
			WorkspaceID: &wsID,
			WorkerID:    worker,
		})
		if err != nil {
			t.Fatalf("Write %q: %v", val, err)
		}
		return id
	}

	oldID := write("postgres-15")
	newID := write("sqlite-wal2")
	if oldID == newID {
		t.Fatal("second fact write must allocate a new row id")
	}

	scope := store.SkillScope{WorkspaceIDs: []string{wsID}}

	// Active view: exactly one row, the new value.
	active, err := svc.List(ctx, store.MemoryFilter{
		Scope: scope, Kind: store.MemoryKindFact, Name: factName,
	})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active fact row, got %d: %+v", len(active), active)
	}
	if active[0].ID != newID || active[0].Content != "sqlite-wal2" {
		t.Fatalf("active row should be the new value, got %+v", active[0])
	}
	if active[0].TValidEnd != nil {
		t.Fatalf("active row must not be invalidated, got t_valid_end=%v", active[0].TValidEnd)
	}

	// History view: two rows; the old one carries the bi-temporal trail.
	all, err := svc.List(ctx, store.MemoryFilter{
		Scope: scope, Kind: store.MemoryKindFact, Name: factName,
		IncludeInvalid: true,
	})
	if err != nil {
		t.Fatalf("List w/ invalid: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 rows (1 active + 1 invalidated), got %d: %+v", len(all), all)
	}
	var oldRow *store.MemoryEntry
	for i := range all {
		if all[i].ID == oldID {
			oldRow = &all[i]
		}
	}
	if oldRow == nil {
		t.Fatalf("old row %s missing from history view: %+v", oldID, all)
	}
	if oldRow.TValidEnd == nil {
		t.Fatal("invalidated row must have t_valid_end stamped")
	}
	if oldRow.InvalidatedBy != newID {
		t.Fatalf("invalidated_by should point at new id %s, got %q", newID, oldRow.InvalidatedBy)
	}

	// Recall surfaces only the active value.
	hits, err := svc.Recall(ctx, store.MemoryFilter{Scope: scope}, "preferred-db", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 active recall hit, got %d: %+v", len(hits), hits)
	}
	if hits[0].Entry.ID != newID {
		t.Fatalf("recall should surface the active row %s, got %s", newID, hits[0].Entry.ID)
	}
}

// TestSuggestionsForRelatedEntity is the smoke test for the AR5
// SuggestionsFor composition's related_entity axis — the one axis that
// works with the default NoopEmbedder (co_recall needs logged events,
// semantic needs an embedder). Two memories linked to the same entity
// must suggest each other via the related_entity source.
func TestSuggestionsForRelatedEntity(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	idA, err := svc.Write(ctx, memory.WriteOptions{Name: "a", Content: "first note body"})
	if err != nil {
		t.Fatalf("Write A: %v", err)
	}
	idB, err := svc.Write(ctx, memory.WriteOptions{Name: "b", Content: "second note body"})
	if err != nil {
		t.Fatalf("Write B: %v", err)
	}
	ent := store.EntityRef{Kind: "task", ID: "T-99"}
	if err := svc.LinkEntity(ctx, idA, ent, ""); err != nil {
		t.Fatalf("LinkEntity A: %v", err)
	}
	if err := svc.LinkEntity(ctx, idB, ent, ""); err != nil {
		t.Fatalf("LinkEntity B: %v", err)
	}

	sugg, err := svc.SuggestionsFor(ctx, idA, store.SkillScope{}, 12)
	if err != nil {
		t.Fatalf("SuggestionsFor: %v", err)
	}
	var hit *store.MemorySuggestion
	for i := range sugg {
		if sugg[i].MemoryID == idB {
			hit = &sugg[i]
		}
	}
	if hit == nil {
		t.Fatalf("expected %s suggested via shared entity, got %+v", idB, sugg)
	}
	if hit.Source != "related_entity" {
		t.Fatalf("expected related_entity source, got %q", hit.Source)
	}
}

func TestVectorRecallFallsBackToFTS(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// Use noop embedder — Recall must still return FTS hits.
	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	_, err = svc.Write(ctx, memory.WriteOptions{Name: "go", Content: "go is a great language"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "language", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected fallback FTS hit")
	}
	if hits[0].Source != "fts" {
		t.Fatalf("expected source=fts, got %q", hits[0].Source)
	}
}

// TestWriteSurfacesContradictionCandidates proves the write-time
// contradiction surfacing: a NOTE written near an existing one surfaces the
// existing id (in WriteResult.Candidates) AND emits a possible_contradiction
// event; an UNRELATED note surfaces nothing. Nothing is auto-invalidated.
func TestWriteSurfacesContradictionCandidates(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	var mu sync.Mutex
	var contradictions []memory.Event
	svc.Notify = func(_ context.Context, ev memory.Event) {
		if ev.Kind == memory.EventKindPossibleContradiction {
			mu.Lock()
			contradictions = append(contradictions, ev)
			mu.Unlock()
		}
	}

	firstID, err := svc.WriteWithResult(ctx, memory.WriteOptions{
		Name:    "deploy-prefs",
		Content: "deploy to production every friday afternoon via the release script",
	})
	if err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if len(firstID.Candidates) != 0 {
		t.Fatalf("first write into empty corpus must surface nothing, got %v", firstID.Candidates)
	}

	// A near-duplicate note: overlapping vocabulary → FTS surfaces the first.
	dup, err := svc.WriteWithResult(ctx, memory.WriteOptions{
		Name:    "deploy-prefs-2",
		Content: "deploy to production on friday afternoon using the release script",
	})
	if err != nil {
		t.Fatalf("Write dup: %v", err)
	}
	found := false
	for _, c := range dup.Candidates {
		if c == firstID.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("near-duplicate note should surface the first id %s, got %v", firstID.ID, dup.Candidates)
	}
	mu.Lock()
	gotEvents := len(contradictions)
	hasCand := len(contradictions) > 0 && contains(contradictions[len(contradictions)-1].Candidates, firstID.ID)
	mu.Unlock()
	if gotEvents == 0 || !hasCand {
		t.Fatalf("expected a possible_contradiction event carrying %s, events=%d", firstID.ID, gotEvents)
	}

	// An unrelated note shares no vocabulary → no candidates, no event.
	mu.Lock()
	contradictions = nil
	mu.Unlock()
	unrelated, err := svc.WriteWithResult(ctx, memory.WriteOptions{
		Name:    "lunch",
		Content: "favourite ramen spot is the tonkotsu place near the office",
	})
	if err != nil {
		t.Fatalf("Write unrelated: %v", err)
	}
	if len(unrelated.Candidates) != 0 {
		t.Fatalf("unrelated note must surface nothing, got %v", unrelated.Candidates)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(contradictions) != 0 {
		t.Fatalf("unrelated note must emit no contradiction event, got %d", len(contradictions))
	}
}

// TestWriteFactSkipsContradictionScan proves facts are left to their
// auto-supersede path (the unique index) and never trigger the advisory
// note-only contradiction scan.
func TestWriteFactSkipsContradictionScan(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	var events int
	var mu sync.Mutex
	svc.Notify = func(_ context.Context, ev memory.Event) {
		if ev.Kind == memory.EventKindPossibleContradiction {
			mu.Lock()
			events++
			mu.Unlock()
		}
	}
	if _, err := svc.WriteWithResult(ctx, memory.WriteOptions{
		Kind: "fact", Name: "tz", Content: "the user timezone is europe/london",
	}); err != nil {
		t.Fatalf("Write fact 1: %v", err)
	}
	res, err := svc.WriteWithResult(ctx, memory.WriteOptions{
		Kind: "fact", Name: "tz", Content: "the user timezone is europe/london updated",
	})
	if err != nil {
		t.Fatalf("Write fact 2: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("fact write must not surface contradiction candidates, got %v", res.Candidates)
	}
	mu.Lock()
	defer mu.Unlock()
	if events != 0 {
		t.Fatalf("fact write must not emit possible_contradiction, got %d", events)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestRecallDrivenRankingPromotesFrequentlyRecalled proves the AR4
// recall-driven term at the service level: with recall events seeded for the
// memory that naturally ranks SECOND (identical content → BM25 tie → stable
// insertion order), that memory surfaces first. The companion
// TestRecallDrivenRankingEmptyLogUnchanged proves the empty-log invariance.
func TestRecallDrivenRankingPromotesFrequentlyRecalled(t *testing.T) {
	ctx := context.Background()

	// Establish the natural (no-recall) order on a tracking-OFF service.
	off, _ := newSvc(t)
	a1, err := off.Write(ctx, memory.WriteOptions{Name: "a", Content: "the cache layer caches results"})
	if err != nil {
		t.Fatalf("off Write A: %v", err)
	}
	a2, err := off.Write(ctx, memory.WriteOptions{Name: "b", Content: "the cache layer caches results"})
	if err != nil {
		t.Fatalf("off Write B: %v", err)
	}
	offHits, err := off.Recall(ctx, store.MemoryFilter{}, "cache", 10)
	if err != nil || len(offHits) < 2 {
		t.Fatalf("off Recall: err=%v n=%d", err, len(offHits))
	}
	natWinner := offHits[0].Entry.ID
	natLoser := offHits[1].Entry.ID
	_, _ = a1, a2

	// Tracking-ON service with the SAME corpus; seed heavy recall events for
	// the natural loser, then prove it overtakes the natural winner.
	svc, d := newSvc(t)
	svc.EnableRecallTrackingForTest()
	w1, err := svc.Write(ctx, memory.WriteOptions{Name: "a", Content: "the cache layer caches results"})
	if err != nil {
		t.Fatalf("svc Write A: %v", err)
	}
	w2, err := svc.Write(ctx, memory.WriteOptions{Name: "b", Content: "the cache layer caches results"})
	if err != nil {
		t.Fatalf("svc Write B: %v", err)
	}
	// Map the natural loser id (from the off service) to this service's id by
	// name: "a" was written first on both, so the position order matches.
	loser := w2
	if natLoser == natWinner { // defensive; identical content shouldn't tie to same id
		t.Fatalf("degenerate natural order")
	}
	if offHits[1].Entry.Name == "a" {
		loser = w1
	}
	// Seed 12 distinct recent result sets for the loser (no pre-Recall, so the
	// stats this call reads are exactly the seed — recallSync logs AFTER
	// ranking, never affecting this call).
	var events []store.MemoryRecallEvent
	for i := 0; i < 12; i++ {
		events = append(events, store.MemoryRecallEvent{
			MemoryID:     loser,
			ResultSetID:  "seed-" + string(rune('a'+i)),
			RankPosition: 1,
		})
	}
	if err := d.LogMemoryRecallEvents(ctx, events); err != nil {
		t.Fatalf("seed recall events: %v", err)
	}
	promoted, err := svc.Recall(ctx, store.MemoryFilter{}, "cache", 10)
	if err != nil || len(promoted) < 2 {
		t.Fatalf("svc promoted Recall: err=%v n=%d", err, len(promoted))
	}
	if promoted[0].Entry.ID != loser {
		t.Fatalf("frequently-recalled memory %s should rank first, got %s (%+v)",
			loser, promoted[0].Entry.ID, promoted)
	}
}

// TestRecallDrivenRankingEmptyLogUnchanged proves the recall term degrades to
// a no-op: a tracking-ON service with NO recall events produces the SAME
// ordering + scores as a tracking-OFF service over the same corpus.
func TestRecallDrivenRankingEmptyLogUnchanged(t *testing.T) {
	ctx := context.Background()
	corpus := []memory.WriteOptions{
		{Name: "cache", Content: "the cache layer caches results for speed"},
		{Name: "db", Content: "the database stores rows on disk durably"},
		{Name: "queue", Content: "the queue buffers cache misses for retry"},
	}

	off, _ := newSvc(t)
	for _, w := range corpus {
		if _, err := off.Write(ctx, w); err != nil {
			t.Fatalf("off Write %s: %v", w.Name, err)
		}
	}
	offHits, err := off.Recall(ctx, store.MemoryFilter{}, "cache", 10)
	if err != nil {
		t.Fatalf("off Recall: %v", err)
	}

	on, _ := newSvc(t)
	on.EnableRecallTrackingForTest() // tracking ON but log is empty
	for _, w := range corpus {
		if _, err := on.Write(ctx, w); err != nil {
			t.Fatalf("on Write %s: %v", w.Name, err)
		}
	}
	onHits, err := on.Recall(ctx, store.MemoryFilter{}, "cache", 10)
	if err != nil {
		t.Fatalf("on Recall: %v", err)
	}

	if len(offHits) != len(onHits) {
		t.Fatalf("len mismatch off=%d on=%d", len(offHits), len(onHits))
	}
	for i := range offHits {
		if offHits[i].Entry.Name != onHits[i].Entry.Name {
			t.Fatalf("order diverged at %d: off=%s on=%s — empty recall log must "+
				"not change ranking", i, offHits[i].Entry.Name, onHits[i].Entry.Name)
		}
	}
}
