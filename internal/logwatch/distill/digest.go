// digest.go — the budget-bounded digest renderer + cheap stats: what
// the log-watch worker (and any interactive agent) actually reads.
// Priority order fills the token budget: new critical/error templates
// → new info templates → busiest count deltas → steady-state summary.
package distill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// QueryStore is the digest service's read slice of store.Store.
type QueryStore interface {
	ListLogSources(ctx context.Context, workspaceID string) ([]*store.LogSource, error)
	ListLogTemplates(ctx context.Context, sourceIDs []string, since time.Time, limit int) ([]*store.LogTemplate, error)
	ListPendingLogTemplates(ctx context.Context, sourceIDs []string, limit int) ([]*store.LogTemplate, error)
	CountLinesByTemplate(ctx context.Context, sourceIDs []string, since time.Time) (map[string]int64, error)
}

// Query serves monitoring.stats and monitoring.digest.
type Query struct {
	store QueryStore
	now   func() time.Time
}

func NewQuery(st QueryStore) *Query { return &Query{store: st, now: time.Now} }

// Stats are the zero-spend gate's counters: cheap, no rendering.
type Stats struct {
	Window           string `json:"window"`
	Lines            int64  `json:"lines"`
	Templates        int    `json:"templates"`
	NewTemplates     int    `json:"new_templates"`     // unacked, first seen in window
	PendingTemplates int    `json:"pending_templates"` // durable queue; survives rolling-window/budget omission
	ErrorDelta       int64  `json:"error_delta"`       // error+critical lines in window
	EvidenceGap      bool   `json:"evidence_gap"`      // one or more truncated pulls
}

