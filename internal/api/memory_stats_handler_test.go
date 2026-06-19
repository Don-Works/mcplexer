// memory_stats_handler_test.go — verifies the shape + correctness of the
// GET /api/v1/memory/stats aggregate. Seeds a handful of memories
// through the live memory.Service so the same code paths the PWA hits in
// prod are exercised end-to-end.
package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMemoryHandlerStatsEmpty(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)

	stats := mustFetchStats(t, srv.URL+"/api/v1/memory/stats")
	if stats.TotalMemories != 0 {
		t.Errorf("total=%d want 0", stats.TotalMemories)
	}
	if stats.TotalBytes != 0 {
		t.Errorf("bytes=%d want 0", stats.TotalBytes)
	}
	if stats.BrainAgeDays != 0 {
		t.Errorf("brain_age_days=%d want 0", stats.BrainAgeDays)
	}
	if stats.BrainAgeBornAt != nil {
		t.Errorf("brain_age_born_at=%v want nil", stats.BrainAgeBornAt)
	}
	if len(stats.WritesPerDay30d) != 30 {
		t.Errorf("len(writes_per_day_30d)=%d want 30", len(stats.WritesPerDay30d))
	}
	for i, p := range stats.WritesPerDay30d {
		if p.Count != 0 {
			t.Errorf("series[%d]=%d want 0", i, p.Count)
		}
		if p.Date == "" {
			t.Errorf("series[%d] missing date", i)
		}
	}
	if stats.NetworkReach.PeerCount != 0 {
		t.Errorf("peer_count=%d want 0", stats.NetworkReach.PeerCount)
	}
	if len(stats.TopTags) != 0 {
		t.Errorf("top_tags=%v want empty", stats.TopTags)
	}
}

func TestMemoryHandlerStatsPopulated(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)

	// Seed: two facts + one note with overlapping tags.
	seedMemory(t, svc, "fact-a", "ssh keys rotate weekly", store.MemoryKindFact, []string{"ops", "security"})
	seedMemory(t, svc, "fact-b", "deploy on fridays = pain", store.MemoryKindFact, []string{"ops"})
	seedMemory(t, svc, "note-a", "what we learned debugging payments", store.MemoryKindNote, []string{"learning", "ops"})

	stats := mustFetchStats(t, srv.URL+"/api/v1/memory/stats")

	if stats.TotalMemories != 3 {
		t.Errorf("total=%d want 3", stats.TotalMemories)
	}
	if stats.TotalBytes == 0 {
		t.Error("total_bytes should be > 0 with seeded content")
	}
	if stats.PagesEquivalent == 0 {
		t.Error("pages_equivalent should be > 0")
	}
	if stats.BrainAgeBornAt == nil {
		t.Fatal("brain_age_born_at should be populated")
	}
	if stats.TypeMix["fact"] != 2 {
		t.Errorf("type_mix.fact=%d want 2", stats.TypeMix["fact"])
	}
	if stats.TypeMix["note"] != 1 {
		t.Errorf("type_mix.note=%d want 1", stats.TypeMix["note"])
	}
	// All freshly seeded → fresh bucket only.
	if stats.RecencyBuckets.Fresh != 3 {
		t.Errorf("fresh=%d want 3", stats.RecencyBuckets.Fresh)
	}
	if stats.RecencyBuckets.Dormant != 0 {
		t.Errorf("dormant=%d want 0", stats.RecencyBuckets.Dormant)
	}
	// 30 contiguous days, with today's bucket carrying all 3 writes.
	if len(stats.WritesPerDay30d) != 30 {
		t.Fatalf("series len=%d want 30", len(stats.WritesPerDay30d))
	}
	last := stats.WritesPerDay30d[len(stats.WritesPerDay30d)-1]
	if last.Count != 3 {
		t.Errorf("today's writes=%d want 3", last.Count)
	}
	// Top tags ordered by frequency: ops=3 wins.
	if len(stats.TopTags) == 0 {
		t.Fatal("expected non-empty top_tags")
	}
	if stats.TopTags[0].Tag != "ops" {
		t.Errorf("top tag=%q want ops", stats.TopTags[0].Tag)
	}
	if stats.TopTags[0].Count != 3 {
		t.Errorf("top tag count=%d want 3", stats.TopTags[0].Count)
	}
	// No peer-origin rows seeded → zero reach (but field present).
	if stats.NetworkReach.SharedMemoryCount != 0 {
		t.Errorf("shared=%d want 0", stats.NetworkReach.SharedMemoryCount)
	}
	// Fresh writes → no decay pressure.
	if stats.DecayPressure != 0 {
		t.Errorf("decay_pressure=%d want 0", stats.DecayPressure)
	}
}

func mustFetchStats(t *testing.T, url string) store.MemoryStats {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var out store.MemoryStats
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}
