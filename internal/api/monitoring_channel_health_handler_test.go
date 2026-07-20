// monitoring_channel_health_handler_test.go — channel delivery health as seen
// through the REAL router.
//
// The defect this covers was never a detection failure: escalate already knew a
// route was broken and said so in a log line. It was that no API could be asked
// "is my alerting working?", so a dead gchat webhook survived six days. These
// tests assert the answer is reachable over HTTP, is not forgeable, and does
// not carry the credential that broke it.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newChannelHealthFixture returns the router plus the store behind it, so a
// test can drive health through the dispatcher's store API and read it back
// over HTTP — the two halves that have to agree.
func newChannelHealthFixture(t *testing.T) (http.Handler, *sqlite.DB, *store.MonitoringChannel) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "chanhealth.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	c := &store.MonitoringChannel{
		WorkspaceID: "acme", Name: "incidents", Kind: store.ChannelKindGChatWebhook,
		ConfigJSON:  `{"auth_scope_id":"scope-test","webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"}`,
		MinSeverity: store.SeverityWarn, Enabled: true,
	}
	if err := db.CreateMonitoringChannel(ctx, c); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return NewRouter(RouterDeps{Store: db}), db, c
}

// decodeChannel parses one channel object out of a response body.
func decodeChannel(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode channel: %v (body %s)", err, body)
	}
	return out
}

// driveFailure mirrors production order: targeting is recorded before the
// throttle, then the attempt fails. Health derives from the targeting debt, so
// a test that only wrote failures would assert a state the dispatcher cannot
// produce.
func driveFailure(t *testing.T, db *sqlite.DB, ctx context.Context, id string, at time.Time, reason string) {
	t.Helper()
	if err := db.RecordMonitoringChannelTargeted(ctx, []string{id}, at); err != nil {
		t.Fatalf("record targeted: %v", err)
	}
	if err := db.RecordMonitoringChannelFailure(ctx, id, at, reason); err != nil {
		t.Fatalf("record failure: %v", err)
	}
}

// TestChannelHealthExposedOnGet is the core ask: the health of a route is
// readable from the route's own resource, as a derived state rather than as
// counters a caller has to interpret.
func TestChannelHealthExposedOnGet(t *testing.T) {
	router, db, c := newChannelHealthFixture(t)
	ctx := context.Background()

	at := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, c.ID, at.Add(time.Duration(i)*time.Minute),
			"delivery failed: webhook status 400")
	}

	rr := doMonitoringRoute(t, router, http.MethodGet, "/api/v1/monitoring-channels/"+c.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	got := decodeChannel(t, rr.Body.Bytes())

	if got["health"] != store.ChannelHealthBroken {
		t.Errorf("health = %v, want %q", got["health"], store.ChannelHealthBroken)
	}
	if got["broken"] != true {
		t.Errorf("broken = %v, want true", got["broken"])
	}
	if got["consecutive_failures"] != float64(store.ChannelBrokenThreshold) {
		t.Errorf("consecutive_failures = %v, want %d",
			got["consecutive_failures"], store.ChannelBrokenThreshold)
	}
	if _, ok := got["first_failure_at"]; !ok {
		t.Error("first_failure_at absent — 'broken since when' is unanswerable")
	}
	// last_success_at is the question an operator asks first. A route that
	// has never delivered must not simply omit the concept; here it is
	// absent-because-never, and `health` carries that meaning explicitly.
	if _, ok := got["last_success_at"]; ok {
		t.Errorf("last_success_at present for never-delivered channel: %v", got["last_success_at"])
	}
}

