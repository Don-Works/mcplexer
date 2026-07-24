package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRewriteGenericMonitoringTitles(t *testing.T) {
	ws, source, template, db := seedMonitoringIncidentFixture(t)
	ctx := context.Background()

	// Make the fixture look like a real novelty storm title with useful sample.
	template.SampleLast = `2026/07/24 [error] recv() failed (104: Connection reset by peer) while reading response header from upstream`
	template.Masked = `recv() failed (104: Connection reset by peer) while reading response header from upstream`
	template.Severity = store.SeverityError
	if _, err := db.UpsertLogTemplate(ctx, template, 1); err != nil {
		t.Fatal(err)
	}

	task := &store.Task{
		WorkspaceID: ws.ID,
		Title:       "new error-class log template on host/api (×3)",
		Meta:        `{"logwatch_class":"template:` + template.ID + `"}`,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	res, err := db.RecordMonitoringTriage(ctx, store.MonitoringTriageRecord{
		WorkspaceID: ws.ID, ClassKey: "template:" + template.ID, TaskID: task.ID,
		Disposition: store.MonitoringDispositionUncertain, Severity: store.SeverityError,
		Title:    "new error-class log template on host/api (×3)",
		SourceID: source.ID, TemplateIDs: []string{template.ID},
		Evidence:   "Observed evidence\n- recv() failed (104: Connection reset by peer)\n",
		ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	rewriter, ok := db.(store.MonitoringTitleRewriteStore)
	if !ok {
		t.Fatal("store does not implement MonitoringTitleRewriteStore")
	}
	reader, ok := db.(store.MonitoringIncidentReadStore)
	if !ok {
		t.Fatal("store does not implement MonitoringIncidentReadStore")
	}
	n, err := rewriter.RewriteGenericMonitoringTitles(ctx, ws.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rewritten=%d want 1", n)
	}
	got, err := reader.GetMonitoringIncident(ctx, ws.ID, res.Incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title == "" || strings.Contains(strings.ToLower(got.Title), "log template on") {
		t.Fatalf("title not rewritten: %q", got.Title)
	}
	// Idempotent.
	n2, err := rewriter.RewriteGenericMonitoringTitles(ctx, ws.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second pass rewrote %d; want 0", n2)
	}
}
