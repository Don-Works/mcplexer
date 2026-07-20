package renotify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// The sweeper's contract is "dispatch exactly what the store judged due, with
// the effective severity, then advance the backoff". The policy that decides
// due-ness is the store's and is exercised against the real policy in
// internal/store/sqlite/monitoring_incident_renotify_test.go — benign,
// below-warn and stopped-recurring incidents are proven silent there, where
// the real monitoringNotificationDue runs. Reimplementing that judgement in a
// fake here would only prove the fake.

// sweepClock is the fake "now" every test sweeps at: twelve hours into the
// 2026-07-20 incident, the point at which the operator had heard nothing.
var sweepClock = time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)

func oneWorkspace() []store.Workspace { return []store.Workspace{{ID: "ws-1"}} }

type markCall struct {
	incidentID string
	severity   string
	at         time.Time
}

type listCall struct {
	workspaceID string
	now         time.Time
	limit       int
}

// fakeStore models the store contract: it hands back the candidates it was
// seeded with, and marking one advances its backoff so it is no longer due.
type fakeStore struct {
	mu         sync.Mutex
	workspaces []store.Workspace
	due        map[string][]*store.MonitoringRenotifyCandidate
	notified   map[string]bool
	lists      []listCall
	marks      []markCall
	listErr    error
	markErr    error
	wsErr      error
}

func (f *fakeStore) ListWorkspaces(context.Context) ([]store.Workspace, error) {
	return f.workspaces, f.wsErr
}

func (f *fakeStore) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.lists)
}

func (f *fakeStore) ListMonitoringIncidentsDueForRenotify(
	_ context.Context, workspaceID string, now time.Time, limit int,
) ([]*store.MonitoringRenotifyCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists = append(f.lists, listCall{workspaceID: workspaceID, now: now, limit: limit})
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []*store.MonitoringRenotifyCandidate{}
	for _, c := range f.due[workspaceID] {
		if f.notified[c.Incident.ID] {
			continue // backoff advanced by a previous mark
		}
		if len(out) == limit {
			break
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeStore) MarkMonitoringIncidentNotified(
	_ context.Context, incidentID, severity string, at time.Time,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks = append(f.marks, markCall{incidentID: incidentID, severity: severity, at: at})
	if f.markErr != nil {
		return f.markErr
	}
	if f.notified == nil {
		f.notified = map[string]bool{}
	}
	f.notified[incidentID] = true
	return nil
}

type fakeNotifier struct {
	sent []distill.Notification
	err  error
}

func (f *fakeNotifier) Notify(_ context.Context, n distill.Notification) error {
	f.sent = append(f.sent, n)
	return f.err
}

func candidate(id, wsID, reason, raw, effective string) *store.MonitoringRenotifyCandidate {
	first := time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC)
	return &store.MonitoringRenotifyCandidate{
		Incident: &store.MonitoringIncident{
			ID: id, WorkspaceID: wsID, ClassKey: "class:" + id, TaskID: "task-" + id,
			Disposition: store.MonitoringDispositionActionable, Severity: raw,
			Title: "order sync stalled", OccurrenceCount: 12, EventCount: 40,
			FirstSeen: first, LastSeen: first.Add(11 * time.Hour),
		},
		NotificationReason: reason,
		EffectiveSeverity:  effective,
	}
}

func newSweeper(st Store, n distill.Notifier, now time.Time) *Sweeper {
	s := New(st, n)
	s.now = func() time.Time { return now }
	return s
}

func TestSweepNotifiesOnceThenRespectsBackoff(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	st := &fakeStore{
		workspaces: []store.Workspace{{ID: "ws-1"}},
		due: map[string][]*store.MonitoringRenotifyCandidate{
			"ws-1": {candidate("inc-1", "ws-1", "persistent_incident", store.SeverityError, store.SeverityError)},
		},
	}
	ntf := &fakeNotifier{}
	s := newSweeper(st, ntf, now)

	s.Sweep(context.Background())
	if len(ntf.sent) != 1 {
		t.Fatalf("first tick: want 1 notification, got %d", len(ntf.sent))
	}
	if len(st.marks) != 1 || st.marks[0].incidentID != "inc-1" || !st.marks[0].at.Equal(now) {
		t.Fatalf("first tick marks: %+v", st.marks)
	}
	if got := ntf.sent[0]; got.IncidentID != "inc-1" || got.TaskID != "task-inc-1" || got.NewIncident {
		t.Fatalf("notification envelope: %+v", got)
	}

	s.Sweep(context.Background())
	if len(ntf.sent) != 1 {
		t.Fatalf("second tick must not re-notify: got %d notifications", len(ntf.sent))
	}
	if len(st.marks) != 1 {
		t.Fatalf("second tick must not re-mark: %+v", st.marks)
	}
}

func TestSweepRecordsEffectiveSeverityNotRawSeverity(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	st := &fakeStore{
		workspaces: []store.Workspace{{ID: "ws-1"}},
		due: map[string][]*store.MonitoringRenotifyCandidate{
			"ws-1": {candidate("inc-1", "ws-1", "age_escalation", store.SeverityWarn, store.SeverityCritical)},
		},
	}
	ntf := &fakeNotifier{}
	newSweeper(st, ntf, now).Sweep(context.Background())

	// Dispatching the raw "warn" would be filtered out by every channel whose
	// min_severity is the default "error" — the original silent failure.
	if ntf.sent[0].Severity != store.SeverityCritical {
		t.Fatalf("dispatched severity = %q, want critical", ntf.sent[0].Severity)
	}
	if st.marks[0].severity != store.SeverityCritical {
		t.Fatalf("recorded severity = %q, want critical", st.marks[0].severity)
	}
	if !strings.Contains(ntf.sent[0].Body, "Severity raised warn to critical") {
		t.Fatalf("body must state the escalation: %q", ntf.sent[0].Body)
	}
}

func TestSweepCoversEveryWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	st := &fakeStore{
		workspaces: []store.Workspace{{ID: "ws-1"}, {ID: "ws-2"}, {ID: "ws-3"}},
		due: map[string][]*store.MonitoringRenotifyCandidate{
			"ws-1": {candidate("inc-1", "ws-1", "persistent_incident", store.SeverityError, store.SeverityError)},
			"ws-3": {candidate("inc-3", "ws-3", "persistent_incident", store.SeverityError, store.SeverityError)},
		},
	}
	ntf := &fakeNotifier{}
	newSweeper(st, ntf, now).Sweep(context.Background())

	if len(st.lists) != 3 {
		t.Fatalf("want one list per workspace, got %d: %+v", len(st.lists), st.lists)
	}
	for i, want := range []string{"ws-1", "ws-2", "ws-3"} {
		if st.lists[i].workspaceID != want {
			t.Fatalf("list %d workspace = %q, want %q", i, st.lists[i].workspaceID, want)
		}
		if !st.lists[i].now.Equal(now) {
			t.Fatalf("list %d now = %v, want fake clock %v", i, st.lists[i].now, now)
		}
	}
	if len(ntf.sent) != 2 {
		t.Fatalf("want 2 notifications across workspaces, got %d", len(ntf.sent))
	}
}

