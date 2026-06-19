package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/auth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// capturingBackend records the args it receives so we can assert that
// the dispatch path delivered substituted plaintext to the downstream.
type capturingBackend struct {
	mu       sync.Mutex
	lastArgs json.RawMessage
}

func (b *capturingBackend) ListTools(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"tools":[]}`), nil
}

func (b *capturingBackend) Call(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.lastArgs = append(json.RawMessage(nil), args...)
	b.mu.Unlock()
	return json.RawMessage(`{"ok":true}`), nil
}

// newManagerWithSecrets stands up a Manager wired to a real
// auth.Injector + secrets.Manager backed by an in-memory SQLite + an
// ephemeral age key. Returns the manager plus the configured auth scope
// ID so tests can write secrets against it.
func newManagerWithSecrets(t *testing.T) (*Manager, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("ephemeral encryptor: %v", err)
	}

	scope := &store.AuthScope{ID: "scope-1", Name: "test", Type: "env", Source: "test"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	sm := secrets.NewManager(db, enc)
	if err := sm.Put(ctx, scope.ID, "api-key", []byte("plaintext-value-XYZ")); err != nil {
		t.Fatalf("put secret: %v", err)
	}

	inj := auth.NewInjector(sm, nil, db)
	return NewManager(db, inj), scope.ID
}

func TestCall_SubstitutesSecretRefBeforeDispatch(t *testing.T) {
	m, scopeID := newManagerWithSecrets(t)
	backend := &capturingBackend{}
	registerInternalServer(t, m, "fakehttp", backend)

	originalArgs := json.RawMessage(`{"endpoint":"/users","api_key":"secret://api-key"}`)
	// Take a copy to verify the caller's slice isn't mutated.
	argsCopy := append(json.RawMessage(nil), originalArgs...)

	_, err := m.Call(context.Background(), "fakehttp", scopeID, "any_tool", originalArgs)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	backend.mu.Lock()
	got := string(backend.lastArgs)
	backend.mu.Unlock()

	if !bytes.Contains([]byte(got), []byte(`"plaintext-value-XYZ"`)) {
		t.Errorf("downstream did not receive substituted plaintext; got: %s", got)
	}
	if bytes.Contains([]byte(got), []byte("secret://")) {
		t.Errorf("downstream still saw the secret-ref placeholder; got: %s", got)
	}
	if !bytes.Equal(originalArgs, argsCopy) {
		t.Errorf("caller's args slice was mutated in place")
	}
}

func TestCall_RefWithoutAuthScope_Errors(t *testing.T) {
	m, _ := newManagerWithSecrets(t)
	backend := &capturingBackend{}
	registerInternalServer(t, m, "fakehttp", backend)

	args := json.RawMessage(`{"api_key":"secret://api-key"}`)
	_, err := m.Call(context.Background(), "fakehttp", "", "any_tool", args)
	if err == nil {
		t.Fatal("expected error when ref is used with no auth scope")
	}
	if !errors.Is(err, ErrSecretRefNoScope) {
		t.Errorf("expected ErrSecretRefNoScope, got: %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lastArgs != nil {
		t.Errorf("downstream should not be reached when ref resolution fails; got args: %s", backend.lastArgs)
	}
}

func TestCall_UnknownRef_Errors(t *testing.T) {
	m, scopeID := newManagerWithSecrets(t)
	backend := &capturingBackend{}
	registerInternalServer(t, m, "fakehttp", backend)

	args := json.RawMessage(`{"api_key":"secret://typo-key"}`)
	_, err := m.Call(context.Background(), "fakehttp", scopeID, "any_tool", args)
	if err == nil {
		t.Fatal("expected error for unknown reference")
	}
	if !errors.Is(err, ErrSecretRefUnknown) {
		t.Errorf("expected ErrSecretRefUnknown, got: %v", err)
	}
}

func TestCall_NoRef_PassesArgsThroughUnchanged(t *testing.T) {
	m, scopeID := newManagerWithSecrets(t)
	backend := &capturingBackend{}
	registerInternalServer(t, m, "fakehttp", backend)

	args := json.RawMessage(`{"endpoint":"/users","limit":10}`)
	_, err := m.Call(context.Background(), "fakehttp", scopeID, "any_tool", args)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !bytes.Equal(backend.lastArgs, args) {
		t.Errorf("expected exact byte passthrough when no refs present\nwant: %s\ngot:  %s", args, backend.lastArgs)
	}
}
