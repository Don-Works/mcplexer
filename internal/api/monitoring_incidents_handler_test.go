// monitoring_incidents_handler_test.go — coverage for the incident read
// surface. The expected-signal endpoint is covered next door in
// monitoring_signals_handler_test.go, which reuses this file's fixture.
//
// The two assertions that matter operationally: an absence incident and a
// collection incident must be distinguishable from the response WITHOUT
// parsing a class-key prefix, and a workspace must not be able to read
// another's incidents through either the list or the by-id endpoint.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// incidentFixture is two workspaces, each with a host, a source and an
// expected-signal rule, so cross-workspace reads have something real to fail at.
type incidentFixture struct {
	db     *sqlite.DB
	ruleA  *store.MonitoringExpectedSignal
	ruleB  *store.MonitoringExpectedSignal
	taskA  string
	taskB  string
	srcAID string
}

func newIncidentFixture(t *testing.T) *incidentFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "incidents.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &incidentFixture{db: db}
	f.ruleA, f.taskA = seedIncidentWorkspace(t, db, "ws-A", "orders-sync")
	f.ruleB, f.taskB = seedIncidentWorkspace(t, db, "ws-B", "billing-sync")
	f.srcAID = f.ruleA.SourceID
	return f
}

// seedIncidentWorkspace builds one workspace's monitoring chain and returns its
// expected-signal rule plus a canonical task for incidents to hang off.
func seedIncidentWorkspace(
	t *testing.T, db *sqlite.DB, wsID, name string,
) (*store.MonitoringExpectedSignal, string) {
	t.Helper()
	ctx := context.Background()
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: wsID, Name: wsID, DefaultPolicy: "allow",
	}); err != nil {
		t.Fatalf("create workspace %s: %v", wsID, err)
	}
	scope := &store.AuthScope{Name: "scope-" + wsID, Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	host := &store.RemoteHost{
		WorkspaceID: wsID, Name: "host-" + wsID, SSHUser: "logwatch",
		SSHHost: "203.0.113.10", SSHPort: 22, AuthScopeID: scope.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, host); err != nil {
		t.Fatalf("create host: %v", err)
	}
	src := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: name,
		Kind: store.LogSourceKindDocker, Selector: name, Enabled: true,
		RetentionDays: 7, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	rule := &store.MonitoringExpectedSignal{
		WorkspaceID: wsID, SourceID: src.ID, Name: name + " heartbeat",
		MatchSubstring: "sync complete", MinCount: 1, WindowSeconds: 3600,
		Severity: store.SeverityError, Enabled: true,
	}
	store.ApplyExpectedSignalDefaults(rule)
	if err := db.CreateMonitoringExpectedSignal(ctx, rule); err != nil {
		t.Fatalf("create expected signal: %v", err)
	}
	task := &store.Task{WorkspaceID: wsID, Title: name + " stopped"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return rule, task.ID
}

// raise records one expected-signal outcome, which is the real path an absence
// or collection incident is created through.
func (f *incidentFixture) raise(
	t *testing.T, rule *store.MonitoringExpectedSignal, taskID string,
	outcome store.ExpectedSignalOutcome, classKey, reason string, at time.Time,
) *store.ExpectedSignalResult {
	t.Helper()
	result, err := f.db.RecordExpectedSignalOutcome(context.Background(), store.ExpectedSignalRecord{
		RuleID: rule.ID, TaskID: taskID, ObservedAt: at,
		Decision: store.ExpectedSignalDecision{
			Outcome: outcome, Raise: true, ClassKey: classKey,
			Severity: store.SeverityError, Title: string(outcome) + " on " + rule.Name,
			Reason: reason, Detail: "detail for " + reason,
		},
	})
	if err != nil {
		t.Fatalf("record %s outcome: %v", outcome, err)
	}
	return result
}

func (f *incidentFixture) handler() *monitoringIncidentHandler {
	return &monitoringIncidentHandler{store: f.db}
}

// TestIncidentListDistinguishesAbsenceFromCollection is the assertion the
// integration scenarios depend on: "the signal stopped" and "we cannot see the
// signal" must be told apart from a field, not from a string prefix.
func TestIncidentListDistinguishesAbsenceFromCollection(t *testing.T) {
	f := newIncidentFixture(t)
	now := time.Now().UTC()
	f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now.Add(-30*time.Minute))
	f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalCollection,
		f.ruleA.CollectionClassKey(), store.ReasonPullFailing, now.Add(-10*time.Minute))

	rec := httptest.NewRecorder()
	f.handler().list(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/incidents?workspace_id=ws-A", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Incidents []struct {
			ClassKey          string `json:"class_key"`
			ClassKind         string `json:"class_kind"`
			ExpectedSignalID  string `json:"expected_signal_id"`
			Severity          string `json:"severity"`
			EffectiveSeverity string `json:"effective_severity"`
			Active            bool   `json:"active"`
			OccurrenceCount   int64  `json:"occurrence_count"`
		} `json:"incidents"`
		Total  int `json:"total"`
		Active int `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 {
		t.Fatalf("total = %d, want 2", out.Total)
	}
	kinds := map[string]string{}
	for _, i := range out.Incidents {
		kinds[i.ClassKind] = i.ExpectedSignalID
		if i.EffectiveSeverity == "" {
			t.Errorf("incident %s has no effective_severity", i.ClassKey)
		}
	}
	if _, ok := kinds[store.IncidentClassAbsence]; !ok {
		t.Errorf("no incident classified as %q: %+v", store.IncidentClassAbsence, out.Incidents)
	}
	if _, ok := kinds[store.IncidentClassCollection]; !ok {
		t.Errorf("no incident classified as %q: %+v", store.IncidentClassCollection, out.Incidents)
	}
	// Both kinds must point back at the rule that raised them, so a client can
	// follow to /monitoring/expected-signals without parsing the class key.
	for kind, ruleID := range kinds {
		if ruleID != f.ruleA.ID {
			t.Errorf("%s expected_signal_id = %q, want %q", kind, ruleID, f.ruleA.ID)
		}
	}
	if out.Active != 2 {
		t.Errorf("active = %d, want 2 (both seen within the active window)", out.Active)
	}
}

// TestIncidentReadsAreWorkspaceScoped covers both endpoints: ws-B's incident
// must be invisible in ws-A's list and must 404 when addressed by id from ws-A.
func TestIncidentReadsAreWorkspaceScoped(t *testing.T) {
	f := newIncidentFixture(t)
	now := time.Now().UTC()
	f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now)
	foreign := f.raise(t, f.ruleB, f.taskB, store.OutcomeSignalAbsent,
		f.ruleB.AbsenceClassKey(), store.ReasonNoMatches, now)

	rec := httptest.NewRecorder()
	f.handler().list(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/incidents?workspace_id=ws-A", nil))
	var listed struct {
		Incidents []struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"incidents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, i := range listed.Incidents {
		if i.WorkspaceID != "ws-A" {
			t.Fatalf("ws-A list leaked incident from %s", i.WorkspaceID)
		}
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/incidents/"+foreign.Incident.ID+"?workspace_id=ws-A", nil)
	req.SetPathValue("id", foreign.Incident.ID)
	rec = httptest.NewRecorder()
	f.handler().get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace get status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

// TestIncidentGetReturnsOccurrenceLedger asserts the episode history is served
// with the incident — the "shape over time" an operator triages on.
func TestIncidentGetReturnsOccurrenceLedger(t *testing.T) {
	f := newIncidentFixture(t)
	now := time.Now().UTC()
	// Two observations an hour apart fall in different 15-minute buckets, so
	// they become two distinct occurrences on one incident.
	f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now.Add(-time.Hour))
	result := f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/incidents/"+result.Incident.ID+"?workspace_id=ws-A", nil)
	req.SetPathValue("id", result.Incident.ID)
	rec := httptest.NewRecorder()
	f.handler().get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Incident struct {
			ID              string `json:"id"`
			ClassKind       string `json:"class_kind"`
			OccurrenceCount int64  `json:"occurrence_count"`
		} `json:"incident"`
		Occurrences []struct {
			OccurrenceKey string `json:"occurrence_key"`
		} `json:"occurrences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Incident.ClassKind != store.IncidentClassAbsence {
		t.Errorf("class_kind = %q, want %q", out.Incident.ClassKind, store.IncidentClassAbsence)
	}
	if len(out.Occurrences) != 2 {
		t.Fatalf("occurrences = %d, want 2: %+v", len(out.Occurrences), out.Occurrences)
	}
	if out.Occurrences[0].OccurrenceKey == out.Occurrences[1].OccurrenceKey {
		t.Errorf("occurrences share a bucket key %q", out.Occurrences[0].OccurrenceKey)
	}
}