func TestSweepBoundsBatchByLimit(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	backlog := make([]*store.MonitoringRenotifyCandidate, 0, 250)
	for i := 0; i < 250; i++ {
		backlog = append(backlog, candidate(fmt.Sprintf("inc-%d", i),
			"ws-1", "persistent_incident", store.SeverityError, store.SeverityError))
	}
	st := &fakeStore{
		workspaces: []store.Workspace{{ID: "ws-1"}},
		due:        map[string][]*store.MonitoringRenotifyCandidate{"ws-1": backlog},
	}
	ntf := &fakeNotifier{}
	s := newSweeper(st, ntf, now)
	s.limit = 10
	s.Sweep(context.Background())

	if st.lists[0].limit != 10 {
		t.Fatalf("limit passed to store = %d, want 10", st.lists[0].limit)
	}
	if len(ntf.sent) != 10 {
		t.Fatalf("a backlog must not stall the tick: got %d notifications, want 10", len(ntf.sent))
	}
}

func TestSweepDoesNotAdvanceBackoffWhenDispatchFails(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	st := &fakeStore{
		workspaces: []store.Workspace{{ID: "ws-1"}},
		due: map[string][]*store.MonitoringRenotifyCandidate{
			"ws-1": {candidate("inc-1", "ws-1", "persistent_incident", store.SeverityError, store.SeverityError)},
		},
	}
	ntf := &fakeNotifier{err: errors.New("no eligible delivery route")}
	newSweeper(st, ntf, now).Sweep(context.Background())

	// Marking a failed dispatch would burn the reminder: the backoff advances
	// and the operator never hears about an incident that is still broken.
	if len(st.marks) != 0 {
		t.Fatalf("failed dispatch must not mark notified: %+v", st.marks)
	}
}

func TestSweepSurvivesStoreErrors(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	t.Run("workspace listing fails", func(t *testing.T) {
		st := &fakeStore{wsErr: errors.New("db down")}
		ntf := &fakeNotifier{}
		newSweeper(st, ntf, now).Sweep(context.Background())
		if len(ntf.sent) != 0 {
			t.Fatalf("want no notifications, got %d", len(ntf.sent))
		}
	})
	t.Run("one workspace query fails, others still swept", func(t *testing.T) {
		st := &fakeStore{
			workspaces: []store.Workspace{{ID: "ws-1"}, {ID: "ws-2"}},
			listErr:    errors.New("query failed"),
		}
		ntf := &fakeNotifier{}
		newSweeper(st, ntf, now).Sweep(context.Background())
		if len(st.lists) != 2 {
			t.Fatalf("a failing workspace must not abort the sweep: %+v", st.lists)
		}
	})
}

func TestSweepStopsOnCancelledContext(t *testing.T) {
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	st := &fakeStore{workspaces: []store.Workspace{{ID: "ws-1"}, {ID: "ws-2"}}}
	ntf := &fakeNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newSweeper(st, ntf, now).Sweep(ctx)
	if len(st.lists) != 0 {
		t.Fatalf("cancelled context must do no work: %+v", st.lists)
	}
}
