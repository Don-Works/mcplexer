// Package admin_test — audit_test_helpers_test.go houses the fakeAuditor
// fixture + payload decoders used by crud_audit_test.go. Lives in its
// own file to keep the per-test-case file under the 300-line budget.
package admin_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// fakeAuditor records every Record call so tests can introspect the
// emitted AuditRecord stream. Thread-safe so the audit-pipeline failure
// case (where Record itself returns an error) doesn't race the test
// harness.
type fakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
	err     error // when non-nil, Record returns this AND still records
}

func (f *fakeAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	return f.err
}

func (f *fakeAuditor) snapshot() []*store.AuditRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*store.AuditRecord, len(f.records))
	copy(out, f.records)
	return out
}

// decodePayload extracts the JSON payload from an AuditRecord. Test
// helpers downstream of this can index into the resulting map without
// the boilerplate.
func decodePayload(t *testing.T, rec *store.AuditRecord) map[string]any {
	t.Helper()
	if len(rec.ParamsRedacted) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(rec.ParamsRedacted, &out); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return out
}

// findRecord returns the first AuditRecord with the given ToolName, or
// fails the test. Cuts the noise from cross-test event interleaving.
func findRecord(t *testing.T, recs []*store.AuditRecord, event string) *store.AuditRecord {
	t.Helper()
	for _, r := range recs {
		if r.ToolName == event {
			return r
		}
	}
	t.Fatalf("no audit record for event %q (have %d records)", event, len(recs))
	return nil
}

// newAuditedService spins up a Service with a wired fake auditor,
// returning everything the table-driven cases below need.
func newAuditedService(t *testing.T) (*admin.Service, *fakeAuditor, string, string) {
	t.Helper()
	svc, _, wsID, scopeID := newTestService(t)
	fa := &fakeAuditor{}
	svc.SetAuditor(fa)
	return svc, fa, wsID, scopeID
}
