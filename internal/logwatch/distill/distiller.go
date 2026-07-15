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
}

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

	aggs := map[string]*templateAgg{}
	var order []string // deterministic processing order
	rows := make([]store.LogLine, 0, len(lines))
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
