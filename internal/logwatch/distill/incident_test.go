package distill

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/store"
)

type markCall struct{ incidentID, severity string }

// fakeIncidentEnsurer models convergence: one stable (task, incident) ref per
// class key, so repeats of a class return the SAME refs and distinct classes
// return distinct ones. A class with no templates yields a task-only ref
// (blank incident id), mirroring the rate-spike path.
type fakeIncidentEnsurer struct {
	byClass   map[string]*IncidentRef
	ensures   []string
	templates map[string][]string
	marks     []markCall
	closes    []string
	ensureErr error
	next      int
}

func (f *fakeIncidentEnsurer) EnsureIncident(_ context.Context, in IncidentInput) (*IncidentRef, error) {
	f.ensures = append(f.ensures, in.ClassKey)
	if f.templates == nil {
		f.templates = map[string][]string{}
	}
	f.templates[in.ClassKey] = in.TemplateIDs
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if f.byClass == nil {
		f.byClass = map[string]*IncidentRef{}
	}
	if ref, ok := f.byClass[in.ClassKey]; ok {
		return &IncidentRef{TaskID: ref.TaskID, IncidentID: ref.IncidentID}, nil
	}
	f.next++
	incidentID := ""
	if len(in.TemplateIDs) > 0 {
		incidentID = fmt.Sprintf("inc-%d", f.next)
	}
	stored := &IncidentRef{TaskID: fmt.Sprintf("task-%d", f.next), IncidentID: incidentID}
	f.byClass[in.ClassKey] = stored
	return &IncidentRef{TaskID: stored.TaskID, IncidentID: stored.IncidentID, NewIncident: true}, nil
}

func (f *fakeIncidentEnsurer) MarkNotified(_ context.Context, incidentID, severity string, _ time.Time) error {
	f.marks = append(f.marks, markCall{incidentID, severity})
	return nil
}

func (f *fakeIncidentEnsurer) CloseIncident(_ context.Context, _, classKey, _ string) error {
	f.closes = append(f.closes, classKey)
	return nil
}

func newLinkedDistiller(fs *fakeDistillStore, notifier *captureNotifier, ens *fakeIncidentEnsurer, clock time.Time) *Distiller {
	d := NewDistiller(fs, notifier).WithIncidents(ens)
	d.now = func() time.Time { return clock }
	return d
}

func anomalyTemplateID(src *store.LogSource, text string) string {
	return TemplateID(src.ID, Normalize(text))
}

// TestDistiller_AnomalyMintsLinkableTask: a new error template fires an anomaly
// whose notification now carries a canonical TaskID (so the renderer emits a
// clickable link), keyed on the shared "template:<id>" class, and the incident
// clock is stamped after the dispatch.
func TestDistiller_AnomalyMintsLinkableTask(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	ens := &fakeIncidentEnsurer{}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	d := newLinkedDistiller(fs, notifier, ens, ts)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}
	text := "ERROR pgx: connection refused host=db-3 attempt=7"

	if err := d.Ingest(context.Background(), src, host, []collect.Line{{TS: ts, Text: text}}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("want 1 anomaly, got %d", len(notifier.notes))
	}
	tplID := anomalyTemplateID(src, text)
	wantClass := "template:" + tplID
	if len(ens.ensures) != 1 || ens.ensures[0] != wantClass {
		t.Fatalf("ensure class: got %v, want [%s]", ens.ensures, wantClass)
	}
	if got := notifier.notes[0].TaskID; got != "task-1" {
		t.Fatalf("notification TaskID: got %q, want task-1 (no link without it)", got)
	}
	// TemplateID/IncidentID (dispatch throttle keys) are left as the distiller set
	// them — only TaskID is enriched.
	if notifier.notes[0].TemplateID != tplID {
		t.Fatalf("template id must be preserved for throttling: %q", notifier.notes[0].TemplateID)
	}
	if len(ens.marks) != 1 || ens.marks[0] != (markCall{"inc-1", store.SeverityError}) {
		t.Fatalf("incident clock not stamped after dispatch: %v", ens.marks)
	}
}

// TestDistiller_AnomalyConvergence: N re-fires of the SAME template (forced with
// Notify lines) roll onto ONE incident and ONE task, never a task per alert.
func TestDistiller_AnomalyConvergence(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	ens := &fakeIncidentEnsurer{}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	d := newLinkedDistiller(fs, notifier, ens, ts)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}
	text := "ERROR pgx: connection refused host=db-3 attempt=7"

	// First ingest is a novel template; the next four are Notify re-fires of the
	// same shape (a known template would otherwise stay quiet).
	for i := range 5 {
		line := collect.Line{TS: ts.Add(time.Duration(i) * time.Minute), Text: text}
		if i > 0 {
			line.Notify = true
		}
		if err := d.Ingest(context.Background(), src, host, []collect.Line{line}); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifier.notes) != 5 {
		t.Fatalf("want 5 fires, got %d", len(notifier.notes))
	}
	wantClass := "template:" + anomalyTemplateID(src, text)
	for i, n := range notifier.notes {
		if n.TaskID != "task-1" {
			t.Fatalf("fire %d converged on the wrong task: %q", i, n.TaskID)
		}
	}
	if len(ens.byClass) != 1 {
		t.Fatalf("convergence broken: %d incident classes, want 1 (%v)", len(ens.byClass), ens.ensures)
	}
	if _, ok := ens.byClass[wantClass]; !ok {
		t.Fatalf("class key drift: %v", ens.ensures)
	}
}

