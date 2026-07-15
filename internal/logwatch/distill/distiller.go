// distiller.go — the collect.Sink implementation: lines → templates →
// novelty → anomaly notifications. Deterministic; the only outbound
// path is the escalate dispatcher (which owns throttles + envelope).
package distill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/store"
)

// Store is the distiller's slice of store.Store.
type Store interface {
	UpsertLogTemplate(ctx context.Context, t *store.LogTemplate, n int64) (bool, error)
	InsertLogLines(ctx context.Context, lines []store.LogLine) error
	PruneLogLines(ctx context.Context, sourceID string, maxAge time.Time, maxBytes int64) (int64, error)
	// CountErrorLinesInWindows backs the rate-spike detector: a
	// current window count and its trailing baseline count.
	CountErrorLinesInWindows(ctx context.Context, sourceID string, baselineSince, currentSince time.Time) (current int64, baseline int64, err error)
	// GetLogSourceErrorSpikeActive/SetLogSourceErrorSpikeActive persist
	// the rate-spike hysteresis latch (see evaluateRateSpike).
	GetLogSourceErrorSpikeActive(ctx context.Context, sourceID string) (bool, error)
	SetLogSourceErrorSpikeActive(ctx context.Context, sourceID string, active bool) error
}

// Rate-spike detector tuning: a short current window compared against
// a trailing, non-overlapping baseline of the same metric. Both are
// wall-clock windows over log_lines.ts (not pull-count windows), so a
// slow-cadence source still gets a meaningful baseline.
const (
	rateSpikeCurrentWindow  = 5 * time.Minute
	rateSpikeBaselineWindow = time.Hour
	// rateSpikeMinCount floors out noise: a lone source going from 1
	// to 6 errors is not a "spike" regardless of ratio.
	rateSpikeMinCount = 10
	// rateSpikeMultiplier: the current window's per-minute rate must
	// exceed the baseline's by more than this factor. A zero baseline
	// (no errors at all in the trailing hour) makes any positive
	// current rate satisfy "> 5x zero" — deliberate, since a
	// from-nothing burst is exactly what this detector exists to
	// catch alongside the steady-chronic-volume case it must ignore.
	rateSpikeMultiplier = 5
)

// Notifier is the anomaly outlet — implemented by escalate.Dispatcher.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Notification is one anomaly heading for the dispatcher. The
// dispatcher renders the deterministic envelope from these fields —
// the distiller never formats channel output.
type Notification struct {
	WorkspaceID string
	Severity    string
	Title       string
	Body        string
	TaskID      string
	// NewIncident is true only for the first notification of a newly
	// discovered incident. The dispatcher uses it to gate high-signal
	// human escalation (PWA/Web Push) without buzzing on evidence updates.
	NewIncident    bool
	RemoteHostName string
	RemoteHostAddr string
	SourceName     string
	TemplateID     string
	// Test bypasses the dispatcher's throttles and stamps the title —
	// the Monitoring UI's "send test notification" path, so operators
	// can verify channels without burning the hourly budget.
	Test bool
}

// Distiller implements collect.Sink.
type Distiller struct {
	store    Store
	notifier Notifier
	now      func() time.Time
}

// NewDistiller wires the sink. notifier may be nil (anomalies are
// then logged only — used before the dispatcher is configured).
func NewDistiller(st Store, notifier Notifier) *Distiller {
	return &Distiller{store: st, notifier: notifier, now: time.Now}
}

// TemplateID is the stable identity of one masked shape on one source.
func TemplateID(sourceID, masked string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + masked))
	return hex.EncodeToString(sum[:16])
}

type templateAgg struct {
	tpl   *store.LogTemplate
	count int64
}

