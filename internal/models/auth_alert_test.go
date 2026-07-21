package models

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingNotifier captures every AuthAlert Notify received and can be primed
// to fail, so a transient-notify path can be exercised.
type recordingNotifier struct {
	alerts   []AuthAlert
	failNext bool
	failErr  error
}

func (n *recordingNotifier) Notify(_ context.Context, a AuthAlert) error {
	if n.failNext {
		n.failNext = false
		if n.failErr == nil {
			n.failErr = errors.New("mesh down")
		}
		return n.failErr
	}
	n.alerts = append(n.alerts, a)
	return nil
}

// enabledOnly returns an IsEnabled predicate that treats the given providers
// as enabled and every other provider as disabled.
func enabledOnly(providers ...string) func(string) bool {
	set := map[string]struct{}{}
	for _, p := range providers {
		set[p] = struct{}{}
	}
	return func(p string) bool { _, ok := set[p]; return ok }
}

// catAt builds a single-provider catalog snapshot in state at time when.
func catAt(when time.Time, provider string, state ModelAuthState) Catalog {
	return Catalog{
		RefreshedAt: when,
		Providers: []ProviderCatalog{
			{Provider: provider, AuthState: state, LastRefreshed: when},
		},
	}
}

func newTestAlerter(n AuthAlertNotifier, enabled func(string) bool) *AuthAlerter {
	return NewAuthAlerter(AuthAlerterOptions{Notifier: n, IsEnabled: enabled})
}