// TestDistiller_DistinctTemplatesDistinctIncidents: unrelated error shapes must
// NOT be merged — each gets its own incident and task.
func TestDistiller_DistinctTemplatesDistinctIncidents(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	ens := &fakeIncidentEnsurer{}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	d := newLinkedDistiller(fs, notifier, ens, ts)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}

	if err := d.Ingest(context.Background(), src, host, []collect.Line{
		{TS: ts, Text: "ERROR pgx: connection refused host=db-3 attempt=7"},
		{TS: ts, Text: "ERROR redis: read timeout on GET session"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 2 {
		t.Fatalf("want 2 anomalies, got %d", len(notifier.notes))
	}
	if len(ens.byClass) != 2 {
		t.Fatalf("distinct shapes must not merge: %d classes, want 2", len(ens.byClass))
	}
	if notifier.notes[0].TaskID == notifier.notes[1].TaskID {
		t.Fatalf("distinct incidents shared a task: %q", notifier.notes[0].TaskID)
	}
}

// TestDistiller_AnomalyEnsureFailureStillNotifies: a failure to file the
// incident degrades to an unlinked alert, never a dropped one.
func TestDistiller_AnomalyEnsureFailureStillNotifies(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	ens := &fakeIncidentEnsurer{ensureErr: errors.New("incident store down")}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	d := newLinkedDistiller(fs, notifier, ens, ts)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}

	if err := d.Ingest(context.Background(), src, host, []collect.Line{
		{TS: ts, Text: "ERROR pgx: connection refused host=db-3 attempt=7"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("alert must still fire on ensure failure, got %d", len(notifier.notes))
	}
	if notifier.notes[0].TaskID != "" {
		t.Fatalf("failed ensure must leave TaskID empty, got %q", notifier.notes[0].TaskID)
	}
	if len(ens.marks) != 0 {
		t.Fatalf("no incident to stamp on ensure failure, got %v", ens.marks)
	}
}

// TestDistiller_AnomalyNotifyFailureSkipsMark: a dispatch failure leaves the
// incident UNstamped so the daemon renotify sweep retries it.
func TestDistiller_AnomalyNotifyFailureSkipsMark(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{err: errors.New("channel unavailable")}
	ens := &fakeIncidentEnsurer{}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	d := newLinkedDistiller(fs, notifier, ens, ts)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}

	if err := d.Ingest(context.Background(), src, host, []collect.Line{
		{TS: ts, Text: "ERROR pgx: connection refused host=db-3 attempt=7"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(ens.ensures) != 1 {
		t.Fatalf("incident should still be filed before dispatch, got %d", len(ens.ensures))
	}
	if len(ens.marks) != 0 {
		t.Fatalf("failed dispatch must not stamp the incident clock, got %v", ens.marks)
	}
}

// TestDistiller_RateSpikeLinkableAndClosesOnRecovery: a rate spike gets a
// linkable task (class ratespike:<src>, task-only so no incident clock stamp),
// and recovery closes that task instead of leaking it open.
func TestDistiller_RateSpikeLinkableAndClosesOnRecovery(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	ens := &fakeIncidentEnsurer{}
	var clock time.Time
	d := NewDistiller(fs, notifier).WithIncidents(ens)
	d.now = func() time.Time { return clock }

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "203.0.113.1"}
	t0 := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	tplID := anomalyTemplateID(src, "ERROR pgx: connection refused host=db-0 attempt=0")
	fs.templates = map[string]int64{tplID: 1}
	fs.templateSev = map[string]string{tplID: store.SeverityError}

	clock = t0.Add(time.Minute)
	mustIngest(t, d, src, host, errorLinesAt(clock, 15))
	if len(notifier.notes) != 1 {
		t.Fatalf("want 1 spike, got %d", len(notifier.notes))
	}
	if got := notifier.notes[0].TaskID; got != "task-1" {
		t.Fatalf("spike TaskID: got %q, want task-1", got)
	}
	if len(ens.ensures) != 1 || ens.ensures[0] != "ratespike:s1" {
		t.Fatalf("spike class: got %v, want [ratespike:s1]", ens.ensures)
	}
	if len(ens.templates["ratespike:s1"]) != 0 {
		t.Fatalf("rate spike must link no template (task-only): %v", ens.templates["ratespike:s1"])
	}
	if len(ens.marks) != 0 {
		t.Fatalf("task-only spike has no incident clock to stamp: %v", ens.marks)
	}

	// Rate falls back to baseline: the latch clears and the canonical task closes.
	clock = t0.Add(30 * time.Minute)
	mustIngest(t, d, src, host, []collect.Line{{TS: clock, Text: "GET /healthz 200"}})
	if len(ens.closes) != 1 || ens.closes[0] != "ratespike:s1" {
		t.Fatalf("recovery must close the spike task: %v", ens.closes)
	}
}
