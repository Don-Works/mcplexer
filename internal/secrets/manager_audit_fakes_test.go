package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeAuditor captures every Record call so tests can assert on the
// exact AuditRecord shape the Manager emitted.
type fakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
	failErr error
}

func (f *fakeAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy so later mutations by the SUT (if any) don't race the test.
	cp := *rec
	f.records = append(f.records, &cp)
	return f.failErr
}

func (f *fakeAuditor) snapshot() []*store.AuditRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*store.AuditRecord, len(f.records))
	copy(out, f.records)
	return out
}

// fakeScopeStore is a minimal AuthScopeStore that holds a single scope's
// encrypted blob in memory. Other methods panic if reached — the
// Manager under test never calls them.
type fakeScopeStore struct {
	scope     *store.AuthScope
	getErr    error
	updateErr error
}

func (s *fakeScopeStore) GetAuthScope(_ context.Context, id string) (*store.AuthScope, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.scope == nil || s.scope.ID != id {
		return nil, store.ErrNotFound
	}
	return s.scope, nil
}

func (s *fakeScopeStore) UpdateAuthScopeEncryptedData(_ context.Context, _ string, data []byte) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if s.scope != nil {
		s.scope.EncryptedData = data
	}
	return nil
}

// Other AuthScopeStore methods are unused by Manager — panic on use to
// surface accidental reach-throughs in future refactors.
func (s *fakeScopeStore) CreateAuthScope(context.Context, *store.AuthScope) error {
	panic("not implemented")
}
func (s *fakeScopeStore) GetAuthScopeByName(context.Context, string) (*store.AuthScope, error) {
	panic("not implemented")
}
func (s *fakeScopeStore) ListAuthScopes(context.Context) ([]store.AuthScope, error) {
	panic("not implemented")
}
func (s *fakeScopeStore) UpdateAuthScope(context.Context, *store.AuthScope) error {
	panic("not implemented")
}
func (s *fakeScopeStore) DeleteAuthScope(context.Context, string) error {
	panic("not implemented")
}
func (s *fakeScopeStore) UpdateAuthScopeTokenData(context.Context, string, []byte) error {
	panic("not implemented")
}

// newTestManager spins up a Manager backed by a fakeScopeStore + an
// ephemeral encryptor, with a fakeAuditor wired so tests can assert.
func newTestManager(t *testing.T) (*Manager, *fakeScopeStore, *fakeAuditor) {
	t.Helper()
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("ephemeral encryptor: %v", err)
	}
	st := &fakeScopeStore{
		scope: &store.AuthScope{ID: "scope-abc", Name: "test"},
	}
	aud := &fakeAuditor{}
	mgr := NewManager(st, enc)
	mgr.SetAuditor(aud)
	return mgr, st, aud
}

// containsBytes is a guard used across every test to verify plaintext
// values never appear in a record's serialized form. Treat ANY raw byte
// of any payload (params + error message + session id, etc.) as fair
// game — we serialize the entire record and search.
func containsBytes(t *testing.T, rec *store.AuditRecord, needle []byte) bool {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal rec: %v", err)
	}
	return bytes.Contains(raw, needle)
}

// assertParamsHasKey verifies ParamsRedacted JSON encodes the expected
// scope_id + key pair.
func assertParamsHasKey(t *testing.T, rec *store.AuditRecord, key string) {
	t.Helper()
	var params map[string]string
	if err := json.Unmarshal(rec.ParamsRedacted, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params["key"] != key {
		t.Errorf("params.key = %q, want %q", params["key"], key)
	}
	if params["scope_id"] == "" {
		t.Errorf("params.scope_id is empty")
	}
}

// assertNoPlaintext is the load-bearing security check: under no
// circumstances may the audit record's serialized form contain the
// plaintext value.
func assertNoPlaintext(t *testing.T, rec *store.AuditRecord, plaintext string) {
	t.Helper()
	if plaintext == "" {
		return
	}
	if containsBytes(t, rec, []byte(plaintext)) {
		t.Fatalf("audit record leaks plaintext value: %s", string(rec.ParamsRedacted))
	}
}
