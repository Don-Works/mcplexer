// auth_alert.go — bounded human notification for an ENABLED delegation
// provider that is UNAUTHENTICATED.
//
// The catalog refresher already computes a per-provider AuthState every cycle
// (see catalog_refresh.go). An enabled provider that reports
// ModelAuthUnauthenticated is a red flag the operator must see: every
// delegation routed to it will fail at login, silently, until someone
// re-authenticates the CLI. This evaluator turns that observed state into a
// single, actionable mesh notification.
//
// DISCIPLINE — this is a suppression surface like every other alert in the
// tree, so it is BOUNDED. It fires exactly ONCE per transition into the
// unauthenticated state (first observation of an enabled+unauthenticated
// provider counts as a transition), and exactly ONCE more when that provider
// recovers to ok. A provider that stays unauthenticated for hours produces one
// message, not one per refresh — otherwise the human mutes the channel and the
// signal is lost.
//
// DETERMINISTIC — pure bookkeeping over the catalog's existing AuthState. No
// LLM call, no provider probe: it reads the snapshot the refresher already
// built. It must never block the refresh path, so a failed notify is logged
// and retried on the next cycle rather than propagated.
package models

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// AuthAlert is one human-facing note about a provider's authentication state.
// Message is pre-rendered (provider + what is wrong + the fix + timestamp) so
// the transport adapter only has to deliver it — the wording is tested here,
// in the deterministic layer, not in the wiring.
type AuthAlert struct {
	Provider  string
	Recovered bool // false = newly unauthenticated; true = back to ok
	At        time.Time
	Message   string
}

// AuthAlertNotifier delivers an AuthAlert to the human (the mesh, in
// production). Kept as a narrow interface so this package never imports the
// mesh transport and tests inject a recording fake. A nil error means the
// alert reached the human; a non-nil error leaves the transition un-acked so
// the next refresh retries it.
type AuthAlertNotifier interface {
	Notify(ctx context.Context, alert AuthAlert) error
}

// AuthAlerterOptions configure an AuthAlerter.
type AuthAlerterOptions struct {
	// Notifier delivers the alert. Required; a nil Notifier makes Evaluate a
	// no-op (the feature is simply off).
	Notifier AuthAlertNotifier
	// IsEnabled reports whether a provider is enabled for alerting — i.e. its
	// MCPLEXER_ALLOW_*_CLI gate is on. A provider the operator hasn't enabled
	// being unauthenticated is not a red flag, it's just off. Defaults to
	// membership of EnabledCLIProviders() when nil.
	IsEnabled func(provider string) bool
}

// AuthAlerter tracks, per provider, whether an unauthenticated alert is
// currently outstanding, so it can fire on the edge into unauthenticated and
// again on recovery — never in between.
type AuthAlerter struct {
	notify    AuthAlertNotifier
	isEnabled func(string) bool

	mu sync.Mutex
	// alerted holds providers for which we have sent an unauthenticated alert
	// and not yet sent a recovery note. This single set is the whole dedup
	// state: presence means "human already warned, stay quiet".
	alerted map[string]struct{}
}

// NewAuthAlerter builds an AuthAlerter from opts.
func NewAuthAlerter(opts AuthAlerterOptions) *AuthAlerter {
	isEnabled := opts.IsEnabled
	if isEnabled == nil {
		isEnabled = defaultAuthAlertEnabled
	}
	return &AuthAlerter{
		notify:    opts.Notifier,
		isEnabled: isEnabled,
		alerted:   map[string]struct{}{},
	}
}

// defaultAuthAlertEnabled reports whether provider is an enabled CLI provider
// (env opt-in set). Re-evaluated per call so it reflects the live gate; the
// gate is process env, so this is cheap and stable within a daemon lifetime.
func defaultAuthAlertEnabled(provider string) bool {
	for _, p := range EnabledCLIProviders() {
		if p == provider {
			return true
		}
	}
	return false
}

