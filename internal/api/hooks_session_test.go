package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeMemoryRecaller returns canned memory rows (or an error) so the
// session hook's SessionStart digest can be exercised without a real
// store. lastFilter records the filter the handler built so tests can
// assert workspace scoping.
type fakeMemoryRecaller struct {
	rows       []store.MemoryEntry
	err        error
	called     bool
	lastFilter store.MemoryFilter
}

func (f *fakeMemoryRecaller) ListMemories(_ context.Context, filter store.MemoryFilter) ([]store.MemoryEntry, error) {
	f.called = true
	f.lastFilter = filter
	return f.rows, f.err
}

func buildSessionReq(t *testing.T, body any) *http.Request {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/session", strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeSessionResp(t *testing.T, rr *httptest.ResponseRecorder) SessionHookResponse {
	t.Helper()
	var resp SessionHookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%q)", err, rr.Body.String())
	}
	return resp
}

// TestSessionHookStartInjectsRecallNudge pins the load-bearing behaviour:
// SessionStart returns an additionalContext that tells the agent to recall
// memory BEFORE acting, echoes the event name, and surfaces a systemMessage.
func TestSessionHookStartInjectsRecallNudge(t *testing.T) {
	h := &hooksHandler{}
	body := SessionHookRequest{
		SessionID:     "sess-1",
		HookEventName: "SessionStart",
		Source:        "startup",
		CWD:           "/Users/me/project",
	}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeSessionResp(t, rr)
	if resp.HookSpecificOutput == nil {
		t.Fatal("expected hookSpecificOutput on SessionStart")
	}
	if resp.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName: got %q want SessionStart", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "RECALL BEFORE ACTING") {
		t.Errorf("additionalContext should carry the recall nudge, got %q", ctx)
	}
	if !strings.Contains(ctx, "memory.recall") {
		t.Errorf("additionalContext should name memory.recall, got %q", ctx)
	}
	if resp.SystemMessage == "" {
		t.Error("expected a systemMessage on SessionStart")
	}
}

// TestSessionHookStartInlinesMemoryDigest verifies that when a recaller is
// wired and returns rows, the SessionStart context inlines a digest of
// those memories as a head-start.
func TestSessionHookStartInlinesMemoryDigest(t *testing.T) {
	mem := &fakeMemoryRecaller{
		rows: []store.MemoryEntry{
			{Name: "deploy-rule", Content: "Never deploy a dirty tree"},
			{Name: "db-lockdown", Content: "~/.mcplexer is off-limits"},
		},
	}
	h := &hooksHandler{memories: mem}
	body := SessionHookRequest{HookEventName: "SessionStart", Source: "startup", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))

	resp := decodeSessionResp(t, rr)
	if !mem.called {
		t.Fatal("expected ListMemories to be called for the digest")
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "deploy-rule") {
		t.Errorf("digest should inline the memory name, got %q", ctx)
	}
	if !strings.Contains(ctx, "Never deploy a dirty tree") {
		t.Errorf("digest should inline the memory content, got %q", ctx)
	}
	if mem.lastFilter.Limit != sessionMemoryDigestLimit {
		t.Errorf("digest limit: got %d want %d", mem.lastFilter.Limit, sessionMemoryDigestLimit)
	}
}

// TestSessionHookStartSourceGatesDigest pins the source-gating behaviour:
// only "startup" / "clear" inject the full RECALL nudge + memory digest;
// "resume" / "compact" (and other non-startup sources) inject only a terse
// one-liner and NEVER re-inject the digest that compaction just discarded.
func TestSessionHookStartSourceGatesDigest(t *testing.T) {
	tests := []struct {
		source      string
		wantDigest  bool
		wantFullCue bool // the imperative "RECALL BEFORE ACTING" nudge
	}{
		{"startup", true, true},
		{"clear", true, true},
		{"resume", false, false},
		{"compact", false, false},
		{"", false, false}, // unknown/empty source → terse, no digest
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			mem := &fakeMemoryRecaller{
				rows: []store.MemoryEntry{
					{Name: "deploy-rule", Content: "Never deploy a dirty tree"},
				},
			}
			h := &hooksHandler{memories: mem}
			body := SessionHookRequest{HookEventName: "SessionStart", Source: tc.source, CWD: "/p"}
			rr := httptest.NewRecorder()
			h.session(rr, buildSessionReq(t, body))

			resp := decodeSessionResp(t, rr)
			ctx := resp.HookSpecificOutput.AdditionalContext
			hasDigest := strings.Contains(ctx, "deploy-rule") ||
				strings.Contains(ctx, "Recent memories")
			if hasDigest != tc.wantDigest {
				t.Errorf("source=%q: hasDigest=%v want %v (ctx=%q)",
					tc.source, hasDigest, tc.wantDigest, ctx)
			}
			hasFullCue := strings.Contains(ctx, "RECALL BEFORE ACTING")
			if hasFullCue != tc.wantFullCue {
				t.Errorf("source=%q: full nudge=%v want %v (ctx=%q)",
					tc.source, hasFullCue, tc.wantFullCue, ctx)
			}
			if !tc.wantDigest && mem.called {
				t.Errorf("source=%q: recaller must NOT be called when no digest is injected",
					tc.source)
			}
			// A terse recall reminder must still ship on every start.
			if !strings.Contains(strings.ToLower(ctx), "memory.recall") {
				t.Errorf("source=%q: every start must keep a recall reminder, got %q",
					tc.source, ctx)
			}
		})
	}
}

