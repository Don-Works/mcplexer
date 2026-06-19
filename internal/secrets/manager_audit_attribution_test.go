package secrets

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/audit"
)

// TestEmitAudit_AttributesToCaller verifies that when the gateway stamps a
// caller Attribution onto ctx, the emitted secret.* row carries the caller's
// session + workspace (so the audit table's Workspace / Session columns line
// up with every other tool row) instead of the scope-attributed placeholder.
func TestEmitAudit_AttributesToCaller(t *testing.T) {
	const (
		scopeID = "scope-abc"
		key     = "API_KEY"
		value   = "model-secret-do-not-leak"
	)

	mgr, _, aud := newTestManager(t)
	if err := mgr.Put(context.Background(), scopeID, key, []byte(value)); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	aud.records = nil // discard the seed row

	ctx := audit.WithAttribution(context.Background(), audit.Attribution{
		SessionID:     "sess-0dd6f647",
		ClientType:    "claude-code",
		Model:         "opus",
		WorkspaceID:   "ws-1",
		WorkspaceName: "Intervals Pro",
		Subpath:       "src/api",
		ActorKind:     "user",
		ActorID:       "sess-0dd6f647",
	})

	if _, err := mgr.Get(ctx, scopeID, key); err != nil {
		t.Fatalf("Get: %v", err)
	}

	recs := aud.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]

	// Caller attribution wins for the columns the audit table renders.
	if r.SessionID != "sess-0dd6f647" {
		t.Errorf("SessionID = %q, want caller session (not scope:%s)", r.SessionID, scopeID)
	}
	if r.WorkspaceID != "ws-1" || r.WorkspaceName != "Intervals Pro" {
		t.Errorf("Workspace = %q/%q, want ws-1/Intervals Pro", r.WorkspaceID, r.WorkspaceName)
	}
	if r.ClientType != "claude-code" {
		t.Errorf("ClientType = %q, want claude-code", r.ClientType)
	}
	if r.Model != "opus" {
		t.Errorf("Model = %q, want opus", r.Model)
	}
	if r.Subpath != "src/api" {
		t.Errorf("Subpath = %q, want src/api", r.Subpath)
	}
	if r.ActorKind != "user" || r.ActorID != "sess-0dd6f647" {
		t.Errorf("Actor = %q/%q, want user/sess-0dd6f647", r.ActorKind, r.ActorID)
	}

	// Scope linkage is preserved so the row still surfaces its secret badge
	// + scope name enrichment in the UI.
	if r.AuthScopeID != scopeID {
		t.Errorf("AuthScopeID = %q, want %q (scope enrichment must survive)", r.AuthScopeID, scopeID)
	}
	assertNoPlaintext(t, r, value)
}

// TestEmitAudit_NoAttributionFallsBackToScope verifies the unattributed path
// (dashboard / API / CLI — no MCP session on ctx) keeps the scope-attributed
// placeholder, which is the honest representation for detached emissions.
func TestEmitAudit_NoAttributionFallsBackToScope(t *testing.T) {
	const (
		scopeID = "scope-abc"
		key     = "API_KEY"
		value   = "model-secret-do-not-leak"
	)

	mgr, _, aud := newTestManager(t)
	// Use Get (not List): successful read-only List is intentionally
	// unaudited now, so it can no longer exercise the emission path.
	if err := mgr.Put(context.Background(), scopeID, key, []byte(value)); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	aud.records = nil // discard the seed row

	if _, err := mgr.Get(context.Background(), scopeID, key); err != nil {
		t.Fatalf("Get: %v", err)
	}

	recs := aud.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]

	if r.SessionID != "scope:"+scopeID {
		t.Errorf("SessionID = %q, want scope:%s", r.SessionID, scopeID)
	}
	if r.WorkspaceID != "" || r.WorkspaceName != "" {
		t.Errorf("Workspace = %q/%q, want empty for unattributed row", r.WorkspaceID, r.WorkspaceName)
	}
	if r.ClientType != "secrets" {
		t.Errorf("ClientType = %q, want secrets", r.ClientType)
	}
	if r.ActorKind != "secrets" {
		t.Errorf("ActorKind = %q, want secrets", r.ActorKind)
	}
}
