// handler_memory_global_guard_test.go — coverage for the provenance guard on
// destructive/mutating ops against GLOBAL (NULL-workspace) memory rows.
//
// The bug: requireMemoryEntryAccess returned nil for any global row, so a
// low-trust worker on the tool allowlist could forget/invalidate/pin/unpin a
// global memory it never authored (forget drops the vec row — destructive).
//
// The fix scopes the decision to provenance: ALLOW same-origin sessions and
// trusted in-process (non-worker) sessions; DENY a worker context that did not
// originate the row. Reads (get/offer) stay open and are out of scope here.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// globalForgetCase drives one row through memory__forget under a chosen
// session/worker context and asserts whether the destructive op is allowed.
type globalForgetCase struct {
	name string
	// authorSession is the SourceSessionID stamped on the seeded global row.
	authorSession string
	// callerSession is the session id bound on the handler at call time.
	callerSession string
	// worker, when non-empty, attaches a worker context for that workspace
	// grant (write access) — simulating a bounded worker on the allowlist.
	worker string
	// inProcess, when true, marks ctx as a trusted in-process worker dispatch
	// (WithInProcessWorkerCall) — the consolidator's path.
	inProcess bool
	// wantForgotten asserts the row was actually deleted (op allowed).
	wantForgotten bool
	// wantDeniedMsg, when non-empty, asserts the RPC error mentions it.
	wantDeniedMsg string
}

func TestMemoryGlobalGuard_Forget(t *testing.T) {
	cases := []globalForgetCase{
		{
			// (a) A worker whose session did NOT author the global row cannot
			// forget it — author session differs from the caller session, so
			// same-origin does not fire and the worker context denies.
			name:          "worker_cannot_forget_foreign_global",
			authorSession: "sess-author",
			callerSession: "sess-worker", // different from author → deny
			worker:        "ws-worker",
			wantForgotten: false,
			wantDeniedMsg: "worker cannot",
		},
		{
			// (b) The session that authored the row can forget it, even with a
			// worker context attached — same-origin always wins.
			name:          "same_origin_worker_can_forget",
			authorSession: "sess-author",
			callerSession: "sess-author",
			worker:        "ws-worker",
			wantForgotten: true,
		},
		{
			// (b') Same-origin without any worker context can forget.
			name:          "same_origin_inprocess_can_forget",
			authorSession: "sess-author",
			callerSession: "sess-author",
			worker:        "",
			wantForgotten: true,
		},
		{
			// (c) A non-worker in-process session (the consolidator) can forget
			// a global row it did not author — no worker context present.
			name:          "nonworker_inprocess_can_forget_foreign",
			authorSession: "sess-other",
			callerSession: "sess-consolidator",
			worker:        "",
			wantForgotten: true,
		},
		{
			// (d) The memory consolidator runs AS an in-process worker: it
			// carries the trusted in-process marker AND a worker workspace
			// grant, has no session (so same-origin can never match), and its
			// job is invalidating/pruning foreign-authored global rows. The
			// in-process marker must override the worker-context denial.
			name:          "inprocess_worker_consolidator_can_forget_foreign",
			authorSession: "sess-author",
			callerSession: "", // nil/empty session — consolidator has no MCP init
			worker:        "ws-worker",
			inProcess:     true,
			wantForgotten: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, svc := newHandlerWithMemoryDB(t)
			// Bind a session id so same-origin comparisons are meaningful.
			// An empty callerSession models the consolidator (no MCP init,
			// nil/empty session) so same-origin can never match.
			if tc.callerSession != "" {
				h.sessions.session = &store.Session{ID: tc.callerSession}
			} else {
				h.sessions.session = &store.Session{ID: ""}
			}

			// Seed a GLOBAL (wsID="") row stamped with the author session.
			id := seedMemoryFromSource(t, svc, "global-secret", "", tc.authorSession)

			callCtx := ctx
			if tc.worker != "" {
				callCtx = WithWorkerWorkspaceAccess(ctx, tc.worker,
					[]WorkerWorkspaceGrant{
						{WorkspaceID: tc.worker, Access: store.WorkerWorkspaceAccessWrite},
					})
			}
			// The consolidator's dispatch composes the in-process trusted
			// marker on top of any worker workspace access.
			if tc.inProcess {
				callCtx = WithInProcessWorkerCall(callCtx)
			}

			raw := json.RawMessage(`{"id":"` + id + `"}`)
			resp, rpcErr, handled := h.dispatchMemoryTool(callCtx, "memory__forget", raw)
			if !handled {
				t.Fatal("memory__forget not handled")
			}

			_, getErr := svc.Get(ctx, id)
			gone := errors.Is(getErr, store.ErrNotFound)

			if tc.wantForgotten {
				if rpcErr != nil {
					t.Fatalf("expected forget to succeed, got RPC error: %v", rpcErr)
				}
				if !gone {
					t.Fatalf("expected row %s to be forgotten; getErr=%v resp=%s",
						id, getErr, string(resp))
				}
				return
			}

			// Denied path: the row must survive and the error must explain why.
			if rpcErr == nil {
				t.Fatalf("expected forget to be denied; resp=%s", string(resp))
			}
			if tc.wantDeniedMsg != "" &&
				!strings.Contains(rpcErr.Message, tc.wantDeniedMsg) {
				t.Fatalf("expected denial mentioning %q, got %q",
					tc.wantDeniedMsg, rpcErr.Message)
			}
			if gone {
				t.Fatalf("denied forget still deleted row %s", id)
			}
			if getErr != nil {
				t.Fatalf("expected surviving row to be readable, got %v", getErr)
			}
		})
	}
}