// TestSessionHookStartRecallerErrorDegrades proves a broken recaller never
// breaks the session: the nudge still ships, the digest is just absent.
func TestSessionHookStartRecallerErrorDegrades(t *testing.T) {
	mem := &fakeMemoryRecaller{err: context.DeadlineExceeded}
	h := &hooksHandler{memories: mem}
	body := SessionHookRequest{HookEventName: "SessionStart", Source: "startup", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	resp := decodeSessionResp(t, rr)
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "RECALL BEFORE ACTING") {
		t.Error("nudge must still ship when the recaller errors")
	}
	if strings.Contains(resp.HookSpecificOutput.AdditionalContext, "Recent memories") {
		t.Error("digest header must be absent on recaller error")
	}
}

// TestSessionHookStartNilRecallerOK confirms a nil recaller (digest
// disabled) still ships the recall nudge without panicking.
func TestSessionHookStartNilRecallerOK(t *testing.T) {
	h := &hooksHandler{memories: nil}
	body := SessionHookRequest{HookEventName: "SessionStart", Source: "startup", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	resp := decodeSessionResp(t, rr)
	if !strings.Contains(resp.HookSpecificOutput.AdditionalContext, "RECALL BEFORE ACTING") {
		t.Error("nudge must ship with a nil recaller")
	}
}

// TestSessionHookEndInjectsCaptureNudge pins the SessionEnd behaviour: the
// capture nudge ships so durable knowledge gets saved before context is lost.
func TestSessionHookEndInjectsCaptureNudge(t *testing.T) {
	h := &hooksHandler{}
	body := SessionHookRequest{HookEventName: "SessionEnd", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	resp := decodeSessionResp(t, rr)
	// SessionEnd cannot inject context (Claude Code rejects
	// hookSpecificOutput/additionalContext there); the nudge rides
	// systemMessage, the only channel SessionEnd has.
	if resp.HookSpecificOutput != nil {
		t.Fatalf("SessionEnd must NOT set hookSpecificOutput (Claude Code rejects it): %+v", resp.HookSpecificOutput)
	}
	if !strings.Contains(resp.SystemMessage, "CAPTURE BEFORE ENDING") {
		t.Errorf("SessionEnd should carry the capture nudge in systemMessage, got %q", resp.SystemMessage)
	}
	if !strings.Contains(resp.SystemMessage, "memory.save") {
		t.Errorf("SessionEnd should name memory.save, got %q", resp.SystemMessage)
	}
}

// TestSessionHookStopDoesNotFloodCaptureNudge is the HIGH-2 regression: Stop
// is a PER-TURN event, so firing it repeatedly must NOT inject the capture
// nudge each turn. We fire Stop several times and assert the capture nudge is
// emitted at most once (here: never), then confirm a single SessionEnd still
// emits it. Without the fix, every Stop carried "CAPTURE BEFORE ENDING".
func TestSessionHookStopDoesNotFloodCaptureNudge(t *testing.T) {
	h := &hooksHandler{}
	nudges := 0
	for i := 0; i < 5; i++ {
		body := SessionHookRequest{SessionID: "sess-stop", HookEventName: "Stop", CWD: "/p"}
		rr := httptest.NewRecorder()
		h.session(rr, buildSessionReq(t, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("Stop #%d status: want 200, got %d", i, rr.Code)
		}
		resp := decodeSessionResp(t, rr)
		// Stop must be a no-op for the nudge: empty hookSpecificOutput.
		if resp.HookSpecificOutput != nil &&
			strings.Contains(resp.HookSpecificOutput.AdditionalContext, "CAPTURE BEFORE ENDING") {
			nudges++
		}
	}
	if nudges > 1 {
		t.Fatalf("Stop flooded the capture nudge: emitted %d times across 5 turns (want <=1)", nudges)
	}
	if nudges != 0 {
		t.Errorf("Stop should emit NO capture nudge, emitted %d", nudges)
	}

	// SessionEnd still emits the capture nudge exactly once — via
	// systemMessage, since SessionEnd cannot inject context.
	endBody := SessionHookRequest{SessionID: "sess-stop", HookEventName: "SessionEnd", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, endBody))
	resp := decodeSessionResp(t, rr)
	if !strings.Contains(resp.SystemMessage, "CAPTURE BEFORE ENDING") {
		t.Fatalf("SessionEnd must still emit the capture nudge in systemMessage, got %+v", resp)
	}
}

// TestSessionHookUnknownEventNoOp verifies forward-compat: an unrecognised
// event name is acknowledged with an empty body, never an error or a stray
// nudge.
func TestSessionHookUnknownEventNoOp(t *testing.T) {
	h := &hooksHandler{}
	body := SessionHookRequest{HookEventName: "SomethingNew", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	resp := decodeSessionResp(t, rr)
	if resp.HookSpecificOutput != nil {
		t.Errorf("unknown event must not inject context, got %+v", resp.HookSpecificOutput)
	}
}

// TestSessionHookWrongMethod / MalformedJSON mirror the pretool hook's
// transport-level guards.
func TestSessionHookWrongMethod(t *testing.T) {
	h := &hooksHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/hooks/session", nil)
	rr := httptest.NewRecorder()
	h.session(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "POST" {
		t.Fatalf("Allow header: want POST, got %q", got)
	}
}

func TestSessionHookMalformedJSON(t *testing.T) {
	h := &hooksHandler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/session", strings.NewReader("{not json"))
	rr := httptest.NewRecorder()
	h.session(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rr.Code)
	}
}

// TestSessionHookEmitsAudit verifies start + end both emit a "nudge" audit
// row tagged with the event-specific tool name, and that an unknown event
// emits nothing (so the audit log isn't flooded by future events we don't
// act on).
func TestSessionHookEmitsAudit(t *testing.T) {
	tests := []struct {
		name        string
		event       string
		wantEmitted int
		wantTool    string
	}{
		{"start emits", "SessionStart", 1, "memory:sessionstart"},
		{"end emits", "SessionEnd", 1, "memory:sessionend"},
		{"stop emits", "Stop", 1, "memory:stop"},
		{"unknown emits nothing", "Future", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aud := &fakeAuditor{}
			h := &hooksHandler{auditor: aud}
			body := SessionHookRequest{SessionID: "s1", HookEventName: tc.event, CWD: "/p"}
			rr := httptest.NewRecorder()
			h.session(rr, buildSessionReq(t, body))

			if got := len(aud.records); got != tc.wantEmitted {
				t.Fatalf("audit count: got %d want %d", got, tc.wantEmitted)
			}
			if tc.wantEmitted == 0 {
				return
			}
			rec := aud.records[0]
			if rec.ToolName != tc.wantTool {
				t.Errorf("tool: got %q want %q", rec.ToolName, tc.wantTool)
			}
			if rec.Status != "nudge" {
				t.Errorf("status: got %q want nudge", rec.Status)
			}
			if rec.ClientType != "claude_code" {
				t.Errorf("client_type: got %q want claude_code", rec.ClientType)
			}
		})
	}
}

// TestSessionHookNilAuditorSafe confirms a nil auditor never panics the
// session hook (older deployments).
func TestSessionHookNilAuditorSafe(t *testing.T) {
	h := &hooksHandler{auditor: nil}
	body := SessionHookRequest{HookEventName: "SessionStart", CWD: "/p"}
	rr := httptest.NewRecorder()
	h.session(rr, buildSessionReq(t, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
}

// TestMemoryDigestLineTruncates pins the preview truncation + name/content
// composition so the injected digest stays terse.
func TestMemoryDigestLineTruncates(t *testing.T) {
	long := strings.Repeat("x", 200)
	tests := []struct {
		name  string
		entry store.MemoryEntry
		want  string
	}{
		{"name and content", store.MemoryEntry{Name: "n", Content: "c"}, "n: c"},
		{"name only", store.MemoryEntry{Name: "n"}, "n"},
		{"content only", store.MemoryEntry{Content: "c"}, "c"},
		{"newlines flattened", store.MemoryEntry{Name: "n", Content: "a\nb"}, "n: a b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryDigestLine(&tc.entry)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	got := memoryDigestLine(&store.MemoryEntry{Name: "n", Content: long})
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long content should be truncated with ellipsis, got %q", got)
	}
	if len(got) > 130 {
		t.Errorf("truncated line too long: %d", len(got))
	}

	// Multibyte (non-ASCII) truncation: a string of 200 multibyte runes must
	// be cut on a rune boundary, never mid-sequence. utf8.ValidString proves
	// the byte-slice bug (which corrupts the final rune) is gone.
	multibyte := strings.Repeat("é", 200) // 2 bytes per rune
	gotMB := memoryDigestLine(&store.MemoryEntry{Content: multibyte})
	if !utf8.ValidString(gotMB) {
		t.Errorf("multibyte truncation produced invalid UTF-8: %q", gotMB)
	}
	if !strings.HasSuffix(gotMB, "…") {
		t.Errorf("multibyte content should be truncated with ellipsis, got %q", gotMB)
	}
	// 120 runes of preview + the ellipsis rune = 121 runes.
	if n := utf8.RuneCountInString(gotMB); n != 121 {
		t.Errorf("multibyte truncation rune count = %d, want 121", n)
	}
}
