package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeAuditor records every audit row so each test can assert exactly
// what landed on the ledger. Goroutine-safe — the mesh package emits
// audit rows on a background goroutine via auditAsync.
//
// ctxs captures the context passed to Record alongside the record. The
// mesh audit pipeline detaches to context.Background() before invoking
// the auditor — auditAsync MUST re-seed the correlation_id onto the
// background ctx so audit.Logger.Record's FromCtx() lookup still
// produces the originating request's id. Tests assert that by reading
// audit.FromCtx(ctxs[i]).
type fakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
	ctxs    []context.Context
}

func (f *fakeAuditor) Record(ctx context.Context, rec *store.AuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	f.ctxs = append(f.ctxs, ctx)
	return nil
}

// waitForCtx mirrors waitFor but returns the captured ctxs so tests can
// assert audit.FromCtx propagated across the background-ctx swap.
func (f *fakeAuditor) waitForCtx(t *testing.T, n int) []context.Context {
	t.Helper()
	_ = f.waitFor(t, n)
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]context.Context, len(f.ctxs))
	copy(out, f.ctxs)
	return out
}

// waitFor polls until the fake has at least `n` records or two seconds
// elapse. The mesh audit pipeline is fire-and-forget (auditAsync runs
// on a background goroutine), so synchronous tests need a short
// sleep-based wait — busy-spinning never yields and the writer never
// runs.
func (f *fakeAuditor) waitFor(t *testing.T, n int) []*store.AuditRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.records)
		f.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*store.AuditRecord, len(f.records))
	copy(out, f.records)
	return out
}

func decodeParams(t *testing.T, rec *store.AuditRecord) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.ParamsRedacted, &m); err != nil {
		t.Fatalf("decode params: %v / %s", err, rec.ParamsRedacted)
	}
	return m
}

func newMgrWithAudit(a Auditor) *Manager {
	m := &Manager{selfPeerID: "self-peer"}
	m.SetAuditor(a)
	return m
}

func TestRecordSkillOffer_FiresOnceWithExpectedParams(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	meta := SessionMeta{SessionID: "sess-1", ClientType: "claude-code"}

	m.RecordSkillOffer(context.Background(), meta, "peer-xyz", "demo-skill", "1.2.3", "success", "")

	recs := a.waitFor(t, 1)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r.ToolName != "mesh__offer_skill" {
		t.Errorf("ToolName = %q", r.ToolName)
	}
	if r.Status != "success" {
		t.Errorf("Status = %q", r.Status)
	}
	if r.ClientType != "claude-code" {
		t.Errorf("ClientType = %q", r.ClientType)
	}
	if r.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", r.SessionID)
	}
	p := decodeParams(t, r)
	if p["recipient_peer_id"] != "peer-xyz" || p["skill_name"] != "demo-skill" || p["skill_version"] != "1.2.3" {
		t.Errorf("params = %v", p)
	}
}

func TestRecordRequestSkill_ErrorStatus(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)

	m.RecordRequestSkill(context.Background(), SessionMeta{SessionID: "sess-2"},
		"peer-abc", "another-skill", "", "error", "denied by peer")

	recs := a.waitFor(t, 1)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r.ToolName != "mesh__request_skill" {
		t.Errorf("ToolName = %q", r.ToolName)
	}
	if r.Status != "error" {
		t.Errorf("Status = %q", r.Status)
	}
	if r.ErrorMessage != "denied by peer" {
		t.Errorf("ErrorMessage = %q", r.ErrorMessage)
	}
	p := decodeParams(t, r)
	if p["requested_from"] != "peer-abc" || p["skill_name"] != "another-skill" {
		t.Errorf("params = %v", p)
	}
	if _, present := p["skill_version"]; present {
		t.Errorf("empty version should not appear in params, got %v", p)
	}
}

