package baseline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestLearnPromotesCleanPeriodicSignal(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f, cleanCandidate())

	l.Learn(context.Background())

	if len(f.created) != 1 {
		t.Fatalf("created %d rules; want exactly 1", len(f.created))
	}
	rule := f.created[0]
	if rule.MatchSubstring != "order sync completed batch=" {
		t.Errorf("rule matcher = %q; the learner must fingerprint message TEXT", rule.MatchSubstring)
	}
	if rule.WindowSeconds != int64(time.Hour/time.Second) {
		t.Errorf("window = %ds; want 3600 (6 missed 10-minute runs)", rule.WindowSeconds)
	}
	if !rule.RequireSourceLiveness {
		t.Error("a learned rule must require source liveness, or total silence " +
			"reports ABSENCE when the honest answer is COLLECTION")
	}
	if rule.MinCount != 1 {
		t.Errorf("min_count = %d; want 1 — anything higher can be tripped by jitter", rule.MinCount)
	}
	b := f.baselines["tpl-orders"]
	if b == nil || b.Decision != store.BaselinePromoted {
		t.Fatalf("baseline = %+v; want a promoted row", b)
	}
	if b.RuleID != rule.ID {
		t.Errorf("baseline rule_id = %q; want %q — ownership runs through this pointer",
			b.RuleID, rule.ID)
	}
	if !b.ObservedAt.Equal(fixedNow) {
		t.Errorf("observed_at = %s; want the injected clock %s", b.ObservedAt, fixedNow)
	}
}

// TestLearnRefusesUnpromotableCandidates is the precision suite. Every case here
// is a job that a naive learner would happily alert on.
func TestLearnRefusesUnpromotableCandidates(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*store.BaselineCandidate)
		want      store.BaselineDecision
		reasonHas string
	}{
		{
			// Genuine noise: an exponential arrival process, which is what a
			// non-scheduled template really looks like.
			//
			// This case used to feed strictly alternating 1-minute and
			// 30-minute gaps and call them "irregular". They are nothing of
			// the kind — that pattern is a perfectly deterministic 31-minute
			// cycle emitting two lines per tick, i.e. exactly the
			// bursty-but-clockwork shape the production job has. Asserting it
			// must be rejected encoded the very defect that made recall zero,
			// so the fixture is now actual randomness.
			name: "irregular arrivals are not a schedule",
			mutate: func(c *store.BaselineCandidate) {
				c.Gaps = noisyGaps(10*time.Minute, 432, 7)
			},
			want:      store.BaselineRejectIrregular,
			reasonHas: "not periodic",
		},
		{
			name: "insufficient history cannot promote",
			mutate: func(c *store.BaselineCandidate) {
				c.FirstSeen = fixedNow.Add(-40 * time.Hour)
			},
			want:      store.BaselineRejectShortSpan,
			reasonHas: "retained history",
		},
		{
			name: "conditional terminal line is refused",
			mutate: func(c *store.BaselineCandidate) {
				// The measured prod case: the invoice job's only observable
				// terminal line is its early return.
				c.Masked = "no invoices to send for <n> accounts"
				c.MatchSubstring = "no invoices to send for"
			},
			want:      store.BaselineRejectConditionalTerminal,
			reasonHas: "no work was done",
		},
		{
			name: "weekday-only shape is refused rather than guessed",
			mutate: func(c *store.BaselineCandidate) {
				c.DayGaps = 8
			},
			want:      store.BaselineRejectDayGaps,
			reasonHas: "weekly or weekday pattern",
		},
		{
			// Short of a whole week the range can miss a weekend entirely,
			// so a weekday-only job still looks continuous.
			name: "less than a full week of day history is not enough",
			mutate: func(c *store.BaselineCandidate) {
				c.DayHistoryDays = store.BaselineMinDayHistoryDays - 1
			},
			want:      store.BaselineRejectDayGaps,
			reasonHas: "one whole week",
		},
		{
			name: "unhealthy collection teaches nothing",
			mutate: func(c *store.BaselineCandidate) {
				c.Health.ConsecutiveFailures = 3
			},
			want:      store.BaselineRejectCollectionUnhealthy,
			reasonHas: "collection has failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cleanCandidate()
			tt.mutate(&c)
			f := newFakeLearnerStore()
			newTestLearner(f, c).Learn(context.Background())

			if len(f.created) != 0 {
				t.Fatalf("created %d rules from an unpromotable candidate; want 0", len(f.created))
			}
			b := f.baselines["tpl-orders"]
			if b == nil {
				t.Fatal("no baseline stored — a rejection with no record is the shrug " +
					"this table exists to avoid")
			}
			if b.Decision != tt.want {
				t.Fatalf("decision = %q; want %q (reason: %s)", b.Decision, tt.want, b.Reason)
			}
			if b.RuleID != "" {
				t.Errorf("rejected baseline points at rule %q", b.RuleID)
			}
			if !strings.Contains(b.Reason, tt.reasonHas) {
				t.Errorf("reason %q does not explain itself with %q", b.Reason, tt.reasonHas)
			}
		})
	}
}

