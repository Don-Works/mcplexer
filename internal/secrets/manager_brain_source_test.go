package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// stubScopeStore is a minimal store.AuthScopeStore for the dual-read tests.
// It returns one scope whose age-DB blob holds the legacy value.
type stubScopeStore struct {
	scope *store.AuthScope
}

func (s *stubScopeStore) GetAuthScope(ctx context.Context, id string) (*store.AuthScope, error) {
	if s.scope == nil || s.scope.ID != id {
		return nil, store.ErrNotFound
	}
	return s.scope, nil
}
func (s *stubScopeStore) GetAuthScopeByName(context.Context, string) (*store.AuthScope, error) {
	return nil, store.ErrNotFound
}
func (s *stubScopeStore) CreateAuthScope(context.Context, *store.AuthScope) error { return nil }
func (s *stubScopeStore) ListAuthScopes(context.Context) ([]store.AuthScope, error) {
	return nil, nil
}
func (s *stubScopeStore) UpdateAuthScope(context.Context, *store.AuthScope) error { return nil }
func (s *stubScopeStore) DeleteAuthScope(context.Context, string) error           { return nil }
func (s *stubScopeStore) UpdateAuthScopeTokenData(context.Context, string, []byte) error {
	return nil
}
func (s *stubScopeStore) UpdateAuthScopeEncryptedData(ctx context.Context, id string, data []byte) error {
	if s.scope != nil {
		s.scope.EncryptedData = data
	}
	return nil
}

// stubBrainSource implements BrainSource with an in-memory map keyed by
// (scopeName, key).
type stubBrainSource struct {
	values map[string]string // "scopeName/key" -> value
	err    error
	hit    bool
}

func (b *stubBrainSource) Get(ctx context.Context, scopeName, key string) ([]byte, bool, error) {
	if b.err != nil {
		return nil, false, b.err
	}
	v, ok := b.values[scopeName+"/"+key]
	if ok {
		b.hit = true
	}
	return []byte(v), ok, nil
}

// ListKeys makes stubBrainSource satisfy secrets.BrainLister: it returns the
// key names recorded for scopeName (present=true) so List can fold SOPS-only
// keys into the inventory. An empty/absent scope yields (nil, false, nil),
// mirroring the "scope not here" contract; a configured err falls through.
func (b *stubBrainSource) ListKeys(ctx context.Context, scopeName string) ([]string, bool, error) {
	if b.err != nil {
		return nil, false, b.err
	}
	prefix := scopeName + "/"
	var keys []string
	for k := range b.values {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k[len(prefix):])
		}
	}
	if len(keys) == 0 {
		return nil, false, nil
	}
	return keys, true, nil
}

// newSealedScope builds an auth scope whose EncryptedData holds {key:legacy}
// sealed with the given encryptor, plus the matching manager.
func newSealedManager(t *testing.T, scopeID, scopeName, key, legacyVal string) (*Manager, *stubScopeStore) {
	t.Helper()
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("NewEphemeralEncryptor: %v", err)
	}
	blob, err := json.Marshal(map[string]string{key: legacyVal})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealed, err := enc.Encrypt(blob)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	st := &stubScopeStore{scope: &store.AuthScope{ID: scopeID, Name: scopeName, EncryptedData: sealed}}
	return NewManager(st, enc), st
}

func TestGet_BrainSourceFirst(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	m.SetBrainSource(&stubBrainSource{values: map[string]string{"stripe/K": "fresh-from-sops"}})

	got, err := m.Get(context.Background(), "sc1", "K")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "fresh-from-sops" {
		t.Fatalf("Get=%q want fresh-from-sops (brain source should win)", got)
	}
}

func TestGet_FallsBackToAge(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	// Brain source has no entry for this scope → miss → fall back to age DB.
	m.SetBrainSource(&stubBrainSource{values: map[string]string{}})

	got, err := m.Get(context.Background(), "sc1", "K")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "legacy-from-db" {
		t.Fatalf("Get=%q want legacy-from-db (fallback)", got)
	}
}

func TestGet_BrainSourceErrorFallsBack(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	m.SetBrainSource(&stubBrainSource{err: errors.New("sops decrypt boom")})

	got, err := m.Get(context.Background(), "sc1", "K")
	if err != nil {
		t.Fatalf("Get: %v (a brain-source error must fall back, not surface)", err)
	}
	if string(got) != "legacy-from-db" {
		t.Fatalf("Get=%q want legacy-from-db (error fallback)", got)
	}
}

