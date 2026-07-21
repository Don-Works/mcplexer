package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestFileClaimRoundTrip(t *testing.T) {
	db := newTestDB(t, "file_claim")
	ctx := context.Background()
	now := time.Now().UTC()

	claim := &store.FileClaim{
		ClaimID:            "fc-del-1",
		ClaimerDisplayName: "delegation del-1",
		Repo:               "/repo/one",
		Paths:              []string{"internal/a.go", "internal/auth/*"},
		Intent:             "refactor auth",
		ClaimedAt:          now,
		ExpiresAt:          now.Add(time.Hour),
	}
	if err := db.InsertFileClaim(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFileClaim(ctx, &store.FileClaim{
		ClaimID:   "fc-del-2",
		Repo:      "/repo/two",
		Paths:     []string{"main.go"},
		ClaimedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	active, err := db.ListFileClaims(ctx, store.FileClaimFilter{ActiveOnly: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active = %d, want 2", len(active))
	}

	byRepo, err := db.ListFileClaims(ctx, store.FileClaimFilter{Repo: "/repo/one", ActiveOnly: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].ClaimID != "fc-del-1" {
		t.Fatalf("byRepo = %+v", byRepo)
	}
	if byRepo[0].Intent != "refactor auth" || len(byRepo[0].Paths) != 2 {
		t.Fatalf("round-trip lost fields: %+v", byRepo[0])
	}

	// Path filter glob-matches stored patterns against a literal path.
	byPath, err := db.ListFileClaims(ctx, store.FileClaimFilter{Path: "internal/auth/token.go", ActiveOnly: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(byPath) != 1 || byPath[0].ClaimID != "fc-del-1" {
		t.Fatalf("byPath = %+v", byPath)
	}

	byClaimer, err := db.ListFileClaims(ctx, store.FileClaimFilter{Claimer: "del-1", ActiveOnly: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(byClaimer) != 1 {
		t.Fatalf("byClaimer = %+v", byClaimer)
	}

	if err := db.ReleaseFileClaim(ctx, "fc-del-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseFileClaim(ctx, "fc-del-1", now.Add(time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second release = %v, want ErrNotFound", err)
	}
	afterRelease, err := db.ListFileClaims(ctx, store.FileClaimFilter{ActiveOnly: true, Now: now.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRelease) != 1 || afterRelease[0].ClaimID != "fc-del-2" {
		t.Fatalf("afterRelease = %+v", afterRelease)
	}

	// Expiry excludes without release.
	expired, err := db.ListFileClaims(ctx, store.FileClaimFilter{ActiveOnly: true, Now: now.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired = %+v, want empty", expired)
	}
}

func TestInsertFileClaimValidation(t *testing.T) {
	db := newTestDB(t, "file_claim")
	ctx := context.Background()
	if err := db.InsertFileClaim(ctx, nil); err == nil {
		t.Fatal("nil claim accepted")
	}
	if err := db.InsertFileClaim(ctx, &store.FileClaim{Paths: []string{"a"}}); err == nil {
		t.Fatal("empty claim_id accepted")
	}
	if err := db.InsertFileClaim(ctx, &store.FileClaim{ClaimID: "x"}); err == nil {
		t.Fatal("empty paths accepted")
	}
}