// TestLearnDoesNotRelearnBreakageAsNormal is the crux. A job that was learned
// and has since gone completely silent contributes NO arrivals, so the next pass
// sees no sample. That must leave the armed rule exactly as it is — never widen
// it, never delete it, never record the outage as the new cadence.
func TestLearnDoesNotRelearnBreakageAsNormal(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f, cleanCandidate())
	l.Learn(context.Background())
	if len(f.created) != 1 {
		t.Fatalf("setup: created %d rules; want 1", len(f.created))
	}
	armed := f.created[0]
	armedWindow := armed.WindowSeconds

	// The job hangs: it emits nothing at all for the whole next horizon.
	silent := cleanCandidate()
	silent.Gaps, silent.LineCount = nil, 0
	f.candidates["src-1"] = []store.BaselineCandidate{silent}
	l.Learn(context.Background())

	if len(f.updated) != 0 {
		t.Fatalf("the rule was rewritten %d time(s) while its job was dead; "+
			"an outage must never move the baseline", len(f.updated))
	}
	if got := f.rules[armed.ID]; got == nil || got.WindowSeconds != armedWindow {
		t.Fatalf("window moved to %v from %d; the rule that would catch the outage "+
			"must not be relaxed by the outage", got, armedWindow)
	}
	if !f.rules[armed.ID].Enabled {
		t.Error("the rule was disabled by its job going silent")
	}
	b := f.baselines["tpl-orders"]
	if b.Decision == store.BaselinePromoted {
		t.Error("a silent job re-promoted itself; silence must never be evidence")
	}
	if b.Decision != store.BaselineRejectFewSamples {
		t.Errorf("decision = %q; want few_samples — no arrivals means no sample", b.Decision)
	}
	if b.RuleID != armed.ID {
		t.Errorf("baseline lost its rule pointer (%q); the rule is still live", b.RuleID)
	}
}

// TestLearnFreezesWhileAnIncidentIsOpen is the second layer of the same defence:
// the one moment the evidence most wants to argue "nothing is the new normal" is
// the moment we stop listening to it.
func TestLearnFreezesWhileAnIncidentIsOpen(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f, cleanCandidate())
	l.Learn(context.Background())
	armed := f.created[0]
	f.rules[armed.ID].ActiveIncidentID = "incident-1"

	// A pass whose evidence would otherwise widen the window a long way.
	slower := cleanCandidate()
	slower.Gaps = periodicGaps(40*time.Minute, time.Minute, 432)
	slower.FirstSeen = fixedNow.Add(-14 * 24 * time.Hour)
	f.candidates["src-1"] = []store.BaselineCandidate{slower}
	l.Learn(context.Background())

	if len(f.updated) != 0 {
		t.Fatalf("baseline adapted %d time(s) with an incident open; it must be frozen",
			len(f.updated))
	}
	b := f.baselines["tpl-orders"]
	if b.Decision != store.BaselineFrozen {
		t.Errorf("decision = %q; want frozen_incident_active so an operator can see why", b.Decision)
	}
	if !strings.Contains(b.Reason, "frozen") {
		t.Errorf("reason %q does not say the baseline was frozen", b.Reason)
	}
}

