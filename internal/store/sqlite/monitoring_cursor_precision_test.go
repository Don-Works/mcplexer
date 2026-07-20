package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestLogSourceCursorKeepsNanosecondPrecision pins the fix for the pull-cursor
// fixed point.
//
// collect/pull.go advances the cursor by exactly one nanosecond so the next
// `docker logs --since` window excludes the line already ingested. Persisting
// that through the second-precision formatTime silently dropped the
// nanoseconds, so the next pull asked for <second>.000000001 — at or before
// every line in that second. The tail was re-ingested and the recomputed
// cursor truncated to the same second again, so cursor_ts could never advance
// past the final second of the stream.
//
// Reproduced on a static fixture with nothing appending: 10 -> 12 -> 13 -> 14
// lines across successive pulls with cursor_ts frozen.
func TestLogSourceCursorKeepsNanosecondPrecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)

	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	host := seedRemoteHost(t, db, ctx, wsID, scopeID)
	src := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID,
		Name: "api", Selector: "example-api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, src); err != nil {
		t.Fatalf("create log source: %v", err)
	}

	// The exact shape pull.go produces: a line timestamp plus 1ns.
	lineTS := time.Date(2026, 7, 20, 18, 21, 59, 0, time.UTC)
	cursor := lineTS.Add(time.Nanosecond)

	if err := db.UpdateLogSourceCursor(ctx, src.ID, cursor, "hash-1"); err != nil {
		t.Fatalf("update cursor: %v", err)
	}

	got, err := db.GetLogSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}

	// Under the old second-precision write this came back as 18:21:59Z, which
	// is NOT after lineTS — so the exclusive window re-included the line.
	if got.CursorTS == nil {
		t.Fatalf("cursor was not persisted at all")
	}
	if !got.CursorTS.After(lineTS) {
		t.Fatalf("cursor %s is not after the line it consumed (%s): the next "+
			"--since window re-includes that line, the tail is re-ingested, and "+
			"the cursor can never advance past this second",
			got.CursorTS.Format(time.RFC3339Nano), lineTS.Format(time.RFC3339Nano))
	}
	if !got.CursorTS.Equal(cursor) {
		t.Errorf("cursor round-trip = %s, want %s",
			got.CursorTS.Format(time.RFC3339Nano), cursor.Format(time.RFC3339Nano))
	}

	// Idempotence: re-persisting what we read must not drift backwards. A
	// truncating writer loses a nanosecond on every hop, so this catches a
	// partial fix that only formats correctly on the first write.
	if err := db.UpdateLogSourceCursor(ctx, src.ID, *got.CursorTS, "hash-2"); err != nil {
		t.Fatalf("re-update cursor: %v", err)
	}
	again, err := db.GetLogSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source again: %v", err)
	}
	if !again.CursorTS.Equal(cursor) {
		t.Errorf("cursor drifted on rewrite: %s, want %s",
			again.CursorTS.Format(time.RFC3339Nano), cursor.Format(time.RFC3339Nano))
	}
}