// Evaluate walks the freshly built catalog and fires bounded notifications for
// enabled providers whose auth state changed. It is the hook the refresher
// invokes right after each refresh publishes a new snapshot.
//
// State machine, per enabled provider:
//   - unauthenticated & not yet alerted -> send alert, mark alerted
//   - unauthenticated & already alerted -> nothing (BOUNDED)
//   - ok & alerted                      -> send recovery, clear alerted
//   - ok & not alerted                  -> nothing
//   - unknown / not_applicable          -> nothing, alert state UNCHANGED
//
// Leaving the alert state untouched on unknown/not_applicable is what stops a
// provider that flaps unauthenticated<->unknown from re-alerting each cycle.
func (a *AuthAlerter) Evaluate(ctx context.Context, cat Catalog) {
	if a == nil || a.notify == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range cat.Providers {
		if !a.isEnabled(p.Provider) {
			continue
		}
		switch p.AuthState {
		case ModelAuthUnauthenticated:
			a.fireUnauthenticated(ctx, p.Provider, cat.RefreshedAt)
		case ModelAuthOK:
			a.fireRecovery(ctx, p.Provider, cat.RefreshedAt)
		default:
			// unknown / not_applicable: not an auth red flag, and must not
			// disturb an outstanding alert's dedup state.
		}
	}
}

// fireUnauthenticated sends the one-time alert for a newly unauthenticated
// provider. Caller holds a.mu.
func (a *AuthAlerter) fireUnauthenticated(ctx context.Context, provider string, at time.Time) {
	if _, done := a.alerted[provider]; done {
		return // BOUNDED: already warned the human, stay quiet.
	}
	alert := AuthAlert{Provider: provider, At: at, Message: renderAuthAlert(provider, at)}
	if err := a.notify.Notify(ctx, alert); err != nil {
		// Leave it un-acked so the next refresh retries; do NOT mark alerted,
		// or a transient mesh failure would swallow the only notification.
		slog.Warn("model catalog: auth alert notify failed",
			"provider", provider, "err", err)
		return
	}
	a.alerted[provider] = struct{}{}
	slog.Warn("model catalog: enabled provider is unauthenticated",
		"provider", provider, "observed", at.Format(time.RFC3339))
}

// fireRecovery sends the one-time recovery note when a previously alerted
// provider is authenticated again. Caller holds a.mu.
func (a *AuthAlerter) fireRecovery(ctx context.Context, provider string, at time.Time) {
	if _, done := a.alerted[provider]; !done {
		return // never alerted -> nothing to recover from.
	}
	alert := AuthAlert{Provider: provider, Recovered: true, At: at, Message: renderAuthRecovery(provider, at)}
	if err := a.notify.Notify(ctx, alert); err != nil {
		// Keep it alerted so the recovery is retried next refresh.
		slog.Warn("model catalog: auth recovery notify failed",
			"provider", provider, "err", err)
		return
	}
	delete(a.alerted, provider)
	slog.Info("model catalog: provider re-authenticated",
		"provider", provider, "observed", at.Format(time.RFC3339))
}

// renderAuthAlert composes the human-facing unauthenticated message: which
// provider, that it is enabled-but-unauthenticated, the consequence, the fix,
// and the observation timestamp. No secrets — provider name and time only.
func renderAuthAlert(provider string, at time.Time) string {
	return "Delegation provider " + provider + " is ENABLED but UNAUTHENTICATED — " +
		"any delegation routed to it will fail at login. Re-authenticate it on this host (" +
		loginHint(provider) + "); the alert clears automatically once it can log in again. " +
		"Observed " + at.UTC().Format(time.RFC3339) + "."
}

// renderAuthRecovery composes the one-time recovery note.
func renderAuthRecovery(provider string, at time.Time) string {
	return "Delegation provider " + provider + " re-authenticated — it was flagged " +
		"enabled-but-unauthenticated and is now OK; delegations to it can proceed. " +
		"Observed " + at.UTC().Format(time.RFC3339) + "."
}

// loginHint returns a best-effort, provider-specific re-auth instruction. The
// CLI providers share the "<name>_cli" convention; the login is running the
// CLI's own auth flow, so we name the base CLI. Unknown providers get a
// generic-but-actionable fallback.
func loginHint(provider string) string {
	name := strings.TrimSuffix(provider, "_cli")
	switch provider {
	case ProviderGrokCLI:
		return "run `grok` and complete its login"
	case ProviderMiMoCLI:
		return "run `mimo` and complete its login"
	case ProviderPiCLI:
		return "run `pi` and complete its login"
	default:
		if name == provider {
			return "re-authenticate the " + provider + " provider"
		}
		return "run `" + name + "` and complete its login"
	}
}