// A learner from an older release may already have promoted a logwatch-owned
// diagnostic. Once the safer classifier sees it, the rule must be retired even
// if its inverted false-positive incident is open.
func TestLearnDisablesPreviouslyPromotedMonitoringSynthetic(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f, cleanCandidate())
	l.Learn(context.Background())
	armed := f.created[0]
	f.rules[armed.ID].ActiveIncidentID = "incident-1"

	synthetic := cleanCandidate()
	synthetic.Masked = "logwatch: source discontinuity — container/service restarted"
	synthetic.MatchSubstring = "logwatch: source discontinuity"
	f.candidates["src-1"] = []store.BaselineCandidate{synthetic}
	l.Learn(context.Background())

	if len(f.updated) != 1 {
		t.Fatalf("updated %d rules; want the unsafe rule disabled once", len(f.updated))
	}
	if f.rules[armed.ID].Enabled {
		t.Fatal("previously promoted monitor-owned rule is still enabled")
	}
	b := f.baselines["tpl-orders"]
	if b.Decision != store.BaselineRejectMonitoringSynthetic {
		t.Fatalf("decision = %q; want %q", b.Decision, store.BaselineRejectMonitoringSynthetic)
	}
	if b.RuleID != armed.ID {
		t.Fatalf("baseline rule pointer = %q; want %q for auditability", b.RuleID, armed.ID)
	}
}

// TestLearnAcceptsALegitimateReschedule is the other side of the drift
// tension, and it has to hold or the two tests above would be satisfied by a
// learner that simply never adapts. A team moving the job from ten minutes to
// thirty is a real change: the source is healthy, no incident is open, and
// there are hundreds of fresh arrivals AT the new cadence. That must be
// accepted — but over several consistent passes, not in one step.
func TestLearnAcceptsALegitimateReschedule(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f, cleanCandidate())
	l.Learn(context.Background())
	armed := f.created[0]
	original := armed.WindowSeconds

	rescheduled := cleanCandidate()
	rescheduled.Gaps = periodicGaps(30*time.Minute, time.Minute, 432)
	rescheduled.FirstSeen = fixedNow.Add(-10 * 24 * time.Hour)
	f.candidates["src-1"] = []store.BaselineCandidate{rescheduled}

	// One pass must not jump the whole way — that is the rate limit doing its
	// job, and it is what buys time for a degradation to be noticed as one.
	l.Learn(context.Background())
	afterOne := f.rules[armed.ID].WindowSeconds
	if afterOne > int64(float64(original)*store.BaselineMaxWidenRatio)+1 {
		t.Errorf("window jumped from %ds to %ds in a single pass; adaptation must be "+
			"rate limited", original, afterOne)
	}

	for i := 0; i < 5; i++ {
		l.Learn(context.Background())
	}
	final := f.rules[armed.ID].WindowSeconds
	if final <= original {
		t.Fatalf("window is still %ds after six consistent passes at a slower cadence; "+
			"a real reschedule must eventually be learned", final)
	}
	// And it must converge on the new cadence rather than widening forever.
	if want := int64(6 * 30 * 60); final > want {
		t.Errorf("window = %ds; want no more than %ds (six 30-minute periods)", final, want)
	}
}

// TestLearnSurvivesAMiningFailure — one unreadable source must not stop the
// fleet's baselines being maintained.
func TestLearnSurvivesAMiningFailure(t *testing.T) {
	f := newFakeLearnerStore()
	l := newTestLearner(f)
	f.mineErr = errors.New("source unreachable")
	l.Learn(context.Background())
	if len(f.upserted) != 0 {
		t.Errorf("wrote %d baselines despite a mining failure", len(f.upserted))
	}
}

// TestNewLearnerWithoutStorageIsInert lets the daemon call the constructor
// unconditionally at boot.
func TestNewLearnerWithoutStorageIsInert(t *testing.T) {
	if l := NewLearner(nil); l != nil {
		t.Fatal("NewLearner(nil) must return nil so boot wiring stays unconditional")
	}
	var l *Learner
	l.Learn(context.Background()) // must not panic
	l.Run(context.Background())
}
