package store_test

// monitoring_channel_health_test.go — the pure logic behind "is this alert
// route working", and the redaction that keeps answering it from leaking the
// credential that broke it.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ptrTime is defined in monitoring_expected_signal_eval_test.go.

// TestHealthState pins the four states. The distinction that matters most is
// unknown vs healthy: a route nobody has ever delivered through is exactly the
// route that turns out to be misconfigured the first time it is needed, and
// reporting it healthy is how the operator finds out too late.
func TestHealthState(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		channel store.MonitoringChannel
		want    string
		broken  bool
	}{
		{"never targeted, never delivered", store.MonitoringChannel{},
			store.ChannelHealthUnknown, false},
		{"delivered and owes nothing",
			store.MonitoringChannel{LastSuccessAt: ptrTime(now)},
			store.ChannelHealthHealthy, false},
		{"one undelivered notification is a wobble",
			store.MonitoringChannel{TargetedSinceSuccess: 1},
			store.ChannelHealthDegraded, false},
		{"just below the threshold",
			store.MonitoringChannel{TargetedSinceSuccess: store.ChannelBrokenThreshold - 1},
			store.ChannelHealthDegraded, false},
		{"at the threshold",
			store.MonitoringChannel{TargetedSinceSuccess: store.ChannelBrokenThreshold},
			store.ChannelHealthBroken, true},
		{"long dead — owed 191 and delivered none",
			store.MonitoringChannel{TargetedSinceSuccess: 191, LastSuccessAt: ptrTime(now)},
			store.ChannelHealthBroken, true},
		{
			// THE INVERSION. This is the 2026-07-14 row: the throttle stopped
			// the route being attempted, so its failure count froze at one,
			// while it went on being owed notification after notification.
			// Health must read the debt, not the frozen counter.
			name: "suppressed dead route — failures frozen, debt still growing",
			channel: store.MonitoringChannel{
				ConsecutiveFailures: 1, TargetedSinceSuccess: 191,
			},
			want: store.ChannelHealthBroken, broken: true,
		},
		{
			// The mirror: a quiet workspace owes its channel nothing, so
			// however old last_success_at is, the route is not broken. Without
			// this the feature cries wolf on every idle channel and gets
			// switched off, which is the silence we started from.
			name: "idle but healthy — old success, no debt",
			channel: store.MonitoringChannel{
				LastSuccessAt: ptrTime(now.Add(-90 * 24 * time.Hour)),
			},
			want: store.ChannelHealthHealthy, broken: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.channel.HealthState(); got != tc.want {
				t.Errorf("HealthState() = %q, want %q", got, tc.want)
			}
			if got := tc.channel.Broken(); got != tc.broken {
				t.Errorf("Broken() = %v, want %v", got, tc.broken)
			}
		})
	}
}

