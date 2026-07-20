package sqlite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedChannel creates one enabled gchat route for health tests.
func seedChannel(t *testing.T, db interface {
	CreateMonitoringChannel(ctx context.Context, c *store.MonitoringChannel) error
}, ctx context.Context, wsID, name string) *store.MonitoringChannel {
	t.Helper()
	c := &store.MonitoringChannel{
		WorkspaceID: wsID, Name: name, Kind: store.ChannelKindGChatWebhook,
		ConfigJSON:  `{"auth_scope_id":"scope-test","webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"}`,
		MinSeverity: store.SeverityWarn, Enabled: true,
	}
	if err := db.CreateMonitoringChannel(ctx, c); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return c
}

// driveFailure mirrors production order: every delivery attempt is preceded by
// the targeting record (written before the throttle), then the failure record.
// Tests that only wrote failures were asserting a state the dispatcher cannot
// actually produce.
func driveFailure(t *testing.T, db *sqlite.DB, ctx context.Context, id string, at time.Time, reason string) {
	t.Helper()
	if err := db.RecordMonitoringChannelTargeted(ctx, []string{id}, at); err != nil {
		t.Fatalf("record targeted: %v", err)
	}
	if err := db.RecordMonitoringChannelFailure(ctx, id, at, reason); err != nil {
		t.Fatalf("record failure: %v", err)
	}
}

// TestChannelHealthNewChannelIsUnknownNotHealthy pins the distinction the whole
// feature rests on: a channel nobody has ever delivered through is NOT healthy.
// Folding "never tried" into "healthy" is how a misconfigured route looks fine
// right up until the first incident it is supposed to carry.
func TestChannelHealthNewChannelIsUnknownNotHealthy(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.HealthState() != store.ChannelHealthUnknown {
		t.Fatalf("new channel health = %q, want %q", got.HealthState(), store.ChannelHealthUnknown)
	}
	if got.Broken() {
		t.Fatal("new channel must not report broken")
	}
	if got.LastSuccessAt != nil {
		t.Fatalf("new channel last_success_at = %v, want nil", got.LastSuccessAt)
	}
}

// TestChannelHealthSurvivesSuppression is the regression for the six-day
// outage. A gchat webhook returned HTTP 400 on every attempt, logged once, and
// was then masked by the workspace hourly notify cap: one failure against many
// suppressions. Suppression writes nothing here, so the stored run must keep
// counting the failures it DID see and keep reporting broken across the gap —
// the state must not decay just because the throttle stopped the route being
// tried.
func TestChannelHealthSurvivesSuppression(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	start := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, c.ID, start.Add(time.Duration(i)*time.Minute),
			"delivery failed: escalate: webhook status 400")
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ConsecutiveFailures != store.ChannelBrokenThreshold {
		t.Fatalf("consecutive_failures = %d, want %d",
			got.ConsecutiveFailures, store.ChannelBrokenThreshold)
	}
	if !got.Broken() {
		t.Fatalf("health = %q, want %q", got.HealthState(), store.ChannelHealthBroken)
	}
	// first_failure_at must mark the START of the run, not the latest
	// failure — "broken since 06:12" is the sentence an operator needs, and
	// a field that walks forward on every attempt can never produce it.
	if got.FirstFailureAt == nil || !got.FirstFailureAt.Equal(start) {
		t.Fatalf("first_failure_at = %v, want %v", got.FirstFailureAt, start)
	}
	wantLast := start.Add(time.Duration(store.ChannelBrokenThreshold-1) * time.Minute)
	if got.LastFailureAt == nil || !got.LastFailureAt.Equal(wantLast) {
		t.Fatalf("last_failure_at = %v, want %v", got.LastFailureAt, wantLast)
	}

	// Six days later, with nothing in between because the cap suppressed
	// every notification, the route must still read broken.
	sixDaysOn := start.Add(6 * 24 * time.Hour)
	if got.FailingFor(sixDaysOn) != 6*24*time.Hour {
		t.Fatalf("failing_for = %v, want 144h", got.FailingFor(sixDaysOn))
	}
}

