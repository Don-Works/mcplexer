package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// alertNow is the fixed evaluation instant every case renders against.
var alertNow = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

func lastSeenAgo(d time.Duration) *time.Time {
	t := alertNow.Add(-d)
	return &t
}

// TestExpectedSignalAlertAbsenceLeadsWithHumanContext is the acceptance test for
// the unreadable-alert incident: the title and body must say WHAT stopped,
// WHERE, HOW abnormal, and WHEN — never the learner's auto/<hash> name.
func TestExpectedSignalAlertAbsenceLeadsWithHumanContext(t *testing.T) {
	a := store.ExpectedSignalAlert{
		Outcome:  store.OutcomeSignalAbsent,
		Reason:   store.ReasonNoMatches,
		Match:    "finished scheduled job for order sync",
		Source:   "orders-api",
		MinCount: 18,
		Window:   30 * time.Minute,
		Observed: 0,
		Total:    4200,
		LastSeen: lastSeenAgo(47 * time.Minute),
		RuleName: "auto/tpl-orders",
		Now:      alertNow,
	}

	title := a.Title()
	for _, want := range []string{
		`"finished scheduled job for order sync"`, // WHAT
		"orders-api", // WHERE
		">=18/30m",   // HOW ABNORMAL (expected)
		"saw 0",      // HOW ABNORMAL (observed)
		"47m ago",    // WHEN
	} {
		if !strings.Contains(title, want) {
			t.Errorf("title %q missing %q", title, want)
		}
	}
	if strings.Contains(title, "auto/") {
		t.Errorf("title must not headline the auto/ rule name: %q", title)
	}
	if strings.ContainsAny(title, "\n\r\t") {
		t.Errorf("title must be one line: %q", title)
	}

	body := a.Body()
	for _, want := range []string{
		"finished scheduled job for order sync",
		"orders-api",
		"Normally at least 18 per 30m",
		"saw 0",
		"real absence",
		"Last seen: 2026-07-20T11:13:00Z (47m ago).",
		"Rule: auto/tpl-orders (learned automatically)", // stable id as a footer
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n---\n%s", want, body)
		}
	}
	// The auto/ name may appear ONLY in the footer, never in the first line.
	if strings.Contains(strings.SplitN(body, "\n", 2)[0], "auto/") {
		t.Errorf("body headline must not carry the auto/ name: %q", body)
	}
}

// TestExpectedSignalAlertEmptyMatch proves a rule that matches ANY line (empty
// MatchSubstring) still produces a sane, non-empty, single-line title.
func TestExpectedSignalAlertEmptyMatch(t *testing.T) {
	a := store.ExpectedSignalAlert{
		Outcome: store.OutcomeSignalAbsent, Reason: store.ReasonNoMatches,
		Match: "", Source: "orders-api", MinCount: 1, Window: time.Hour,
		Observed: 0, Total: 0, RuleName: "auto/x", Now: alertNow,
	}
	title := a.Title()
	if !strings.Contains(title, "any log line") {
		t.Errorf("empty match should read 'any log line': %q", title)
	}
	if !strings.Contains(title, ">=1/1h") || !strings.Contains(title, "orders-api") {
		t.Errorf("title lost the cadence or source: %q", title)
	}
	if strings.ContainsAny(title, "\n\r\t") {
		t.Errorf("title must be one line: %q", title)
	}
	// A never-seen signal must not claim a bogus "last seen".
	if strings.Contains(title, "ago") || strings.Contains(title, "1970") {
		t.Errorf("nil last-seen must not render a time: %q", title)
	}
	if !strings.Contains(title, "not seen in retained history") {
		t.Errorf("nil last-seen should say so: %q", title)
	}
}

// TestExpectedSignalAlertTruncatesLongMultilineMatch is the injection/one-line
// guard: arbitrary multi-line log text must be flattened and hard-truncated.
func TestExpectedSignalAlertTruncatesLongMultilineMatch(t *testing.T) {
	raw := "line one\nline two\r\tpayload " + strings.Repeat("x", 400)
	a := store.ExpectedSignalAlert{
		Outcome: store.OutcomeSignalAbsent, Reason: store.ReasonNoMatches,
		Match: raw, Source: "orders-api", MinCount: 1, Window: time.Hour,
		Observed: 0, RuleName: "auto/x", Now: alertNow,
	}
	title := a.Title()
	if strings.ContainsAny(title, "\n\r\t") {
		t.Fatalf("newlines/tabs must be stripped from the title: %q", title)
	}
	if !strings.Contains(title, "…") {
		t.Errorf("an over-length match must be truncated with an ellipsis: %q", title)
	}
	if !strings.Contains(title, "line one line two") {
		t.Errorf("multiline match should collapse to spaces: %q", title)
	}
	// The quoted fragment itself must be bounded well under the raw length.
	frag := betweenQuotes(title)
	if n := len([]rune(frag)); n > 80 {
		t.Errorf("fragment length %d exceeds the title cap", n)
	}
	if strings.ContainsAny(betweenQuotes(a.Body()), "\n\r\t") {
		t.Errorf("body fragment must be flattened to one line: %q", a.Body())
	}
}

