// skill_stats_handler_test.go (W6) — single + batch endpoints over a
// real SQLite store seeded with W2 skill_runs rows. Mirrors the sister
// W2 handler-test setup (skill_registry_handler_test.go).
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newSkillStatsTestServer(t *testing.T) (*httptest.Server, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "skill-stats.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := NewRouter(RouterDeps{Store: db})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db
}

// seedSkillRun is a one-liner test helper for inserting a skill_runs row.
// outcome="" → "running"; durationMs=0 → CompletedAt nil.
func seedSkillRun(t *testing.T, db *sqlite.DB, name string, startedAgo time.Duration, durationMs int64, outcome string) {
	t.Helper()
	startedAt := time.Now().Add(-startedAgo)
	row := store.SkillRun{
		SkillName:   name,
		StartedAt:   startedAt,
		WorkspaceID: "test-workspace",
		Outcome:     outcome,
	}
	if durationMs > 0 {
		c := startedAt.Add(time.Duration(durationMs) * time.Millisecond)
		row.CompletedAt = &c
	}
	if err := db.RecordSkillRun(context.Background(), &row); err != nil {
		t.Fatalf("RecordSkillRun: %v", err)
	}
}

func TestSkillStatsHandler_Single_HappyPath(t *testing.T) {
	srv, db := newSkillStatsTestServer(t)
	seedSkillRun(t, db, "foo", 1*time.Hour, 200, store.SkillRunOutcomeSuccess)
	seedSkillRun(t, db, "foo", 2*time.Hour, 300, store.SkillRunOutcomeSuccess)
	seedSkillRun(t, db, "foo", 3*time.Hour, 400, store.SkillRunOutcomeFailure)
	// Run for a different skill — should not pollute this skill's stats.
	seedSkillRun(t, db, "other", 1*time.Hour, 100, store.SkillRunOutcomeSuccess)

	var resp struct {
		Skill string                   `json:"skill"`
		Stats skillregistry.SkillStats `json:"stats"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/foo/stats", &resp)
	if resp.Skill != "foo" {
		t.Errorf("skill = %q, want \"foo\"", resp.Skill)
	}
	if resp.Stats.Invocations != 3 {
		t.Errorf("Invocations = %d, want 3", resp.Stats.Invocations)
	}
	// 2 of 3 terminal runs succeeded.
	if got, want := resp.Stats.SuccessRate, 2.0/3.0; absFloatDelta(got, want) > 0.0001 {
		t.Errorf("SuccessRate = %v, want %v", got, want)
	}
	if resp.Stats.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30", resp.Stats.WindowDays)
	}
}

func TestSkillStatsHandler_Single_UnknownSkill_ReturnsZeroes(t *testing.T) {
	srv, _ := newSkillStatsTestServer(t)
	var resp struct {
		Skill string                   `json:"skill"`
		Stats skillregistry.SkillStats `json:"stats"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/never-run/stats", &resp)
	if resp.Stats.Invocations != 0 {
		t.Errorf("Invocations = %d, want 0 for never-run skill", resp.Stats.Invocations)
	}
}

func TestSkillStatsHandler_Single_CustomWindow(t *testing.T) {
	srv, db := newSkillStatsTestServer(t)
	seedSkillRun(t, db, "foo", 6*time.Hour, 100, store.SkillRunOutcomeSuccess)
	seedSkillRun(t, db, "foo", 5*24*time.Hour, 100, store.SkillRunOutcomeSuccess)
	var resp struct {
		Stats skillregistry.SkillStats `json:"stats"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/foo/stats?window_days=1", &resp)
	if resp.Stats.Invocations != 1 {
		t.Errorf("Invocations = %d, want 1 (1-day window)", resp.Stats.Invocations)
	}
	if resp.Stats.WindowDays != 1 {
		t.Errorf("WindowDays = %d, want 1", resp.Stats.WindowDays)
	}
}

func TestSkillStatsHandler_Single_InvalidWindow_400(t *testing.T) {
	srv, _ := newSkillStatsTestServer(t)
	cases := []string{"abc", "0", "-1", "9999"}
	for _, raw := range cases {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/v1/skills/foo/stats?window_days=" + raw) //nolint:noctx,gosec
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for window_days=%q", resp.StatusCode, raw)
			}
		})
	}
}

func TestSkillStatsHandler_Batch_HappyPath(t *testing.T) {
	srv, db := newSkillStatsTestServer(t)
	seedSkillRun(t, db, "foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess)
	seedSkillRun(t, db, "foo", 2*time.Hour, 100, store.SkillRunOutcomeFailure)
	seedSkillRun(t, db, "bar", 1*time.Hour, 100, store.SkillRunOutcomeSuccess)

	var resp struct {
		Stats      map[string]skillregistry.SkillStats `json:"stats"`
		WindowDays int                                 `json:"window_days"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/stats?names=foo,bar,unknown", &resp)
	if len(resp.Stats) != 3 {
		t.Errorf("returned %d entries, want 3", len(resp.Stats))
	}
	if resp.Stats["foo"].Invocations != 2 {
		t.Errorf("foo.Invocations = %d, want 2", resp.Stats["foo"].Invocations)
	}
	if resp.Stats["foo"].SuccessRate != 0.5 {
		t.Errorf("foo.SuccessRate = %v, want 0.5", resp.Stats["foo"].SuccessRate)
	}
	if resp.Stats["bar"].Invocations != 1 {
		t.Errorf("bar.Invocations = %d, want 1", resp.Stats["bar"].Invocations)
	}
	if resp.Stats["unknown"].Invocations != 0 {
		t.Errorf("unknown.Invocations = %d, want 0 (graceful zero)", resp.Stats["unknown"].Invocations)
	}
	if resp.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30", resp.WindowDays)
	}
}

func TestSkillStatsHandler_Batch_MissingNames_400(t *testing.T) {
	srv, _ := newSkillStatsTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/skills/stats") //nolint:noctx,gosec
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing names", resp.StatusCode)
	}
}

func TestSkillStatsHandler_Batch_Dedupes(t *testing.T) {
	srv, db := newSkillStatsTestServer(t)
	seedSkillRun(t, db, "foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess)

	var resp struct {
		Stats map[string]skillregistry.SkillStats `json:"stats"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/stats?names=foo,foo,foo,%20foo%20", &resp)
	if len(resp.Stats) != 1 {
		t.Errorf("returned %d entries, want 1 (deduped)", len(resp.Stats))
	}
}

// TestSkillStatsHandler_BatchPrecedence verifies the static /skills/stats
// route is preferred over the /skills/{name}/stats parameterised route
// (a name="stats" would otherwise route to getForSkill — risky if the
// route registration order regresses).
func TestSkillStatsHandler_BatchPrecedence(t *testing.T) {
	srv, _ := newSkillStatsTestServer(t)
	// Calling /skills/stats with no `names=` must hit getBatch (400),
	// not getForSkill (which would treat "stats" as the skill name and
	// return 200 with zeroed stats).
	resp, err := http.Get(srv.URL + "/api/v1/skills/stats") //nolint:noctx,gosec
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := json.Marshal(map[string]any{})
		t.Errorf("status = %d, want 400 — getBatch was not preferred; body=%s", resp.StatusCode, body)
	}
}

func absFloatDelta(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
