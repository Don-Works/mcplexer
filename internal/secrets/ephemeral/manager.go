package ephemeral

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// Notifier fires a user-facing notification when a new prompt is created.
// Implemented by *notify.Bus in production; nil-safe.
type Notifier interface {
	Publish(id, label, reason string)
}

// Manager owns the lifecycle of ephemeral secret prompts: pending row
// bookkeeping, file write with 0600 perms, delete-on-read watching, and the
// sweep timer.
type Manager struct {
	store    store.SecretPromptStore
	dir      string // {data_dir}/secrets/ephemeral
	notifier Notifier
	bus      *Bus
	audit    AuditHook
	watcher  watcher

	mu       sync.Mutex
	pending  map[string]*pendingPrompt
	stopOnce sync.Once
	stopCh   chan struct{}
}

// pendingPrompt holds the in-memory waiter for a row. The result channel is
// closed by Submit, Cancel, or the sweeper.
type pendingPrompt struct {
	req      PromptRequest
	result   chan promptOutcome
	deadline time.Time
}

type promptOutcome struct {
	res PromptResult
	err error
}

// New constructs a Manager. dataDir is the parent directory; the manager
// creates dataDir/secrets/ephemeral with 0700 perms. Any of notifier, bus,
// audit may be nil. The background sweeper is NOT started until Start(ctx)
// is called.
func New(
	ctx context.Context,
	s store.SecretPromptStore,
	dataDir string,
	notifier Notifier,
	bus *Bus,
	audit AuditHook,
) (*Manager, error) {
	dir := filepath.Join(dataDir, "secrets", "ephemeral")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	m := &Manager{
		store:    s,
		dir:      dir,
		notifier: notifier,
		bus:      bus,
		audit:    audit,
		watcher:  newWatcher(),
		pending:  make(map[string]*pendingPrompt),
		stopCh:   make(chan struct{}),
	}
	return m, nil
}

// Start launches the background sweeper. Call Stop to release goroutines.
func (m *Manager) Start(ctx context.Context) {
	go m.sweepLoop(ctx)
}

// Stop halts the sweeper.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	if m.watcher != nil {
		m.watcher.Close()
	}
}

// ListPendingForTest returns the manager's pending rows from the store. It
// is intentionally exported so cross-package tests in internal/gateway can
// drive the manager without depending on the SQL schema directly.
func (m *Manager) ListPendingForTest() ([]store.SecretPrompt, error) {
	return m.store.ListPendingSecretPrompts(context.Background())
}

// defaultPromptTimeout bounds how long a secret__prompt waits on the human
// before auto-expiring to status=timeout. Kept short so a blocked agent does
// not hang indefinitely and the dashboard "waiting on secrets" surface stays
// current; callers needing longer may pass an explicit req.Timeout.
const defaultPromptTimeout = 2 * time.Minute