// TestChannelHealthListIsProbeSafe covers the trap the integration rig hit: any
// unknown /api/v1/... path falls through to the SPA catch-all and answers 200
// with index.html, so a probe keying on status alone reports every endpoint as
// present. A probe for THIS capability must be able to tell a real channel list
// from the SPA, which means asserting JSON and asserting the field exists.
func TestChannelHealthListIsProbeSafe(t *testing.T) {
	router, db, c := newChannelHealthFixture(t)
	ctx := context.Background()
	delivered := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if err := db.RecordMonitoringChannelSuccess(ctx, c.ID, delivered); err != nil {
		t.Fatalf("record success: %v", err)
	}

	rr := doMonitoringRoute(t, router,
		http.MethodGet, "/api/v1/monitoring-channels?workspace_id=acme", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want JSON — an HTML body here is the SPA catch-all", ct)
	}
	if strings.Contains(rr.Body.String(), "<!doctype") ||
		strings.Contains(rr.Body.String(), "<html") {
		t.Fatalf("SPA HTML served for a real API route: %s", rr.Body)
	}

	var list []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("list did not decode as JSON array: %v (body %s)", err, rr.Body)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0]["health"] != store.ChannelHealthHealthy {
		t.Errorf("health = %v, want %q", list[0]["health"], store.ChannelHealthHealthy)
	}
	if list[0]["broken"] != false {
		t.Errorf("broken = %v, want false", list[0]["broken"])
	}
	if list[0]["last_success_at"] == nil {
		t.Error("last_success_at absent after a delivery — the first question an operator asks")
	}
}

// TestChannelHealthNotForgeableOverAPI: health is established by delivery, not
// by a PATCH. A route an operator has "fixed" in the UI but which has not
// actually delivered must keep reading broken — otherwise the field becomes a
// claim rather than evidence, and the six-day outage repeats with a green tick
// on top of it.
func TestChannelHealthNotForgeableOverAPI(t *testing.T) {
	router, db, c := newChannelHealthFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, c.ID, at, "webhook status 400")
	}

	rr := doMonitoringRoute(t, router, http.MethodPatch, "/api/v1/monitoring-channels/"+c.ID,
		`{"consecutive_failures":0,"broken":false,"health":"healthy",`+
			`"last_error":"","last_success_at":"2026-07-20T09:00:00Z"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}

	rr = doMonitoringRoute(t, router, http.MethodGet, "/api/v1/monitoring-channels/"+c.ID, "")
	got := decodeChannel(t, rr.Body.Bytes())
	if got["health"] != store.ChannelHealthBroken {
		t.Errorf("health = %v after forging PATCH, want %q", got["health"], store.ChannelHealthBroken)
	}
	if got["broken"] != true {
		t.Errorf("broken = %v after forging PATCH, want true", got["broken"])
	}
	if got["last_success_at"] != nil {
		t.Errorf("last_success_at forged to %v, want absent", got["last_success_at"])
	}
}

// TestChannelHealthRoundTripPatch is the regression for the way adding a
// derived field nearly broke channel editing outright. The API decodes with
// DisallowUnknownFields and PATCH is a read-modify-write, so a response field
// the request cannot carry turns "GET, change the webhook, PATCH it back" —
// exactly what an operator does after finding a route broken — into a 400.
func TestChannelHealthRoundTripPatch(t *testing.T) {
	router, _, c := newChannelHealthFixture(t)

	rr := doMonitoringRoute(t, router, http.MethodGet, "/api/v1/monitoring-channels/"+c.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rr.Code)
	}
	// Feed the response straight back with one real edit, byte for byte —
	// health and broken included, as any honest client would.
	body := decodeChannel(t, rr.Body.Bytes())
	body["min_severity"] = store.SeverityCritical
	edited, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal edit: %v", err)
	}

	rr = doMonitoringRoute(t, router, http.MethodPatch,
		"/api/v1/monitoring-channels/"+c.ID, string(edited))
	if rr.Code != http.StatusOK {
		t.Fatalf("round-trip patch status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	if got := decodeChannel(t, rr.Body.Bytes()); got["min_severity"] != store.SeverityCritical {
		t.Errorf("min_severity = %v, want %q", got["min_severity"], store.SeverityCritical)
	}

	// Strictness must survive: a typo is still a 400, not a silent no-op.
	rr = doMonitoringRoute(t, router, http.MethodPatch,
		"/api/v1/monitoring-channels/"+c.ID, `{"nmae":"typo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("typo'd field status = %d, want 400 — strictness was lost", rr.Code)
	}
}

