package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeStore is an in-memory HookReceiptStore for unit tests. It deliberately
// avoids any locking — tests are single-goroutine and we want the failure
// mode "you accidentally introduced concurrency" to be obvious.
type fakeStore struct {
	clients  map[string]*store.InstalledClient
	receipts []store.InstallReceipt
}

func newFakeStore() *fakeStore {
	return &fakeStore{clients: map[string]*store.InstalledClient{}}
}

func (f *fakeStore) UpsertInstalledClient(_ context.Context, c *store.InstalledClient) error {
	cp := *c
	f.clients[c.ID] = &cp
	return nil
}

func (f *fakeStore) GetInstalledClient(_ context.Context, id string) (*store.InstalledClient, error) {
	c, ok := f.clients[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeStore) CreateInstallReceipt(_ context.Context, r *store.InstallReceipt) error {
	if r.AppliedAt.IsZero() {
		r.AppliedAt = time.Now().UTC()
	}
	// Insert at the head so the slice mirrors the sqlite DESC-by-applied_at
	// ordering ListInstallReceipts is contracted to return.
	f.receipts = append([]store.InstallReceipt{*r}, f.receipts...)
	return nil
}

func (f *fakeStore) ListInstallReceipts(
	_ context.Context, clientID string, includeReversed bool,
) ([]store.InstallReceipt, error) {
	var out []store.InstallReceipt
	for _, r := range f.receipts {
		if clientID != "" && r.ClientID != clientID {
			continue
		}
		if !includeReversed && r.ReversedAt != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) MarkReceiptReversed(_ context.Context, id, reverseError string) error {
	now := time.Now().UTC()
	for i := range f.receipts {
		if f.receipts[i].ID == id {
			f.receipts[i].ReversedAt = &now
			f.receipts[i].ReverseError = reverseError
			return nil
		}
	}
	return errors.New("receipt not found")
}

// newTestInstaller wires a HookInstaller with a fresh tempdir HOME + fake
// store. Returned helpers let each test poke the FS directly without
// re-deriving paths.
func newTestInstaller(t *testing.T) (*HookInstaller, *fakeStore, string) {
	t.Helper()
	home := t.TempDir()
	fs := newFakeStore()
	inst, err := NewHookInstaller(home, fs, "")
	if err != nil {
		t.Fatalf("NewHookInstaller: %v", err)
	}
	return inst, fs, home
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return out
}

func writeJSON(t *testing.T, path string, v map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
