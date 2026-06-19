package secrets

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// assertCommon runs the invariant checks every audit row must satisfy
// regardless of which event it is: ClientType, SessionID format,
// AuthScopeID set, Timestamp non-zero, ULID-like ID.
func assertCommon(t *testing.T, rec *store.AuditRecord, scopeID string) {
	t.Helper()
	if rec.ClientType != "secrets" {
		t.Errorf("ClientType = %q, want %q", rec.ClientType, "secrets")
	}
	if rec.SessionID != "scope:"+scopeID {
		t.Errorf("SessionID = %q, want %q", rec.SessionID, "scope:"+scopeID)
	}
	if rec.AuthScopeID != scopeID {
		t.Errorf("AuthScopeID = %q, want %q", rec.AuthScopeID, scopeID)
	}
	if rec.ActorKind != "secrets" {
		t.Errorf("ActorKind = %q, want %q", rec.ActorKind, "secrets")
	}
	if rec.ActorID != scopeID {
		t.Errorf("ActorID = %q, want %q", rec.ActorID, scopeID)
	}
	if rec.ID == "" {
		t.Error("ID is empty — want ULID")
	}
	if rec.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
	if rec.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if time.Since(rec.Timestamp) > time.Minute {
		t.Errorf("Timestamp not recent: %v", rec.Timestamp)
	}
}

// TestManager_EmitsAuditPerOperation walks each public method through
// happy + sad paths and asserts on event name, status, params shape,
// and plaintext absence.
func TestManager_EmitsAuditPerOperation(t *testing.T) {
	const (
		scopeID = "scope-abc"
		key     = "API_KEY"
		value   = "model-secret-do-not-leak"
	)

	t.Run("Put emits secret.write ok", func(t *testing.T) {
		mgr, _, aud := newTestManager(t)
		if err := mgr.Put(context.Background(), scopeID, key, []byte(value)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		r := recs[0]
		if r.ToolName != "secret.write" {
			t.Errorf("ToolName = %q, want secret.write", r.ToolName)
		}
		if r.Status != "ok" {
			t.Errorf("Status = %q, want ok", r.Status)
		}
		assertCommon(t, r, scopeID)
		assertParamsHasKey(t, r, key)
		assertNoPlaintext(t, r, value)
	})

	t.Run("Get success emits secret.read ok", func(t *testing.T) {
		mgr, _, aud := newTestManager(t)
		_ = mgr.Put(context.Background(), scopeID, key, []byte(value))
		aud.records = nil // discard Put's emission so we test Get only

		got, err := mgr.Get(context.Background(), scopeID, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got, []byte(value)) {
			t.Errorf("Get value = %q, want %q", got, value)
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		r := recs[0]
		if r.ToolName != "secret.read" {
			t.Errorf("ToolName = %q, want secret.read", r.ToolName)
		}
		if r.Status != "ok" {
			t.Errorf("Status = %q, want ok", r.Status)
		}
		assertCommon(t, r, scopeID)
		assertParamsHasKey(t, r, key)
		assertNoPlaintext(t, r, value)
	})

	t.Run("Get not-found emits secret.read error", func(t *testing.T) {
		mgr, _, aud := newTestManager(t)
		_, err := mgr.Get(context.Background(), scopeID, "missing")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get missing: got %v, want ErrNotFound", err)
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		r := recs[0]
		if r.ToolName != "secret.read" {
			t.Errorf("ToolName = %q, want secret.read", r.ToolName)
		}
		if r.Status != "error" {
			t.Errorf("Status = %q, want error", r.Status)
		}
		if r.ErrorMessage != "key not found" {
			t.Errorf("ErrorMessage = %q, want %q", r.ErrorMessage, "key not found")
		}
		assertCommon(t, r, scopeID)
		assertParamsHasKey(t, r, "missing")
	})

	t.Run("Delete emits secret.delete ok", func(t *testing.T) {
		mgr, _, aud := newTestManager(t)
		_ = mgr.Put(context.Background(), scopeID, key, []byte(value))
		aud.records = nil

		if err := mgr.Delete(context.Background(), scopeID, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		r := recs[0]
		if r.ToolName != "secret.delete" {
			t.Errorf("ToolName = %q, want secret.delete", r.ToolName)
		}
		if r.Status != "ok" {
			t.Errorf("Status = %q, want ok", r.Status)
		}
		assertCommon(t, r, scopeID)
		assertParamsHasKey(t, r, key)
	})

	t.Run("Delete not-found emits secret.delete error", func(t *testing.T) {
		mgr, _, aud := newTestManager(t)
		err := mgr.Delete(context.Background(), scopeID, "ghost")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Delete ghost: got %v, want ErrNotFound", err)
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1", len(recs))
		}
		r := recs[0]
		if r.Status != "error" {
			t.Errorf("Status = %q, want error", r.Status)
		}
		if r.ErrorMessage != "key not found" {
			t.Errorf("ErrorMessage = %q, want %q", r.ErrorMessage, "key not found")
		}
	})

	t.Run("List success emits no audit row", func(t *testing.T) {
		// Read-only enumeration is intentionally NOT audited on success:
		// secret__list_refs is a frequent read-only discovery call and a
		// row per scope per call floods the trail with no forensic value
		// (no secret values are revealed). See Manager.List.
		mgr, _, aud := newTestManager(t)
		_ = mgr.Put(context.Background(), scopeID, key, []byte(value))
		_ = mgr.Put(context.Background(), scopeID, "OTHER", []byte("v2"))
		aud.records = nil

		keys, err := mgr.List(context.Background(), scopeID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(keys) != 2 {
			t.Errorf("len(keys) = %d, want 2", len(keys))
		}
		recs := aud.snapshot()
		if len(recs) != 0 {
			t.Fatalf("got %d records, want 0 (successful enumeration is not audited)", len(recs))
		}
	})

	t.Run("List failure still emits secret.list error", func(t *testing.T) {
		// Failed/denied enumeration remains audited — that's the
		// security-interesting event we keep.
		mgr, _, aud := newTestManager(t)
		aud.records = nil

		_, err := mgr.List(context.Background(), "nonexistent-scope")
		if err == nil {
			t.Fatalf("List on missing scope: want error, got nil")
		}
		recs := aud.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d records, want 1 (failed enumeration IS audited)", len(recs))
		}
		r := recs[0]
		if r.ToolName != "secret.list" {
			t.Errorf("ToolName = %q, want secret.list", r.ToolName)
		}
		if r.Status != "error" {
			t.Errorf("Status = %q, want error", r.Status)
		}
	})
}
