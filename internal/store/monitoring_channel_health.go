package store

// monitoring_channel_health.go — the derived health of one alert route, and
// the guarantee that it is never serialized without it.
//
// The counters on MonitoringChannel are evidence, not an answer. Asking every
// caller — REST, the dashboard, a probe, a future CLI — to re-derive "is this
// route broken" from a failure count and two nullable timestamps is how the
// derivation drifts between surfaces and how one of them quietly gets it wrong.
// The state is computed once, here, and MarshalJSON emits it on every channel
// that leaves the process, so no serialization path can omit it by forgetting.

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// MonitoringChannelHealthStore is the dispatcher's slice of the store for
// recording delivery health. It is defined at the consumer boundary rather
// than folded into MonitoringStore, following MonitoringRenotifyStore and
// MonitoringExpectedSignalStore: adding a health ledger must not force every
// store mock across the tree to grow two methods. Folding it in broke
// internal/routing and internal/gateway test mocks immediately, which is the
// precise cost those two interfaces were written to avoid paying again.
// *sqlite.DB satisfies it.
type MonitoringChannelHealthStore interface {
	// RecordMonitoringChannelFailure extends the channel's consecutive-failure
	// run and stores a redacted reason.
	RecordMonitoringChannelFailure(ctx context.Context, id string, at time.Time, reason string) error
	// RecordMonitoringChannelSuccess clears the run and stamps last_success_at.
	// This is the primary health signal: it is the only one that stays true
	// while a route is suppressed rather than attempted.
	RecordMonitoringChannelSuccess(ctx context.Context, id string, at time.Time) error
	// RecordMonitoringChannelTargeted marks these channels as owed one
	// notification. Called BEFORE the throttle decision, which is the whole
	// point: it is the only health input suppression cannot silence. Batched
	// because one notification fans out to every eligible route at once.
	RecordMonitoringChannelTargeted(ctx context.Context, ids []string, at time.Time) error
}

// Channel health states, ordered by how much attention they deserve.
const (
	// ChannelHealthUnknown — never attempted a delivery, so nothing is
	// proven either way. Deliberately NOT folded into healthy: a route
	// nobody has ever exercised is exactly the route that turns out to be
	// misconfigured the first time it matters.
	ChannelHealthUnknown = "unknown"
	// ChannelHealthHealthy — the last attempt succeeded.
	ChannelHealthHealthy = "healthy"
	// ChannelHealthDegraded — failing, but not yet long enough to call it
	// broken. Endpoints wobble; this is the wobble.
	ChannelHealthDegraded = "degraded"
	// ChannelHealthBroken — a sustained run of failures. Alerts sent to this
	// route are not arriving.
	ChannelHealthBroken = "broken"
)

// ChannelBrokenThreshold is the consecutive-failure run at which a route stops
// being a blip and starts being broken. Three survives a transient endpoint
// wobble while a permanent rejection (bad token, wrong URL) reaches it on the
// third attempt whatever the throttle is doing.
//
// It lives in store rather than in the dispatcher because two places need to
// agree on it: the dispatcher, which escalates to ERROR on crossing it, and
// this package, which reports `broken` over the API. Two constants would let
// the API and the log disagree about the same channel — the API saying healthy
// while the log says broken is worse than either alone. escalate pins its
// threshold to this one and a test in that package asserts they match.
const ChannelBrokenThreshold = 3

// ChannelStaleAfter is how long a route must be owed messages it has not
// delivered before that debt alone counts as broken.
//
// It has to exceed the longest suppression a HEALTHY route can legitimately sit
// under, or the throttle marks working channels broken. The two suppressions
// that withhold a whole notification are the workspace hourly notify cap and
// the per-template cooldown, both one hour, so anything at or under an hour
// would fire on a route that is merely waiting for its budget to reset. Two
// hours clears both with margin for clock skew and the 5m sweep tick.
//
// The cost of the margin is bounded and worth naming: a route that dies while
// suppressed takes up to two hours to be called broken on debt alone. Against
// the six days it actually took, and given the refusal arm reports instantly
// whenever the route IS attempted, that is a good trade. Shortening it below
// the cap would buy minutes and reintroduce the false positive.
const ChannelStaleAfter = 2 * time.Hour

// HealthState derives the channel's current delivery health.
//
// The signal is TargetedSinceSuccess — notifications this route was eligible
// for that it has not delivered — and deliberately NOT ConsecutiveFailures.
// The dispatcher throttles before it consults channels, so a suppressed
// notification is never attempted and the failure counter cannot advance;
// on 2026-07-14 other traffic in the workspace spent the hourly budget and the
// dead webhook's failure count froze at one for six days. Targeting is
// recorded before the throttle, so it advances whether the notification was
// delivered, failed, or suppressed. Health has to hang off the observable that
// suppression cannot silence.
//
// ConsecutiveFailures and LastError remain as colour — they say HOW it is
// failing, which is the diagnosis — but nothing derived here depends on them.
// Every failure is also a targeting, so TargetedSinceSuccess already dominates
// the failure count and no case is lost by preferring it.
func (c MonitoringChannel) HealthState() string {
	return c.HealthStateAt(time.Now().UTC())
}