func TestRecordGrantPeerScope_CapturesGranter(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	meta := SessionMeta{SessionID: "granter-sess"}

	m.RecordGrantPeerScope(context.Background(), meta, "peer-99", "mesh.skill_request", "success", "")

	recs := a.waitFor(t, 1)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	p := decodeParams(t, recs[0])
	if p["peer_id"] != "peer-99" || p["scope"] != "mesh.skill_request" {
		t.Errorf("params = %v", p)
	}
	if p["granted_by"] != "granter-sess" {
		t.Errorf("granted_by = %v; want session id", p["granted_by"])
	}
}

func TestRecordRevokePeerScope_FallsBackToPeerIDWhenSessionMissing(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)

	m.RecordRevokePeerScope(context.Background(), SessionMeta{}, "peer-99", "mesh.skill_request", "success", "")

	recs := a.waitFor(t, 1)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	r := recs[0]
	if r.SessionID != "mesh:self-peer" {
		t.Errorf("SessionID fallback = %q, want mesh:self-peer", r.SessionID)
	}
	if r.ClientType != "mesh" {
		t.Errorf("ClientType fallback = %q", r.ClientType)
	}
	p := decodeParams(t, r)
	if p["revoked_by"] != "mesh:self-peer" {
		t.Errorf("revoked_by = %v", p["revoked_by"])
	}
}

func TestRecordSetAgentStatus_TransitionFields(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	meta := SessionMeta{SessionID: "agent-1"}

	m.RecordSetAgentStatus(context.Background(), meta, "agent-1", "idle", "running tests", "success", "")

	recs := a.waitFor(t, 1)
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	p := decodeParams(t, recs[0])
	if p["agent_id"] != "agent-1" {
		t.Errorf("agent_id = %v", p["agent_id"])
	}
	if p["old_status"] != "idle" || p["new_status"] != "running tests" {
		t.Errorf("status transition fields wrong: %v", p)
	}
}

func TestRecordMeshAudit_NilAuditorIsNoop(t *testing.T) {
	t.Parallel()
	// Manager with no auditor — every recorder must early-out without
	// panicking and without recording anything anywhere.
	m := &Manager{selfPeerID: "self"}
	ctx := context.Background()
	meta := SessionMeta{SessionID: "x"}

	// Each call should return cleanly even though there's nothing
	// listening. No assertion needed beyond "no panic".
	m.RecordSkillOffer(ctx, meta, "p", "s", "v", "success", "")
	m.RecordRequestSkill(ctx, meta, "p", "s", "v", "success", "")
	m.RecordGrantPeerScope(ctx, meta, "p", "s", "success", "")
	m.RecordRevokePeerScope(ctx, meta, "p", "s", "success", "")
	m.RecordSetAgentStatus(ctx, meta, "a", "old", "new", "success", "")

	// Also verify a nil receiver doesn't panic — defense for the rare
	// path where mesh is disabled entirely.
	var nilMgr *Manager
	nilMgr.RecordSkillOffer(ctx, meta, "p", "s", "v", "success", "")
	nilMgr.RecordGrantPeerScope(ctx, meta, "p", "s", "success", "")
}

