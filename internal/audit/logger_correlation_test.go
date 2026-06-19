package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeAuditStore captures every inserted AuditRecord so tests can
// inspect the post-Record state (CorrelationID populated, ParamsRedacted
// fallback applied). The interface methods we don't exercise are
// satisfied by embedding store.AuditStore (left nil — calls would panic,
// but Record only touches InsertAuditRecord).
type fakeAuditStore struct {
	store.AuditStore
	records []*store.AuditRecord
}

func (f *fakeAuditStore) InsertAuditRecord(_ context.Context, r *store.AuditRecord) error {
	cp := *r
	f.records = append(f.records, &cp)
	return nil
}

// nopScopeStore satisfies store.AuthScopeStore for the redaction-hints
// path; GetAuthScope returns ErrNotFound so loadRedactionHints
// short-circuits to an empty hint set. Other methods embedded as
// store.AuthScopeStore (left nil) are not exercised by Record.
type nopScopeStore struct {
	store.AuthScopeStore
}

func (nopScopeStore) GetAuthScope(context.Context, string) (*store.AuthScope, error) {
	return nil, store.ErrNotFound
}

func TestLogger_RecordPopulatesCorrelationFromCtx(t *testing.T) {
	a := &fakeAuditStore{}
	l := NewLogger(a, nopScopeStore{}, nil)

	ctx := WithCorrelation(context.Background(), "id-ctx")
	rec := &store.AuditRecord{
		ToolName:       "secret.read",
		ParamsRedacted: json.RawMessage(`{"scope_id":"s1"}`),
	}
	if err := l.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(a.records) != 1 {
		t.Fatalf("got %d records, want 1", len(a.records))
	}
	got := a.records[0]
	if got.CorrelationID != "id-ctx" {
		t.Fatalf("CorrelationID = %q, want id-ctx", got.CorrelationID)
	}
	var params map[string]any
	if err := json.Unmarshal(got.ParamsRedacted, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params["correlation_id"] != "id-ctx" {
		t.Fatalf("params correlation_id = %v, want id-ctx", params["correlation_id"])
	}
	if params["scope_id"] != "s1" {
		t.Fatalf("params lost scope_id: %v", params)
	}
}

func TestLogger_RecordPreservesExplicitCorrelation(t *testing.T) {
	a := &fakeAuditStore{}
	l := NewLogger(a, nopScopeStore{}, nil)

	ctx := WithCorrelation(context.Background(), "id-ctx")
	rec := &store.AuditRecord{
		ToolName:      "worker_run.started",
		CorrelationID: "id-explicit",
	}
	if err := l.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if a.records[0].CorrelationID != "id-explicit" {
		t.Fatalf("explicit id clobbered: %q", a.records[0].CorrelationID)
	}
}

func TestLogger_RecordNoIDLeavesEmpty(t *testing.T) {
	a := &fakeAuditStore{}
	l := NewLogger(a, nopScopeStore{}, nil)

	rec := &store.AuditRecord{ToolName: "x"}
	if err := l.Record(context.Background(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if a.records[0].CorrelationID != "" {
		t.Fatalf("CorrelationID = %q, want empty", a.records[0].CorrelationID)
	}
}

func TestEnsureCorrelationInParams(t *testing.T) {
	tests := []struct {
		name   string
		params json.RawMessage
		id     string
		check  func(t *testing.T, out json.RawMessage)
	}{
		{
			name:   "empty params seeded with correlation_id",
			params: nil,
			id:     "id-1",
			check: func(t *testing.T, out json.RawMessage) {
				var m map[string]any
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if m["correlation_id"] != "id-1" {
					t.Fatalf("got %v", m)
				}
			},
		},
		{
			name:   "existing key preserved (idempotent)",
			params: json.RawMessage(`{"correlation_id":"keep","x":1}`),
			id:     "ignored",
			check: func(t *testing.T, out json.RawMessage) {
				var m map[string]any
				_ = json.Unmarshal(out, &m)
				if m["correlation_id"] != "keep" {
					t.Fatalf("overwrote explicit: %v", m)
				}
			},
		},
		{
			name:   "object payload gets key merged",
			params: json.RawMessage(`{"x":1,"y":"a"}`),
			id:     "id-merge",
			check: func(t *testing.T, out json.RawMessage) {
				var m map[string]any
				_ = json.Unmarshal(out, &m)
				if m["correlation_id"] != "id-merge" || m["x"] == nil || m["y"] != "a" {
					t.Fatalf("merge lost data: %v", m)
				}
			},
		},
		{
			name:   "non-object payload left alone",
			params: json.RawMessage(`[1,2,3]`),
			id:     "id-skip",
			check: func(t *testing.T, out json.RawMessage) {
				if string(out) != "[1,2,3]" {
					t.Fatalf("array mutated: %s", out)
				}
			},
		},
		{
			name:   "empty id is a no-op",
			params: json.RawMessage(`{"x":1}`),
			id:     "",
			check: func(t *testing.T, out json.RawMessage) {
				if string(out) != `{"x":1}` {
					t.Fatalf("empty id mutated: %s", out)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, ensureCorrelationInParams(tc.params, tc.id))
		})
	}
}