// resolveSources maps optional explicit ids onto the workspace's
// sources, returning (ids, byID).
func (q *Query) resolveSources(ctx context.Context, workspaceID string, sourceIDs []string) ([]string, map[string]*store.LogSource, error) {
	all, err := q.store.ListLogSources(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*store.LogSource{}
	for _, s := range all {
		byID[s.ID] = s
	}
	if len(sourceIDs) == 0 {
		ids := make([]string, 0, len(all))
		for _, s := range all {
			ids = append(ids, s.ID)
		}
		return ids, byID, nil
	}
	for _, id := range sourceIDs {
		if _, ok := byID[id]; !ok {
			return nil, nil, fmt.Errorf("distill: source %s not in workspace", id)
		}
	}
	return sourceIDs, byID, nil
}

// Stats computes the window counters.
func (q *Query) Stats(ctx context.Context, workspaceID string, sourceIDs []string, window time.Duration) (*Stats, error) {
	ids, _, err := q.resolveSources(ctx, workspaceID, sourceIDs)
	if err != nil {
		return nil, err
	}
	since := q.now().UTC().Add(-window)
	tpls, err := q.store.ListLogTemplates(ctx, ids, since, 0)
	if err != nil {
		return nil, err
	}
	counts, err := q.store.CountLinesByTemplate(ctx, ids, since)
	if err != nil {
		return nil, err
	}
	pending, err := q.store.ListPendingLogTemplates(ctx, ids, 0)
	if err != nil {
		return nil, err
	}
	st := &Stats{Window: window.String(), Templates: len(tpls), PendingTemplates: len(pending)}
	for _, t := range tpls {
		n := counts[t.ID]
		st.Lines += n
		if !t.Acked && !t.FirstSeen.Before(since) {
			st.NewTemplates++
		}
		if store.SeverityRank(t.Severity) >= store.SeverityRank(store.SeverityError) {
			st.ErrorDelta += n
		}
		if isEvidenceGap(t) && n > 0 {
			st.EvidenceGap = true
		}
	}
	return st, nil
}

// DigestOptions parameterize one render.
type DigestOptions struct {
	WorkspaceID  string
	SourceIDs    []string // empty = all in workspace
	Window       time.Duration
	BudgetTokens int    // ~4 chars/token; default 2000
	MinSeverity  string // drop templates below this floor; "" = info
	MaxSamples   int    // samples per entry; default/max 3
	PendingOnly  bool   // durable worker queue; false keeps interactive window view
}

// Digest renders the budget-bounded window summary.
func (q *Query) Digest(ctx context.Context, opts DigestOptions) (string, error) {
	if opts.BudgetTokens <= 0 {
		opts.BudgetTokens = 2000
	}
	if opts.Window <= 0 {
		opts.Window = 15 * time.Minute
	}
	maxSamples := opts.MaxSamples
	if maxSamples <= 0 || maxSamples > 3 {
		maxSamples = 3
	}
	minRank := store.SeverityRank(store.SeverityInfo)
	if opts.MinSeverity != "" {
		if minRank = store.SeverityRank(opts.MinSeverity); minRank < 0 {
			return "", fmt.Errorf("distill: invalid min_severity %q", opts.MinSeverity)
		}
	}
	ids, byID, err := q.resolveSources(ctx, opts.WorkspaceID, opts.SourceIDs)
	if err != nil {
		return "", err
	}
	since := q.now().UTC().Add(-opts.Window)
	var tpls []*store.LogTemplate
	if opts.PendingOnly {
		tpls, err = q.store.ListPendingLogTemplates(ctx, ids, 0)
	} else {
		tpls, err = q.store.ListLogTemplates(ctx, ids, since, 0)
	}
	if err != nil {
		return "", err
	}
	counts, err := q.store.CountLinesByTemplate(ctx, ids, since)
	if err != nil {
		return "", err
	}

	var totalLines, evidenceGapLines int64
	newCount := 0
	entries := tpls[:0]
	for _, t := range tpls {
		totalLines += counts[t.ID]
		if isEvidenceGap(t) {
			evidenceGapLines += counts[t.ID]
		}
		if !t.Acked && !t.FirstSeen.Before(since) {
			newCount++
		}
		if store.SeverityRank(t.Severity) >= minRank {
			entries = append(entries, t)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		pi, pj := digestPriority(entries[i], since), digestPriority(entries[j], since)
		if pi != pj {
			return pi > pj
		}
		return counts[entries[i].ID] > counts[entries[j].ID]
	})

	var b strings.Builder
	if evidenceGapLines > 0 {
		fmt.Fprintf(&b, "EVIDENCE GAP: UNTRUSTWORTHY WINDOW — %d truncated pull event(s); missing lines mean silence is not evidence of health. Triage collection health first; cause unverified.\n", evidenceGapLines)
	}
	if opts.PendingOnly {
		fmt.Fprintf(&b, "pending triage queue (%s count window): %d lines → %d templates (%d new)\n",
			opts.Window, totalLines, len(tpls), newCount)
	} else {
		fmt.Fprintf(&b, "window %s: %d lines → %d templates (%d new)\n",
			opts.Window, totalLines, len(tpls), newCount)
	}
	budget := opts.BudgetTokens * 4
	skipped := 0
	rendered := false
	for _, t := range entries {
		// Avoid the historical N×(history + up-to-5000-line evidence) query
		// fan-out for entries that cannot possibly fit. The cheap projection
		// uses already-loaded samples/correlation; only candidates with room
		// earn the more expensive evidence reads.
		cheapEvidence := templateEvidence{
			correlationKey: correlationKey(byID[t.SourceID], t.SampleLast),
			samples:        nonEmptySamples(t.SampleLast, t.SampleFirst),
		}
		if len(cheapEvidence.samples) > maxSamples {
			cheapEvidence.samples = cheapEvidence.samples[:maxSamples]
		}
		cheapEntry := renderEntry(t, counts[t.ID], byID[t.SourceID], since, cheapEvidence)
		if b.Len()+len(cheapEntry) > budget {
			// The highest-priority entry must always render: an entry
			// wider than the whole budget would otherwise be skipped on
			// every run, staying durably pending forever — and a
			// pending_only worker whose postcondition requires zero
			// pending templates could never converge. Fall back to a
			// minimal projection (truncated mask, no evidence) that
			// still carries template_id for acking.
			if !rendered {
				small := *t
				small.Masked = truncate(t.Masked, 160)
				minEntry := renderEntry(&small, counts[t.ID], byID[t.SourceID], since, templateEvidence{})
				if b.Len()+len(minEntry) <= budget {
					b.WriteString(minEntry)
					rendered = true
					continue
				}
			}
			skipped++
			continue
		}
		rendered = true
		evidence := q.templateEvidence(ctx, t, byID[t.SourceID])
		if len(evidence.samples) > maxSamples {
			evidence.samples = evidence.samples[:maxSamples]
		}
		entry := renderEntry(t, counts[t.ID], byID[t.SourceID], since, evidence)
		if b.Len()+len(entry) > budget {
			// Rich history/cardinality can make an otherwise salient template
			// too large. Keep the cheap complete projection we already proved
			// fits; this both preserves the item and fills the budget so later
			// entries are rejected without further evidence queries.
			b.WriteString(cheapEntry)
			continue
		}
		b.WriteString(entry)
	}
	if skipped > 0 {
		if opts.PendingOnly {
			fmt.Fprintf(&b, "… %d pending templates omitted by budget — they remain queued for the next gated run\n", skipped)
		} else {
			fmt.Fprintf(&b, "… %d more templates omitted by budget — raise budget_tokens or filter with min_severity/source_ids\n", skipped)
		}
	}
	return b.String(), nil
}

func isEvidenceGap(t *store.LogTemplate) bool {
	return strings.HasPrefix(strings.ToLower(t.Masked), "logwatch: pull truncated")
}

// digestPriority: new error/critical (3) → new anything (2) →
// error-class (1) → rest (0).
func digestPriority(t *store.LogTemplate, since time.Time) int {
	isNew := !t.Acked && !t.FirstSeen.Before(since)
	isErr := store.SeverityRank(t.Severity) >= store.SeverityRank(store.SeverityError)
	switch {
	case isNew && isErr:
		return 3
	case isNew:
		return 2
	case isErr:
		return 1
	default:
		return 0
	}
}

func renderEntry(
	t *store.LogTemplate, windowCount int64, src *store.LogSource,
	since time.Time, evidence templateEvidence,
) string {
	marks := strings.ToUpper(t.Severity)
	if !t.Acked && !t.FirstSeen.Before(since) {
		marks = "NEW ✱ " + marks
	}
	srcName := t.SourceID
	if src != nil {
		srcName = src.Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] ×%d %s %q %s→%s\n    template_id: %s\n    history: %s\n",
		srcName, windowCount, marks, t.Masked,
		t.FirstSeen.UTC().Format("15:04:05"), t.LastSeen.UTC().Format("15:04:05"),
		t.ID,
		renderHistory(t, evidence))
	if evidence.correlationKey != "" {
		fmt.Fprintf(&b, "    correlation_key: %s\n", evidence.correlationKey)
	}
	if evidence.cardinality != "" {
		fmt.Fprintf(&b, "    masked_value_cardinality (sampled %d retained lines): %s\n",
			evidence.cardinalityRows, evidence.cardinality)
	}
	for i, sample := range evidence.samples {
		fmt.Fprintf(&b, "    sample[%d]: %s\n", i+1, truncate(sample, 240))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
