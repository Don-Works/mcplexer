// skill_graph_handler_test.go (W6) — end-to-end roundtrip from
// publishing W4 skills through the registry, fetching the composition
// graph, and asserting both topology + per-node stats summary.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newSkillGraphTestServer wires up both the SkillRegistry (for graph
// topology) and the SQLite store (so tests can RecordSkillRun directly
// to seed per-node stats summaries). Mirrors newSkillRegistryTestServer
// + newSkillStatsTestServer but returns the underlying *sqlite.DB so
// the test can call both surfaces in one helper.
func newSkillGraphTestServer(t *testing.T) (*httptest.Server, *skillregistry.Registry, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "skill-graph.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := skillregistry.New(db)
	r := NewRouter(RouterDeps{Store: db, SkillRegistry: reg})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, reg, db
}

const w6GraphSkillA = `---
name: extract-source
description: Use when extracting a structured payload from a source document.
produces:
  - "markdown"
---
# Extract
`

const w6GraphSkillB = `---
name: draft-deck
description: Use when drafting a deck from extracted markdown.
consumes:
  - "markdown"
produces:
  - "json:reveal-deck-config"
---
# Draft
`

const w6GraphSkillC = `---
name: publish-deck
description: Use when publishing a generated deck.
consumes:
  - "json:reveal-deck-config"
---
# Publish
`

func TestSkillGraphHandler_FullPipeline(t *testing.T) {
	srv, reg := newSkillRegistryTestServer(t)

	for _, body := range []string{w6GraphSkillA, w6GraphSkillB, w6GraphSkillC} {
		if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
			Body: body,
		}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	var resp struct {
		Graph      skillregistry.SkillGraph `json:"graph"`
		WindowDays int                      `json:"window_days"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/graph", &resp)
	if len(resp.Graph.Nodes) != 3 {
		t.Fatalf("Nodes len = %d, want 3", len(resp.Graph.Nodes))
	}
	if len(resp.Graph.Edges) != 2 {
		t.Errorf("Edges len = %d, want 2 (extract→draft, draft→publish)", len(resp.Graph.Edges))
	}
	wantEdges := map[string]bool{
		"extract-source→draft-deck@markdown":              false,
		"draft-deck→publish-deck@json:reveal-deck-config": false,
	}
	for _, e := range resp.Graph.Edges {
		key := e.From + "→" + e.To + "@" + e.ArtifactType
		if _, ok := wantEdges[key]; ok {
			wantEdges[key] = true
		} else {
			t.Errorf("unexpected edge: %s", key)
		}
	}
	for k, v := range wantEdges {
		if !v {
			t.Errorf("missing expected edge: %s", k)
		}
	}
	if resp.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30", resp.WindowDays)
	}
	// No skill runs recorded → all nodes have nil StatsSummary.
	for _, n := range resp.Graph.Nodes {
		if n.StatsSummary != nil {
			t.Errorf("node %s StatsSummary = %+v, want nil with no runs", n.Name, n.StatsSummary)
		}
	}
}

// TestSkillGraphHandler_EmptyRegistry verifies the empty case returns
// {nodes:[], edges:[]} — not null — so the dashboard renders an empty
// graph without nullguards.
func TestSkillGraphHandler_EmptyRegistry(t *testing.T) {
	srv, _ := newSkillRegistryTestServer(t)
	var resp struct {
		Graph skillregistry.SkillGraph `json:"graph"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/graph", &resp)
	if resp.Graph.Nodes == nil {
		t.Error("Nodes must be non-nil empty slice")
	}
	if resp.Graph.Edges == nil {
		t.Error("Edges must be non-nil empty slice")
	}
}

// TestSkillGraphHandler_InvalidWindowDays_400 verifies the window_days
// validation we share with the stats handler also gates this endpoint.
func TestSkillGraphHandler_InvalidWindowDays_400(t *testing.T) {
	srv, _ := newSkillRegistryTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/skills/graph?window_days=abc") //nolint:noctx,gosec
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestSkillGraphHandler_StatsSummaryRoundtrip verifies the per-node
// stats summary is computed from real W2 skill_runs rows + plumbed
// onto the matching graph node.
func TestSkillGraphHandler_StatsSummaryRoundtrip(t *testing.T) {
	srv, reg, db := newSkillGraphTestServer(t)
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Body: w6GraphSkillA}); err != nil {
		t.Fatalf("Publish A: %v", err)
	}
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{Body: w6GraphSkillB}); err != nil {
		t.Fatalf("Publish B: %v", err)
	}
	// Two successes + one failure for extract-source over the last 30d.
	now := time.Now()
	for _, age := range []time.Duration{1 * time.Hour, 2 * time.Hour} {
		started := now.Add(-age)
		completed := started.Add(150 * time.Millisecond)
		if err := db.RecordSkillRun(context.Background(), &store.SkillRun{
			SkillName:   "extract-source",
			WorkspaceID: "ws-test",
			StartedAt:   started,
			CompletedAt: &completed,
			Outcome:     store.SkillRunOutcomeSuccess,
		}); err != nil {
			t.Fatalf("RecordSkillRun: %v", err)
		}
	}
	failedStart := now.Add(-3 * time.Hour)
	failedEnd := failedStart.Add(50 * time.Millisecond)
	if err := db.RecordSkillRun(context.Background(), &store.SkillRun{
		SkillName:   "extract-source",
		WorkspaceID: "ws-test",
		StartedAt:   failedStart,
		CompletedAt: &failedEnd,
		Outcome:     store.SkillRunOutcomeFailure,
	}); err != nil {
		t.Fatalf("RecordSkillRun: %v", err)
	}

	var resp struct {
		Graph skillregistry.SkillGraph `json:"graph"`
	}
	fetchJSON(t, srv.URL+"/api/v1/skills/graph", &resp)
	var extractNode *skillregistry.SkillGraphNode
	for i := range resp.Graph.Nodes {
		if resp.Graph.Nodes[i].Name == "extract-source" {
			extractNode = &resp.Graph.Nodes[i]
		}
	}
	if extractNode == nil {
		t.Fatal("extract-source node missing from graph")
	}
	if extractNode.StatsSummary == nil {
		t.Fatal("extract-source.StatsSummary nil — stats lookup didn't fire")
	}
	if extractNode.StatsSummary.Invocations != 3 {
		t.Errorf("Invocations = %d, want 3", extractNode.StatsSummary.Invocations)
	}
	// 2 of 3 terminal succeeded.
	if got, want := extractNode.StatsSummary.SuccessRate, 2.0/3.0; absFloatDelta(got, want) > 0.0001 {
		t.Errorf("SuccessRate = %v, want %v", got, want)
	}
	if extractNode.StatsSummary.LastRunAt == nil {
		t.Error("LastRunAt nil — should be the most-recent recorded run")
	}
}