// TestChannelHealthAPIDoesNotLeakCredential is the P0 guard at the surface that
// actually exposes it. A gchat webhook URL embeds key+token and IS the
// credential; last_error is served to anyone who can list channels.
func TestChannelHealthAPIDoesNotLeakCredential(t *testing.T) {
	router, db, c := newChannelHealthFixture(t)
	ctx := context.Background()
	leak := "post https://chat.example.com/v1/spaces/AAA/messages?key=AIzaLEAKED&token=deadbeef: 400"
	driveFailure(t, db, ctx, c.ID, time.Now().UTC(), leak)

	rr := doMonitoringRoute(t, router, http.MethodGet, "/api/v1/monitoring-channels/"+c.ID, "")
	body := rr.Body.String()
	for _, secret := range []string{"AIzaLEAKED", "deadbeef", "key=", "token="} {
		if strings.Contains(body, secret) {
			t.Fatalf("API leaked %q: %s", secret, body)
		}
	}
	got := decodeChannel(t, rr.Body.Bytes())
	if le, _ := got["last_error"].(string); !strings.Contains(le, "400") {
		t.Errorf("last_error lost the diagnosis: %q", le)
	}
}

// TestChannelHealthStatusSummary covers the aggregate an operator actually
// asks for: one call, "is my alerting working?". notify_enabled cannot answer
// it — the six-day outage ran with notify_enabled true throughout, because the
// notifier WAS wired and the route behind it was dead.
func TestChannelHealthStatusSummary(t *testing.T) {
	router, db, broken := newChannelHealthFixture(t)
	ctx := context.Background()

	// A second route that works, so the summary has both states to reduce.
	working := &store.MonitoringChannel{
		WorkspaceID: "acme", Name: "mesh-sink", Kind: store.ChannelKindMesh,
		ConfigJSON: `{}`, MinSeverity: store.SeverityError, Enabled: true,
	}
	if err := db.CreateMonitoringChannel(ctx, working); err != nil {
		t.Fatalf("create working channel: %v", err)
	}
	if err := db.RecordMonitoringChannelSuccess(ctx, working.ID, time.Now().UTC()); err != nil {
		t.Fatalf("record success: %v", err)
	}
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, broken.ID, time.Now().UTC(), "webhook status 400")
	}

	rr := doMonitoringRoute(t, router, http.MethodGet,
		"/api/v1/monitoring/status?workspace_id=acme", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var body struct {
		NotifyEnabled bool `json:"notify_enabled"`
		Channels      *struct {
			Total       int      `json:"total"`
			Healthy     int      `json:"healthy"`
			Broken      int      `json:"broken"`
			BrokenNames []string `json:"broken_names"`
			AllBroken   bool     `json:"all_broken"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v (body %s)", err, rr.Body)
	}
	if body.Channels == nil {
		t.Fatal("status carried no channel health for a named workspace")
	}
	if body.Channels.Total != 2 || body.Channels.Broken != 1 || body.Channels.Healthy != 1 {
		t.Fatalf("summary = %+v, want total 2 / broken 1 / healthy 1", body.Channels)
	}
	if len(body.Channels.BrokenNames) != 1 || body.Channels.BrokenNames[0] != "incidents" {
		t.Errorf("broken_names = %v, want [incidents] — a count without names is another lookup",
			body.Channels.BrokenNames)
	}
	if body.Channels.AllBroken {
		t.Error("all_broken true while a healthy route exists")
	}

	// Backward compatibility: without workspace_id the block is absent, not
	// an error — existing callers of this endpoint must be unaffected.
	rr = doMonitoringRoute(t, router, http.MethodGet, "/api/v1/monitoring/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("bare status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `"channels"`) {
		t.Errorf("channels block present without workspace_id: %s", rr.Body)
	}
}

// TestChannelHealthAllBrokenIsStated: when every route is down there is no
// path to the operator, and the product must say so plainly on a surface that
// does not depend on those routes rather than implying a page is on its way.
func TestChannelHealthAllBrokenIsStated(t *testing.T) {
	router, db, broken := newChannelHealthFixture(t)
	ctx := context.Background()
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, broken.ID, time.Now().UTC(), "webhook status 400")
	}

	rr := doMonitoringRoute(t, router, http.MethodGet,
		"/api/v1/monitoring/status?workspace_id=acme", "")
	var body struct {
		Channels struct {
			AllBroken bool `json:"all_broken"`
			Broken    int  `json:"broken"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Channels.AllBroken {
		t.Fatal("all_broken false while every route is dead — the one state where " +
			"no notification can reach anyone must be stated, not inferred")
	}
}
