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
	Window       string `json:"window"`
	Lines        int64  `json:"lines"`
	Templates    int    `json:"templates"`
	NewTemplates int    `json:"new_templates"` // unacked, first seen in window
	ErrorDelta   int64  `json:"error_delta"`   // error+critical lines in window
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
	st := &Stats{Window: window.String(), Templates: len(tpls)}
	for _, t := range tpls {
		n := counts[t.ID]
		st.Lines += n
		if !t.Acked && !t.FirstSeen.Before(since) {
			st.NewTemplates++
		}
		if store.SeverityRank(t.Severity) >= store.SeverityRank(store.SeverityError) {
			st.ErrorDelta += n
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
}

// Digest renders the budget-bounded window summary.
func (q *Query) Digest(ctx context.Context, opts DigestOptions) (string, error) {
	if opts.BudgetTokens <= 0 {
		opts.BudgetTokens = 2000
	}
	if opts.Window <= 0 {
		opts.Window = 15 * time.Minute
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
	tpls, err := q.store.ListLogTemplates(ctx, ids, since, 0)
	if err != nil {
		return "", err
	}
	counts, err := q.store.CountLinesByTemplate(ctx, ids, since)
	if err != nil {
		return "", err
	}

	var totalLines int64
	newCount := 0
	entries := tpls[:0]
	for _, t := range tpls {
		totalLines += counts[t.ID]
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
	fmt.Fprintf(&b, "window %s: %d lines → %d templates (%d new)\n",
		opts.Window, totalLines, len(tpls), newCount)
	budget := opts.BudgetTokens * 4
	skipped := 0
	for _, t := range entries {
		entry := renderEntry(t, counts[t.ID], byID[t.SourceID], since)
		if b.Len()+len(entry) > budget {
			skipped++
			continue
		}
		b.WriteString(entry)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "… %d more templates omitted by budget — raise budget_tokens or filter with min_severity/source_ids\n", skipped)
	}
	return b.String(), nil
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

func renderEntry(t *store.LogTemplate, windowCount int64, src *store.LogSource, since time.Time) string {
	marks := strings.ToUpper(t.Severity)
	if !t.Acked && !t.FirstSeen.Before(since) {
		marks = "NEW ✱ " + marks
	}
	srcName := t.SourceID
	if src != nil {
		srcName = src.Name
	}
	return fmt.Sprintf("[%s] ×%d %s %q %s→%s\n    sample: %s\n",
		srcName, windowCount, marks, t.Masked,
		t.FirstSeen.UTC().Format("15:04:05"), t.LastSeen.UTC().Format("15:04:05"),
		truncate(t.SampleLast, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
