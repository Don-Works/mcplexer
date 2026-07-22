// Package baseline infers what "normal" looks like, so nobody has to describe
// it.
//
// The 2026-07-20 incident: a recurring order-sync job hung. The process stayed
// alive and emitted nothing, so process tables, unit status and container
// healthchecks all stayed green. No error-pattern detector could have caught
// it, because there was no error to match. The only observable was "time since
// the last successful completion exceeded the normal cadence" — and nothing
// knew what the normal cadence was.
//
// The absence evaluator that answers exactly that question already existed
// (store.EvaluateExpectedSignal) and was correct, and was dead code, because it
// needs a rule and no rule could ever be created. The operator's position on
// who would write one: "no user is gonna describe those alerts to be honest -
// you should just infer them from the logs + operations of the system - what
// does normal look like basically."
//
// So this package mines retained history for templates that arrive at a stable
// cadence — the fingerprint of a scheduled job — and promotes the confident
// ones into rules on its own. It is entirely deterministic: median, MAD and a
// p95 over rows the daemon already stores. No model is called, no prompt grows,
// no worker is woken, and the pull path is never touched.
package baseline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// learnInterval is how often the learner re-derives baselines.
//
// Hourly is already far faster than the evidence can meaningfully change: a
// promotion needs 72 hours of arrivals and 14 days of gap-free day history, so
// a baseline moves on a scale of days. The cadence exists to pick up new
// sources and new jobs within a working day, not to chase the data.
const learnInterval = time.Hour

// learnStartupDelay staggers the first pass past daemon boot so a restart
// storm does not put every node's learning scan on the same instant.
const learnStartupDelay = 5 * time.Minute

// Store is the learner's slice of storage.
type Store interface {
	store.MonitoringBaselineStore
	store.MonitoringExpectedSignalStore
}

// Learner mines baselines and maintains the rules they imply.
type Learner struct {
	store    Store
	now      func() time.Time
	interval time.Duration
}

// NewLearner wires a learner. Returns nil when storage is missing, so a caller
// can start it unconditionally at boot.
func NewLearner(st Store) *Learner {
	if st == nil {
		return nil
	}
	return &Learner{store: st, now: time.Now, interval: learnInterval}
}

// Run loops until ctx is cancelled. Call in a goroutine at daemon boot —
// without that call this package is dead code and the evaluator has no rules,
// which is the state that produced the twelve-hour silence.
func (l *Learner) Run(ctx context.Context) {
	if l == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(learnStartupDelay):
	}
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		l.Learn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Learn runs one full pass. Exported so the daemon and tests can drive a single
// pass without a timer.
func (l *Learner) Learn(ctx context.Context) {
	if l == nil {
		return
	}
	sources, err := l.store.ListEnabledLogSources(ctx)
	if err != nil {
		slog.Warn("baseline: list sources", "error", err)
		return
	}
	for _, src := range sources {
		if ctx.Err() != nil {
			return
		}
		l.learnSource(ctx, src)
	}
}

// learnSource mines and reconciles one source. A failure on one source is
// logged and skipped: a single unreadable source must not stop the fleet's
// baselines being maintained.
func (l *Learner) learnSource(ctx context.Context, src *store.LogSource) {
	if src == nil {
		return
	}
	now := l.now().UTC()
	candidates, err := l.store.MineBaselineCandidates(
		ctx, src, now.Add(-store.BaselineLearnHorizon), now)
	if err != nil {
		slog.Warn("baseline: mine candidates", "source", src.Name, "error", err)
		return
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		if err := l.reconcile(ctx, c, now); err != nil {
			slog.Warn("baseline: reconcile candidate", "source", src.Name,
				"template", c.TemplateID, "error", err)
		}
	}
}

// reconcile judges one candidate and applies the result to its rule.
func (l *Learner) reconcile(ctx context.Context, c store.BaselineCandidate, now time.Time) error {
	verdict := store.EvaluateBaselineCandidate(c)
	existing, ruleID, err := l.liveRule(ctx, c.TemplateID)
	if err != nil {
		return err
	}
	decision := store.ReconcileBaseline(existing, verdict)
	ruleID, err = l.apply(ctx, c, verdict, decision, existing, ruleID)
	if err != nil {
		return err
	}
	return l.store.UpsertSignalBaseline(ctx, buildBaseline(c, verdict, decision, ruleID, now))
}

