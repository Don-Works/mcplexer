package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestManager_NilAuditorIsNoOp confirms that an unset auditor causes no
// panic on any code path — the Manager must remain fully functional
// when audit isn't wired.
func TestManager_NilAuditorIsNoOp(t *testing.T) {
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	st := &fakeScopeStore{scope: &store.AuthScope{ID: "scope-abc", Name: "x"}}
	mgr := NewManager(st, enc) // no SetAuditor

	ctx := context.Background()
	if err := mgr.Put(ctx, "scope-abc", "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := mgr.Get(ctx, "scope-abc", "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := mgr.List(ctx, "scope-abc"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := mgr.Delete(ctx, "scope-abc", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestManager_AuditFiresOnStoreFailure confirms that when the underlying
// store errors, the audit row STILL goes out with status="error" and the
// error message attached. This is the "tamper / DB failure" forensics
// trail — silent suppression would let an attacker who can break the
// store also break the audit trail.
func TestManager_AuditFiresOnStoreFailure(t *testing.T) {
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	boom := errors.New("disk on fire")
	cases := []struct {
		name  string
		setup func(*fakeScopeStore)
		op    func(*Manager) error
		event string
	}{
		{
			name: "Put on update failure",
			setup: func(s *fakeScopeStore) {
				s.scope = &store.AuthScope{ID: "scope-abc"}
				s.updateErr = boom
			},
			op: func(m *Manager) error {
				return m.Put(context.Background(), "scope-abc", "k", []byte("v"))
			},
			event: "secret.write",
		},
		{
			name: "Get on scope fetch failure",
			setup: func(s *fakeScopeStore) {
				s.getErr = boom
			},
			op: func(m *Manager) error {
				_, err := m.Get(context.Background(), "scope-abc", "k")
				return err
			},
			event: "secret.read",
		},
		{
			name: "Delete on scope fetch failure",
			setup: func(s *fakeScopeStore) {
				s.getErr = boom
			},
			op: func(m *Manager) error {
				return m.Delete(context.Background(), "scope-abc", "k")
			},
			event: "secret.delete",
		},
		{
			name: "List on scope fetch failure",
			setup: func(s *fakeScopeStore) {
				s.getErr = boom
			},
			op: func(m *Manager) error {
				_, err := m.List(context.Background(), "scope-abc")
				return err
			},
			event: "secret.list",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeScopeStore{}
			tc.setup(s)
			aud := &fakeAuditor{}
			mgr := NewManager(s, enc)
			mgr.SetAuditor(aud)

			if err := tc.op(mgr); err == nil {
				t.Fatal("expected error, got nil")
			}

			recs := aud.snapshot()
			if len(recs) != 1 {
				t.Fatalf("got %d records, want 1", len(recs))
			}
			r := recs[0]
			if r.ToolName != tc.event {
				t.Errorf("ToolName = %q, want %q", r.ToolName, tc.event)
			}
			if r.Status != "error" {
				t.Errorf("Status = %q, want error", r.Status)
			}
			if r.ErrorMessage == "" {
				t.Error("ErrorMessage is empty; want propagated cause")
			}
		})
	}
}

// TestManager_NewManagerWithAuditor verifies the convenience constructor
// wires audit emission from the first call.
func TestManager_NewManagerWithAuditor(t *testing.T) {
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	s := &fakeScopeStore{scope: &store.AuthScope{ID: "scope-abc"}}
	aud := &fakeAuditor{}
	mgr := NewManagerWithAuditor(s, enc, aud)

	if err := mgr.Put(context.Background(), "scope-abc", "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := len(aud.snapshot()); got != 1 {
		t.Errorf("len(records) = %d, want 1 — auditor was not wired", got)
	}
}

// TestManager_AuditorRecordErrorIsSwallowed asserts that a failing
// Auditor.Record call does NOT propagate back to the caller — audit
// failures must NEVER break the user's secret access path.
func TestManager_AuditorRecordErrorIsSwallowed(t *testing.T) {
	enc, err := NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	s := &fakeScopeStore{scope: &store.AuthScope{ID: "scope-abc"}}
	aud := &fakeAuditor{failErr: errors.New("audit DB exploded")}
	mgr := NewManager(s, enc)
	mgr.SetAuditor(aud)

	if err := mgr.Put(context.Background(), "scope-abc", "k", []byte("v")); err != nil {
		t.Fatalf("Put leaked audit failure: %v", err)
	}
}
