package distill

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/store"
)

// TestNormalize_Masking pins each masking rule.
func TestNormalize_Masking(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ERROR pgx: connection refused host=db-3 attempt=7", "ERROR pgx: connection refused host=db-<n> attempt=<n>"},
		{"ERROR pgx: connection refused host=db-31 attempt=12", "ERROR pgx: connection refused host=db-<n> attempt=<n>"},
		{"request 550e8400-e29b-41d4-a716-446655440000 done", "request <uuid> done"},
		{"commit deadbeefcafe1234 pushed", "commit <hex> pushed"},
		{"dial 192.0.2.1:5432: refused", "dial <ip>: refused"},
		{"GET /healthz 200 in 12ms", "GET /healthz <n> in <dur>"},
		{"at 2026-07-08T14:02:11.123Z worker started", "at <ts> worker started"},
		{"\x1b[31mERROR\x1b[0m boom", "ERROR boom"},
		{`ERROR: duplicate key violates unique constraint "orders_external_reference_key" (SQLSTATE 23505)`, `ERROR: duplicate key violates unique constraint "orders_external_reference_key" (SQLSTATE 23505)`},
		{`db errno=1062 UniqueViolationError orderNum=SO-900001 ip=203.0.113.9`, `db errno=1062 UniqueViolationError orderNum=SO-<n> ip=<ip>`},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestNormalize_CorpusCompression is the M3 acceptance gate: a
// 10k-line synthetic corpus in realistic shapes must collapse to
// under 50 templates.
func TestNormalize_CorpusCompression(t *testing.T) {
	shapes := []func(i int) string{
		func(i int) string { return fmt.Sprintf("GET /api/v1/users/%d 200 in %dms", i, i%97) },
		func(i int) string { return fmt.Sprintf("POST /api/v1/sync 201 in %dms trace=%08x", i%53, i*2654435761) },
		func(i int) string {
			return fmt.Sprintf("2026-07-08T14:%02d:%02d.000Z INFO cache hit key=user:%d", i%60, i%60, i)
		},
		func(i int) string { return fmt.Sprintf("WARN slow query took %d.%dms rows=%d", i%40+10, i%10, i%1000) },
		func(i int) string {
			return fmt.Sprintf("ERROR pgx: connection refused host=db-%d attempt=%d", i%5, i%20)
		},
		func(i int) string { return fmt.Sprintf("session %s expired", uuidLike(i)) },
		func(i int) string { return fmt.Sprintf("dial 10.0.%d.%d:5432: i/o timeout", i%255, (i*7)%255) },
		func(i int) string { return "worker heartbeat ok" },
	}
	seen := map[string]struct{}{}
	for i := range 10_000 {
		seen[Normalize(shapes[i%len(shapes)](i))] = struct{}{}
	}
	if len(seen) >= 50 {
		t.Fatalf("10k lines produced %d templates, want <50", len(seen))
	}
	t.Logf("10k lines → %d templates", len(seen))
}

func uuidLike(i int) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", i, i%0xffff, i%0xffff, i%0xffff, i)
}