// TestChannelHealthRecoveryClearsRun is the other half: a channel that starts
// working again must stop reading broken, and must not keep a stale error that
// makes a live route look dead to every human who scans the list.
func TestChannelHealthRecoveryClearsRun(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	failed := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		driveFailure(t, db, ctx, c.ID, failed.Add(time.Duration(i)*time.Minute),
			"webhook status 400")
	}
	fixed := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if err := db.RecordMonitoringChannelSuccess(ctx, c.ID, fixed); err != nil {
		t.Fatalf("record success: %v", err)
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.HealthState() != store.ChannelHealthHealthy {
		t.Fatalf("health = %q, want %q", got.HealthState(), store.ChannelHealthHealthy)
	}
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0", got.ConsecutiveFailures)
	}
	if got.FirstFailureAt != nil {
		t.Fatalf("first_failure_at = %v, want nil after recovery", got.FirstFailureAt)
	}
	if got.LastError != "" {
		t.Fatalf("last_error = %q, want cleared after recovery", got.LastError)
	}
	if got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(fixed) {
		t.Fatalf("last_success_at = %v, want %v", got.LastSuccessAt, fixed)
	}
	// last_failure_at is deliberately KEPT: "working now, last failed on the
	// 14th" is useful history and does not misrepresent the present.
	if got.LastFailureAt == nil {
		t.Fatal("last_failure_at must survive recovery as history")
	}
}

// TestChannelHealthDegradedBeforeBroken pins the middle state. One failed send
// is a wobble, not an outage; calling it broken would train the operator to
// ignore the field, which is the same end state as not having it.
func TestChannelHealthDegradedBeforeBroken(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	at := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	driveFailure(t, db, ctx, c.ID, at, "timeout")
	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.HealthState() != store.ChannelHealthDegraded {
		t.Fatalf("health after 1 failure = %q, want %q",
			got.HealthState(), store.ChannelHealthDegraded)
	}
	if got.Broken() {
		t.Fatal("one failure must not report broken")
	}
}

// TestChannelHealthErrorIsRedacted is the P0 guard. last_error is persisted and
// served by the REST API, and a Google Chat webhook URL embeds key+token in its
// query string — the URL IS the credential. Redaction happens in the store so
// no sender can bypass it by forgetting to scrub its own error text.
func TestChannelHealthErrorIsRedacted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	leak := "post https://chat.example.com/v1/spaces/AAA/messages?key=AIzaLEAKED&token=deadbeef: 400"
	if err := db.RecordMonitoringChannelFailure(
		ctx, c.ID, time.Now().UTC(), leak,
	); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, secret := range []string{"AIzaLEAKED", "deadbeef", "key=", "token="} {
		if strings.Contains(got.LastError, secret) {
			t.Fatalf("last_error leaked %q: %s", secret, got.LastError)
		}
	}
	// The diagnosable part must survive, or the field is useless.
	if !strings.Contains(got.LastError, "chat.example.com") {
		t.Fatalf("last_error lost its host: %s", got.LastError)
	}
	if !strings.Contains(got.LastError, "400") {
		t.Fatalf("last_error lost the status code: %s", got.LastError)
	}
}

// TestChannelHealthUpdateCannotForgeHealth: an operator editing a channel must
// not be able to reset or fake its health. UpdateMonitoringChannel's column
// list excludes the health columns, so a PATCH carrying them is inert — health
// is established by delivery, not by assertion.
func TestChannelHealthUpdateCannotForgeHealth(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	at := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		driveFailure(t, db, ctx, c.ID, at, "webhook status 400")
	}

	edit, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	edit.Name = "incidents-renamed"
	edit.ConsecutiveFailures = 0
	edit.LastError = ""
	success := time.Now().UTC()
	edit.LastSuccessAt = &success
	if err := db.UpdateMonitoringChannel(ctx, edit); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Name != "incidents-renamed" {
		t.Fatalf("name not updated: %q", got.Name)
	}
	if !got.Broken() {
		t.Fatalf("health = %q after forged update, want still %q",
			got.HealthState(), store.ChannelHealthBroken)
	}
	if got.LastSuccessAt != nil {
		t.Fatalf("last_success_at forged to %v, want nil", got.LastSuccessAt)
	}
}

