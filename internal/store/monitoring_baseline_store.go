// monitoring_baseline_store.go — the persistence seam for baseline learning.
//
// Defined at the consumer boundary rather than folded into store.Store, for the
// same reason MonitoringExpectedSignalStore is: adding inference must not force
// every existing store mock across the tree to grow another six methods.
// *sqlite.DB satisfies it.
package store

import (
	"context"
	"time"
)

// MonitoringBaselineStore is everything the learner needs from storage.
type MonitoringBaselineStore interface {
	// ListEnabledLogSources is the learner's scheduling view.
	ListEnabledLogSources(ctx context.Context) ([]*LogSource, error)

	// MineBaselineCandidates measures every plausible recurring template on
	// one source over the learning horizon. It performs NO judgement — it
	// gathers arrival gaps, hour-bucket occupancy, long-horizon day gaps and
	// verified matcher statistics, and hands them to
	// EvaluateBaselineCandidate. Work is bounded per source; a source whose
	// history exceeds the scan budget reports ScanTruncated so the resulting
	// span is read as a floor rather than a fact.
	MineBaselineCandidates(
		ctx context.Context, src *LogSource, horizonStart, now time.Time,
	) ([]BaselineCandidate, error)

	// UpsertSignalBaseline records one candidate's evidence and verdict,
	// keyed on template. Rejections are stored with the same weight as
	// promotions: "why is there no rule for this job?" is the question an
	// operator actually asks.
	UpsertSignalBaseline(ctx context.Context, b *SignalBaseline) error

	// GetSignalBaselineByTemplate returns the stored baseline for one
	// template, or ErrNotFound.
	GetSignalBaselineByTemplate(ctx context.Context, templateID string) (*SignalBaseline, error)

	// ListSignalBaselines returns a workspace's baselines, promoted first
	// then by confidence — the operator's "what does the system think normal
	// looks like" view.
	ListSignalBaselines(ctx context.Context, workspaceID string, limit int) ([]*SignalBaseline, error)

	// ListSignalBaselinesForSource is the same view scoped to one source.
	ListSignalBaselinesForSource(ctx context.Context, sourceID string, limit int) ([]*SignalBaseline, error)
}

// BaselineLearnHorizon is how far back one learning pass looks. It exceeds the
// 7-day default raw-line retention on purpose: the query is bounded by the
// retained rows themselves, so asking for more than exists costs nothing and
// the horizon does not silently become the limit when an operator raises
// retention to learn slower jobs.
const BaselineLearnHorizon = 14 * 24 * time.Hour

// BaselineMaxScanLines bounds the per-source line scan for one pass. At roughly
// a microsecond per indexed row this is a fraction of a second per source, run
// hourly — invisible next to the collector's own SSH round-trips, and it can
// never grow with a source's volume because the scan takes the most RECENT
// rows and reports truncation.
const BaselineMaxScanLines = 300000

// BaselineMaxTemplatesPerSource bounds how many distinct templates one pass
// will track gap samples for. Ordered by arrival count, so the chattiest
// shapes — which is where scheduled jobs live — are always included.
const BaselineMaxTemplatesPerSource = 200

// BaselineMaxGapsPerTemplate bounds the per-template gap sample. Well above
// BaselineMinDeltas so the robust statistics still see a full picture, and low
// enough that 200 templates cannot blow up the heap.
const BaselineMaxGapsPerTemplate = 4000

// BaselineDayHistoryWindow is how far back the long-horizon day table is
// consulted for the weekly-shape veto. Four weeks gives four samples per
// weekday — the least that could distinguish "Saturdays are quiet" from "that
// Saturday was quiet", which is why anything short of it is a rejection rather
// than an inferred weekday mask.
const BaselineDayHistoryWindow = 28 * 24 * time.Hour
