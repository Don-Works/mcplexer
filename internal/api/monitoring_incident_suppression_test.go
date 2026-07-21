// monitoring_incident_suppression_test.go — proves the incident GET view
// reflects operator ack/silence state as an IN-FORCE fact, not a raw column.
//
// The derived flags (suppressed / ack_active / silence_active) are computed off
// the same predicates the notification policy uses, so these tests drive the
// REAL AckMonitoringIncident / SilenceMonitoringIncident / UnsilenceMonitoringIncident
// store paths (the ones behind the actions stream's POST endpoints) and read the
// result back through the GET view a client renders. The expiry case is the one
// a client cannot get right on its own: silenced_until is in the past, so the
// row must read NOT silenced even though the column is set.
//
// Reuses the incidentFixture defined in monitoring_incidents_handler_test.go.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestIncidentViewAckAndSilenceInForce — an acknowledged incident and a
// silenced incident each read back as suppressed, with the right sub-flag and
// attribution, and the states are independent (ack on one is not silence).
func TestIncidentViewAckAndSilenceInForce(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	acked := f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now)
	silenced := f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalCollection,
		f.ruleA.CollectionClassKey(), store.ReasonPullFailing, now)

	if _, err := f.db.AckMonitoringIncident(ctx, store.MonitoringIncidentActionRef{
		WorkspaceID: "ws-A", IncidentID: acked.Incident.ID, Actor: "alice",
	}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := f.db.SilenceMonitoringIncident(ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: store.MonitoringIncidentActionRef{
			WorkspaceID: "ws-A", IncidentID: silenced.Incident.ID, Actor: "bob",
		},
		Duration: time.Hour,
	}); err != nil {
		t.Fatalf("silence: %v", err)
	}

	ackView := f.getView(t, acked.Incident.ID)
	if !ackView.Suppressed || !ackView.AckActive || ackView.SilenceActive {
		t.Errorf("acked incident flags wrong: suppressed=%v ack=%v silence=%v",
			ackView.Suppressed, ackView.AckActive, ackView.SilenceActive)
	}
	if ackView.AckedBy != "alice" || ackView.AckedAt == nil {
		t.Errorf("ack attribution missing: by=%q at=%v", ackView.AckedBy, ackView.AckedAt)
	}

	silView := f.getView(t, silenced.Incident.ID)
	if !silView.Suppressed || !silView.SilenceActive || silView.AckActive {
		t.Errorf("silenced incident flags wrong: suppressed=%v ack=%v silence=%v",
			silView.Suppressed, silView.AckActive, silView.SilenceActive)
	}
	if silView.SilencedBy != "bob" || silView.SilencedUntil == nil ||
		!silView.SilencedUntil.After(now) {
		t.Errorf("silence attribution wrong: by=%q until=%v", silView.SilencedBy, silView.SilencedUntil)
	}

	// And it reaches the wire: the GET-by-id handler serialises the flags.
	assertHandlerSuppression(t, f, silenced.Incident.ID)
}

// TestIncidentViewSilenceExpiryAndUnsilence — an expired silence window reads
// NOT silenced despite silenced_until being populated, and unsilence clears the
// state entirely. These are the two ways a "silenced" chip must turn itself off.
func TestIncidentViewSilenceExpiryAndUnsilence(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	inc := f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, now)

	// Silence taken two hours ago for one hour: the window closed an hour ago.
	if _, err := f.db.SilenceMonitoringIncident(ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: store.MonitoringIncidentActionRef{
			WorkspaceID: "ws-A", IncidentID: inc.Incident.ID, Actor: "bob",
			At: now.Add(-2 * time.Hour),
		},
		Duration: time.Hour,
	}); err != nil {
		t.Fatalf("silence: %v", err)
	}
	expired := f.getView(t, inc.Incident.ID)
	if expired.SilencedUntil == nil {
		t.Fatal("silenced_until should remain populated after expiry")
	}
	if expired.Suppressed || expired.SilenceActive {
		t.Errorf("expired silence still reads in-force: suppressed=%v silence=%v",
			expired.Suppressed, expired.SilenceActive)
	}

	// Re-silence live, then unsilence: the state must clear completely.
	if _, err := f.db.SilenceMonitoringIncident(ctx, store.MonitoringIncidentSilenceInput{
		MonitoringIncidentActionRef: store.MonitoringIncidentActionRef{
			WorkspaceID: "ws-A", IncidentID: inc.Incident.ID, Actor: "bob",
		},
		Duration: time.Hour,
	}); err != nil {
		t.Fatalf("re-silence: %v", err)
	}
	if v := f.getView(t, inc.Incident.ID); !v.SilenceActive {
		t.Fatal("re-silence should be in force")
	}
	if _, err := f.db.UnsilenceMonitoringIncident(ctx, store.MonitoringIncidentActionRef{
		WorkspaceID: "ws-A", IncidentID: inc.Incident.ID, Actor: "bob",
	}); err != nil {
		t.Fatalf("unsilence: %v", err)
	}
	cleared := f.getView(t, inc.Incident.ID)
	if cleared.Suppressed || cleared.SilenceActive || cleared.SilencedUntil != nil {
		t.Errorf("unsilence did not clear: suppressed=%v silence=%v until=%v",
			cleared.Suppressed, cleared.SilenceActive, cleared.SilencedUntil)
	}
}

// getView reads one incident back through the store view the GET handler serves.
func (f *incidentFixture) getView(t *testing.T, id string) *store.MonitoringIncidentView {
	t.Helper()
	view, err := f.db.GetMonitoringIncident(context.Background(), "ws-A", id)
	if err != nil {
		t.Fatalf("get incident view: %v", err)
	}
	return view
}

// assertHandlerSuppression confirms the derived flags survive JSON serialisation
// through the real GET-by-id handler, under the exact keys the dashboard reads.
func assertHandlerSuppression(t *testing.T, f *incidentFixture, id string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/incidents/"+id+"?workspace_id=ws-A", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	f.handler().get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Incident struct {
			Suppressed    bool `json:"suppressed"`
			SilenceActive bool `json:"silence_active"`
		} `json:"incident"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Incident.Suppressed || !out.Incident.SilenceActive {
		t.Errorf("wire flags not set: suppressed=%v silence_active=%v",
			out.Incident.Suppressed, out.Incident.SilenceActive)
	}
}