// TestChannelHealthUnknownChannel returns the sentinel rather than silently
// succeeding — a health write against a deleted channel is a wiring bug, and a
// no-op UPDATE would hide it.
func TestChannelHealthUnknownChannel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	err := db.RecordMonitoringChannelFailure(ctx, "no-such-channel", now, "boom")
	if !errors.Is(err, store.ErrMonitoringChannelNotFound) {
		t.Fatalf("failure on unknown channel = %v, want ErrMonitoringChannelNotFound", err)
	}
	err = db.RecordMonitoringChannelSuccess(ctx, "no-such-channel", now)
	if !errors.Is(err, store.ErrMonitoringChannelNotFound) {
		t.Fatalf("success on unknown channel = %v, want ErrMonitoringChannelNotFound", err)
	}
}

// Compile-time proof that *sqlite.DB satisfies the consumer-boundary
// interface. The health methods are deliberately NOT on store.Store — folding
// them in forced every store mock in internal/routing and internal/gateway to
// grow two methods and broke both packages' tests. This assertion is what
// keeps the narrow interface honest now that nothing else references it
// structurally.
var _ store.MonitoringChannelHealthStore = (*sqlite.DB)(nil)

// TestChannelHealthBrokenWhileNeverAttempted is the 2026-07-14 row reproduced
// at the store layer: a route suppressed out of the delivery path entirely.
//
// One failure got through before the workspace hourly cap was spent by other
// traffic; after that the route was never attempted again, so its failure count
// froze at one while it went on being owed notification after notification. A
// health check reading consecutive_failures sees a wobble. Reading the debt, it
// sees what it is — a dead route.
func TestChannelHealthBrokenWhileNeverAttempted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	start := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	driveFailure(t, db, ctx, c.ID, start, "webhook status 400")

	// The cap is now spent by unrelated traffic. Notifications keep being
	// generated and this route keeps being eligible, but deliverChannels is
	// never reached, so NO further failure is ever recorded.
	for i := 1; i <= 190; i++ {
		if err := db.RecordMonitoringChannelTargeted(
			ctx, []string{c.ID}, start.Add(time.Duration(i)*time.Minute),
		); err != nil {
			t.Fatalf("record targeted %d: %v", i, err)
		}
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive_failures = %d, want 1 — the premise of this test "+
			"is that suppression froze it", got.ConsecutiveFailures)
	}
	if got.TargetedSinceSuccess != 191 {
		t.Fatalf("targeted_since_success = %d, want 191", got.TargetedSinceSuccess)
	}
	if !got.Broken() {
		t.Fatalf("health = %q, want broken — a route owed 191 notifications that "+
			"delivered none is dead, whatever its frozen failure count says",
			got.HealthState())
	}
}

// TestChannelHealthTargetingClearedBySuccess: the debt must be cleared by a
// real delivery and by nothing else, or a recovered route reads broken forever.
func TestChannelHealthTargetingClearedBySuccess(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "incidents")

	at := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	if err := db.RecordMonitoringChannelTargeted(
		ctx, []string{c.ID}, at,
	); err != nil {
		t.Fatalf("record targeted: %v", err)
	}
	for i := 0; i < 5; i++ {
		driveFailure(t, db, ctx, c.ID, at, "webhook status 400")
	}
	if err := db.RecordMonitoringChannelSuccess(ctx, c.ID, at.Add(time.Hour)); err != nil {
		t.Fatalf("record success: %v", err)
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetedSinceSuccess != 0 {
		t.Fatalf("targeted_since_success = %d, want 0 after a real delivery",
			got.TargetedSinceSuccess)
	}
	if got.HealthState() != store.ChannelHealthHealthy {
		t.Fatalf("health = %q, want healthy", got.HealthState())
	}
}

