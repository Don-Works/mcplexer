// monitoring_notify_outcome_test.go — the REST notify boundary must not report
// success for a route that never accepted a message.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/logwatch/escalate"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// scriptedNotifier returns a canned outcome, standing in for the dispatcher so
// the boundary can be tested without driving a real fan-out.
type scriptedNotifier struct {
	outcome escalate.Outcome
	err     error
}

func (s *scriptedNotifier) Notify(context.Context, distill.Notification) error { return s.err }

func (s *scriptedNotifier) NotifyWithOutcome(
	context.Context, distill.Notification,
) (escalate.Outcome, error) {
	return s.outcome, s.err
}

func newNotifyRouter(t *testing.T, n *scriptedNotifier) http.Handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateWorkspace(context.Background(),
		&store.Workspace{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return NewRouter(RouterDeps{Store: db, MonitoringNotifier: n})
}

func postNotify(t *testing.T, router http.Handler) (int, notifyResponse) {
	t.Helper()
	rr := doMonitoringRoute(t, router, http.MethodPost, "/api/v1/monitoring/notify",
		`{"workspace_id":"acme","severity":"error","title":"probe","body":"b","new_incident":true}`)
	var body notifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode notify response: %v (body %s)", err, rr.Body)
	}
	return rr.Code, body
}

// TestNotifyReportsReleasedToBackoffAsFailure is the send-7 regression.
//
// After maxReminderDeliveryRetries the dispatcher releases a failing reminder
// to policy backoff and returns nil. The backoff is correct. Reporting it as
// 200 dispatched:true to a caller who asked "did this send work?" is not — the
// rig measured exactly this against a route that had rejected all eight sends.
func TestNotifyReportsReleasedToBackoffAsFailure(t *testing.T) {
	router := newNotifyRouter(t, &scriptedNotifier{
		// Released to backoff: a failed outcome carried out on a nil error.
		outcome: escalate.Outcome{
			Status: escalate.StatusFailed, Attempted: 1,
			Failures: []string{"incidents: delivery failed"},
		},
		err: nil,
	})

	code, body := postNotify(t, router)
	if code == http.StatusOK {
		t.Fatalf("released-to-backoff reported %d — a route that has never "+
			"accepted a message must not answer 200", code)
	}
	if code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", code)
	}
	if body.Dispatched {
		t.Error("dispatched:true for a notification nobody received")
	}
	if body.Status != string(escalate.StatusFailed) {
		t.Errorf("status = %q, want %q", body.Status, escalate.StatusFailed)
	}
	if len(body.Failures) == 0 {
		t.Error("no failure detail — the caller cannot tell what went wrong")
	}
}

// TestNotifyDistinguishesTheFourOutcomes: the caller must be able to tell a
// legitimate no-op from a throttle from a refusal. Collapsing them is what made
// the original defect invisible.
func TestNotifyDistinguishesTheFourOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		outcome    escalate.Outcome
		wantCode   int
		dispatched bool
	}{
		{
			name: "delivered",
			outcome: escalate.Outcome{
				Status: escalate.StatusDelivered, Delivered: 1, Attempted: 1,
			},
			wantCode: http.StatusOK, dispatched: true,
		},
		{
			// No route was eligible — every channel sits above this severity,
			// or none is configured. Nothing was tried so nothing was lost,
			// but the caller must still see that nobody was told.
			name:     "not attempted is a no-op, not a success",
			outcome:  escalate.Outcome{Status: escalate.StatusNotAttempted},
			wantCode: http.StatusOK, dispatched: false,
		},
		{
			// Suppression is deliberately NOT an error: making it one pushes
			// callers to retry around the throttle, and spam gets alerting
			// muted — the original incident by another route.
			name: "suppressed by throttle",
			outcome: escalate.Outcome{
				Status: escalate.StatusSuppressed, Suppressed: "workspace hourly notify cap",
			},
			wantCode: http.StatusOK, dispatched: false,
		},
		{
			name: "attempted and refused",
			outcome: escalate.Outcome{
				Status: escalate.StatusFailed, Attempted: 2,
				Failures: []string{"incidents: delivery failed", "pager: delivery failed"},
			},
			wantCode: http.StatusBadGateway, dispatched: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newNotifyRouter(t, &scriptedNotifier{outcome: tc.outcome})
			code, body := postNotify(t, router)
			if code != tc.wantCode {
				t.Errorf("status = %d, want %d", code, tc.wantCode)
			}
			if body.Dispatched != tc.dispatched {
				t.Errorf("dispatched = %v, want %v", body.Dispatched, tc.dispatched)
			}
			if body.Status != string(tc.outcome.Status) {
				t.Errorf("status = %q, want %q", body.Status, tc.outcome.Status)
			}
		})
	}
}

// TestNotifySuppressionReasonIsVisible: "suppressed because we already know
// this route is broken" is a truthful answer. An opaque 200 is not.
func TestNotifySuppressionReasonIsVisible(t *testing.T) {
	router := newNotifyRouter(t, &scriptedNotifier{
		outcome: escalate.Outcome{
			Status: escalate.StatusSuppressed, Suppressed: "workspace hourly notify cap",
		},
	})
	_, body := postNotify(t, router)
	if body.Suppressed != "workspace hourly notify cap" {
		t.Errorf("suppressed = %q, want the reason — a caller told 'no' deserves 'why'",
			body.Suppressed)
	}
}

// TestNotifyInternalErrorStillErrors: a genuine internal failure (no outcome
// produced at all) must keep reporting as an error rather than being dressed up
// as a delivery verdict.
func TestNotifyInternalErrorStillErrors(t *testing.T) {
	router := newNotifyRouter(t, &scriptedNotifier{
		outcome: escalate.Outcome{}, // no status — nothing was decided
		err:     context.DeadlineExceeded,
	})
	rr := doMonitoringRoute(t, router, http.MethodPost, "/api/v1/monitoring/notify",
		`{"workspace_id":"acme","severity":"error","title":"probe","new_incident":true}`)
	if rr.Code < 400 {
		t.Fatalf("internal failure reported %d, want an error status", rr.Code)
	}
}
