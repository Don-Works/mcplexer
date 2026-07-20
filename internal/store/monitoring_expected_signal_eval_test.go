package store_test

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func ptrTime(t time.Time) *time.Time { return &t }

// baseRule is a 6-hour, always-on rule that has already proven it can see its
// signal. Each case below perturbs exactly one dimension.
func baseRule(created time.Time) store.MonitoringExpectedSignal {
	r := store.MonitoringExpectedSignal{
		ID: "rule-1", WorkspaceID: "ws-1", SourceID: "src-1",
		Name: "orders ingested", MatchSubstring: "order ingested",
		MinCount: 1, WindowSeconds: int64((6 * time.Hour).Seconds()),
		Severity: store.SeverityError, Enabled: true,
		RequireSourceLiveness: true, CreatedAt: created,
		LastSignalAt: ptrTime(created.Add(time.Hour)),
	}
	store.ApplyExpectedSignalDefaults(&r)
	return r
}

func healthyPull() store.SourceCollectionHealth {
	return store.SourceCollectionHealth{Enabled: true}
}

func TestEvaluateExpectedSignal(t *testing.T) {
	created := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		mutate      func(*store.MonitoringExpectedSignal)
		observed    store.ExpectedSignalObservation
		health      store.SourceCollectionHealth
		now         time.Time
		wantOutcome store.ExpectedSignalOutcome
		wantRaise   bool
		wantReason  string
		wantPresent bool
		wantClass   string
	}{
		{
			name:        "signal present raises nothing and marks presence",
			observed:    store.ExpectedSignalObservation{MatchCount: 4, TotalLines: 90},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalHealthy,
			wantPresent: true,
		},
		{
			name:        "signal absent past window raises absence",
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 120},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalAbsent,
			wantRaise:   true,
			wantReason:  store.ReasonNoMatches,
			wantClass:   "absence:rule-1",
		},
		{
			name:        "below min_count is an absence with its own reason",
			mutate:      func(r *store.MonitoringExpectedSignal) { r.MinCount = 10 },
			observed:    store.ExpectedSignalObservation{MatchCount: 3, TotalLines: 120},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalAbsent,
			wantRaise:   true,
			wantReason:  store.ReasonBelowMinCount,
			wantClass:   "absence:rule-1",
		},
		{
			name:     "absent but pulls failing is a COLLECTION incident",
			observed: store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0},
			health: store.SourceCollectionHealth{
				Enabled: true, ConsecutiveFailures: 3,
			},
			wantOutcome: store.OutcomeSignalCollection,
			wantRaise:   true,
			wantReason:  store.ReasonPullFailing,
			wantClass:   "absence-collection:rule-1",
		},
		{
			name:        "absent but source disabled is a COLLECTION incident",
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0},
			health:      store.SourceCollectionHealth{Enabled: false},
			wantOutcome: store.OutcomeSignalCollection,
			wantRaise:   true,
			wantReason:  store.ReasonSourceDisabled,
			wantClass:   "absence-collection:rule-1",
		},
		{
			name:        "source produced no lines at all is COLLECTION, never absence",
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalCollection,
			wantRaise:   true,
			wantReason:  store.ReasonSourceSilent,
			wantClass:   "absence-collection:rule-1",
		},
		{
			name: "liveness requirement can be waived for signal-only sources",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.RequireSourceLiveness = false
			},
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalAbsent,
			wantRaise:   true,
			wantReason:  store.ReasonNoMatches,
			wantClass:   "absence:rule-1",
		},
		{
			name: "degraded pulls below the raise threshold are inconclusive",
			observed: store.ExpectedSignalObservation{
				MatchCount: 0, TotalLines: 120,
			},
			health:      store.SourceCollectionHealth{Enabled: true, ConsecutiveFailures: 1},
			wantOutcome: store.OutcomeSignalInconclusive,
		},
		{
			name: "never-yet-seen rule does not fire",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.LastSignalAt = nil
			},
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 120},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalAwaitingFirst,
		},
		{
			name: "retained history proves first sight even without rule state",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.LastSignalAt = nil
			},
			observed: store.ExpectedSignalObservation{
				MatchCount: 0, TotalLines: 120,
				LastMatchAt: ptrTime(now.Add(-30 * time.Hour)),
			},
			health:      healthyPull(),
			wantOutcome: store.OutcomeSignalAbsent,
			wantRaise:   true,
			wantClass:   "absence:rule-1",
			wantReason:  store.ReasonNoMatches,
		},
		{
			name:        "fresh rule inside its first window is warming up",
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 120},
			health:      healthyPull(),
			now:         created.Add(2 * time.Hour),
			wantOutcome: store.OutcomeSignalWarmingUp,
		},
		{
			name:        "disabled rule is inert",
			mutate:      func(r *store.MonitoringExpectedSignal) { r.Enabled = false },
			observed:    store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0},
			health:      store.SourceCollectionHealth{Enabled: false, ConsecutiveFailures: 9},
			wantOutcome: store.OutcomeSignalDisabled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := baseRule(created)
			if tc.mutate != nil {
				tc.mutate(&rule)
			}
			at := tc.now
			if at.IsZero() {
				at = now
			}
			got := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
				Rule: rule, Observed: tc.observed, Health: tc.health,
				Now: at, Location: time.UTC,
			})
			if got.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q (detail: %s)", got.Outcome, tc.wantOutcome, got.Detail)
			}
			if got.Raise != tc.wantRaise {
				t.Fatalf("raise = %v, want %v", got.Raise, tc.wantRaise)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.SignalPresent != tc.wantPresent {
				t.Fatalf("signal_present = %v, want %v", got.SignalPresent, tc.wantPresent)
			}
			if got.ClassKey != tc.wantClass {
				t.Fatalf("class_key = %q, want %q", got.ClassKey, tc.wantClass)
			}
			if tc.wantRaise && got.Severity != rule.Severity {
				t.Fatalf("severity = %q, want %q", got.Severity, rule.Severity)
			}
		})
	}
}