// TestChannelHealthTargetingIsBatched: one notification fans out to every
// eligible route in a single statement. Per-channel writes would put N round
// trips on the notification path, and a partial failure would count some routes
// and not others — skewing the very comparison health is derived from.
func TestChannelHealthTargetingIsBatched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	a := seedChannel(t, db, ctx, wsID, "route-a")
	b := seedChannel(t, db, ctx, wsID, "route-b")

	at := time.Now().UTC()
	if err := db.RecordMonitoringChannelTargeted(ctx, []string{a.ID, b.ID}, at); err != nil {
		t.Fatalf("record targeted: %v", err)
	}
	for _, id := range []string{a.ID, b.ID} {
		got, err := db.GetMonitoringChannel(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.TargetedSinceSuccess != 1 {
			t.Fatalf("%s targeted_since_success = %d, want 1", got.Name, got.TargetedSinceSuccess)
		}
	}
	// A concurrently deleted channel must not fail the notification: the id
	// set was read moments earlier and a delete is normal operation.
	if err := db.RecordMonitoringChannelTargeted(
		ctx, []string{a.ID, "deleted-channel"}, at,
	); err != nil {
		t.Fatalf("targeting a since-deleted channel must not error: %v", err)
	}
	if err := db.RecordMonitoringChannelTargeted(ctx, nil, at); err != nil {
		t.Fatalf("empty targeting set must be a no-op: %v", err)
	}
}

// TestChannelHealthSuppressionBurstDoesNotBreakHealthyRoute is the false
// positive a live run caught, at the store layer.
//
// The throttle withholds a whole notification before any channel is consulted,
// so a capped burst increments the debt of EVERY eligible route — including one
// that delivered seconds earlier. Counting debt alone reported a working mesh
// sink as broken after three capped sends. An operator who sees healthy routes
// flagged broken switches the flag off, and then nobody is told about anything:
// strictly worse than the silence this feature exists to end.
func TestChannelHealthSuppressionBurstDoesNotBreakHealthyRoute(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)
	c := seedChannel(t, db, ctx, wsID, "working-sink")

	now := time.Now().UTC()
	// It just delivered.
	if err := db.RecordMonitoringChannelSuccess(ctx, c.ID, now.Add(-30*time.Second)); err != nil {
		t.Fatalf("record success: %v", err)
	}
	// Then the workspace hit its hourly cap and the next three notifications
	// were withheld before any channel was consulted.
	for i := 0; i < store.ChannelBrokenThreshold; i++ {
		if err := db.RecordMonitoringChannelTargeted(ctx, []string{c.ID}, now); err != nil {
			t.Fatalf("record targeted: %v", err)
		}
	}

	got, err := db.GetMonitoringChannel(ctx, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetedSinceSuccess != store.ChannelBrokenThreshold {
		t.Fatalf("targeted_since_success = %d, want %d — premise of the test",
			got.TargetedSinceSuccess, store.ChannelBrokenThreshold)
	}
	if got.HealthState() != store.ChannelHealthHealthy {
		t.Fatalf("health = %q for a route that delivered 30s ago and was then "+
			"merely throttled — plain suppression is not a health problem, and "+
			"a permanent amber gets read as no signal at all", got.HealthState())
	}
	// Same row, two hours on with the debt never cleared, IS broken.
	if !got.BrokenAt(now.Add(store.ChannelStaleAfter + time.Minute)) {
		t.Fatal("still not broken after the staleness window — a route owed " +
			"messages it never delivers must eventually surface")
	}
}