// TestClassifier_DefaultsAndOverrides covers the rule ladder.
func TestClassifier_DefaultsAndOverrides(t *testing.T) {
	c, err := NewClassifier("")
	if err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]string{
		"panic: runtime error":                  store.SeverityCritical,
		"OOM-killed container":                  store.SeverityCritical,
		"ERROR pgx: connection refused":         store.SeverityError,
		"request timed out after 30s":           store.SeverityError,
		"WARN slow query":                       store.SeverityWarn,
		"logwatch: pull truncated at 100 bytes": store.SeverityCritical,
		"GET /healthz 200":                      store.SeverityInfo,
		// explicit level beats keyword false-positives (the production
		// case: a filename literally named "Failed")
		`info acme/service.go:76 ignoring file as it is not an xml {"file": "Failed"}`: store.SeverityInfo,
		// app timestamp BEFORE the level (a real-world format: "<ts> info <pkg> <msg>")
		"2026-07-10T08:23:08.923Z\tinfo\tacme/service.go:110\tignoring file as it is not an xml\t{\"file\": \"Failed\"}": store.SeverityInfo,
		`{"level":"info","msg":"job failed to find any orders"}`:                                                         store.SeverityInfo,
		`error acme/service.go:80 connection refused`:                                                                    store.SeverityError,
		`{"level":"error","msg":"db down"}`:                                                                              store.SeverityError,
		// An explicit application level is authoritative even when the
		// handled message contains error/catastrophe vocabulary.
		`info worker handled panic: nil deref`: store.SeverityInfo,
		`time=2026-07-16T18:01:00Z level=INFO msg="order already handled" error="order already exists: ERROR: duplicate key value violates unique constraint orders_key"`: store.SeverityInfo,
		`{"level":"info","msg":"handled","error":"ERROR: duplicate key"}`:                                                                                                 store.SeverityInfo,
		`{"severity":"WARN","msg":"retry scheduled","error":"ERROR: downstream"}`:                                                                                         store.SeverityWarn,
		`{"log.level":"debug","msg":"wrapped exception"}`:                                                                                                                 store.SeverityInfo,
		`{"level":"info","severity":"error","msg":"handled"}`:                                                                                                             store.SeverityInfo,
		`INFO handled payload={"level":"error","message":"downstream failed"}`:                                                                                            store.SeverityInfo,
		// With no structured level, the conservative keyword fallback remains.
		`worker panic: nil deref`: store.SeverityCritical,
	} {
		if got := c.Classify(line); got != want {
			t.Errorf("Classify(%q) = %q, want %q", line, got, want)
		}
	}

	over, err := NewClassifier(`[{"pattern": "deprecation", "severity": "error"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got := over.Classify("deprecation notice"); got != store.SeverityError {
		t.Fatalf("override: got %q", got)
	}
	if _, err := NewClassifier(`[{"pattern": "(", "severity": "warn"}]`); err == nil {
		t.Fatal("invalid regexp must error")
	}
	if _, err := NewClassifier(`[{"pattern": "x", "severity": "high"}]`); err == nil {
		t.Fatal("invalid severity must error")
	}
}

// fakeDistillStore records upserts/inserts in memory.
type fakeDistillStore struct {
	templates   map[string]int64
	templateSev map[string]string
	lines       []store.LogLine
	spikeActive map[string]bool
}

func (f *fakeDistillStore) UpsertLogTemplate(_ context.Context, tpl *store.LogTemplate, n int64) (bool, error) {
	if f.templates == nil {
		f.templates = map[string]int64{}
		f.templateSev = map[string]string{}
	}
	_, existed := f.templates[tpl.ID]
	f.templates[tpl.ID] += n
	f.templateSev[tpl.ID] = tpl.Severity
	return !existed, nil
}
func (f *fakeDistillStore) InsertLogLines(_ context.Context, lines []store.LogLine) error {
	f.lines = append(f.lines, lines...)
	return nil
}
func (f *fakeDistillStore) PruneLogLines(context.Context, string, time.Time, int64) (int64, error) {
	return 0, nil
}

// CountErrorLinesInWindows mirrors the sqlite implementation's window
// semantics (current: ts >= currentSince; baseline: ts in
// [baselineSince, currentSince)) over the in-memory line log.
func (f *fakeDistillStore) CountErrorLinesInWindows(_ context.Context, sourceID string, baselineSince, currentSince time.Time) (int64, int64, error) {
	var current, baseline int64
	for _, l := range f.lines {
		if l.SourceID != sourceID ||
			store.SeverityRank(f.templateSev[l.TemplateID]) < store.SeverityRank(store.SeverityError) {
			continue
		}
		switch {
		case !l.TS.Before(currentSince):
			current++
		case !l.TS.Before(baselineSince):
			baseline++
		}
	}
	return current, baseline, nil
}

func (f *fakeDistillStore) GetLogSourceErrorSpikeActive(_ context.Context, sourceID string) (bool, error) {
	return f.spikeActive[sourceID], nil
}

func (f *fakeDistillStore) SetLogSourceErrorSpikeActive(_ context.Context, sourceID string, active bool) error {
	if f.spikeActive == nil {
		f.spikeActive = map[string]bool{}
	}
	f.spikeActive[sourceID] = active
	return nil
}

type captureNotifier struct {
	notes []Notification
	err   error
}

func (c *captureNotifier) Notify(_ context.Context, n Notification) error {
	c.notes = append(c.notes, n)
	return c.err
}

// TestDistiller_NoveltyWakesOnceOnErrorClass: a NEW error template
// fires exactly one anomaly; repeats and info-class novelty don't.
func TestDistiller_NoveltyWakesOnceOnErrorClass(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	d := NewDistiller(fs, notifier)

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "192.0.2.1"}
	ts := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)

	batch1 := []collect.Line{
		{TS: ts, Text: "ERROR pgx: connection refused host=db-3 attempt=7"},
		{TS: ts, Text: "ERROR pgx: connection refused host=db-4 attempt=9"}, // same template
		{TS: ts, Text: "INFO новый info shape appears"},
	}
	if err := d.Ingest(context.Background(), src, host, batch1); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("expected exactly 1 anomaly, got %d", len(notifier.notes))
	}
	n := notifier.notes[0]
	if n.Severity != store.SeverityError || n.RemoteHostName != "ip-prod-1" || n.TemplateID == "" {
		t.Fatalf("anomaly shape: %+v", n)
	}

	// Same error shape again — known template, no re-fire.
	if err := d.Ingest(context.Background(), src, host,
		[]collect.Line{{TS: ts.Add(time.Minute), Text: "ERROR pgx: connection refused host=db-9 attempt=1"}}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("known template must not re-fire, got %d", len(notifier.notes))
	}
	if len(fs.lines) != 4 {
		t.Fatalf("all lines persist to ring buffer: %d", len(fs.lines))
	}
}

// TestDigest_BudgetRespected renders within ±10% of budget and puts
// new error templates first.
func TestDigest_BudgetRespected(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	src := &store.LogSource{ID: "s1", Name: "api", WorkspaceID: "ws"}
	var tpls []*store.LogTemplate
	counts := map[string]int64{}
	for i := range 200 {
		id := fmt.Sprintf("tpl-%03d", i)
		sev := store.SeverityInfo
		first := now.Add(-2 * time.Hour) // old
		if i == 7 {
			sev, first = store.SeverityError, now.Add(-time.Minute) // the new error
		}
		tpls = append(tpls, &store.LogTemplate{
			ID: id, SourceID: "s1", Masked: fmt.Sprintf("shape %03d value <n>", i),
			Severity: sev, FirstSeen: first, LastSeen: now,
			SampleLast: fmt.Sprintf("shape %03d value 42", i),
		})
		counts[id] = int64(200 - i)
	}
	q := &Query{store: &fakeQueryStore{sources: []*store.LogSource{src}, tpls: tpls, counts: counts},
		now: func() time.Time { return now }}

	out, err := q.Digest(context.Background(), DigestOptions{
		WorkspaceID: "ws", Window: 15 * time.Minute, BudgetTokens: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	budgetChars := 500 * 4
	if len(out) > budgetChars+budgetChars/10 {
		t.Fatalf("digest %d chars exceeds budget %d (+10%%)", len(out), budgetChars)
	}
	firstEntry := strings.SplitN(out, "\n", 3)[1]
	if !strings.Contains(firstEntry, "NEW ✱ ERROR") || !strings.Contains(firstEntry, "shape 007") {
		t.Fatalf("new error template must render first, got: %s", firstEntry)
	}
	if !strings.Contains(out, "template_id: tpl-007") {
		t.Fatal("digest entries must expose the exact stable template id for task dedupe")
	}
	if !strings.Contains(out, "omitted by budget") {
		t.Fatal("budget overflow must be explicit, not silent")
	}
}

// TestDigest_OversizedEntryStillRenders: an entry wider than the whole
// budget must render as a minimal projection (truncated mask, template_id
// intact) instead of being skipped on every run and pending forever.
func TestDigest_OversizedEntryStillRenders(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	src := &store.LogSource{ID: "s1", Name: "api", WorkspaceID: "ws"}
	wide := strings.Repeat("x", 2000)
	tpl := &store.LogTemplate{
		ID: "tpl-big", SourceID: "s1", Masked: wide,
		Severity: store.SeverityError, FirstSeen: now.Add(-time.Minute), LastSeen: now,
		SampleLast: wide,
	}
	q := &Query{store: &fakeQueryStore{sources: []*store.LogSource{src}, tpls: []*store.LogTemplate{tpl}, counts: map[string]int64{"tpl-big": 3}},
		now: func() time.Time { return now }}

	out, err := q.Digest(context.Background(), DigestOptions{
		WorkspaceID: "ws", Window: 15 * time.Minute, BudgetTokens: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "template_id: tpl-big") {
		t.Fatalf("oversized entry must still render its template_id for acking, got: %s", out)
	}
	if strings.Contains(out, wide) {
		t.Fatal("oversized mask must be truncated, not rendered whole")
	}
	if budgetChars := 200 * 4; len(out) > budgetChars+budgetChars/10 {
		t.Fatalf("digest %d chars exceeds budget %d (+10%%)", len(out), budgetChars)
	}
}

type fakeQueryStore struct {
	sources []*store.LogSource
	tpls    []*store.LogTemplate
	counts  map[string]int64
}

func (f *fakeQueryStore) ListLogSources(context.Context, string) ([]*store.LogSource, error) {
	return f.sources, nil
}
func (f *fakeQueryStore) ListLogTemplates(context.Context, []string, time.Time, int) ([]*store.LogTemplate, error) {
	return f.tpls, nil
}
func (f *fakeQueryStore) ListPendingLogTemplates(context.Context, []string, int) ([]*store.LogTemplate, error) {
	out := make([]*store.LogTemplate, 0, len(f.tpls))
	for _, tpl := range f.tpls {
		if !tpl.Acked && tpl.TriagedAt == nil {
			out = append(out, tpl)
		}
	}
	return out, nil
}
func (f *fakeQueryStore) CountLinesByTemplate(context.Context, []string, time.Time) (map[string]int64, error) {
	return f.counts, nil
}
