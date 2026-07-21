package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

// failingAuditStore returns an error on every InsertAuditRecord call so we
// can exercise the swallow-and-log path in recordAudit. All other methods
// are no-op stubs because the audit Logger never calls them in this path.
type failingAuditStore struct{}

func (failingAuditStore) InsertAuditRecord(_ context.Context, _ *store.AuditRecord) error {
	return errors.New("forced insert failure")
}
func (failingAuditStore) QueryAuditRecords(_ context.Context, _ store.AuditFilter) ([]store.AuditRecord, int, error) {
	return nil, 0, nil
}
func (failingAuditStore) GetAuditStats(_ context.Context, _ string, _, _ time.Time) (*store.AuditStats, error) {
	return nil, nil
}
func (failingAuditStore) GetDashboardTimeSeries(_ context.Context, _, _ time.Time) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (failingAuditStore) GetDashboardTimeSeriesBucketed(_ context.Context, _, _ time.Time, _ int) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (failingAuditStore) GetToolLeaderboard(_ context.Context, _, _ time.Time, _ int) ([]store.ToolLeaderboardEntry, error) {
	return nil, nil
}
func (failingAuditStore) GetServerHealth(_ context.Context, _, _ time.Time) ([]store.ServerHealthEntry, error) {
	return nil, nil
}
func (failingAuditStore) GetErrorBreakdown(_ context.Context, _, _ time.Time, _ int) ([]store.ErrorBreakdownEntry, error) {
	return nil, nil
}
func (failingAuditStore) GetRouteHitMap(_ context.Context, _, _ time.Time) ([]store.RouteHitEntry, error) {
	return nil, nil
}
func (failingAuditStore) GetAuditCacheStats(_ context.Context, _, _ time.Time) (*store.AuditCacheStats, error) {
	return nil, nil
}
func (failingAuditStore) PruneAuditRecords(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (failingAuditStore) CountChildCLIToolCalls(_ context.Context, _ string, _, _ time.Time, _ []string) (int, error) {
	return 0, nil
}
func (failingAuditStore) SearchAuditRecords(_ context.Context, _ store.AuditFilter, _ int) ([]store.AuditRecord, string, error) {
	return nil, "fts", nil
}
func (failingAuditStore) AuditAnomalies(_ context.Context, _ string, _ time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (failingAuditStore) AuditSecurityEvents(_ context.Context, _ string, _ time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (failingAuditStore) CountAuditMatching(_ context.Context, _ store.AuditFilter) (int, error) {
	return 0, nil
}
func (failingAuditStore) ListSavedSearches(_ context.Context) ([]store.SavedSearch, error) {
	return nil, nil
}
func (failingAuditStore) GetSavedSearch(_ context.Context, _ string) (*store.SavedSearch, error) {
	return nil, nil
}
func (failingAuditStore) CreateSavedSearch(_ context.Context, _ *store.SavedSearch) error {
	return nil
}
func (failingAuditStore) UpdateSavedSearch(_ context.Context, _ *store.SavedSearch) error {
	return nil
}
func (failingAuditStore) DeleteSavedSearch(_ context.Context, _ string) error { return nil }
func (failingAuditStore) EvaluateSavedSearches(_ context.Context, _ time.Time) ([]store.FiredSavedSearch, error) {
	return nil, nil
}

// noopScopeStore satisfies AuthScopeStore. Tests use the empty
// AuthScopeID path so GetAuthScope is never reached.
type noopScopeStore struct{}

func (noopScopeStore) CreateAuthScope(_ context.Context, _ *store.AuthScope) error { return nil }
func (noopScopeStore) GetAuthScope(_ context.Context, _ string) (*store.AuthScope, error) {
	return nil, nil
}
func (noopScopeStore) GetAuthScopeByName(_ context.Context, _ string) (*store.AuthScope, error) {
	return nil, nil
}
func (noopScopeStore) ListAuthScopes(_ context.Context) ([]store.AuthScope, error) { return nil, nil }
func (noopScopeStore) UpdateAuthScope(_ context.Context, _ *store.AuthScope) error { return nil }
func (noopScopeStore) DeleteAuthScope(_ context.Context, _ string) error           { return nil }
func (noopScopeStore) UpdateAuthScopeTokenData(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (noopScopeStore) UpdateAuthScopeEncryptedData(_ context.Context, _ string, _ []byte) error {
	return nil
}

// captureSlog swaps the default slog with a text handler writing into the
// returned buffer. The returned restore func reinstates the previous logger.
// Set at LevelDebug so we observe Warn AND Error alike.
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	return &buf, func() { slog.SetDefault(prev) }
}

// TestRecordAuditFailure_LogsAtWarnNotError pins the audit-failure log
// level at WARN. Pre-fix this was slog.Error which alarmed on-call
// dashboards for transient SQLite hiccups the gateway already swallowed.
// The gateway must continue serving traffic — the demote acknowledges
// "we tried, we noticed, we didn't escalate".
func TestRecordAuditFailure_LogsAtWarnNotError(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	auditor := audit.NewLogger(failingAuditStore{}, noopScopeStore{}, nil)
	h := &handler{
		auditor:  auditor,
		sessions: &sessionManager{}, // zero-value sessionManager is nil-safe for the getters used here
	}

	h.recordAudit(
		context.Background(),
		"test__tool",
		nil, // params
		nil, // route
		nil, // result
		nil, // rpcErr
		time.Now(),
	)

	out := buf.String()
	if !strings.Contains(out, "audit record failed") {
		t.Fatalf("expected audit failure log line, got:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("audit failure logged at ERROR level — must be WARN:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected level=WARN in log line, got:\n%s", out)
	}
}

// TestRecordAuditBlockedFailure_LogsAtWarn pins the same level demotion
// on the recordAuditBlocked path. The two branches share a swallow
// pattern and were both slog.Error pre-fix; they must move together.
func TestRecordAuditBlockedFailure_LogsAtWarn(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	auditor := audit.NewLogger(failingAuditStore{}, noopScopeStore{}, nil)
	h := &handler{
		auditor:  auditor,
		sessions: &sessionManager{},
	}

	h.recordAuditBlocked(
		context.Background(),
		"test__tool",
		nil,
		nil,
		nil,
		nil,
		time.Now(),
	)

	out := buf.String()
	if !strings.Contains(out, "audit record failed") {
		t.Fatalf("expected audit failure log line, got:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("blocked audit failure logged at ERROR level — must be WARN:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected level=WARN in log line, got:\n%s", out)
	}
}

func TestContextCostStatsRecordWithoutAuditor(t *testing.T) {
	h := &handler{}
	ctx := context.Background()
	result := marshalToolResult("hello world")

	h.recordAudit(ctx, "example__ok", json.RawMessage(`{}`), nil, result, nil, time.Now())
	h.recordAuditBlocked(ctx, "example__blocked", json.RawMessage(`{}`), nil, nil,
		&RPCError{Code: CodeInvalidRequest, Message: "blocked"}, time.Now())

	stats := h.ContextCostStats()
	if stats.ToolResultsTotal != 2 {
		t.Fatalf("ToolResultsTotal = %d, want 2", stats.ToolResultsTotal)
	}
	if stats.ToolResultBytesTotal != uint64(len(result)) {
		t.Fatalf("ToolResultBytesTotal = %d, want %d", stats.ToolResultBytesTotal, len(result))
	}
	if stats.BlockedToolResultsTotal != 1 {
		t.Fatalf("BlockedToolResultsTotal = %d, want 1", stats.BlockedToolResultsTotal)
	}
	if stats.ByTool["example__ok"].Calls != 1 {
		t.Fatalf("example__ok calls = %d, want 1", stats.ByTool["example__ok"].Calls)
	}
}

func TestExtractToolErrorTextIncludesLintAndRuntimeCause(t *testing.T) {
	result := json.RawMessage(`{
		"isError":true,
		"content":[
			{"type":"text","text":"--- Lint warnings ---\n[warning] local value looked like a namespace"},
			{"type":"text","text":"Error: TypeError: Object has no member 'retrieve'"},
			{"type":"text","text":"--- 1 tool call executed ---"}
		]
	}`)
	got := extractToolErrorText(result)
	for _, want := range []string{"Lint warnings", "TypeError", "tool call executed"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit error text missing %q: %s", want, got)
		}
	}
}

func TestExtractToolErrorTextIsBounded(t *testing.T) {
	long := strings.Repeat("failure ", 1000)
	text, _ := json.Marshal(long)
	result := json.RawMessage(`{"isError":true,"content":[{"type":"text","text":` + string(text) + `}]}`)
	if got := extractToolErrorText(result); len(got) != maxAuditToolErrorBytes {
		t.Fatalf("bounded error length = %d, want %d", len(got), maxAuditToolErrorBytes)
	}
}

func TestExtractToolErrorTextTruncatesOnRuneBoundary(t *testing.T) {
	// 3-byte runes ensure the byte cap lands mid-rune (2000 % 3 != 0).
	long := strings.Repeat("—", 1000)
	text, _ := json.Marshal(long)
	result := json.RawMessage(`{"isError":true,"content":[{"type":"text","text":` + string(text) + `}]}`)
	got := extractToolErrorText(result)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated error text is not valid UTF-8: %q", got[len(got)-8:])
	}
	if len(got) > maxAuditToolErrorBytes || len(got) < maxAuditToolErrorBytes-utf8.UTFMax {
		t.Fatalf("bounded error length = %d, want within [%d, %d]", len(got), maxAuditToolErrorBytes-utf8.UTFMax, maxAuditToolErrorBytes)
	}
}

func TestExecuteCodeAuditCorrelatesWrapperAndInnerCalls(t *testing.T) {
	h, ms := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.auditor = audit.NewLogger(ms, noopScopeStore{}, nil)

	params, err := json.Marshal(CallToolRequest{
		Name:      "mcpx__execute_code",
		Arguments: json.RawMessage(`{"code":"print(mcpx.whoami());"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := h.handleToolsCall(context.Background(), params); rpcErr != nil {
		t.Fatalf("execute_code: %v", rpcErr)
	}

	var wrapper, inner *store.AuditRecord
	for i := range ms.auditRecords {
		rec := &ms.auditRecords[i]
		switch rec.ToolName {
		case "mcpx__execute_code":
			wrapper = rec
		case "mcpx__whoami":
			inner = rec
		}
	}
	if wrapper == nil || inner == nil {
		t.Fatalf("missing wrapper/inner audit rows: %+v", ms.auditRecords)
	}
	if wrapper.ExecutionID == "" || wrapper.ExecutionID != inner.ExecutionID {
		t.Fatalf("execution ids do not correlate: wrapper=%q inner=%q", wrapper.ExecutionID, inner.ExecutionID)
	}
	if wrapper.CorrelationID != wrapper.ExecutionID || inner.CorrelationID != inner.ExecutionID {
		t.Fatalf("correlation ids do not mirror execution ids: wrapper=%+v inner=%+v", wrapper, inner)
	}
}