// TestEvaluateExpectedSignalSchedule is the anti-3am-false-positive suite: a
// business-hours rule must stay silent overnight AND must not fire the moment
// business hours open on the strength of overnight quiet.
func TestEvaluateExpectedSignalSchedule(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// 09:00-17:00 local, Monday-Friday, 6h window.
	businessHours := func(r *store.MonitoringExpectedSignal) {
		r.Timezone = "Europe/London"
		r.ActiveStartMinute, r.ActiveEndMinute = 9*60, 17*60
		r.ActiveDaysMask = 0b0111110 // Mon..Fri
	}
	cases := []struct {
		name        string
		mutate      func(*store.MonitoringExpectedSignal)
		local       time.Time
		wantOutcome store.ExpectedSignalOutcome
	}{
		{
			name: "3am on a weekday is outside active hours",
			// Monday 2026-07-20 03:00 local.
			local:       time.Date(2026, 7, 20, 3, 0, 0, 0, london),
			wantOutcome: store.OutcomeSignalOutsideActiveHours,
		},
		{
			name:        "09:05 has not accumulated a full window of active time",
			local:       time.Date(2026, 7, 20, 9, 5, 0, 0, london),
			wantOutcome: store.OutcomeSignalPartialWindow,
		},
		{
			name:        "15:30 is a full 6h into the active period and fires",
			local:       time.Date(2026, 7, 20, 15, 30, 0, 0, london),
			wantOutcome: store.OutcomeSignalAbsent,
		},
		{
			name:        "Sunday is not an active day",
			local:       time.Date(2026, 7, 19, 12, 0, 0, 0, london),
			wantOutcome: store.OutcomeSignalOutsideActiveHours,
		},
		{
			name: "wrapping nightly batch window is active after midnight",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.ActiveStartMinute, r.ActiveEndMinute = 22*60, 6*60
				r.ActiveDaysMask = 0x7F
				r.WindowSeconds = int64(time.Hour.Seconds())
			},
			local:       time.Date(2026, 7, 20, 2, 0, 0, 0, london),
			wantOutcome: store.OutcomeSignalAbsent,
		},
		{
			name: "wrapping window just after it opens is a partial window",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.ActiveStartMinute, r.ActiveEndMinute = 22*60, 6*60
				r.ActiveDaysMask = 0x7F
				r.WindowSeconds = int64(time.Hour.Seconds())
			},
			local:       time.Date(2026, 7, 20, 22, 10, 0, 0, london),
			wantOutcome: store.OutcomeSignalPartialWindow,
		},
		{
			name: "wrapping window outside both halves is quiet",
			mutate: func(r *store.MonitoringExpectedSignal) {
				r.ActiveStartMinute, r.ActiveEndMinute = 22*60, 6*60
				r.ActiveDaysMask = 0x7F
			},
			local:       time.Date(2026, 7, 20, 12, 0, 0, 0, london),
			wantOutcome: store.OutcomeSignalOutsideActiveHours,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := baseRule(created)
			businessHours(&rule)
			if tc.mutate != nil {
				tc.mutate(&rule)
			}
			got := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
				Rule: rule,
				Observed: store.ExpectedSignalObservation{
					MatchCount: 0, TotalLines: 500,
				},
				Health: healthyPull(), Now: tc.local.UTC(), Location: london,
			})
			if got.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q (detail: %s)", got.Outcome, tc.wantOutcome, got.Detail)
			}
		})
	}
}