func TestRecordMeshAudit_TableDrivenEventNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		emit      func(*Manager, context.Context, SessionMeta)
		wantEvent string
		wantField string
	}{
		{
			"skill_offer", func(m *Manager, c context.Context, s SessionMeta) {
				m.RecordSkillOffer(c, s, "p", "sk", "", "success", "")
			}, "mesh__offer_skill", "recipient_peer_id",
		},
		{
			"request_skill", func(m *Manager, c context.Context, s SessionMeta) {
				m.RecordRequestSkill(c, s, "p", "sk", "", "success", "")
			}, "mesh__request_skill", "requested_from",
		},
		{
			"grant_peer_scope", func(m *Manager, c context.Context, s SessionMeta) {
				m.RecordGrantPeerScope(c, s, "p", "sc", "success", "")
			}, "mesh__grant_peer_scope", "granted_by",
		},
		{
			"revoke_peer_scope", func(m *Manager, c context.Context, s SessionMeta) {
				m.RecordRevokePeerScope(c, s, "p", "sc", "success", "")
			}, "mesh__revoke_peer_scope", "revoked_by",
		},
		{
			"set_agent_status", func(m *Manager, c context.Context, s SessionMeta) {
				m.RecordSetAgentStatus(c, s, "a", "o", "n", "success", "")
			}, "mesh__set_agent_status", "new_status",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &fakeAuditor{}
			m := newMgrWithAudit(a)
			tc.emit(m, context.Background(), SessionMeta{SessionID: "s"})
			recs := a.waitFor(t, 1)
			if len(recs) != 1 {
				t.Fatalf("want 1 record, got %d (names=%v)", len(recs), names(recs))
			}
			if recs[0].ToolName != tc.wantEvent {
				t.Errorf("ToolName = %q, want %q", recs[0].ToolName, tc.wantEvent)
			}
			p := decodeParams(t, recs[0])
			if _, ok := p[tc.wantField]; !ok {
				t.Errorf("params missing %q field: %v", tc.wantField, p)
			}
			// Records must not leak the literal "secret" sentinel — basic
			// sanity that no test inputs collide with future redaction logic.
			if strings.Contains(string(recs[0].ParamsRedacted), "SECRET") {
				t.Errorf("unexpected SECRET token in params: %s", recs[0].ParamsRedacted)
			}
		})
	}
}

// TestMeshAudit_CorrelationIDSurvivesBackgroundCtxSwap is the regression
// guard for the C1 fix: auditAsync detaches to context.Background()
// before calling Record, so the correlation_id seeded on the request
// ctx by upstream handlers MUST be re-attached to the background ctx —
// otherwise audit.Logger.Record's FromCtx() lookup returns "" and every
// mesh audit row lands with correlation_id="", breaking forensics
// joins.
//
// We assert by reading audit.FromCtx on the ctx the fake auditor saw,
// rather than rec.CorrelationID, because the fake bypasses the
// Logger.Record auto-stamp path — the contract under test is "the ctx
// the goroutine hands to the auditor still carries the id".
func TestMeshAudit_CorrelationIDSurvivesBackgroundCtxSwap(t *testing.T) {
	t.Parallel()
	const wantID = "test-correlation-123"

	cases := []struct {
		name string
		emit func(*Manager, context.Context, SessionMeta)
	}{
		{"skill_offer", func(m *Manager, c context.Context, s SessionMeta) {
			m.RecordSkillOffer(c, s, "p", "sk", "1", "success", "")
		}},
		{"request_skill", func(m *Manager, c context.Context, s SessionMeta) {
			m.RecordRequestSkill(c, s, "p", "sk", "1", "success", "")
		}},
		{"grant_peer_scope", func(m *Manager, c context.Context, s SessionMeta) {
			m.RecordGrantPeerScope(c, s, "p", "sc", "success", "")
		}},
		{"revoke_peer_scope", func(m *Manager, c context.Context, s SessionMeta) {
			m.RecordRevokePeerScope(c, s, "p", "sc", "success", "")
		}},
		{"set_agent_status", func(m *Manager, c context.Context, s SessionMeta) {
			m.RecordSetAgentStatus(c, s, "a", "old", "new", "success", "")
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &fakeAuditor{}
			m := newMgrWithAudit(a)
			ctx := audit.WithCorrelation(context.Background(), wantID)
			tc.emit(m, ctx, SessionMeta{SessionID: "s"})

			ctxs := a.waitForCtx(t, 1)
			if len(ctxs) != 1 {
				t.Fatalf("want 1 ctx, got %d", len(ctxs))
			}
			if got := audit.FromCtx(ctxs[0]); got != wantID {
				t.Errorf("FromCtx on auditor ctx = %q, want %q (the background-ctx swap dropped the correlation id)", got, wantID)
			}
		})
	}
}