// TestMemoryGlobalGuard_OtherMutatingOps confirms the same guard fires for
// invalidate / pin / unpin (all route through requireMemoryEntryAccess with
// write=true) when an EXTERNAL worker context is present — a worker workspace
// grant WITHOUT the trusted in-process marker. The DENIED caller here is an
// external bounded worker, NOT the in-process consolidator (which carries the
// in-process marker and is exercised in TestMemoryGlobalGuard_Forget case (d)
// + TestMemoryGlobalGuard_InProcessWorkerAllowed below).
func TestMemoryGlobalGuard_OtherMutatingOps(t *testing.T) {
	ops := []struct {
		tool string
	}{
		{"memory__invalidate"},
		{"memory__pin"},
		{"memory__unpin"},
	}

	for _, op := range ops {
		t.Run(op.tool, func(t *testing.T) {
			ctx := context.Background()
			h, svc := newHandlerWithMemoryDB(t)
			h.sessions.session = &store.Session{ID: "sess-caller"}
			id := seedMemoryFromSource(t, svc, "global-protected", "", "sess-author")

			// EXTERNAL worker: worker workspace access present, but NO trusted
			// in-process marker — so the guard must deny.
			workerCtx := WithWorkerWorkspaceAccess(ctx, "ws-worker",
				[]WorkerWorkspaceGrant{
					{WorkspaceID: "ws-worker", Access: store.WorkerWorkspaceAccessWrite},
				})

			raw := json.RawMessage(`{"id":"` + id + `"}`)
			_, rpcErr, handled := h.dispatchMemoryTool(workerCtx, op.tool, raw)
			if !handled {
				t.Fatalf("%s not handled", op.tool)
			}
			if rpcErr == nil {
				t.Fatalf("%s: expected EXTERNAL worker to be denied on a global row",
					op.tool)
			}
			if !strings.Contains(rpcErr.Message, "global memories") {
				t.Fatalf("%s: expected global-guard denial, got %q",
					op.tool, rpcErr.Message)
			}

			// Row must still exist and remain valid (not invalidated).
			entry, err := svc.Get(ctx, id)
			if err != nil {
				t.Fatalf("%s: expected protected global row to survive: %v",
					op.tool, err)
			}
			if op.tool == "memory__invalidate" && entry.TValidEnd != nil {
				t.Fatalf("memory__invalidate: denied op still invalidated the row")
			}
		})
	}
}