// TestAuthAlertFiresOnceOnTransition — an enabled provider going ok ->
// unauthenticated produces exactly ONE alert naming the provider, and staying
// unauthenticated across further refreshes produces NO further alerts.
func TestAuthAlertFiresOnceOnTransition(t *testing.T) {
	n := &recordingNotifier{}
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	base := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(base, ProviderGrokCLI, ModelAuthOK))
	if len(n.alerts) != 0 {
		t.Fatalf("ok state alerted: %+v", n.alerts)
	}

	a.Evaluate(context.Background(), catAt(base.Add(time.Hour), ProviderGrokCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 1 {
		t.Fatalf("transition into unauthenticated: got %d alerts, want 1", len(n.alerts))
	}
	got := n.alerts[0]
	if got.Provider != ProviderGrokCLI || got.Recovered {
		t.Fatalf("alert = %+v, want provider=%s recovered=false", got, ProviderGrokCLI)
	}
	if !strings.Contains(got.Message, ProviderGrokCLI) ||
		!strings.Contains(got.Message, "UNAUTHENTICATED") {
		t.Fatalf("message not actionable: %q", got.Message)
	}

	// Bounded: five more unauthenticated refreshes, still exactly one alert.
	for i := 0; i < 5; i++ {
		a.Evaluate(context.Background(), catAt(base.Add(time.Duration(2+i)*time.Hour), ProviderGrokCLI, ModelAuthUnauthenticated))
	}
	if len(n.alerts) != 1 {
		t.Fatalf("bounded alert violated: got %d alerts after repeats, want 1", len(n.alerts))
	}
}

// TestAuthAlertFirstObservationAtBoot — the very first snapshot showing an
// enabled+unauthenticated provider (no prior ok) still fires one alert.
func TestAuthAlertFirstObservationAtBoot(t *testing.T) {
	n := &recordingNotifier{}
	a := newTestAlerter(n, enabledOnly(ProviderMiMoCLI))
	when := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(when, ProviderMiMoCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 1 {
		t.Fatalf("first-observation alert: got %d, want 1", len(n.alerts))
	}
	if !strings.Contains(n.alerts[0].Message, when.Format(time.RFC3339)) {
		t.Fatalf("message omits observation timestamp: %q", n.alerts[0].Message)
	}
}

// TestAuthAlertRecovery — unauthenticated -> ok emits exactly one recovery
// note, and a subsequent ok does not repeat it.
func TestAuthAlertRecovery(t *testing.T) {
	n := &recordingNotifier{}
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	base := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(base, ProviderGrokCLI, ModelAuthUnauthenticated))
	a.Evaluate(context.Background(), catAt(base.Add(time.Hour), ProviderGrokCLI, ModelAuthOK))
	if len(n.alerts) != 2 {
		t.Fatalf("expected alert+recovery = 2 sends, got %d: %+v", len(n.alerts), n.alerts)
	}
	rec := n.alerts[1]
	if !rec.Recovered || rec.Provider != ProviderGrokCLI {
		t.Fatalf("recovery note = %+v, want recovered=true provider=%s", rec, ProviderGrokCLI)
	}
	if !strings.Contains(rec.Message, "re-authenticated") {
		t.Fatalf("recovery message not clear: %q", rec.Message)
	}

	// Further ok refreshes must not repeat the recovery.
	a.Evaluate(context.Background(), catAt(base.Add(2*time.Hour), ProviderGrokCLI, ModelAuthOK))
	if len(n.alerts) != 2 {
		t.Fatalf("recovery repeated: got %d sends, want 2", len(n.alerts))
	}
}

// TestAuthAlertReAlertsAfterRecovery — a genuine second transition into
// unauthenticated (after a recovery) fires a fresh alert.
func TestAuthAlertReAlertsAfterRecovery(t *testing.T) {
	n := &recordingNotifier{}
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	base := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	states := []ModelAuthState{
		ModelAuthUnauthenticated, // alert 1
		ModelAuthOK,              // recovery 1
		ModelAuthUnauthenticated, // alert 2
	}
	for i, st := range states {
		a.Evaluate(context.Background(), catAt(base.Add(time.Duration(i)*time.Hour), ProviderGrokCLI, st))
	}
	if len(n.alerts) != 3 {
		t.Fatalf("re-transition: got %d sends, want 3 (alert, recovery, alert)", len(n.alerts))
	}
	if n.alerts[2].Recovered {
		t.Fatalf("third send should be a fresh alert, got recovery: %+v", n.alerts[2])
	}
}

// TestAuthAlertDisabledProviderSilent — a DISABLED provider that is
// unauthenticated is just off, not a red flag: no notification.
func TestAuthAlertDisabledProviderSilent(t *testing.T) {
	n := &recordingNotifier{}
	// grok enabled, mimo NOT enabled.
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	when := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(when, ProviderMiMoCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 0 {
		t.Fatalf("disabled provider alerted: %+v", n.alerts)
	}
}

// TestAuthAlertUnknownAndNotApplicableSilent — neither unknown nor
// not_applicable is an auth red flag; neither notifies nor disturbs an
// outstanding alert's dedup state.
func TestAuthAlertUnknownAndNotApplicableSilent(t *testing.T) {
	n := &recordingNotifier{}
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	base := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(base, ProviderGrokCLI, ModelAuthUnknown))
	a.Evaluate(context.Background(), catAt(base.Add(time.Hour), ProviderGrokCLI, ModelAuthNotApplicable))
	if len(n.alerts) != 0 {
		t.Fatalf("unknown/not_applicable alerted: %+v", n.alerts)
	}

	// unauthenticated -> unknown -> unauthenticated must NOT re-alert: the
	// transient unknown leaves the outstanding alert intact.
	a.Evaluate(context.Background(), catAt(base.Add(2*time.Hour), ProviderGrokCLI, ModelAuthUnauthenticated))
	a.Evaluate(context.Background(), catAt(base.Add(3*time.Hour), ProviderGrokCLI, ModelAuthUnknown))
	a.Evaluate(context.Background(), catAt(base.Add(4*time.Hour), ProviderGrokCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 1 {
		t.Fatalf("flap through unknown re-alerted: got %d, want 1", len(n.alerts))
	}
}

// TestAuthAlertRetriesOnNotifyFailure — a failed notify must not consume the
// transition: the next refresh retries and the alert eventually lands once.
func TestAuthAlertRetriesOnNotifyFailure(t *testing.T) {
	n := &recordingNotifier{failNext: true}
	a := newTestAlerter(n, enabledOnly(ProviderGrokCLI))
	base := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

	a.Evaluate(context.Background(), catAt(base, ProviderGrokCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 0 {
		t.Fatalf("failed notify should record nothing, got %+v", n.alerts)
	}
	// Retry: notify now succeeds -> exactly one alert.
	a.Evaluate(context.Background(), catAt(base.Add(time.Hour), ProviderGrokCLI, ModelAuthUnauthenticated))
	if len(n.alerts) != 1 {
		t.Fatalf("retry after failure: got %d alerts, want 1", len(n.alerts))
	}
}

// TestAuthAlertNilNotifierNoPanic — a nil notifier (no mesh transport) makes
// Evaluate a safe no-op.
func TestAuthAlertNilNotifierNoPanic(t *testing.T) {
	a := NewAuthAlerter(AuthAlerterOptions{IsEnabled: enabledOnly(ProviderGrokCLI)})
	a.Evaluate(context.Background(), catAt(time.Now(), ProviderGrokCLI, ModelAuthUnauthenticated))
}

// TestDefaultAuthAlertEnabledTracksEnabledCLIProviders — the default predicate
// mirrors EnabledCLIProviders so alerting scope and dispatch scope cannot drift.
func TestDefaultAuthAlertEnabledTracksEnabledCLIProviders(t *testing.T) {
	t.Setenv(grokCLIAllowEnvVar, "1")
	if !defaultAuthAlertEnabled(ProviderGrokCLI) {
		t.Fatal("grok gate on but default predicate reports disabled")
	}
	t.Setenv(grokCLIAllowEnvVar, "")
	if defaultAuthAlertEnabled(ProviderGrokCLI) {
		t.Fatal("grok gate off but default predicate reports enabled")
	}
	// A non-CLI provider (no gate) is not in EnabledCLIProviders, so it is not
	// an alerting target even though CLIProviderAllowed defaults it to true.
	if defaultAuthAlertEnabled("anthropic") {
		t.Fatal("non-CLI provider treated as an alerting target")
	}
}
