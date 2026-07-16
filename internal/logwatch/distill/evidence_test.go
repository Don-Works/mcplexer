package distill

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type evidenceQueryStore struct {
	*fakeQueryStore
	history map[string]*store.LogTemplateHistory
	lines   map[string][]*store.LogLine
}

func (f *evidenceQueryStore) GetLogTemplateHistory(_ context.Context, id string) (*store.LogTemplateHistory, error) {
	return f.history[id], nil
}

func (f *evidenceQueryStore) ListLogLinesForTemplateEvidence(_ context.Context, id string, limit int) ([]*store.LogLine, error) {
	lines := f.lines[id]
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

func TestStats_CountersAndEvidenceGap(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	q := &Query{now: func() time.Time { return now }, store: &fakeQueryStore{
		sources: []*store.LogSource{{ID: "s1", WorkspaceID: "ws"}},
		tpls: []*store.LogTemplate{
			{ID: "a", Severity: store.SeverityInfo, FirstSeen: now.Add(-time.Hour), LastSeen: now},
			{ID: "b", Severity: store.SeverityError, FirstSeen: now.Add(-time.Minute), LastSeen: now},
			{ID: "c", Severity: store.SeverityCritical, FirstSeen: now.Add(-2 * time.Minute), LastSeen: now, Acked: true},
			{ID: "gap", Masked: "logwatch: pull truncated — window incomplete", Severity: store.SeverityCritical, FirstSeen: now.Add(-time.Minute), LastSeen: now, Acked: true},
		},
		counts: map[string]int64{"a": 100, "b": 5, "c": 2, "gap": 1},
	}}
	st, err := q.Stats(context.Background(), "ws", nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines != 108 || st.NewTemplates != 1 || st.ErrorDelta != 8 || !st.EvidenceGap {
		t.Fatalf("stats: %+v", st)
	}
}

func TestCorrelationKey_PortExposureUsesHost(t *testing.T) {
	src := &store.LogSource{Name: "api", RemoteHostID: "host-1"}
	got := correlationKey(src, "logwatch: published port exposure observed — container=admin-ui")
	if got != "host:host-1|docker-port-exposure" {
		t.Fatalf("port correlation key: %q", got)
	}
}

func TestDigest_RendersLifetimeCardinalityCorrelationAndSamples(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	tpl := &store.LogTemplate{
		ID: "tpl-orders", SourceID: "s1",
		Masked:   `level=error app/orders.go:91 orderNum=SO-<n> request_id=<uuid>`,
		Severity: store.SeverityError, Count: 866,
		FirstSeen: now.Add(-580 * 24 * time.Hour), LastSeen: now,
		SampleFirst: `level=error app/orders.go:91 orderNum=SO-100001 request_id=550e8400-e29b-41d4-a716-446655440000`,
		SampleLast:  `level=error app/orders.go:91 orderNum=SO-100002 request_id=ea3ef4c0-ae0c-4d2b-b2d8-a184b30c12ca`,
	}
	lines := []*store.LogLine{
		{TemplateID: tpl.ID, Line: tpl.SampleFirst},
		{TemplateID: tpl.ID, Line: tpl.SampleLast},
	}
	q := &Query{now: func() time.Time { return now }, store: &evidenceQueryStore{
		fakeQueryStore: &fakeQueryStore{
			sources: []*store.LogSource{{ID: "s1", Name: "orders", WorkspaceID: "ws"}},
			tpls:    []*store.LogTemplate{tpl}, counts: map[string]int64{tpl.ID: 2},
		},
		history: map[string]*store.LogTemplateHistory{tpl.ID: {
			RetainedCount: 14, RetainedDistinctDays: 7,
			ObservedDistinctDays: 580,
			ObservedFirstDay:     now.Add(-580 * 24 * time.Hour), ObservedLastDay: now,
			AverageObservedDayGap: 24 * time.Hour,
		}},
		lines: map[string][]*store.LogLine{tpl.ID: lines},
	}}

	out, err := q.Digest(context.Background(), DigestOptions{
		WorkspaceID: "ws", Window: 15 * time.Minute, BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"lifetime_count=866", "observed_distinct_days=580",
		"observed_day_cadence=24h0m0s", "retained_distinct_days=7",
		"correlation_key: orders|app/orders.go:91",
		"ordernum=2 distinct [SO-100001, SO-100002]", "request_id=2 distinct", "sample[2]:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("digest missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "request_id=2 distinct [") {
		t.Fatalf("UUID-like identifiers must not be inlined in cardinality evidence:\n%s", out)
	}
}
