// handler_monitoring_baselines.go — the operator's window into what the daemon
// decided "normal" looks like.
//
// A learned rule nobody can interrogate is a rule nobody trusts. Nothing here
// configures anything: it is a read surface over monitoring_signal_baselines,
// which stores REJECTIONS with the same weight as promotions precisely so that
// "why is there no alert for this job?" has an answer instead of a shrug.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// baselineReader is the slice of storage this surface needs. Asserted rather
// than required, so a store without baseline support degrades to a clear message
// instead of a nil dereference.
type baselineReader interface {
	ListSignalBaselines(ctx context.Context, workspaceID string, limit int) ([]*store.SignalBaseline, error)
	ListSignalBaselinesForSource(ctx context.Context, sourceID string, limit int) ([]*store.SignalBaseline, error)
}

type monitoringBaselineArgs struct {
	WorkspaceID  string `json:"workspace_id"`
	SourceID     string `json:"source_id"`
	Decision     string `json:"decision"`
	PromotedOnly bool   `json:"promoted_only"`
	Limit        int    `json:"limit"`
}

func (h *handler) handleMonitoringBaselines(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var args monitoringBaselineArgs
	_ = json.Unmarshal(raw, &args)
	wsID, rpc := h.dataWorkspace(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc)
	}
	reader, ok := h.store.(baselineReader)
	if !ok {
		return marshalErrorResult("this daemon's store does not support learned baselines")
	}
	rows, err := listMonitoringBaselines(ctx, reader, wsID, args)
	if err != nil {
		return marshalErrorResult(err.Error())
	}
	views := make([]monitoringBaselineView, 0, len(rows))
	for _, b := range rows {
		if !baselineMatchesFilter(b, args) {
			continue
		}
		views = append(views, newMonitoringBaselineView(b))
	}
	return monitoringJSON(map[string]any{
		"baselines": views,
		"summary":   summarizeBaselines(views),
	})
}

func listMonitoringBaselines(
	ctx context.Context, reader baselineReader, wsID string, args monitoringBaselineArgs,
) ([]*store.SignalBaseline, error) {
	if strings.TrimSpace(args.SourceID) != "" {
		return reader.ListSignalBaselinesForSource(ctx, args.SourceID, args.Limit)
	}
	return reader.ListSignalBaselines(ctx, wsID, args.Limit)
}

func baselineMatchesFilter(b *store.SignalBaseline, args monitoringBaselineArgs) bool {
	if args.PromotedOnly && b.RuleID == "" {
		return false
	}
	if d := strings.TrimSpace(args.Decision); d != "" && string(b.Decision) != d {
		return false
	}
	return true
}

// monitoringBaselineView is one baseline rendered for a human. The raw evidence
// is kept verbatim so a promotion can be re-derived by hand; Explain is the
// one-line version for someone scanning a list.
type monitoringBaselineView struct {
	TemplateID     string  `json:"template_id"`
	SourceID       string  `json:"source_id"`
	Masked         string  `json:"masked"`
	MatchSubstring string  `json:"match_substring,omitempty"`
	Decision       string  `json:"decision"`
	Promoted       bool    `json:"promoted"`
	RuleID         string  `json:"rule_id,omitempty"`
	Confidence     float64 `json:"confidence"`
	Explain        string  `json:"explain"`
	Reason         string  `json:"reason"`

	PeriodSeconds  float64 `json:"period_seconds"`
	WindowSeconds  int64   `json:"window_seconds"`
	RelativeMAD    float64 `json:"relative_mad"`
	P95Ratio       float64 `json:"p95_ratio"`
	SampleCount    int     `json:"sample_count"`
	CyclesObserved float64 `json:"cycles_observed"`
	HourOccupancy  float64 `json:"hour_occupancy"`
	SpanSeconds    float64 `json:"span_seconds"`
	ScanTruncated  bool    `json:"scan_truncated,omitempty"`
	LearnedRuns    int64   `json:"learned_runs"`
	ObservedAt     string  `json:"observed_at,omitempty"`
}

func newMonitoringBaselineView(b *store.SignalBaseline) monitoringBaselineView {
	v := monitoringBaselineView{
		TemplateID: b.TemplateID, SourceID: b.SourceID, Masked: b.Masked,
		MatchSubstring: b.MatchSubstring, Decision: string(b.Decision),
		Promoted: b.RuleID != "", RuleID: b.RuleID, Confidence: b.Confidence,
		Reason: b.Reason, PeriodSeconds: b.PeriodSeconds, WindowSeconds: b.WindowSeconds,
		RelativeMAD: b.RelativeMAD, P95Ratio: b.P95Ratio, SampleCount: b.SampleCount,
		CyclesObserved: b.CyclesObserved, HourOccupancy: b.HourOccupancy,
		SpanSeconds: b.SpanSeconds, ScanTruncated: b.ScanTruncated,
		LearnedRuns: b.LearnedRuns,
	}
	if !b.ObservedAt.IsZero() {
		v.ObservedAt = b.ObservedAt.UTC().Format(time.RFC3339)
	}
	v.Explain = explainBaseline(b)
	return v
}

// explainBaseline renders the one-line answer to "what did you decide, and would
// this fire?". It always states the consequence, because a decision code alone
// does not tell an operator whether they are covered.
func explainBaseline(b *store.SignalBaseline) string {
	if b.Decision == store.BaselinePromoted && b.RuleID != "" {
		return fmt.Sprintf(
			"MONITORED: normally arrives every %s; alerts if nothing matches %q for %s (confidence %.2f).",
			baselineHuman(b.PeriodSeconds), b.MatchSubstring,
			baselineHuman(float64(b.WindowSeconds)), b.Confidence)
	}
	if b.Decision == store.BaselineFrozen {
		return "NOT UPDATED: an incident is open on this rule, so the baseline is frozen " +
			"and a broken cadence cannot be accepted as the new normal while it is broken."
	}
	return fmt.Sprintf("NOT MONITORED (%s): no alert exists for this signal.", b.Decision)
}

// baselineHuman renders a second count at operator resolution.
func baselineHuman(seconds float64) string {
	if seconds <= 0 {
		return "n/a"
	}
	d := time.Duration(seconds) * time.Second
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < 48*time.Hour {
		return d.Round(time.Second).String()
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

// summarizeBaselines gives the headline an operator wants first: how much of
// what the daemon looked at is actually covered, and why the rest is not.
func summarizeBaselines(views []monitoringBaselineView) map[string]any {
	byDecision := map[string]int{}
	promoted := 0
	for _, v := range views {
		byDecision[v.Decision]++
		if v.Promoted {
			promoted++
		}
	}
	return map[string]any{
		"examined":    len(views),
		"monitored":   promoted,
		"by_decision": byDecision,
		"note": "Baselines are inferred from retained log history by plain statistics " +
			"(median, MAD, p95) — no model is involved and nobody configures them. " +
			"Promotion is deliberately biased to precision: a candidate the evidence " +
			"cannot settle is recorded as a rejection with a reason rather than " +
			"promoted on a guess.",
	}
}