// aggregateLines masks + classifies each line, grouping by stable
// template id. order is the deterministic first-seen processing order
// UpsertLogTemplate/fireAnomaly rely on downstream.
func aggregateLines(src *store.LogSource, classifier *Classifier, lines []collect.Line) (aggs map[string]*templateAgg, order []string, rows []store.LogLine) {
	aggs = map[string]*templateAgg{}
	rows = make([]store.LogLine, 0, len(lines))
	for _, line := range lines {
		masked := Normalize(line.Text)
		if masked == "" {
			continue
		}
		id := TemplateID(src.ID, masked)
		agg, ok := aggs[id]
		if !ok {
			agg = &templateAgg{tpl: &store.LogTemplate{
				ID: id, SourceID: src.ID, Masked: masked,
				Severity:  classifier.Classify(line.Text),
				FirstSeen: line.TS, SampleFirst: line.Text,
			}}
			aggs[id] = agg
			order = append(order, id)
		}
		agg.count++
		agg.tpl.LastSeen = line.TS
		agg.tpl.SampleLast = line.Text
		rows = append(rows, store.LogLine{
			SourceID: src.ID, TemplateID: id, TS: line.TS, Line: line.Text,
		})
	}
	return aggs, order, rows
}

// Ingest distills one pull's lines: aggregate per template, upsert
// (novelty), persist raw lines, prune retention, fire anomalies for
// NEW error-class templates (ratified wake floor: error-class
// immediate, info batches into the next digest).
func (d *Distiller) Ingest(ctx context.Context, src *store.LogSource, host *store.RemoteHost, lines []collect.Line) error {
	if len(lines) == 0 {
		return nil
	}
	classifier, err := NewClassifier(src.SeverityRulesJSON)
	if err != nil {
		return err
	}
	aggs, order, rows := aggregateLines(src, classifier, lines)

	for _, id := range order {
		agg := aggs[id]
		isNew, err := d.store.UpsertLogTemplate(ctx, agg.tpl, agg.count)
		if err != nil {
			return fmt.Errorf("distill: upsert template: %w", err)
		}
		if isNew && store.SeverityRank(agg.tpl.Severity) >= store.SeverityRank(store.SeverityError) {
			d.fireAnomaly(ctx, src, host, agg)
		}
	}

	if err := d.store.InsertLogLines(ctx, rows); err != nil {
		return fmt.Errorf("distill: insert lines: %w", err)
	}
	d.evaluateRateSpike(ctx, src, host)
	maxAge := d.now().UTC().AddDate(0, 0, -src.RetentionDays)
	if _, err := d.store.PruneLogLines(ctx, src.ID, maxAge, int64(src.RetentionMB)<<20); err != nil {
		slog.Warn("distill: prune", "source", src.Name, "error", err)
	}
	return nil
}

// fireAnomaly reports a never-seen-before error-class template. The
// dispatcher throttles per (workspace, template), so a storm of new
// shapes still lands as bounded notifications.
func (d *Distiller) fireAnomaly(ctx context.Context, src *store.LogSource, host *store.RemoteHost, agg *templateAgg) {
	if d.notifier == nil {
		slog.Info("distill: anomaly (no dispatcher wired)",
			"source", src.Name, "severity", agg.tpl.Severity, "template", agg.tpl.Masked)
		return
	}
	n := Notification{
		WorkspaceID:    src.WorkspaceID,
		Severity:       agg.tpl.Severity,
		Title:          fmt.Sprintf("new %s-class log template on %s/%s (×%d)", agg.tpl.Severity, host.Name, src.Name, agg.count),
		Body:           fmt.Sprintf("Template: %s\nFirst sample: %s", agg.tpl.Masked, agg.tpl.SampleFirst),
		NewIncident:    true,
		RemoteHostName: host.Name,
		RemoteHostAddr: host.SSHHost,
		SourceName:     src.Name,
		TemplateID:     agg.tpl.ID,
	}
	if err := d.notifier.Notify(ctx, n); err != nil {
		slog.Warn("distill: notify", "source", src.Name, "error", err)
	}
}

