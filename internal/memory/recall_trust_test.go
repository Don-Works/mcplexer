// recall_trust_test.go — regression coverage for the memory-trust fixes:
// double-FTS-sanitize ("or" injection), recency/pinned reranking, the
// short-content save guard, and digest debounce + real-path rendering.
package memory_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestRecallMultiWordQueryNoOrInjection guards the double-sanitize bug:
// Recall used to pre-quote the query (`"dark" OR "mode"`) before
// SearchMemories sanitised it again, splitting on the quote characters
// and injecting the literal term "or" — so every 2-word recall surfaced
// any memory containing the word "or".
func TestRecallMultiWordQueryNoOrInjection(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	junkID, err := svc.Write(ctx, memory.WriteOptions{
		Name: "junk-or", Content: "either this thing or that thing",
	})
	if err != nil {
		t.Fatalf("Write junk: %v", err)
	}
	wantID, err := svc.Write(ctx, memory.WriteOptions{
		Name: "dark-mode", Content: "user prefers dark mode in the dashboard",
	})
	if err != nil {
		t.Fatalf("Write target: %v", err)
	}

	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "dark mode", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var sawWant bool
	for _, h := range hits {
		if h.Entry.ID == junkID {
			t.Fatalf("memory whose only matching token is 'or' surfaced: %+v", h.Entry)
		}
		if h.Entry.ID == wantID {
			sawWant = true
		}
	}
	if !sawWant {
		t.Fatalf("expected %s in hits, got %+v", wantID, hits)
	}
}

// TestRecallRanksRecentSubstantiveAboveStaleJunk asserts the post-RRF
// rerank: a months-old one-character probe row must not outrank a
// substantive recent memory for a relevant query.
func TestRecallRanksRecentSubstantiveAboveStaleJunk(t *testing.T) {
	ctx := context.Background()
	svc, db := newSvc(t)

	// Seed the junk row at the store layer (the service's save guard
	// now rejects 1-char bodies) with a 120-day-old updated_at.
	old := time.Now().UTC().Add(-120 * 24 * time.Hour)
	junk := &store.MemoryEntry{
		Name: "probe", Content: "x",
		CreatedAt: old, UpdatedAt: old, TValidStart: old,
	}
	if err := db.WriteMemory(ctx, junk); err != nil {
		t.Fatalf("seed junk: %v", err)
	}

	goodID, err := svc.Write(ctx, memory.WriteOptions{
		Name:    "probe-calibration",
		Content: "probe calibration requires a ten minute sensor warmup before measurements",
	})
	if err != nil {
		t.Fatalf("Write substantive: %v", err)
	}

	hits, err := svc.Recall(ctx, store.MemoryFilter{}, "probe calibration", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if hits[0].Entry.ID != goodID {
		t.Fatalf("expected substantive recent memory first, got %q (name=%q)",
			hits[0].Entry.ID, hits[0].Entry.Name)
	}
}

// TestWriteShortContentGuard covers the save guard: trimmed content
// under 8 chars is rejected with an error naming the limit, unless the
// save is pinned.
func TestWriteShortContentGuard(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		content string
		pinned  bool
		wantErr bool
	}{
		{name: "too_short_unpinned", content: "v1", wantErr: true},
		{name: "whitespace_padded_short", content: "   ok    ", wantErr: true},
		{name: "seven_chars", content: "1234567", wantErr: true},
		{name: "eight_chars_ok", content: "12345678"},
		{name: "short_but_pinned", content: "v1", pinned: true},
		{name: "substantive", content: "rotate the ssh keys weekly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			_, err := svc.Write(ctx, memory.WriteOptions{
				Name: "guard", Content: tc.content, Pinned: tc.pinned,
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected short-content rejection for %q", tc.content)
				}
				if !strings.Contains(err.Error(), "minimum 8") {
					t.Fatalf("error should name the limit, got: %v", err)
				}
				if !strings.Contains(err.Error(), "pinned:true") {
					t.Fatalf("error should mention the pinned override, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.content, err)
			}
		})
	}
}

// TestDigestDebounceAndRealPath asserts (a) the digest no longer writes
// inline on save — it waits for the debounce flush — and (b) the
// rendered digest prints its real file path, not the historical
// "path/to/this/file" placeholder.
func TestDigestDebounceAndRealPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	digest := memory.NewFileDigester(dir)
	svc := memory.NewService(d, memory.NoopEmbedder{}, digest)

	if _, err := svc.Write(ctx, memory.WriteOptions{
		Name: "global-note", Content: "a substantive global memory body",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := digest.Path("")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("digest must not be written inline on save (debounced); stat err=%v", err)
	}

	svc.FlushDigestsForTest()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("digest not written after flush: %v", err)
	}
	if strings.Contains(string(body), "path/to/this/file") {
		t.Fatal("digest still renders the placeholder path")
	}
	if !strings.Contains(string(body), "@"+path) {
		t.Fatalf("digest should print its real @import path %q, got:\n%s", path, body)
	}
	if !strings.Contains(string(body), "global-note") {
		t.Fatalf("digest missing entry content:\n%s", body)
	}
}
