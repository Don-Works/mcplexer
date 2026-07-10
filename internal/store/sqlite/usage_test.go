package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCachedProviderSnapshotCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	snap := store.ProviderSnapshot{
		Provider: store.ProviderClaude,
		Label:    "Claude Pro",
		Status:   store.StatusOK,
		Source:   "auto",
		Observed: store.ObservedUsage{
			Requests:    42,
			InputTokens: 100000,
			CostUSD:     1.23,
		},
	}
	snapJSON, _ := json.Marshal(snap)

	cached := &store.CachedProviderSnapshot{
		Provider:  store.ProviderClaude,
		Snapshot:  string(snapJSON),
		UpdatedAt: now,
	}
	if err := db.UpsertCachedProviderSnapshot(ctx, cached); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetCachedProviderSnapshot(ctx, store.ProviderClaude)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Provider != store.ProviderClaude {
		t.Fatalf("provider mismatch: %s", got.Provider)
	}

	var parsed store.ProviderSnapshot
	if err := json.Unmarshal([]byte(got.Snapshot), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Observed.Requests != 42 {
		t.Fatalf("requests mismatch: %d", parsed.Observed.Requests)
	}
	if parsed.Observed.CostUSD != 1.23 {
		t.Fatalf("cost mismatch: %f", parsed.Observed.CostUSD)
	}
}

func TestCachedProviderSnapshotUpsertOverwrite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	cached := &store.CachedProviderSnapshot{
		Provider:  store.ProviderCodex,
		Snapshot:  `{"provider":"codex","observed":{"requests":1}}`,
		UpdatedAt: now,
	}
	if err := db.UpsertCachedProviderSnapshot(ctx, cached); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	cached.Snapshot = `{"provider":"codex","observed":{"requests":5}}`
	cached.UpdatedAt = now.Add(time.Minute)
	if err := db.UpsertCachedProviderSnapshot(ctx, cached); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := db.GetCachedProviderSnapshot(ctx, store.ProviderCodex)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var parsed store.ProviderSnapshot
	_ = json.Unmarshal([]byte(got.Snapshot), &parsed)
	if parsed.Observed.Requests != 5 {
		t.Fatalf("expected overwrite to requests=5, got %d", parsed.Observed.Requests)
	}
}

func TestCachedProviderSnapshotNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.GetCachedProviderSnapshot(ctx, "nonexistent")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListCachedProviderSnapshots(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, p := range []string{store.ProviderClaude, store.ProviderGrok} {
		cached := &store.CachedProviderSnapshot{
			Provider:  p,
			Snapshot:  `{"provider":"` + p + `"}`,
			UpdatedAt: now,
		}
		if err := db.UpsertCachedProviderSnapshot(ctx, cached); err != nil {
			t.Fatalf("upsert %s: %v", p, err)
		}
	}

	list, err := db.ListCachedProviderSnapshots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestCachedOpenRouterCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	or := store.OpenRouterSnapshot{
		Status: store.StatusOK,
		Credits: store.ORCreditInfo{
			Usage:     5.0,
			Limit:     100.0,
			Remaining: 95.0,
		},
		ByHarness: []store.ORHarnessUsage{
			{
				Harness:     "claude_code",
				Requests:    10,
				InputTokens: 50000,
				CostUSD:     2.50,
				Models: []store.ORModelUsage{
					{Model: "anthropic/claude-sonnet-4", Requests: 10, InputTokens: 50000, CostUSD: 2.50},
				},
			},
		},
	}
	orJSON, _ := json.Marshal(or)

	cached := &store.CachedOpenRouter{
		Snapshot:  string(orJSON),
		UpdatedAt: now,
	}
	if err := db.UpsertCachedOpenRouter(ctx, cached); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetCachedOpenRouter(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	var parsed store.OpenRouterSnapshot
	if err := json.Unmarshal([]byte(got.Snapshot), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Credits.Remaining != 95.0 {
		t.Fatalf("remaining mismatch: %f", parsed.Credits.Remaining)
	}
	if len(parsed.ByHarness) != 1 {
		t.Fatalf("harness count: %d", len(parsed.ByHarness))
	}
	if parsed.ByHarness[0].Harness != "claude_code" {
		t.Fatalf("harness mismatch: %s", parsed.ByHarness[0].Harness)
	}
	if len(parsed.ByHarness[0].Models) != 1 {
		t.Fatalf("model count: %d", len(parsed.ByHarness[0].Models))
	}
}

func TestCachedOpenRouterNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.GetCachedOpenRouter(ctx)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCachedOpenRouterUpsertOverwrite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	cached := &store.CachedOpenRouter{
		Snapshot:  `{"credits":{"remaining":100}}`,
		UpdatedAt: now,
	}
	if err := db.UpsertCachedOpenRouter(ctx, cached); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	cached.Snapshot = `{"credits":{"remaining":50}}`
	cached.UpdatedAt = now.Add(time.Minute)
	if err := db.UpsertCachedOpenRouter(ctx, cached); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := db.GetCachedOpenRouter(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var parsed store.OpenRouterSnapshot
	_ = json.Unmarshal([]byte(got.Snapshot), &parsed)
	if parsed.Credits.Remaining != 50 {
		t.Fatalf("expected overwrite to remaining=50, got %f", parsed.Credits.Remaining)
	}
}
