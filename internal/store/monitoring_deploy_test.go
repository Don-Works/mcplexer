package store

import (
	"testing"
	"time"
)

var deployTestNow = time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)

// TestIsDeployBannerMatchesRealBanners covers the detector's precision in both
// directions. A false positive now discards real arrivals from the learner's
// evidence, so over-matching is still the more damaging error even though it no
// longer suppresses any alert.
func TestIsDeployBannerMatchesRealBanners(t *testing.T) {
	tests := []struct {
		name   string
		masked string
		want   bool
	}{
		{
			// The measured production banner, after masking. The version
			// string masks away, which is what lets one rule cover every
			// future release with nobody updating it.
			name:   "the real production version banner",
			masked: "info api/main.go:159 running version: v<n>.<n>.<n>",
			want:   true,
		},
		{"server started", "server started on port <n>", true},
		{"listening banner", "listening on <ip>", true},
		{"daemon starting", "daemon starting, pid <n>", true},
		{"started with version", "started worker version <n>", true},

		// Nothing below is a release, so nothing below may excise history.
		{"an ordinary completion", "order sync completed batch=<n> in <dur>", false},
		{"a version mismatch failure", "unsupported client version <n> rejected", false},
		{"a restart of something else", "restarting connection pool after <dur>", false},
		{"a health probe", "health probe ok <dur>", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDeployBanner(tt.masked); got != tt.want {
				t.Errorf("IsDeployBanner(%q) = %v; want %v", tt.masked, got, tt.want)
			}
		})
	}
}

// TestDeploySpansGap pins the excision predicate. It decides which inter-arrival
// gaps are restart artefacts rather than evidence of cadence, so an off-by-one
// at either boundary either keeps a restart gap in the baseline or discards a
// real one.
func TestDeploySpansGap(t *testing.T) {
	at := func(m int) time.Time { return deployTestNow.Add(time.Duration(m) * time.Minute) }
	deploys := []time.Time{at(10), at(50)}

	tests := []struct {
		name      string
		prev, ts  time.Time
		wantSpans bool
	}{
		{"a gap containing a deploy", at(5), at(15), true},
		{"a gap entirely before", at(0), at(9), false},
		{"a gap entirely after", at(20), at(40), false},
		{"a gap between two deploys", at(15), at(45), false},
		{"a deploy exactly at the gap end counts", at(5), at(10), true},
		{"a deploy exactly at the gap start does not", at(10), at(20), false},
		{"a long gap covering both deploys", at(0), at(60), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeploySpansGap(deploys, tt.prev, tt.ts); got != tt.wantSpans {
				t.Errorf("DeploySpansGap(%s, %s) = %v; want %v",
					tt.prev.Format(time.TimeOnly), tt.ts.Format(time.TimeOnly),
					got, tt.wantSpans)
			}
		})
	}

	if DeploySpansGap(nil, at(0), at(60)) {
		t.Error("no deploys must never excise a gap")
	}
}

// TestNoSuppressionMechanismExists is a guard against reintroducing the
// approach this file deliberately abandoned.
//
// An earlier revision opened a bounded deploy-grace window that suppressed
// absence raises. It was removed because a grace window is a suppression
// mechanism with an expiry, and this system had already been bitten twice by
// those — a per-template cooldown and a workspace hourly cap that between them
// hid a dead alert channel for six days. Under evidence subtraction there is no
// window, so there is nothing that can fail to expire. If a future change adds
// a suppression state back to the evaluator, the compile break here is the
// intended warning.
func TestNoSuppressionMechanismExists(t *testing.T) {
	// ExpectedSignalInput carries only OBSERVATION. If a field appears here
	// that gates output rather than describing evidence, this test should be
	// re-read before it is updated.
	rule := &MonitoringExpectedSignal{
		Enabled: true, MinCount: 1, WindowSeconds: 600, Severity: SeverityError,
		RequireSourceLiveness: true,
	}
	ApplyExpectedSignalDefaults(rule)
	last := deployTestNow.Add(-time.Hour)
	rule.LastSignalAt = &last
	in := ExpectedSignalInput{
		Rule:     *rule,
		Observed: ExpectedSignalObservation{TotalLines: 10, MatchCount: 0},
		Health:   SourceCollectionHealth{Enabled: true},
		Now:      deployTestNow,
		Location: time.UTC,
	}
	if d := EvaluateExpectedSignal(in); !d.Raise {
		t.Errorf("outcome = %q; a silent signal must raise with no deploy concept "+
			"anywhere on this path", d.Outcome)
	}
}