// HealthStateAt derives health as of now. Two INDEPENDENT kinds of evidence can
// call a route broken, because neither covers the other's case:
//
//   - Direct refusal: consecutive_failures at the threshold. Available only
//     when the route is actually attempted, but conclusive when it is, and
//     immediate — no waiting.
//   - Unpaid debt: targeted_since_success at the threshold AND nothing
//     delivered for longer than any legitimate suppression could last. This is
//     the one that survives the throttle, and the only one that could have
//     caught 2026-07-14.
//
// The debt arm REQUIRES the time condition, and that is not belt-and-braces —
// it is load bearing. The throttle suppresses a whole notification before any
// channel is consulted, so a suppressed burst increments the debt of every
// eligible route, healthy ones included. Counting debt alone therefore marks a
// perfectly working channel broken as soon as its workspace hits the hourly
// cap three times, which a live run reproduced immediately: a mesh sink that
// had just delivered six notifications reported `broken` because the next
// three were capped. That false positive is worse than the silence this whole
// feature exists to fix — an operator who sees healthy routes flagged broken
// switches the flag off, and then nobody is told about anything.
//
// Time is what separates the two: a healthy route's debt is minutes old and
// clears the moment the budget frees; a dead route's is hours or days old and
// never clears.
func (c MonitoringChannel) HealthStateAt(now time.Time) string {
	refused := c.ConsecutiveFailures >= ChannelBrokenThreshold
	inDebt := c.TargetedSinceSuccess >= ChannelBrokenThreshold
	stale := c.UndeliveredFor(now) >= ChannelStaleAfter
	switch {
	case refused || (inDebt && stale):
		return ChannelHealthBroken
	// Degraded requires EVIDENCE OF TROUBLE, not merely unpaid debt. A route
	// that delivered a minute ago and is now waiting on the hourly budget has
	// nothing wrong with it, and the workspace cap is spent routinely — so
	// treating plain suppression as degradation would leave healthy channels
	// showing a warning most of the time. A dashboard that always shows amber
	// gets read as "always amber", which is the same erosion of trust that
	// makes a broken route invisible.
	//
	// So: an actual refusal counts, and so does debt on a route that has never
	// delivered at all (unproven AND already owed something is worth a look).
	// Debt on a route with a recent success does not.
	case c.ConsecutiveFailures > 0 || (c.TargetedSinceSuccess > 0 && c.LastSuccessAt == nil):
		return ChannelHealthDegraded
	case c.LastSuccessAt != nil:
		return ChannelHealthHealthy
	default:
		// Never owed a notification and never delivered one. Unknown, not
		// healthy: an idle route is unproven, not working.
		return ChannelHealthUnknown
	}
}

// UndeliveredFor is how long this route has been owed messages it has not
// delivered — the wall-clock span an operator asks for ("dead since when?").
// It measures from the last success rather than the first failure, because the
// last success is the last moment the route is known to have worked; a route
// suppressed into silence has no failures to measure from.
func (c MonitoringChannel) UndeliveredFor(now time.Time) time.Duration {
	if c.TargetedSinceSuccess == 0 {
		return 0
	}
	switch {
	case c.LastSuccessAt != nil:
		return now.Sub(*c.LastSuccessAt)
	case c.FirstFailureAt != nil:
		// Never delivered, but has been tried: measure from the first refusal.
		return now.Sub(*c.FirstFailureAt)
	case !c.CreatedAt.IsZero():
		// Never delivered and never even attempted — configured wrong and
		// suppressed from birth. Creation is the only honest starting point,
		// and without this arm such a route could never go stale at all.
		return now.Sub(c.CreatedAt)
	}
	return 0
}

// Broken reports whether alerts routed here are known not to be arriving.
func (c MonitoringChannel) BrokenAt(now time.Time) bool {
	return c.HealthStateAt(now) == ChannelHealthBroken
}

// Broken reports whether alerts routed here are known not to be arriving.
func (c MonitoringChannel) Broken() bool {
	return c.HealthState() == ChannelHealthBroken
}

// FailingFor is how long the current failure run has lasted, 0 when healthy.
func (c MonitoringChannel) FailingFor(now time.Time) time.Duration {
	if c.FirstFailureAt == nil {
		return 0
	}
	return now.Sub(*c.FirstFailureAt)
}

