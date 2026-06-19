package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestAuditRecordTierConsentRoundTrip pins the migration-082 contract:
// the tier + accepted_by + grant_origin + denial_reason columns survive
// an INSERT → SELECT round-trip with NULL semantics intact (legacy
// rows scan back as empty/zero, not as the literal string "null"). The
// bulletproof consent_audit scenario asserts on the JSON shape, so any
// regression here cascades to a hard FAIL in the overnight rig.
func TestAuditRecordTierConsentRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	cases := []struct {
		name string
		in   store.AuditRecord
	}{
		{
			name: "tier1_auto_pair_no_grant_origin",
			in: store.AuditRecord{
				Timestamp:  now,
				CreatedAt:  now,
				ToolName:   "mesh__skill_share",
				Status:     "ok",
				ActorKind:  "mesh",
				ActorID:    "peer-bob",
				Tier:       "same_user",
				AcceptedBy: json.RawMessage(`{"kind":"auto_pair"}`),
			},
		},
		{
			name: "tier2_human_grant_origin",
			in: store.AuditRecord{
				Timestamp: now,
				CreatedAt: now,
				ToolName:  "mesh__memory_share",
				Status:    "ok",
				ActorKind: "mesh",
				ActorID:   "peer-carol",
				Tier:      "same_org",
				AcceptedBy: json.RawMessage(
					`{"kind":"human","user_id":"u-1","agent_id":"a-2","timestamp":"2026-05-27T12:00:00Z"}`,
				),
				GrantOrigin: json.RawMessage(
					`{"peer_id":"p","agent_id":"a","grant_id":"g"}`,
				),
			},
		},
		{
			name: "tier3_cross_org_denied",
			in: store.AuditRecord{
				Timestamp:    now,
				CreatedAt:    now,
				ToolName:     "mesh__skill_share",
				Status:       "denied",
				ActorKind:    "mesh",
				ActorID:      "peer-eve",
				Tier:         "cross_org",
				DenialReason: "cross_org_no_grant",
				ErrorMessage: "scope mesh.skill_request required",
			},
		},
		{
			name: "legacy_no_tier_no_envelope",
			in: store.AuditRecord{
				Timestamp: now,
				CreatedAt: now,
				ToolName:  "downstream__example",
				Status:    "success",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.in
			if err := db.InsertAuditRecord(ctx, &rec); err != nil {
				t.Fatalf("insert: %v", err)
			}
			rows, _, err := db.QueryAuditRecords(ctx, store.AuditFilter{
				ID: &rec.ID, Limit: 1,
			})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows = %d, want 1", len(rows))
			}
			got := rows[0]

			if got.Tier != tc.in.Tier {
				t.Errorf("Tier = %q, want %q", got.Tier, tc.in.Tier)
			}
			if got.DenialReason != tc.in.DenialReason {
				t.Errorf("DenialReason = %q, want %q",
					got.DenialReason, tc.in.DenialReason)
			}
			if string(got.AcceptedBy) != string(tc.in.AcceptedBy) {
				t.Errorf("AcceptedBy round-trip differs:\n  got  %s\n  want %s",
					string(got.AcceptedBy), string(tc.in.AcceptedBy))
			}
			if string(got.GrantOrigin) != string(tc.in.GrantOrigin) {
				t.Errorf("GrantOrigin round-trip differs:\n  got  %s\n  want %s",
					string(got.GrantOrigin), string(tc.in.GrantOrigin))
			}
		})
	}
}

// TestAuditRecordTierColumnsAreNullable confirms that the migration
// added NULLable columns — the row inserts cleanly with all four fields
// at their Go zero values (Tier="", AcceptedBy=nil, ...) and reads back
// without panicking on a typed-nil scan.
func TestAuditRecordTierColumnsAreNullable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := store.AuditRecord{
		Timestamp: now,
		CreatedAt: now,
		ToolName:  "test__legacy",
		Status:    "success",
	}
	if err := db.InsertAuditRecord(ctx, &rec); err != nil {
		t.Fatalf("insert with empty tier columns: %v", err)
	}
	rows, _, err := db.QueryAuditRecords(ctx, store.AuditFilter{
		ID: &rec.ID, Limit: 1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Tier != "" || got.DenialReason != "" {
		t.Errorf("expected empty tier columns, got Tier=%q DenialReason=%q",
			got.Tier, got.DenialReason)
	}
	if got.AcceptedBy != nil {
		t.Errorf("AcceptedBy on empty row = %q, want nil", string(got.AcceptedBy))
	}
	if got.GrantOrigin != nil {
		t.Errorf("GrantOrigin on empty row = %q, want nil", string(got.GrantOrigin))
	}
}