// TestEvaluateExpectedSignalRecoveryAtAnyHour: a signal returning outside
// active hours must still count as recovery, otherwise an incident raised at
// 16:59 would stay latched all night.
func TestEvaluateExpectedSignalRecoveryAtAnyHour(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	rule := baseRule(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	rule.Timezone = "Europe/London"
	rule.ActiveStartMinute, rule.ActiveEndMinute = 9*60, 17*60
	rule.ActiveDaysMask = 0b0111110

	got := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule:     rule,
		Observed: store.ExpectedSignalObservation{MatchCount: 2, TotalLines: 40},
		Health:   healthyPull(),
		Now:      time.Date(2026, 7, 20, 3, 0, 0, 0, london).UTC(),
		Location: london,
	})
	if got.Outcome != store.OutcomeSignalHealthy || !got.SignalPresent || got.Raise {
		t.Fatalf("3am recovery must be healthy+present+silent, got %+v", got)
	}
}

func TestValidateMonitoringExpectedSignal(t *testing.T) {
	valid := func() *store.MonitoringExpectedSignal {
		r := &store.MonitoringExpectedSignal{
			WorkspaceID: "ws-1", SourceID: "src-1", Name: "orders",
			WindowSeconds: 3600,
		}
		store.ApplyExpectedSignalDefaults(r)
		return r
	}
	cases := []struct {
		name    string
		mutate  func(*store.MonitoringExpectedSignal)
		wantErr bool
	}{
		{name: "defaults are valid"},
		{name: "window too short", mutate: func(r *store.MonitoringExpectedSignal) { r.WindowSeconds = 30 }, wantErr: true},
		{name: "window beyond retention", mutate: func(r *store.MonitoringExpectedSignal) { r.WindowSeconds = 8 * 24 * 3600 }, wantErr: true},
		{name: "empty name", mutate: func(r *store.MonitoringExpectedSignal) { r.Name = "" }, wantErr: true},
		{name: "missing source", mutate: func(r *store.MonitoringExpectedSignal) { r.SourceID = "" }, wantErr: true},
		{name: "bad severity", mutate: func(r *store.MonitoringExpectedSignal) { r.Severity = "loud" }, wantErr: true},
		{name: "bad min severity", mutate: func(r *store.MonitoringExpectedSignal) { r.MinSeverity = "loud" }, wantErr: true},
		{name: "empty min severity allowed", mutate: func(r *store.MonitoringExpectedSignal) { r.MinSeverity = "" }},
		{name: "bad day mask", mutate: func(r *store.MonitoringExpectedSignal) { r.ActiveDaysMask = 255 }, wantErr: true},
		{name: "minute out of range", mutate: func(r *store.MonitoringExpectedSignal) { r.ActiveStartMinute = 2000 }, wantErr: true},
		{name: "unknown timezone", mutate: func(r *store.MonitoringExpectedSignal) { r.Timezone = "Mars/Olympus" }, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			err := store.ValidateMonitoringExpectedSignal(r)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
