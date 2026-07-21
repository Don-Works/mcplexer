package models

import (
	"context"
	"testing"
	"time"
)

// TestRefresherInvokesOnRefreshHook — the OnRefresh hook fires after every
// Refresh with the freshly published snapshot, so the auth-alert evaluator
// rides the catalog cadence. HasRefreshHook reports the wiring for the daemon
// boot test.
func TestRefresherInvokesOnRefreshHook(t *testing.T) {
	when := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	var got []Catalog
	r := NewRefresher(RefresherOptions{
		Providers: []string{ProviderGrokCLI},
		Clock:     fixedClock(when),
		OnRefresh: func(_ context.Context, c Catalog) { got = append(got, c) },
	})
	if !r.HasRefreshHook() {
		t.Fatal("HasRefreshHook = false after wiring OnRefresh")
	}

	cat := r.Refresh(context.Background())
	if len(got) != 1 {
		t.Fatalf("hook invoked %d times, want 1", len(got))
	}
	if !got[0].RefreshedAt.Equal(cat.RefreshedAt) {
		t.Fatalf("hook saw stale snapshot: %v vs returned %v", got[0].RefreshedAt, cat.RefreshedAt)
	}

	// A second refresh invokes the hook again — evaluation is per-cycle.
	r.Refresh(context.Background())
	if len(got) != 2 {
		t.Fatalf("hook invoked %d times over two refreshes, want 2", len(got))
	}
}

// TestRefresherNoHookIsSafe — a refresher without OnRefresh reports no hook and
// refreshes without panicking.
func TestRefresherNoHookIsSafe(t *testing.T) {
	r := NewRefresher(RefresherOptions{Providers: []string{ProviderGrokCLI}})
	if r.HasRefreshHook() {
		t.Fatal("HasRefreshHook = true with no OnRefresh")
	}
	r.Refresh(context.Background())
}
