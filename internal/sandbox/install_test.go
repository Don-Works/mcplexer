package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeStore is the in-memory ReceiptStore used by install_test. Mirrors
// the shape of internal/install.fakeStore so future refactors that share
// a test fake stay easy.
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

func newTestInstaller(t *testing.T) (*Installer, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	inst, err := NewInstaller(t.TempDir(), fs)
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	return inst, fs
}

func TestEnableSandbox_RecordsReceipt(t *testing.T) {
	inst, fs := newTestInstaller(t)
	ctx := context.Background()

	r, err := inst.EnableSandbox(ctx, "claude_code")
	if err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	if r == nil || r.Action != sandboxEnableAction {
		t.Fatalf("unexpected receipt: %+v", r)
	}
	if len(fs.receipts) != 1 {
		t.Fatalf("want 1 receipt, got %d", len(fs.receipts))
	}
	if !fs.clients["claude_code"].SandboxEnabled {
		t.Fatalf("client.SandboxEnabled not flipped")
	}
}

func TestEnableSandbox_IsIdempotent(t *testing.T) {
	inst, fs := newTestInstaller(t)
	ctx := context.Background()

	r1, err := inst.EnableSandbox(ctx, "claude_code")
	if err != nil {
		t.Fatalf("EnableSandbox #1: %v", err)
	}
	r2, err := inst.EnableSandbox(ctx, "claude_code")
	if err != nil {
		t.Fatalf("EnableSandbox #2: %v", err)
	}
	if r1.ID != r2.ID {
		t.Fatalf("second call should reuse existing receipt: %s vs %s", r1.ID, r2.ID)
	}
	if len(fs.receipts) != 1 {
		t.Fatalf("want 1 receipt after dup call, got %d", len(fs.receipts))
	}
}

func TestDisableSandbox_MarksReversed(t *testing.T) {
	inst, fs := newTestInstaller(t)
	ctx := context.Background()

	if _, err := inst.EnableSandbox(ctx, "claude_code"); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	if err := inst.DisableSandbox(ctx, "claude_code"); err != nil {
		t.Fatalf("DisableSandbox: %v", err)
	}
	if fs.receipts[0].ReversedAt == nil {
		t.Fatalf("receipt not reversed: %+v", fs.receipts[0])
	}
	if fs.clients["claude_code"].SandboxEnabled {
		t.Fatalf("SandboxEnabled should be false after disable")
	}
}

func TestDisableSandbox_NoOpWhenAbsent(t *testing.T) {
	inst, _ := newTestInstaller(t)
	if err := inst.DisableSandbox(context.Background(), "claude_code"); err != nil {
		t.Fatalf("Disable on empty store: %v", err)
	}
}

func TestStatus_ReflectsLifecycle(t *testing.T) {
	inst, _ := newTestInstaller(t)
	ctx := context.Background()

	on, err := inst.Status(ctx, "claude_code")
	if err != nil || on {
		t.Fatalf("initial status want=false,nil got=%v,%v", on, err)
	}
	if _, err := inst.EnableSandbox(ctx, "claude_code"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	on, _ = inst.Status(ctx, "claude_code")
	if !on {
		t.Fatalf("after enable, status should be true")
	}
	if err := inst.DisableSandbox(ctx, "claude_code"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	on, _ = inst.Status(ctx, "claude_code")
	if on {
		t.Fatalf("after disable, status should be false")
	}
}

func TestNewInstaller_Validation(t *testing.T) {
	if _, err := NewInstaller("", newFakeStore()); err == nil {
		t.Fatal("expected error for empty home")
	}
	if _, err := NewInstaller(t.TempDir(), nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}
