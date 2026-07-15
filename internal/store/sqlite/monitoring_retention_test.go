package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedSourceWithTemplate creates a source and one known template,
// ready for log_lines to be inserted against.
func seedSourceWithTemplate(t *testing.T, db interface {
	CreateLogSource(ctx context.Context, s *store.LogSource) error
	UpsertLogTemplate(ctx context.Context, t *store.LogTemplate, n int64) (bool, error)
}, ctx context.Context, wsID, hostID string) (*store.LogSource, *store.LogTemplate) {
	t.Helper()
	s := &store.LogSource{WorkspaceID: wsID, RemoteHostID: hostID, Name: "api", Selector: "api", Enabled: true}
	if err := db.CreateLogSource(ctx, s); err != nil {
		t.Fatalf("create source: %v", err)
	}
	tpl := &store.LogTemplate{
		ID: "tpl-" + s.ID, SourceID: s.ID, Masked: "GET / <n>", Severity: store.SeverityInfo,
		FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}
	if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
		t.Fatalf("upsert template: %v", err)
	}
	return s, tpl
}

// TestPruneLogLines_ByAge asserts the age cutoff keeps only lines at
// or after maxAge.
func TestPruneLogLines_ByAge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s, tpl := seedSourceWithTemplate(t, db, ctx, wsID, h.ID)

	now := time.Now().UTC()
	lines := []store.LogLine{
		{SourceID: s.ID, TemplateID: tpl.ID, TS: now.Add(-48 * time.Hour), Line: "old line"},
		{SourceID: s.ID, TemplateID: tpl.ID, TS: now.Add(-time.Minute), Line: "fresh line"},
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert lines: %v", err)
	}

	removed, err := db.PruneLogLines(ctx, s.ID, now.Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 row removed by age, got %d", removed)
	}
	remaining, err := db.SearchLogLines(ctx, s.ID, "", 10)
	if err != nil || len(remaining) != 1 || remaining[0].Line != "fresh line" {
		t.Fatalf("expected only the fresh line to survive: %v %+v", err, remaining)
	}
}

// TestPruneLogLines_ByBytes asserts repeated pruning converges under
// the byte budget rather than looping forever.
func TestPruneLogLines_ByBytes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s, tpl := seedSourceWithTemplate(t, db, ctx, wsID, h.ID)

	base := time.Now().UTC().Add(-time.Hour)
	line100 := strings.Repeat("x", 100)
	lines := make([]store.LogLine, 10) // 10 * 100B = 1000B, budget 300B
	for i := range lines {
		lines[i] = store.LogLine{SourceID: s.ID, TemplateID: tpl.ID, TS: base.Add(time.Duration(i) * time.Second), Line: line100}
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert lines: %v", err)
	}

	removedTotal, err := db.PruneLogLines(ctx, s.ID, time.Time{}, 300)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removedTotal == 0 {
		t.Fatal("expected pruning to remove rows over budget")
	}
	remaining, err := db.SearchLogLines(ctx, s.ID, "", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(remaining)*100 > 300 {
		t.Fatalf("expected convergence under budget, got %d rows (%dB)", len(remaining), len(remaining)*100)
	}
}

// TestPruneLogLines_SingleOversizedRow is the half-delete-loop
// regression: count/2 truncates to 0 for a single row, so a lone line
// bigger than the whole budget must still be removed in one pass
// rather than sitting over budget forever.
func TestPruneLogLines_SingleOversizedRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s, tpl := seedSourceWithTemplate(t, db, ctx, wsID, h.ID)

	huge := store.LogLine{SourceID: s.ID, TemplateID: tpl.ID, TS: time.Now().UTC(), Line: strings.Repeat("x", 5000)}
	if err := db.InsertLogLines(ctx, []store.LogLine{huge}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	removed, err := db.PruneLogLines(ctx, s.ID, time.Time{}, 1000)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected the oversized row pruned in one pass, got %d removed", removed)
	}
	remaining, err := db.SearchLogLines(ctx, s.ID, "", 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("oversized row must not survive: %v %d remaining", err, len(remaining))
	}
}

// TestLogLinesCascade_SourceDelete proves migration 135's FK: deleting
// a log_source cascades its log_lines (previously they were orphaned,
// relying only on the next age/byte prune to catch them).
func TestLogLinesCascade_SourceDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s, tpl := seedSourceWithTemplate(t, db, ctx, wsID, h.ID)

	line := store.LogLine{SourceID: s.ID, TemplateID: tpl.ID, TS: time.Now().UTC(), Line: "GET / 200"}
	if err := db.InsertLogLines(ctx, []store.LogLine{line}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.DeleteLogSource(ctx, s.ID); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	remaining, err := db.SearchLogLines(ctx, s.ID, "", 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("source delete must cascade log_lines: %v %d remaining", err, len(remaining))
	}
}

// TestLogLinesCascade_TemplateDelete proves the log_lines.template_id
// FK: a template deleted independently of its source still cascades.
func TestLogLinesCascade_TemplateDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s, tpl := seedSourceWithTemplate(t, db, ctx, wsID, h.ID)

	line := store.LogLine{SourceID: s.ID, TemplateID: tpl.ID, TS: time.Now().UTC(), Line: "GET / 200"}
	if err := db.InsertLogLines(ctx, []store.LogLine{line}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Raw().ExecContext(ctx, `DELETE FROM log_templates WHERE id = ?`, tpl.ID); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	remaining, err := db.SearchLogLines(ctx, s.ID, "", 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("template delete must cascade log_lines: %v %d remaining", err, len(remaining))
	}
}

func TestLogLinesRejectCrossSourceTemplate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	host := seedRemoteHost(t, db, ctx, wsID, scopeID)
	source, _ := seedSourceWithTemplate(t, db, ctx, wsID, host.ID)
	other := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "worker", Selector: "worker", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherTemplate := &store.LogTemplate{
		ID: "tpl-other", SourceID: other.ID, Masked: "worker failed", Severity: store.SeverityError,
		FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}
	if _, err := db.UpsertLogTemplate(ctx, otherTemplate, 1); err != nil {
		t.Fatal(err)
	}
	err := db.InsertLogLines(ctx, []store.LogLine{{
		SourceID: source.ID, TemplateID: otherTemplate.ID, TS: time.Now().UTC(), Line: "wrong tenant/source",
	}})
	if err == nil {
		t.Fatal("log line accepted a template owned by a different source")
	}
}