// RequestPrompt creates a pending row, fires the user notification, and
// returns immediately with the prompt id and absolute expires_at. The caller
// must then call Wait(ctx, id) to block until the user resolves it.
func (m *Manager) RequestPrompt(
	ctx context.Context, req PromptRequest,
) (PromptCreated, error) {
	if req.Timeout <= 0 {
		req.Timeout = defaultPromptTimeout
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	expires := now.Add(req.Timeout)

	row := &store.SecretPrompt{
		ID:           id,
		Reason:       req.Reason,
		Label:        req.Label,
		Requester:    req.Requester,
		Status:       "pending",
		ExpiresAt:    expires,
		CreatedAt:    now,
		DeleteOnRead: req.DeleteOnRead,
	}
	if err := m.store.CreateSecretPrompt(ctx, row); err != nil {
		return PromptCreated{}, fmt.Errorf("create prompt row: %w", err)
	}

	m.mu.Lock()
	m.pending[id] = &pendingPrompt{
		req:      req,
		result:   make(chan promptOutcome, 1),
		deadline: expires,
	}
	m.mu.Unlock()

	if m.notifier != nil {
		m.notifier.Publish(id, req.Label, req.Reason)
	}
	if m.bus != nil {
		m.bus.Publish(Event{
			Type:      "pending",
			ID:        id,
			Reason:    req.Reason,
			Label:     req.Label,
			Requester: req.Requester,
			Status:    "pending",
			ExpiresAt: expires,
			CreatedAt: now,
		})
	}
	if m.audit != nil {
		m.audit("created", req, id)
	}
	return PromptCreated{ID: id, ExpiresAt: expires}, nil
}

// Wait blocks until the prompt is submitted, cancelled, expires, or ctx is
// done. The returned PromptResult contains the file path only on success.
func (m *Manager) Wait(ctx context.Context, id string) (PromptResult, error) {
	m.mu.Lock()
	p, ok := m.pending[id]
	m.mu.Unlock()
	if !ok {
		return PromptResult{}, ErrPromptNotFound
	}
	timer := time.NewTimer(time.Until(p.deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return PromptResult{}, ctx.Err()
	case out := <-p.result:
		return out.res, out.err
	case <-timer.C:
		m.timeoutPrompt(context.Background(), id)
		return PromptResult{}, ErrPromptTimeout
	}
}

// Submit writes secretBytes to a daemon-owned 0600 file and resolves the
// pending waiter. The file path is a 256-bit random unguessable id.
func (m *Manager) Submit(ctx context.Context, id string, secretBytes []byte) error {
	m.mu.Lock()
	p, ok := m.pending[id]
	m.mu.Unlock()
	if !ok {
		return ErrPromptNotFound
	}

	path, err := m.writeSecretFile(secretBytes)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if err := m.store.CompleteSecretPrompt(ctx, id, "submitted", path, now); err != nil {
		_ = os.Remove(path)
		if errors.Is(err, store.ErrNotFound) {
			return ErrPromptAlreadyResolved
		}
		return fmt.Errorf("complete prompt: %w", err)
	}

	m.mu.Lock()
	delete(m.pending, id)
	m.mu.Unlock()

	if p.req.DeleteOnRead {
		m.watcher.WatchAndDelete(path)
	}

	p.result <- promptOutcome{
		res: PromptResult{
			ID:        id,
			Path:      path,
			Handle:    id,
			ExpiresAt: p.deadline,
		},
	}
	if m.bus != nil {
		m.bus.Publish(Event{Type: "resolved", ID: id, Status: "submitted"})
	}
	if m.audit != nil {
		m.audit("submitted", p.req, id)
	}
	return nil
}

// Cancel resolves the prompt with ErrUserCancelled. The DB row is finalized
// even when there is no in-memory waiter (an orphaned pending row left behind
// by a daemon restart) so the UI can dismiss it; the result channel and audit
// hook only fire when a live waiter was present.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	m.mu.Lock()
	p, ok := m.pending[id]
	if ok {
		delete(m.pending, id)
	}
	m.mu.Unlock()

	now := time.Now().UTC()
	if err := m.store.CompleteSecretPrompt(ctx, id, "cancelled", "", now); err != nil {
		// No pending row to cancel — neither in memory nor in the store.
		if errors.Is(err, store.ErrNotFound) {
			return ErrPromptNotFound
		}
		return fmt.Errorf("complete prompt: %w", err)
	}
	if ok {
		p.result <- promptOutcome{err: ErrUserCancelled}
	}
	if m.bus != nil {
		m.bus.Publish(Event{Type: "resolved", ID: id, Status: "cancelled"})
	}
	if m.audit != nil && ok {
		m.audit("cancelled", p.req, id)
	}
	return nil
}

// timeoutPrompt finalizes a row that the Wait timer or the sweeper fired on.
// Best-effort — the row may already be terminal if the user resolved at the
// same instant. The DB row is transitioned to timeout even when there is no
// in-memory waiter (an orphaned pending row left behind by a daemon restart),
// which is exactly the case the sweeper must clean up; the result channel and
// audit hook only fire when a live waiter was present.
func (m *Manager) timeoutPrompt(ctx context.Context, id string) {
	m.mu.Lock()
	p, ok := m.pending[id]
	if ok {
		delete(m.pending, id)
	}
	m.mu.Unlock()

	now := time.Now().UTC()
	// CompleteSecretPrompt is a no-op (ErrNotFound) if the row is already
	// terminal; ignore that — this is best-effort finalization.
	_ = m.store.CompleteSecretPrompt(ctx, id, "timeout", "", now)
	if ok {
		// best-effort signal so any blocked Wait callers exit immediately.
		select {
		case p.result <- promptOutcome{err: ErrPromptTimeout}:
		default:
		}
	}
	if m.bus != nil {
		m.bus.Publish(Event{Type: "resolved", ID: id, Status: "timeout"})
	}
	if m.audit != nil && ok {
		m.audit("timeout", p.req, id)
	}
}
