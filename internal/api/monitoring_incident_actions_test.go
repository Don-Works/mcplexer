// monitoring_incident_actions_test.go — HTTP coverage for the operator action
// surface. The store-level semantics (pause vs escalation, expiry, recurrence)
// are pinned next to the store; here the assertions are the wire contract:
// actor is mandatory, an unbounded silence is refused, a foreign incident is a
// 404, and the suppression read path returns what was paused with attribution.
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (f *incidentFixture) actionHandler() *monitoringIncidentActionHandler {
	return &monitoringIncidentActionHandler{store: f.db, resolution: f.db}
}

// post issues one action request against the handler method, wiring the path id
// and workspace_id exactly as the router would.
func post(t *testing.T, fn http.HandlerFunc, id, wsID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?workspace_id="+wsID, reader)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func (f *incidentFixture) raiseAbsence(t *testing.T) string {
	t.Helper()
	res := f.raise(t, f.ruleA, f.taskA, store.OutcomeSignalAbsent,
		f.ruleA.AbsenceClassKey(), store.ReasonNoMatches, time.Now().UTC())
	return res.Incident.ID
}

func TestIncidentAckEndpointAttributesAndSurfaces(t *testing.T) {
	f := newIncidentFixture(t)
	h := f.actionHandler()
	id := f.raiseAbsence(t)

	rec := post(t, h.ack, id, "ws-A", map[string]string{"actor": "max"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ack status = %d (body %s)", rec.Code, rec.Body)
	}
	var acked struct {
		Incident struct {
			AckedAt *time.Time `json:"acked_at"`
			AckedBy string     `json:"acked_by"`
		} `json:"incident"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &acked); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if acked.Incident.AckedAt == nil || acked.Incident.AckedBy != "max" {
		t.Fatalf("ack not attributed: %+v", acked.Incident)
	}

	// The suppression read path must list it.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitoring/suppressions?workspace_id=ws-A", nil)
	sup := httptest.NewRecorder()
	h.suppressions(sup, req)
	if sup.Code != http.StatusOK {
		t.Fatalf("suppressions status = %d (body %s)", sup.Code, sup.Body)
	}
	var listed struct {
		Suppressed []struct {
			ID        string `json:"id"`
			AckActive bool   `json:"ack_active"`
		} `json:"suppressed"`
	}
	if err := json.Unmarshal(sup.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode suppressions: %v", err)
	}
	if len(listed.Suppressed) != 1 || listed.Suppressed[0].ID != id || !listed.Suppressed[0].AckActive {
		t.Fatalf("acked incident not surfaced as suppressed: %+v", listed.Suppressed)
	}
}

func TestIncidentSilenceEndpointValidatesDuration(t *testing.T) {
	f := newIncidentFixture(t)
	h := f.actionHandler()
	id := f.raiseAbsence(t)

	// Missing duration → 400 at the handler.
	if rec := post(t, h.silence, id, "ws-A", map[string]string{"actor": "max"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("silence without duration = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	// Beyond the store's bound → 400 (ErrMonitoringSilenceUnbounded).
	unbounded := map[string]string{"actor": "max", "duration": "200h"}
	if rec := post(t, h.silence, id, "ws-A", unbounded); rec.Code != http.StatusBadRequest {
		t.Fatalf("unbounded silence = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	// A bounded silence is accepted.
	ok := map[string]string{"actor": "max", "duration": "2h"}
	if rec := post(t, h.silence, id, "ws-A", ok); rec.Code != http.StatusOK {
		t.Fatalf("bounded silence = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

func TestIncidentActionEndpointRequiresActor(t *testing.T) {
	f := newIncidentFixture(t)
	h := f.actionHandler()
	id := f.raiseAbsence(t)
	if rec := post(t, h.ack, id, "ws-A", map[string]string{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("ack without actor = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

func TestIncidentActionEndpointIsWorkspaceScoped(t *testing.T) {
	f := newIncidentFixture(t)
	h := f.actionHandler()
	id := f.raiseAbsence(t)
	// ws-B may not act on ws-A's incident: it reads as not-found, not a 500.
	if rec := post(t, h.ack, id, "ws-B", map[string]string{"actor": "eve"}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace ack = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

func TestIncidentDismissEndpointResolves(t *testing.T) {
	f := newIncidentFixture(t)
	h := f.actionHandler()
	id := f.raiseAbsence(t)
	rec := post(t, h.dismiss, id, "ws-A", map[string]string{"actor": "max", "status_text": "wontfix"})
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Resolution struct {
			Outcome         string `json:"outcome"`
			ResolvedByActor string `json:"resolved_by_actor"`
		} `json:"resolution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Resolution.Outcome != store.MonitoringOutcomeBenign || out.Resolution.ResolvedByActor != "max" {
		t.Fatalf("dismiss did not return an attributed benign resolution: %+v", out.Resolution)
	}
	// It shows on the dismissed side of the suppression read path.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitoring/suppressions?workspace_id=ws-A", nil)
	sup := httptest.NewRecorder()
	h.suppressions(sup, req)
	var listed struct {
		Dismissed []struct {
			IncidentID string `json:"incident_id"`
		} `json:"dismissed"`
	}
	if err := json.Unmarshal(sup.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode suppressions: %v", err)
	}
	if len(listed.Dismissed) != 1 || listed.Dismissed[0].IncidentID != id {
		t.Fatalf("dismissed incident not surfaced: %+v", listed.Dismissed)
	}
}