// TestMemoryGlobalGuard_InProcessWorkerAllowed proves the consolidator path
// WORKS: a context carrying the trusted in-process marker (composed on top of
// a worker workspace grant, exactly as the consolidator's dispatch does), with
// a nil/empty handler session and a row authored by a DIFFERENT session, is
// ALLOWED to mutate foreign-authored GLOBAL rows. This is the core
// consolidator job (Pass-1 invalidation/pruning of foreign global rows) that
// must not be blocked.
func TestMemoryGlobalGuard_InProcessWorkerAllowed(t *testing.T) {
	t.Run("invalidate", func(t *testing.T) {
		ctx := context.Background()
		h, svc := newHandlerWithMemoryDB(t)
		// Consolidator has no MCP init — nil/empty session.
		h.sessions.session = &store.Session{ID: ""}
		id := seedMemoryFromSource(t, svc, "global-foreign", "", "sess-author")

		// In-process trusted marker composed with worker workspace access.
		callCtx := WithInProcessWorkerCall(WithWorkerWorkspaceAccess(ctx, "ws-worker",
			[]WorkerWorkspaceGrant{
				{WorkspaceID: "ws-worker", Access: store.WorkerWorkspaceAccessWrite},
			}))

		raw := json.RawMessage(`{"id":"` + id + `"}`)
		_, rpcErr, handled := h.dispatchMemoryTool(callCtx, "memory__invalidate", raw)
		if !handled {
			t.Fatal("memory__invalidate not handled")
		}
		if rpcErr != nil {
			t.Fatalf("consolidator (in-process worker) invalidate denied: %v", rpcErr)
		}
		entry, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatalf("row should still be readable after invalidate: %v", err)
		}
		if entry.TValidEnd == nil {
			t.Fatal("memory__invalidate by consolidator did not invalidate the row")
		}
	})

	t.Run("forget", func(t *testing.T) {
		ctx := context.Background()
		h, svc := newHandlerWithMemoryDB(t)
		h.sessions.session = &store.Session{ID: ""}
		id := seedMemoryFromSource(t, svc, "global-foreign", "", "sess-author")

		callCtx := WithInProcessWorkerCall(WithWorkerWorkspaceAccess(ctx, "ws-worker",
			[]WorkerWorkspaceGrant{
				{WorkspaceID: "ws-worker", Access: store.WorkerWorkspaceAccessWrite},
			}))

		raw := json.RawMessage(`{"id":"` + id + `"}`)
		_, rpcErr, handled := h.dispatchMemoryTool(callCtx, "memory__forget", raw)
		if !handled {
			t.Fatal("memory__forget not handled")
		}
		if rpcErr != nil {
			t.Fatalf("consolidator (in-process worker) forget denied: %v", rpcErr)
		}
		if _, getErr := svc.Get(ctx, id); !errors.Is(getErr, store.ErrNotFound) {
			t.Fatalf("consolidator forget should delete row; getErr=%v", getErr)
		}
	})
}

// TestMemoryGlobalGuard_ReadStaysOpen confirms get is NOT gated by the new
// guard — reads/exfiltration are out of scope for this fix.
func TestMemoryGlobalGuard_ReadStaysOpen(t *testing.T) {
	ctx := context.Background()
	h, svc := newHandlerWithMemoryDB(t)
	h.sessions.session = &store.Session{ID: "sess-caller"}
	id := seedMemoryFromSource(t, svc, "global-readable", "", "sess-author")

	workerCtx := WithWorkerWorkspaceAccess(ctx, "ws-worker",
		[]WorkerWorkspaceGrant{
			{WorkspaceID: "ws-worker", Access: store.WorkerWorkspaceAccessWrite},
		})

	raw := json.RawMessage(`{"id":"` + id + `"}`)
	resp, rpcErr, handled := h.dispatchMemoryTool(workerCtx, "memory__get", raw)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__get: handled=%v rpcErr=%v", handled, rpcErr)
	}
	if !strings.Contains(string(resp), "global-readable") {
		t.Fatalf("worker should still be able to read a global row; got %s",
			string(resp))
	}
}