// TestMeshAudit_CorrelationIDEmptyCtxStaysEmpty confirms the no-id path
// is a clean no-op — auditAsync must not invent an id when the ctx
// carries none.
func TestMeshAudit_CorrelationIDEmptyCtxStaysEmpty(t *testing.T) {
	t.Parallel()
	a := &fakeAuditor{}
	m := newMgrWithAudit(a)
	m.RecordSkillOffer(context.Background(), SessionMeta{SessionID: "s"}, "p", "sk", "", "success", "")

	ctxs := a.waitForCtx(t, 1)
	if len(ctxs) != 1 {
		t.Fatalf("want 1 ctx, got %d", len(ctxs))
	}
	if got := audit.FromCtx(ctxs[0]); got != "" {
		t.Errorf("FromCtx on auditor ctx = %q, want \"\" for un-seeded ctx", got)
	}
}

func names(recs []*store.AuditRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ToolName)
	}
	return out
}

// panickingAuditor panics inside Record so we can prove the mesh audit
// goroutine recovers and doesn't take the daemon down with it. A real
// production analogue: a nil deref in a downstream store, a JSON
// marshal blow-up, a future code-mode handler bug.
type panickingAuditor struct {
	called atomic.Int32
}

func (p *panickingAuditor) Record(_ context.Context, _ *store.AuditRecord) error {
	p.called.Add(1)
	panic("simulated downstream blow-up")
}

// TestAuditAsync_RecoversFromPanic asserts the recover() inside the
// audit goroutine catches a panicking auditor and the parent goroutine
// (this test) survives. Without recover the runtime would tear down
// the whole process; running this test under `go test` would crash and
// report a panic — observing a clean pass IS the assertion.
//
// We also intercept the slog output to verify "mesh audit goroutine
// panicked" lands in the log with the captured event name so operators
// have a forensics breadcrumb.
func TestAuditAsync_RecoversFromPanic(t *testing.T) {
	// Don't t.Parallel() — we replace the default slog handler with a
	// buffered one to capture the panic line, and running in parallel
	// would race against other tests using slog.
	//
	// The buffer is wrapped behind a mutex because the slog write happens
	// on the audit goroutine while this test reads from the main one —
	// race detector flagged the unsynchronised access otherwise.
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	pa := &panickingAuditor{}
	m := newMgrWithAudit(pa)

	// Emit a recordable event — the panic happens on the audit goroutine,
	// not on this goroutine. The call MUST return cleanly.
	m.RecordSkillOffer(context.Background(), SessionMeta{SessionID: "sess-panic"},
		"peer", "skill", "1.0", "success", "")

	// Poll until the auditor was actually invoked (and thus panicked).
	deadline := time.Now().Add(2 * time.Second)
	for pa.called.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pa.called.Load() != 1 {
		t.Fatalf("auditor Record not invoked; calls=%d", pa.called.Load())
	}
	// Give the deferred recover() + slog.Error a beat to run.
	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "mesh audit goroutine panicked") &&
		time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// One additional yield so the recover goroutine's slog.Error call
	// fully returns before the t.Cleanup swap restores slog.Default —
	// otherwise the audit goroutine's slog.Default() read races with
	// SetDefault. Slog.Error is synchronous, but Go's scheduler may not
	// have unblocked us yet.
	time.Sleep(20 * time.Millisecond)
	got := buf.String()
	if !strings.Contains(got, "mesh audit goroutine panicked") {
		t.Errorf("recover log missing; slog output=%q", got)
	}
	if !strings.Contains(got, "mesh__offer_skill") {
		t.Errorf("event name not captured in panic log; slog output=%q", got)
	}
	if !strings.Contains(got, "simulated downstream blow-up") {
		t.Errorf("panic value not included in slog; output=%q", got)
	}

	// Sanity: parent goroutine still runs. If recover() didn't catch,
	// the test process would already be dead.
	if t.Failed() {
		return
	}
}

// syncBuffer wraps bytes.Buffer with a mutex so it's safe to use as a
// slog target whose write and read happen on different goroutines
// (the audit pipeline writes from its async goroutine; the test reads
// on the main one). Race detector flagged the bare bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// String returns the current contents under the lock.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
