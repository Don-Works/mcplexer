package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedDeployLine writes one template plus n occurrences of its line, ending at
// baselineFixtureNow.
func seedDeployLine(
	t *testing.T, db *sqlite.DB, ctx context.Context, src *store.LogSource,
	id, masked, raw, severity string, n int, last time.Time,
) {
	t.Helper()
	tpl := &store.LogTemplate{
		ID: id, SourceID: src.ID, Masked: masked, Severity: severity,
		FirstSeen: last.Add(-time.Hour), LastSeen: last,
	}
	if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
	lines := make([]store.LogLine, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, store.LogLine{
			SourceID: src.ID, TemplateID: id,
			TS: last.Add(-time.Duration(i) * time.Minute), Line: raw,
		})
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert %s lines: %v", id, err)
	}
}

// TestRecentDeploysReadsBannersFromRealRows proves deploy detection works off
// the logs the daemon already holds — no new table, no ingest hook — and that
// the guards against opening a window for the wrong line actually bind.
func TestRecentDeploysReadsBannersFromRealRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	now := baselineFixtureNow

	// The measured production banner: one line, info severity.
	seedDeployLine(t, db, ctx, src, "tpl-banner",
		"info api/main.go:159 running version: v<n>.<n>.<n>",
		"info api/main.go:159 running version: v5.7.7", store.SeverityInfo, 1, now)
	// An ordinary job line must never open a window.
	seedDeployLine(t, db, ctx, src, "tpl-work",
		"order sync completed batch=<n>", "order sync completed batch=418",
		store.SeverityInfo, 40, now)
	// An ERROR mentioning a version is a failure, not a release. Honouring it
	// would suppress anomaly alerts exactly when something is going wrong.
	seedDeployLine(t, db, ctx, src, "tpl-verr",
		"unsupported client version <n> rejected", "unsupported client version 3 rejected",
		store.SeverityError, 2, now)
	// A banner-shaped line that fires constantly is not a banner.
	seedDeployLine(t, db, ctx, src, "tpl-chatty",
		"worker started handling request <n>", "worker started handling request 9",
		store.SeverityInfo, 60, now)

	deploys, err := db.RecentDeploys(ctx, src.ID, now.Add(-store.BaselineLearnHorizon))
	if err != nil {
		t.Fatalf("recent deploys: %v", err)
	}
	if len(deploys) != 1 {
		t.Fatalf("found %d deploys; want exactly the one real banner", len(deploys))
	}
	if !deploys[0].Equal(now.UTC()) {
		t.Errorf("deploy at %s; want %s", deploys[0], now.UTC())
	}
}

// TestRecentDeploysIgnoresOldBanners checks the lookback bound: a release from
// outside the learning horizon is not evidence about the history being mined.
func TestRecentDeploysIgnoresOldBanners(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	now := baselineFixtureNow

	seedDeployLine(t, db, ctx, src, "tpl-banner",
		"info api/main.go:159 running version: v<n>.<n>.<n>",
		"info api/main.go:159 running version: v5.7.7",
		store.SeverityInfo, 1, now.Add(-30*24*time.Hour))

	deploys, err := db.RecentDeploys(ctx, src.ID, now.Add(-store.BaselineLearnHorizon))
	if err != nil {
		t.Fatalf("recent deploys: %v", err)
	}
	if len(deploys) != 0 {
		t.Fatalf("a deploy outside the learning horizon is still visible (%v)", deploys)
	}
}