// TestPut_ShadowsBrainSource is the dual-write finding guard: after a secret
// is migrated to the SOPS brain source, a rotation via Put must win on the
// next Get — the (mtime-cached) SOPS file must NOT silently shadow the new
// DB value. Before the fix Get always preferred the brain source, so the
// rotated value was invisible.
func TestPut_ShadowsBrainSource(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	bs := &stubBrainSource{values: map[string]string{"stripe/K": "migrated-to-sops"}}
	m.SetBrainSource(bs)

	// Sanity: before any Put, the brain source wins (migration is live).
	if got, err := m.Get(context.Background(), "sc1", "K"); err != nil || string(got) != "migrated-to-sops" {
		t.Fatalf("pre-Put Get = (%q,%v), want migrated-to-sops", got, err)
	}

	// Rotate the secret via Put (writes the age-DB blob, not SOPS).
	if err := m.Put(context.Background(), "sc1", "K", []byte("rotated-fresh")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get must now return the rotated value, NOT the stale SOPS value.
	got, err := m.Get(context.Background(), "sc1", "K")
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if string(got) != "rotated-fresh" {
		t.Fatalf("Get=%q want rotated-fresh (DB write must win over stale SOPS cache)", got)
	}
}

// TestDelete_ShadowsBrainSource verifies a deleted key does not reappear from
// the cached SOPS file.
func TestDelete_ShadowsBrainSource(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	m.SetBrainSource(&stubBrainSource{values: map[string]string{"stripe/K": "migrated-to-sops"}})

	if err := m.Delete(context.Background(), "sc1", "K"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// After delete the SOPS source is shadowed and the age DB no longer holds
	// the key → not-found.
	if _, err := m.Get(context.Background(), "sc1", "K"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete err=%v, want ErrNotFound (deleted key must not resurface from SOPS)", err)
	}
}

// sortedSet collapses a key slice into a presence set for order-independent
// comparison (List makes no ordering guarantee).
func sortedSet(keys []string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s[k] = struct{}{}
	}
	return s
}

// TestList_DualSource is the inventory-symmetry guard (the List single-read
// gap). List must surface keys that live ONLY in the SOPS brain source, must
// not duplicate keys present in both, and must NOT resurface a key the DB has
// authoritatively deleted (dbFresher shadowing) — mirroring the Get/Delete
// contract. With no brain source wired, List is the age-DB blob verbatim.
func TestList_DualSource(t *testing.T) {
	const scopeID, scopeName = "sc1", "stripe"

	tests := []struct {
		name string
		// setup configures the manager (brain source, deletes) and returns it.
		setup    func(t *testing.T) *Manager
		wantKeys []string
	}{
		{
			name: "nil brain source lists age-db blob only",
			setup: func(t *testing.T) *Manager {
				m, _ := newSealedManager(t, scopeID, scopeName, "DB_KEY", "v")
				return m
			},
			wantKeys: []string{"DB_KEY"},
		},
		{
			name: "sops-only key is folded into the inventory",
			setup: func(t *testing.T) *Manager {
				m, _ := newSealedManager(t, scopeID, scopeName, "DB_KEY", "v")
				m.SetBrainSource(&stubBrainSource{values: map[string]string{
					scopeName + "/SOPS_ONLY": "sops-val",
				}})
				return m
			},
			wantKeys: []string{"DB_KEY", "SOPS_ONLY"},
		},
		{
			name: "key present in both is not duplicated",
			setup: func(t *testing.T) *Manager {
				m, _ := newSealedManager(t, scopeID, scopeName, "DB_KEY", "v")
				m.SetBrainSource(&stubBrainSource{values: map[string]string{
					scopeName + "/DB_KEY":    "also-in-sops",
					scopeName + "/SOPS_ONLY": "sops-val",
				}})
				return m
			},
			wantKeys: []string{"DB_KEY", "SOPS_ONLY"},
		},
		{
			name: "db-deleted key does not resurface from sops cache",
			setup: func(t *testing.T) *Manager {
				m, _ := newSealedManager(t, scopeID, scopeName, "DB_KEY", "v")
				// SOPS still holds DB_KEY (stale cached file), but the DB has
				// authoritatively deleted it → must stay shadowed in List too.
				m.SetBrainSource(&stubBrainSource{values: map[string]string{
					scopeName + "/DB_KEY": "stale-in-sops",
				}})
				if err := m.Delete(context.Background(), scopeID, "DB_KEY"); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				return m
			},
			wantKeys: []string{},
		},
		{
			name: "brain error falls back to age-db enumeration",
			setup: func(t *testing.T) *Manager {
				m, _ := newSealedManager(t, scopeID, scopeName, "DB_KEY", "v")
				m.SetBrainSource(&stubBrainSource{err: errors.New("sops decrypt boom")})
				return m
			},
			wantKeys: []string{"DB_KEY"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			got, err := m.List(context.Background(), scopeID)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			gotSet, wantSet := sortedSet(got), sortedSet(tc.wantKeys)
			if len(gotSet) != len(wantSet) {
				t.Fatalf("List=%v want %v", got, tc.wantKeys)
			}
			for k := range wantSet {
				if _, ok := gotSet[k]; !ok {
					t.Fatalf("List=%v missing %q (want %v)", got, k, tc.wantKeys)
				}
			}
		})
	}
}

func TestGet_NilBrainSourceUnchanged(t *testing.T) {
	m, _ := newSealedManager(t, "sc1", "stripe", "K", "legacy-from-db")
	// No SetBrainSource → today's behaviour exactly.
	got, err := m.Get(context.Background(), "sc1", "K")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "legacy-from-db" {
		t.Fatalf("Get=%q want legacy-from-db", got)
	}
}
