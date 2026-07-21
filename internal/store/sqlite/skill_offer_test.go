package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func sampleSkillOffer(id, direction string, expiresAt time.Time) *store.SkillOffer {
	return &store.SkillOffer{
		OfferID:      id,
		Direction:    direction,
		PeerID:       "12D3KooWPEER",
		Name:         "pdf",
		Version:      4,
		ContentHash:  "abc123",
		BundleSHA256: "def456",
		Description:  "Convert markdown to a branded PDF",
		Metadata:     map[string]string{"imported_via": "skill_push"},
		ExpiresAt:    expiresAt,
	}
}

func TestSkillOfferCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)

	t.Run("insert+get round-trips fields", func(t *testing.T) {
		in := sampleSkillOffer("01OFFERIN", "inbound", future)
		if err := db.InsertSkillOffer(ctx, in); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got, err := db.GetSkillOffer(ctx, "01OFFERIN")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Name != "pdf" || got.Version != 4 || got.ContentHash != "abc123" ||
			got.BundleSHA256 != "def456" || got.Status != "pending" {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
		if got.Metadata["imported_via"] != "skill_push" {
			t.Fatalf("metadata not preserved: %+v", got.Metadata)
		}
	})

	t.Run("duplicate offer_id is ErrAlreadyExists", func(t *testing.T) {
		dup := sampleSkillOffer("01OFFERIN", "inbound", future)
		if err := db.InsertSkillOffer(ctx, dup); !errors.Is(err, store.ErrAlreadyExists) {
			t.Fatalf("want ErrAlreadyExists, got %v", err)
		}
	})

	t.Run("missing offer is ErrNotFound", func(t *testing.T) {
		if _, err := db.GetSkillOffer(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("list filters by direction + pending only", func(t *testing.T) {
		if err := db.InsertSkillOffer(ctx, sampleSkillOffer("01OFFEROUT", "outbound", future)); err != nil {
			t.Fatalf("insert outbound: %v", err)
		}
		inbound, err := db.ListPendingSkillOffers(ctx, "inbound")
		if err != nil {
			t.Fatalf("list inbound: %v", err)
		}
		if len(inbound) != 1 || inbound[0].OfferID != "01OFFERIN" {
			t.Fatalf("want 1 inbound (01OFFERIN), got %+v", inbound)
		}
		if _, err := db.ListPendingSkillOffers(ctx, "sideways"); err == nil {
			t.Fatalf("want error for bad direction")
		}
	})

	t.Run("accept records published version + leaves pending list", func(t *testing.T) {
		if err := db.DecideSkillOffer(ctx, "01OFFERIN", "accepted", time.Now().UTC(), 7); err != nil {
			t.Fatalf("decide: %v", err)
		}
		got, err := db.GetSkillOffer(ctx, "01OFFERIN")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status != "accepted" || got.PublishedVersion != 7 || got.DecidedAt == nil {
			t.Fatalf("accept not recorded: %+v", got)
		}
		inbound, _ := db.ListPendingSkillOffers(ctx, "inbound")
		if len(inbound) != 0 {
			t.Fatalf("accepted offer should leave pending list, got %+v", inbound)
		}
	})

	t.Run("deciding an already-decided offer is ErrNotFound", func(t *testing.T) {
		if err := db.DecideSkillOffer(ctx, "01OFFERIN", "rejected", time.Now().UTC(), 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("bad status is rejected", func(t *testing.T) {
		if err := db.DecideSkillOffer(ctx, "01OFFEROUT", "delivered", time.Now().UTC(), 0); err == nil {
			t.Fatalf("want error for unsupported status")
		}
	})
}

func TestExpireOldSkillOffers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)

	if err := db.InsertSkillOffer(ctx, sampleSkillOffer("01EXPIRED", "inbound", past)); err != nil {
		t.Fatalf("insert expired: %v", err)
	}
	if err := db.InsertSkillOffer(ctx, sampleSkillOffer("01FRESH", "inbound", future)); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}

	n, err := db.ExpireOldSkillOffers(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 expired, got %d", n)
	}
	expired, err := db.GetSkillOffer(ctx, "01EXPIRED")
	if err != nil {
		t.Fatalf("get expired: %v", err)
	}
	if expired.Status != "expired" {
		t.Fatalf("want status expired, got %q", expired.Status)
	}
	pending, _ := db.ListPendingSkillOffers(ctx, "inbound")
	if len(pending) != 1 || pending[0].OfferID != "01FRESH" {
		t.Fatalf("only fresh offer should remain pending, got %+v", pending)
	}
}
