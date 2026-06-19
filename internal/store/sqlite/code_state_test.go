package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCodeStateSetGetListDelete(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := &store.CodeStateEntry{
		WorkspaceID:     "ws-a",
		Key:             "customers",
		ValueJSON:       json.RawMessage(`{"count":2,"names":["acme","globex"]}`),
		SourceSessionID: "sess-1",
	}
	if err := db.SetCodeState(ctx, e); err != nil {
		t.Fatalf("set: %v", err)
	}
	if e.Bytes == 0 {
		t.Fatalf("bytes not computed on set")
	}

	got, err := db.GetCodeState(ctx, "ws-a", "customers")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.ValueJSON) != `{"count":2,"names":["acme","globex"]}` {
		t.Fatalf("value mismatch: %s", got.ValueJSON)
	}
	if got.SourceSessionID != "sess-1" {
		t.Fatalf("source session mismatch: %q", got.SourceSessionID)
	}
	created := got.CreatedAt

	// Overwrite preserves created_at and replaces the value.
	if err := db.SetCodeState(ctx, &store.CodeStateEntry{
		WorkspaceID: "ws-a", Key: "customers", ValueJSON: json.RawMessage(`{"count":3}`),
	}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got2, err := db.GetCodeState(ctx, "ws-a", "customers")
	if err != nil {
		t.Fatalf("get after overwrite: %v", err)
	}
	if string(got2.ValueJSON) != `{"count":3}` {
		t.Fatalf("overwrite value: %s", got2.ValueJSON)
	}
	if !got2.CreatedAt.Equal(created) {
		t.Fatalf("created_at not preserved across overwrite: %v vs %v", got2.CreatedAt, created)
	}

	// Second key in ws-a, plus an unrelated key in ws-b for isolation.
	mustSet(t, db, ctx, "ws-a", "cust-meta", `1`)
	mustSet(t, db, ctx, "ws-b", "other", `1`)

	all, err := db.ListCodeState(ctx, store.CodeStateFilter{WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 keys in ws-a, got %d", len(all))
	}
	for _, x := range all {
		if len(x.ValueJSON) != 0 {
			t.Fatalf("list views must omit values, got %s", x.ValueJSON)
		}
		if x.Bytes == 0 {
			t.Fatalf("list views must include bytes")
		}
	}

	pref, err := db.ListCodeState(ctx, store.CodeStateFilter{WorkspaceID: "ws-a", Prefix: "cust-"})
	if err != nil {
		t.Fatalf("prefix list: %v", err)
	}
	if len(pref) != 1 || pref[0].Key != "cust-meta" {
		t.Fatalf("prefix filter mismatch: %+v", pref)
	}

	if err := db.DeleteCodeState(ctx, "ws-a", "customers"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetCodeState(ctx, "ws-a", "customers"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	if err := db.DeleteCodeState(ctx, "ws-a", "customers"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound deleting absent key, got %v", err)
	}
}

func TestCodeStateTTLHidesAndPrunes(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)

	mustSetTTL(t, db, ctx, "w", "expired", `1`, &past, false)
	mustSetTTL(t, db, ctx, "w", "expired-pinned", `1`, &past, true)
	mustSetTTL(t, db, ctx, "w", "fresh", `1`, &future, false)

	if _, err := db.GetCodeState(ctx, "w", "expired"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired get should be ErrNotFound, got %v", err)
	}

	visible, err := db.ListCodeState(ctx, store.CodeStateFilter{WorkspaceID: "w"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(visible) != 1 || visible[0].Key != "fresh" {
		t.Fatalf("only fresh should be visible, got %+v", visible)
	}

	withExpired, err := db.ListCodeState(ctx, store.CodeStateFilter{WorkspaceID: "w", IncludeExpired: true})
	if err != nil {
		t.Fatalf("list include_expired: %v", err)
	}
	if len(withExpired) != 3 {
		t.Fatalf("include_expired want 3, got %d", len(withExpired))
	}

	// Prune removes only expired AND unpinned entries.
	n, err := db.PruneExpiredCodeState(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("prune want 1 removed, got %d", n)
	}
	after, err := db.ListCodeState(ctx, store.CodeStateFilter{WorkspaceID: "w", IncludeExpired: true})
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("after prune want 2 (pinned + fresh), got %d", len(after))
	}
}

func mustSet(t *testing.T, db *DB, ctx context.Context, ws, key, value string) {
	t.Helper()
	if err := db.SetCodeState(ctx, &store.CodeStateEntry{
		WorkspaceID: ws, Key: key, ValueJSON: json.RawMessage(value),
	}); err != nil {
		t.Fatalf("set %s/%s: %v", ws, key, err)
	}
}

func mustSetTTL(t *testing.T, db *DB, ctx context.Context, ws, key, value string, ttl *time.Time, pinned bool) {
	t.Helper()
	if err := db.SetCodeState(ctx, &store.CodeStateEntry{
		WorkspaceID: ws, Key: key, ValueJSON: json.RawMessage(value),
		TTLExpiresAt: ttl, Pinned: pinned,
	}); err != nil {
		t.Fatalf("set %s/%s: %v", ws, key, err)
	}
}
