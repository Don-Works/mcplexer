package mesh

import (
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestFormatReceiveResult_EmptySessionID is the regression test for the
// "slice bounds out of range [:8] with length 0" panic that took
// mesh__receive offline whenever a row had an empty SessionID (a
// legacy mesh_agents row, or a message whose sender had no session).
//
// Before the idtrunc helper landed, this test panicked at the two
// `[:8]` slices in FormatReceiveResult. Now it must render the agent /
// message with an empty name segment (or fall back to the empty
// string) and return normally.
func TestFormatReceiveResult_EmptySessionID(t *testing.T) {
	now := time.Now()
	r := &ReceiveResult{
		Agents: []store.MeshAgent{
			{SessionID: "", LastSeenAt: now},                  // unnamed, no session — the historic crasher.
			{SessionID: "shortid", Name: "", LastSeenAt: now}, // shorter than 8.
			{SessionID: "abcdefghijkl", LastSeenAt: now},      // normal — should truncate to "abcdefgh".
		},
		Messages: []store.MeshMessage{
			{ID: "m1", SessionID: "", Kind: "finding", Priority: "normal", Content: "x", CreatedAt: now},
			{ID: "m2", SessionID: "short", Kind: "finding", Priority: "normal", Content: "y", CreatedAt: now},
		},
		Stats: MeshStats{ActiveAgents: 3, LiveMessages: 2, NewForYou: 2},
	}

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("FormatReceiveResult panicked: %v", rec)
		}
	}()
	out := FormatReceiveResult(r, "")

	// Sanity: header line is present.
	if !strings.Contains(out, "Mesh Status") {
		t.Errorf("expected status header, got: %q", out)
	}
	// Truncated name should appear for the well-formed agent.
	if !strings.Contains(out, "abcdefgh") {
		t.Errorf("expected truncated session 'abcdefgh', got: %q", out)
	}
}

func TestFormatReceiveResult_PreviewsLargeContent(t *testing.T) {
	now := time.Now()
	msg := store.MeshMessage{
		ID:        "msg-large",
		SessionID: "sender",
		Kind:      "finding",
		Priority:  "normal",
		Content:   strings.Repeat("a", DefaultReceivePreviewBytes+25),
		CreatedAt: now,
	}
	out := FormatReceiveResult(&ReceiveResult{
		Messages: []store.MeshMessage{msg},
		Stats:    MeshStats{NewForYou: 1},
	}, "receiver")

	if strings.Contains(out, strings.Repeat("a", DefaultReceivePreviewBytes+1)) {
		t.Fatalf("receive output included more than the preview cap: %q", out)
	}
	if !strings.Contains(out, "truncated 512/537 bytes; hydrate: msg-large") {
		t.Fatalf("missing truncation/hydrate hint: %q", out)
	}
}

func TestFormatHydrateResult_CapsHistoricalLargeRows(t *testing.T) {
	now := time.Now()
	msg := &store.MeshMessage{
		ID:        "msg-hydrate",
		SessionID: "sender",
		Kind:      "finding",
		Priority:  "normal",
		Content:   strings.Repeat("b", 80),
		CreatedAt: now,
	}
	out := FormatHydrateResult(msg, 10)

	if strings.Contains(out, strings.Repeat("b", 11)) {
		t.Fatalf("hydrate output exceeded requested cap: %q", out)
	}
	if !strings.Contains(out, "truncated: true (10/80 bytes)") {
		t.Fatalf("missing hydrate truncation notice: %q", out)
	}
}

func TestFormatThreadResult_UsesSharedContentBudget(t *testing.T) {
	now := time.Now()
	msgs := []store.MeshMessage{
		{ID: "root", SessionID: "sender", Kind: "question", Priority: "normal", Content: strings.Repeat("r", 8), CreatedAt: now},
		{ID: "reply", SessionID: "sender", Kind: "reply", Priority: "normal", Content: strings.Repeat("c", 8), CreatedAt: now},
	}
	out := FormatThreadResult(msgs, 10)

	if strings.Contains(out, strings.Repeat("c", 3)) {
		t.Fatalf("thread output exceeded shared content budget: %q", out)
	}
	if !strings.Contains(out, "truncated: true") {
		t.Fatalf("missing thread truncation notice: %q", out)
	}
}

// TestFormatSendResult_FutureExpiryRendersCountdown is the regression
// test for the "expires just now" bug: FormatSendResult used to pass
// the FUTURE ExpiresAt through past-oriented formatRelativeTime, so
// every send confirmation claimed the message was already expiring.
func TestFormatSendResult_FutureExpiryRendersCountdown(t *testing.T) {
	msg := &store.MeshMessage{
		ID: "m1", Kind: "finding", Priority: "high",
		ExpiresAt: time.Now().Add(4*time.Hour + time.Minute),
	}
	out := FormatSendResult(msg)
	if !strings.Contains(out, "expires in 4h") {
		t.Fatalf("future expiry must render as a countdown, got %q", out)
	}
}

func TestFormatUntil(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"past is expired", now.Add(-time.Minute), "expired"},
		{"seconds away", now.Add(30 * time.Second), "in <1m"},
		{"minutes away", now.Add(5*time.Minute + time.Second), "in 5m"},
		{"hours away", now.Add(3*time.Hour + time.Minute), "in 3h"},
		{"days away", now.Add(26 * time.Hour), "in 1d"},
	}
	for _, tc := range cases {
		if got := formatUntil(tc.t); got != tc.want {
			t.Errorf("%s: formatUntil = %q, want %q", tc.name, got, tc.want)
		}
	}
}