// MarshalJSON emits the stored row plus the derived state. The value receiver
// covers both MonitoringChannel and *MonitoringChannel, which matters because
// the REST handlers serialize a slice of pointers on list and a value on
// create. `broken` is a separate boolean rather than something a caller infers
// from the `health` string so that a probe or an alert rule can key on one
// unambiguous field.
func (c MonitoringChannel) MarshalJSON() ([]byte, error) {
	type alias MonitoringChannel // sheds the method set; avoids infinite recursion
	return json.Marshal(struct {
		alias
		Health string `json:"health"`
		Broken bool   `json:"broken"`
	}{alias(c), c.HealthState(), c.Broken()})
}

// UnmarshalJSON accepts the derived fields MarshalJSON emits, and ignores them.
//
// This is not cosmetic. The REST API decodes with DisallowUnknownFields, and
// PATCH /monitoring-channels/{id} is a read-modify-write: the UI GETs a
// channel, changes one thing, and PUTs the whole object back. Emitting `health`
// and `broken` without accepting them turns every such round-trip into a 400 —
// adding a health field would have broken the ability to edit a channel at all,
// including the one thing an operator does after finding a route broken, which
// is fix its config.
//
// Strictness is deliberately preserved rather than dropped: the decoder inside
// still disallows unknown fields, so a typo'd key is still rejected. Only the
// two derived names are tolerated, and they are discarded — health is
// established by delivery, never by assertion. The store's UPDATE column list
// is the second, independent guard on that.
func (c *MonitoringChannel) UnmarshalJSON(data []byte) error {
	type alias MonitoringChannel
	aux := struct {
		*alias
		Health *string `json:"health"`
		Broken *bool   `json:"broken"`
	}{alias: (*alias)(c)}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(&aux)
}

// maxChannelErrorLen bounds what a remote endpoint can write into our database
// through an error message. A webhook that returns a 20 MB HTML error page
// should cost us one short line, not a row that has to be paged in on every
// channel list.
const maxChannelErrorLen = 300

// credentialish masks the shapes that turn an error message into a credential
// leak. The gchat sender already strips its own resolved webhook URL, but this
// runs at the persistence boundary because that is the last point where the
// guarantee can still be made for EVERY sender — including mesh, telegram,
// whatsapp, and whatever is added next by someone who did not read this file.
// A stored secret is worse than a logged one: last_error is served by the REST
// API to anyone who can list channels.
var credentialish = []struct {
	re   *regexp.Regexp
	with string
}{
	// Any URL carrying a query string. Google Chat incoming webhooks put
	// key= and token= in the query and the URL IS the credential, so the
	// query is dropped wholesale rather than by parameter name — an
	// allowlist of "safe" parameters is a guess about someone else's API.
	// The host and path survive, which is what makes the error diagnosable.
	{regexp.MustCompile(`(https?://[^\s?]+)\?\S*`), "$1?[redacted]"},
	// secret:// refs point at a credential and add nothing to a reason. The
	// replacement deliberately contains no ":" or "=" after the word
	// "secret": rules are applied in sequence over the same string, so a
	// placeholder like "secret://[redacted]" gets re-matched by the
	// assignment rule below and degrades to "secret=[redacted]". Placeholders
	// have to be inert with respect to every later rule.
	{regexp.MustCompile(`secret://[A-Za-z0-9_.\-]+`), "[redacted-secret-ref]"},
	// "Bearer <token>" — the one credential idiom where whitespace alone is
	// the delimiter, so it gets its own rule.
	{regexp.MustCompile(`(?i)\bbearer\s+\S+`), "bearer [redacted]"},
	// key=/token:/password= style assignments. An explicit `:` or `=` is
	// REQUIRED: matching on whitespace instead swallowed the word after any
	// occurrence of "auth", turning the real error "get auth scope
	// missing-scope: not found" into "get auth=[redacted] missing-scope: not
	// found". Over-redaction is safe but it is not free — it costs exactly
	// the diagnosability this field exists to provide, and a reason nobody
	// can act on is barely better than no reason.
	{regexp.MustCompile(
		`(?i)\b(api[_-]?key|auth[_-]?token|access[_-]?token|token|key|secret|password|passwd)\b\s*[:=]\s*\S+`,
	), "$1=[redacted]"},
}

// RedactChannelError scrubs and bounds a failure reason for storage. Applied by
// the store on every write so no caller can bypass it.
func RedactChannelError(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse newlines first: an HTML error body persisted verbatim makes
	// the field unreadable in every surface that renders it on one line.
	s = strings.Join(strings.Fields(s), " ")
	for _, p := range credentialish {
		s = p.re.ReplaceAllString(s, p.with)
	}
	if len(s) > maxChannelErrorLen {
		// Truncate on a rune boundary; error text can carry UTF-8 and a
		// mid-rune cut produces a replacement char in every consumer.
		s = strings.ToValidUTF8(s[:maxChannelErrorLen], "") + "…"
	}
	return s
}