// liveRule resolves the learner-owned rule for a template, if any.
//
// Ownership runs through the baseline row's rule_id and nothing else. An
// operator-authored rule has no baseline pointing at it, so it is invisible
// here and is never adapted or overwritten by the learner.
func (l *Learner) liveRule(
	ctx context.Context, templateID string,
) (*store.MonitoringExpectedSignal, string, error) {
	prior, err := l.store.GetSignalBaselineByTemplate(ctx, templateID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if prior.RuleID == "" {
		return nil, "", nil
	}
	rule, err := l.store.GetMonitoringExpectedSignal(ctx, prior.RuleID)
	if errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrMonitoringExpectedSignalNotFound) {
		// The operator deleted the rule. That is a decision, not a gap: the
		// pointer is dropped and the next pass may propose it afresh, with the
		// full promotion ladder applied again.
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return rule, prior.RuleID, nil
}

// apply performs the create/update the reconciliation asked for.
func (l *Learner) apply(
	ctx context.Context, c store.BaselineCandidate, verdict store.BaselineVerdict,
	decision store.BaselineReconciliation, existing *store.MonitoringExpectedSignal, ruleID string,
) (string, error) {
	switch decision.Action {
	case store.BaselineActionCreate:
		rule := store.ProposeExpectedSignal(c, verdict)
		rule.WindowSeconds = decision.WindowSeconds
		if err := l.store.CreateMonitoringExpectedSignal(ctx, rule); err != nil {
			return "", err
		}
		slog.Info("baseline: learned a recurring signal", "source", c.SourceID,
			"rule", rule.Name, "period_seconds", int64(verdict.Stats.Median),
			"window_seconds", rule.WindowSeconds, "samples", verdict.Stats.Count,
			"confidence", verdict.Confidence)
		return rule.ID, nil
	case store.BaselineActionUpdate:
		if existing == nil {
			return ruleID, nil
		}
		previous := existing.WindowSeconds
		existing.WindowSeconds = decision.WindowSeconds
		if err := l.store.UpdateMonitoringExpectedSignal(ctx, existing); err != nil {
			return ruleID, err
		}
		slog.Info("baseline: adapted a learned signal", "rule", existing.Name,
			"window_seconds", existing.WindowSeconds, "previous_window_seconds", previous,
			"reason", decision.Reason)
		return existing.ID, nil
	case store.BaselineActionDisable:
		if existing == nil || !existing.Enabled {
			return ruleID, nil
		}
		existing.Enabled = false
		if err := l.store.UpdateMonitoringExpectedSignal(ctx, existing); err != nil {
			return ruleID, err
		}
		slog.Info("baseline: disabled unsafe synthetic monitoring rule",
			"rule", existing.Name, "template", c.TemplateID,
			"reason", decision.Reason)
		return existing.ID, nil
	default:
		return ruleID, nil
	}
}

// buildBaseline projects the candidate and verdict into the persisted record.
// The reconciliation's reason wins over the verdict's when adaptation was
// suppressed, because "frozen: an incident is open" is the answer the operator
// needs, not the statistics that were ignored.
func buildBaseline(
	c store.BaselineCandidate, v store.BaselineVerdict,
	decision store.BaselineReconciliation, ruleID string, now time.Time,
) *store.SignalBaseline {
	b := &store.SignalBaseline{
		WorkspaceID: c.WorkspaceID, SourceID: c.SourceID, TemplateID: c.TemplateID,
		RuleID: ruleID, Masked: c.Masked, MatchSubstring: c.MatchSubstring,
		Decision: v.Decision, Reason: v.Reason,
		PeriodSeconds: v.Stats.Median, P95Seconds: v.Stats.P95, MADSeconds: v.Stats.MAD,
		RelativeMAD: v.Stats.RelativeMAD, P95Ratio: v.Stats.P95Ratio,
		SampleCount: v.Stats.Count, CyclesObserved: v.Cycles,
		HourOccupancy: v.Occupancy, SpanSeconds: c.Span().Seconds(),
		Confidence: v.Confidence, WindowSeconds: decision.WindowSeconds,
		ActiveStartMinute: 0, ActiveEndMinute: 24 * 60,
		ScanTruncated: c.ScanTruncated,
		FirstSeen:     c.FirstSeen, LastSeen: c.LastSeen, ObservedAt: now,
	}
	if decision.Frozen {
		b.Decision = store.BaselineFrozen
		b.Reason = decision.Reason
	}
	return b
}