// evaluateRateSpike compares the source's current error/critical rate
// against its trailing baseline and edge-triggers the hysteresis
// latch: a notification fires only on the false→true transition, so
// a sustained spike wakes once and a chronic elevated rate that never
// crosses the ratio wakes nobody. Recovery (true→false) clears the
// latch silently, re-arming the next spike.
func (d *Distiller) evaluateRateSpike(ctx context.Context, src *store.LogSource, host *store.RemoteHost) {
	now := d.now().UTC()
	currentSince := now.Add(-rateSpikeCurrentWindow)
	baselineSince := currentSince.Add(-rateSpikeBaselineWindow)

	current, baseline, err := d.store.CountErrorLinesInWindows(ctx, src.ID, baselineSince, currentSince)
	if err != nil {
		slog.Warn("distill: rate spike count", "source", src.Name, "error", err)
		return
	}
	currentRate := float64(current) / rateSpikeCurrentWindow.Minutes()
	baselineRate := float64(baseline) / rateSpikeBaselineWindow.Minutes()
	isSpike := current >= rateSpikeMinCount && currentRate > rateSpikeMultiplier*baselineRate

	active, err := d.store.GetLogSourceErrorSpikeActive(ctx, src.ID)
	if err != nil {
		slog.Warn("distill: rate spike state", "source", src.Name, "error", err)
		return
	}

	switch {
	case isSpike && !active:
		if err := d.fireRateSpike(ctx, src, host, now, current, currentRate, baselineRate); err != nil {
			slog.Warn("distill: notify rate spike", "source", src.Name, "error", err)
			return
		}
		if err := d.store.SetLogSourceErrorSpikeActive(ctx, src.ID, true); err != nil {
			slog.Warn("distill: rate spike arm", "source", src.Name, "error", err)
		}
	case !isSpike && active:
		if err := d.store.SetLogSourceErrorSpikeActive(ctx, src.ID, false); err != nil {
			slog.Warn("distill: rate spike re-arm", "source", src.Name, "error", err)
		}
	}
}

// fireRateSpike reports a sustained error/critical rate more than
// rateSpikeMultiplier above the source's trailing baseline. Unlike
// fireAnomaly (a never-seen-before shape), this fires on ordinary,
// previously-acked templates too — a chronic-but-known error type
// that suddenly accelerates is exactly what per-template novelty
// misses. The key is stable within one hysteresis episode and changes after
// recovery, so a genuine re-offence is not swallowed by the dispatcher's
// per-template cooldown.
func (d *Distiller) fireRateSpike(
	ctx context.Context, src *store.LogSource, host *store.RemoteHost, episodeAt time.Time,
	current int64, currentRate, baselineRate float64,
) error {
	spikeKey := fmt.Sprintf("ratespike:%s:%x", src.ID, episodeAt.UnixNano())
	if d.notifier == nil {
		slog.Info("distill: rate spike (no dispatcher wired)",
			"source", src.Name, "current", current, "current_rate", currentRate, "baseline_rate", baselineRate)
		return nil
	}
	n := Notification{
		WorkspaceID: src.WorkspaceID,
		Severity:    store.SeverityError,
		Title: fmt.Sprintf("error rate spike on %s/%s (×%d in %s, >%dx baseline)",
			host.Name, src.Name, current, rateSpikeCurrentWindow, rateSpikeMultiplier),
		Body: fmt.Sprintf("%d error/critical lines in the last %s (%.1f/min) vs a trailing baseline of %.2f/min over %s.",
			current, rateSpikeCurrentWindow, currentRate, baselineRate, rateSpikeBaselineWindow),
		NewIncident:    true,
		RemoteHostName: host.Name,
		RemoteHostAddr: host.SSHHost,
		SourceName:     src.Name,
		TemplateID:     spikeKey,
	}
	if err := d.notifier.Notify(ctx, n); err != nil {
		return err
	}
	return nil
}
