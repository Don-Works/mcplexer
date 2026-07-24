package distill

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type evidenceQueryStore struct {
	*fakeQueryStore
	history       map[string]*store.LogTemplateHistory
	lines         map[string][]*store.LogLine
	evidenceReads int
}

func (f *evidenceQueryStore) GetLogTemplateHistory(_ context.Context, id string) (*store.LogTemplateHistory, error) {
	f.evidenceReads++
	return f.history[id], nil
}

func (f *evidenceQueryStore) ListLogLinesForTemplateEvidence(_ context.Context, id string, limit int) ([]*store.LogLine, error) {
	f.evidenceReads++
	lines := f.lines[id]
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

func TestDigestBudgetBoundsEvidenceReads(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	templates := make([]*store.LogTemplate, 200)
	counts := make(map[string]int64, len(templates))
	lines := make(map[string][]*store.LogLine, len(templates))
	for idx := range templates {
		id := fmt.Sprintf("tpl-%03d", idx)
		templates[idx] = &store.LogTemplate{
			ID: id, SourceID: "s1", Masked: fmt.Sprintf("error shape %03d value=<n>", idx),
			Severity: store.SeverityError, FirstSeen: now.Add(-time.Minute), LastSeen: now,
			SampleLast: strings.Repeat("sample ", 25),
		}
		counts[id] = 1
		lines[id] = []*store.LogLine{{TemplateID: id, Line: strings.Repeat("sample ", 25)}}
	}
	st := &evidenceQueryStore{
		fakeQueryStore: &fakeQueryStore{
			sources: []*store.LogSource{{ID: "s1", Name: "api", WorkspaceID: "ws"}},
			tpls:    templates, counts: counts,
		},
		lines: lines,
	}
	q := &Query{now: func() time.Time { return now }, store: st}
	if _, err := q.Digest(context.Background(), DigestOptions{
		WorkspaceID: "ws", Window: 15 * time.Minute, BudgetTokens: 200,
		MaxSamples: 1, PendingOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if st.evidenceReads > 6 {
		t.Fatalf("200-template digest performed %d evidence reads; budget must bound N+1 work", st.evidenceReads)
	}
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

func TestCorrelationKey_DeduplicatesOperationalFamilies(t *testing.T) {
	src := &store.LogSource{Name: "webhooks"}
	tests := []struct {
		name   string
		sample string
		want   string
	}{
		{
			name:   "scanner php probe",
			sample: `203.0.113.8 - - [22/Jul/2026] "GET /vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php HTTP/1.1" 200 476`,
			want:   "webhooks|scanner-probe",
		},
		{
			name:   "scanner env probe",
			sample: `203.0.113.8 - - [22/Jul/2026] "HEAD /.env HTTP/1.1" 200 476`,
			want:   "webhooks|scanner-probe",
		},
		{
			name:   "purchase order rejection from either call site",
			sample: "WARN purchase_order/service.go:93 PO Number Request failed with reason Invalid Employee number",
			want:   "webhooks|purchase-order-number-rejected",
		},
		{
			name:   "postgres protocol variant",
			sample: "ERROR SQLSTATE 08P01 protocol_violation prepared_statement_lost",
			want:   "webhooks|postgres-protocol",
		},
		{
			name:   "workout compiler variant",
			sample: "ERROR create_workout compilation failed: section_steps_missing",
			want:   "webhooks|create-workout-compilation",
		},
		{
			name:   "monitor source discontinuity",
			sample: "logwatch: source discontinuity — container/service restarted, recreated, or logs rotated",
			want:   "webhooks|source-discontinuity",
		},
		{
			name:   "nginx permission denied on .inc",
			sample: `2026/07/24 08:00:00 [emerg] open() "/etc/nginx/storefront-state/storefront-proxy-pass.inc" failed (13: Permission denied)`,
			want:   "webhooks|nginx-config-permission",
		},
		{
			name:   "nginx permission denied on .conf sibling",
			sample: `2026/07/24 08:00:01 [emerg] open() "/etc/nginx/conf.d/storefront.conf" failed (13: Permission denied)`,
			want:   "webhooks|nginx-config-permission",
		},
		{
			name:   "nginx upstream reset",
			sample: `2026/07/24 08:56:21 [error] 12#12: *99 recv() failed (104: Connection reset by peer) while reading response header from upstream, client: 203.0.113.9, server: _, request: "GET /wp-json/emb/v1/product/1 HTTP/1.1", upstream: "fastcgi://203.0.113.10:9000"`,
			want:   "webhooks|nginx-upstream-failure",
		},
		{
			name:   "store api product slug",
			sample: `Error [StoreApiError]: Store API 502 for /products?slug=anthem-organic-heavyweight-t-shirt`,
			want:   "webhooks|store-api|/products",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := correlationKey(src, tt.sample); got != tt.want {
				t.Fatalf("correlationKey = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestAnomalyClass_NginxPermissionSiblingsShareClass(t *testing.T) {
	src := &store.LogSource{Name: "stack"}
	a := &store.LogTemplate{
		ID:         "tpl-inc",
		SampleLast: `open() "/etc/nginx/storefront-state/storefront-proxy-pass.inc" failed (13: Permission denied)`,
	}
	b := &store.LogTemplate{
		ID:         "tpl-conf",
		SampleLast: `open() "/etc/nginx/conf.d/storefront.conf" failed (13: Permission denied)`,
	}
	gotA, gotB := anomalyClassForTemplate(src, a), anomalyClassForTemplate(src, b)
	if gotA != gotB {
		t.Fatalf("siblings must share class: %q vs %q", gotA, gotB)
	}
	if !strings.HasPrefix(gotA, "correlation:") {
		t.Fatalf("want correlation class, got %q", gotA)
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

	compact, err := q.Digest(context.Background(), DigestOptions{
		WorkspaceID: "ws", Window: 15 * time.Minute,
		BudgetTokens: 200, MaxSamples: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compact, "sample[1]:") || strings.Contains(compact, "sample[2]:") {
		t.Fatalf("compact digest must include exactly one sample:\n%s", compact)
	}
	if len(compact) >= 1024 {
		t.Fatalf("compact digest must stay below code-mode text projection threshold: %d bytes", len(compact))
	}
}