// TestExpectedSignalAlertRecovery checks the recovery text is human-readable and
// names the same signal and source as the raise.
func TestExpectedSignalAlertRecovery(t *testing.T) {
	a := store.ExpectedSignalAlert{
		Match: "finished scheduled job for order sync", Source: "orders-api",
		RuleName: "auto/tpl-orders", Now: alertNow,
	}
	title := a.RecoveryTitle()
	if !strings.HasPrefix(title, "Recovered:") {
		t.Errorf("recovery title must announce recovery: %q", title)
	}
	for _, want := range []string{"finished scheduled job for order sync", "orders-api"} {
		if !strings.Contains(title, want) {
			t.Errorf("recovery title missing %q: %q", want, title)
		}
	}
	if strings.ContainsAny(title, "\n\r\t") {
		t.Errorf("recovery title must be one line: %q", title)
	}
	if strings.Contains(title, "auto/") {
		t.Errorf("recovery title must not headline the auto/ name: %q", title)
	}
	if !strings.Contains(a.RecoveryBody(), "auto/tpl-orders") {
		t.Errorf("recovery body should keep the auto/ id as a footer: %q", a.RecoveryBody())
	}
}

// TestExpectedSignalAlertCollection proves the collection variant says "can't
// verify" rather than claiming the signal stopped, and keeps the failure count.
func TestExpectedSignalAlertCollection(t *testing.T) {
	a := store.ExpectedSignalAlert{
		Outcome: store.OutcomeSignalCollection, Reason: store.ReasonPullFailing,
		Match: "order sync completed", Source: "orders-api", MinCount: 1,
		Window: time.Hour, Failures: 3, RuleName: "auto/x", Now: alertNow,
	}
	title := a.Title()
	if !strings.HasPrefix(title, "can't verify") {
		t.Errorf("collection title must not claim the signal stopped: %q", title)
	}
	if !strings.Contains(title, "log collection is failing") || !strings.Contains(title, "orders-api") {
		t.Errorf("collection title missing reason or source: %q", title)
	}
	body := a.Body()
	if !strings.Contains(body, "COLLECTION problem") {
		t.Errorf("collection body must flag the visibility problem: %q", body)
	}
	if !strings.Contains(body, "3 consecutive") {
		t.Errorf("collection body should keep the failure count: %q", body)
	}
}

// TestNewExpectedSignalAlertCopiesFacts checks the constructor's field wiring:
// the observed last match wins over the rule latch, and a blank source label
// falls back to the raw id.
func TestNewExpectedSignalAlertCopiesFacts(t *testing.T) {
	obsMatch := time.Date(2026, 7, 20, 11, 30, 0, 0, time.UTC)
	ruleLatch := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	rule := store.MonitoringExpectedSignal{
		SourceID: "src-1", Name: "auto/x", MatchSubstring: "m",
		MinCount: 2, WindowSeconds: 3600, LastSignalAt: &ruleLatch,
	}
	d := store.ExpectedSignalDecision{
		Outcome: store.OutcomeSignalAbsent, Reason: store.ReasonNoMatches,
		WindowEnd: alertNow,
	}
	obs := store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 100, LastMatchAt: &obsMatch}

	a := store.NewExpectedSignalAlert(rule, d, obs, store.SourceCollectionHealth{}, "")
	if a.Source != "src-1" {
		t.Errorf("blank source should fall back to the raw id, got %q", a.Source)
	}
	if a.LastSeen == nil || !a.LastSeen.Equal(obsMatch) {
		t.Errorf("observed last match should win over the rule latch, got %v", a.LastSeen)
	}
	if !a.Now.Equal(alertNow) {
		t.Errorf("Now should track the decision window end, got %v", a.Now)
	}
	if !strings.Contains(a.Title(), "src-1") {
		t.Errorf("title should carry the fallback source id: %q", a.Title())
	}
}

// betweenQuotes returns the text inside the first pair of double quotes, or "".
func betweenQuotes(s string) string {
	i := strings.IndexByte(s, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i+1:], '"')
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}