// TestRedactChannelError is the P0 guard on a field that is persisted AND
// served over the REST API. A Google Chat webhook URL embeds key+token in its
// query string and IS the credential, so the query is dropped wholesale — an
// allowlist of "safe" parameters would be a guess about someone else's API.
func TestRedactChannelError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       string
		mustNot  []string
		mustHave []string
	}{
		{
			name:     "webhook url query carries the credential",
			in:       "Post \"https://chat.example.com/v1/spaces/AAA/messages?key=AIzaSECRET&token=abc123\": 400",
			mustNot:  []string{"AIzaSECRET", "abc123", "key=", "token="},
			mustHave: []string{"chat.example.com", "400"},
		},
		{
			name:     "bearer token in free text",
			in:       "unauthorized: Bearer sk-abcdef0123456789abcdef",
			mustNot:  []string{"sk-abcdef0123456789abcdef"},
			mustHave: []string{"unauthorized"},
		},
		{
			name:     "assignment forms",
			in:       "rejected (token=deadbeef, password: hunter2)",
			mustNot:  []string{"deadbeef", "hunter2"},
			mustHave: []string{"rejected"},
		},
		{
			name:     "secret ref points at a credential",
			in:       "could not resolve secret://GCHAT_WEBHOOK_INCIDENTS in scope acme",
			mustNot:  []string{"GCHAT_WEBHOOK_INCIDENTS"},
			mustHave: []string{"could not resolve", "scope acme"},
		},
		{
			name:     "ordinary error survives intact",
			in:       "escalate: webhook status 400",
			mustNot:  []string{"[redacted]"},
			mustHave: []string{"escalate: webhook status 400"},
		},
		{
			// Regression from live rig output. Matching credential words on
			// whitespace rather than on an explicit :/= turned this real
			// error into "get auth=[redacted] missing-scope: not found",
			// eating the word "scope" and the diagnosis with it.
			name:     "credential words in ordinary prose are not assignments",
			in:       "escalate: resolve webhook ref: get auth scope missing-scope: not found",
			mustNot:  []string{"[redacted]"},
			mustHave: []string{"auth scope missing-scope", "not found"},
		},
		{
			name:     "but a real assignment is still caught",
			in:       "rejected: api_key=AIzaSECRET and access_token: tok-999",
			mustNot:  []string{"AIzaSECRET", "tok-999"},
			mustHave: []string{"rejected"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := store.RedactChannelError(tc.in)
			for _, s := range tc.mustNot {
				if strings.Contains(got, s) {
					t.Errorf("leaked %q: %s", s, got)
				}
			}
			for _, s := range tc.mustHave {
				if !strings.Contains(got, s) {
					t.Errorf("lost %q, leaving the error undiagnosable: %s", s, got)
				}
			}
		})
	}
}

// TestRedactChannelErrorBounds: a remote endpoint must not be able to write an
// unbounded blob into a column served on every channel list, and a multi-line
// HTML error body must not make the field unreadable in surfaces that render
// it on one line.
func TestRedactChannelErrorBounds(t *testing.T) {
	long := "escalate: " + strings.Repeat("A", 5000)
	got := store.RedactChannelError(long)
	if len([]byte(got)) > 400 {
		t.Errorf("stored error is %d bytes, want bounded", len(got))
	}
	if strings.ContainsAny(store.RedactChannelError("line one\nline two\r\n\tline three"), "\n\r\t") {
		t.Error("newlines survived; the field is unreadable inline")
	}
	if store.RedactChannelError("   ") != "" {
		t.Error("whitespace-only error should normalise to empty")
	}
	// Truncation must not cut a rune in half, or every consumer renders a
	// replacement character.
	multibyte := strings.Repeat("é", 4000)
	if !json.Valid(mustJSON(t, store.RedactChannelError(multibyte))) {
		t.Error("truncated multibyte error is not valid JSON-encodable text")
	}
}

func mustJSON(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestChannelJSONRoundTrip: MarshalJSON adds derived fields and UnmarshalJSON
// must accept them, because the REST API decodes with DisallowUnknownFields and
// PATCH is a read-modify-write. Without this, adding a health field would break
// channel editing — including the edit an operator makes to FIX a broken route.
func TestChannelJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 14, 6, 12, 0, 0, time.UTC)
	original := store.MonitoringChannel{
		ID: "chan-1", WorkspaceID: "acme", Name: "incidents",
		Kind: store.ChannelKindGChatWebhook, MinSeverity: store.SeverityWarn,
		Enabled: true, ConsecutiveFailures: 4, TargetedSinceSuccess: 4,
		FirstFailureAt: ptrTime(now), LastFailureAt: ptrTime(now.Add(time.Hour)),
		LastError: "webhook status 400",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if envelope["health"] != store.ChannelHealthBroken || envelope["broken"] != true {
		t.Fatalf("derived fields missing or wrong: %v", envelope)
	}

	var back store.MonitoringChannel
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("round-trip decode failed — PATCH would 400: %v", err)
	}
	if back.ConsecutiveFailures != original.ConsecutiveFailures || back.Name != original.Name {
		t.Fatalf("round-trip lost data: %+v", back)
	}
	// Strictness must survive: a typo is still an error, not a silent no-op.
	if err := json.Unmarshal([]byte(`{"nmae":"typo"}`), &back); err == nil {
		t.Error("unknown field accepted — the API lost its strictness")
	}
}
