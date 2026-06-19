// Package skillregistry — graph.go (W6) constructs the skill composition
// DAG from W4's `produces:` / `consumes:` manifest extras.
//
// One edge per (producer, consumer, artifact_type). If skill A produces
// "markdown" and skill B consumes "markdown", you get exactly one
// "markdown"-typed edge A→B in the result. Multiple shared artifact
// types between the same pair yield one edge each — keeping the wire
// shape narrow lets the frontend colour-code by type without joining
// across rows.
//
// Cycles are permitted and not flagged. The "DAG" framing in the
// milestone spec is aspirational — real-world skill libraries develop
// cycles all the time (telegram-responder produces "telegram-reply"
// which itself consumes "telegram-reply" via threading). The dashboard
// renders them as a force-directed graph; downstream pipeline planners
// will dedupe / break cycles when they need to.
//
// Isolated nodes (no produces nor consumes, or no matches) ARE included
// in the graph — the dashboard surfaces them at the periphery so the
// human can see "this skill has no declared dependencies" without
// switching surfaces.
//
// The optional stats lookup lets the graph carry summarised reputation
// data per node without forcing every caller to compute it (an HTTP
// handler that wants graph+stats can pay the cost once; a CLI that only
// wants the topology pays nothing).
package skillregistry

import (
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// SkillGraph is the W6 composition view. JSON-shape stable; mirrored in
// web/src/api/skill-graph.ts.
type SkillGraph struct {
	Nodes []SkillGraphNode `json:"nodes"`
	Edges []SkillGraphEdge `json:"edges"`
}

// SkillGraphNode is one skill. StatsSummary is nil when the caller did
// not pass a stats lookup (or when the lookup returned no data) — the
// frontend renders the node anyway, just without the reputation chip.
type SkillGraphNode struct {
	Name         string             `json:"name"`
	Version      int                `json:"version"`
	Description  string             `json:"description,omitempty"`
	Produces     []string           `json:"produces,omitempty"`
	Consumes     []string           `json:"consumes,omitempty"`
	WorkspaceID  *string            `json:"workspace_id,omitempty"`
	StatsSummary *SkillStatsSummary `json:"stats_summary,omitempty"`
}

// SkillGraphEdge is one directed (producer → consumer, artifact_type)
// connection. ArtifactType is the exact string matched between the
// producer's produces[] and the consumer's consumes[].
type SkillGraphEdge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	ArtifactType string `json:"artifact_type"`
}

// SkillStatsSummary is the lightweight reputation chip carried on each
// node. Strict subset of SkillStats — invocations + success rate are
// the load-bearing numbers for "is this skill worth chaining into your
// pipeline?". Full stats live behind /api/v1/skills/{name}/stats.
type SkillStatsSummary struct {
	Invocations   int        `json:"invocations"`
	SuccessRate   float64    `json:"success_rate"`
	P95DurationMs int64      `json:"p95_duration_ms,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
}

// StatsLookup returns a SkillStatsSummary for the named skill, or nil
// when no stats exist. The graph builder calls this once per node; the
// HTTP handler wires it to AggregateSkillRuns over a 30d window.
type StatsLookup func(skillName string) *SkillStatsSummary

// BuildGraph constructs the W6 composition graph from a slice of skill
// registry head entries. statsLookup is optional (pass nil to skip the
// reputation chip).
//
// Determinism: nodes are sorted by Name asc; edges by (From, To,
// ArtifactType) asc. Tests can assert exact equality across runs.
func BuildGraph(entries []store.SkillRegistryEntry, statsLookup StatsLookup) SkillGraph {
	nodes := make([]SkillGraphNode, 0, len(entries))
	consumersByType := map[string][]string{} // artifact → list of skill names
	for i := range entries {
		e := entries[i]
		extra := ExtraFromEntry(&e)
		node := SkillGraphNode{
			Name:        e.Name,
			Version:     e.Version,
			Description: e.Description,
			Produces:    sortedCopy(extra.Produces),
			Consumes:    sortedCopy(extra.Consumes),
			WorkspaceID: e.WorkspaceID,
		}
		if statsLookup != nil {
			if summary := statsLookup(e.Name); summary != nil {
				node.StatsSummary = summary
			}
		}
		nodes = append(nodes, node)
		for _, c := range extra.Consumes {
			if c == "" {
				continue
			}
			consumersByType[c] = append(consumersByType[c], e.Name)
		}
	}

	// Build edges: for each producer skill × each artifact-type it
	// produces, look up consumers of that type and emit one edge per
	// (producer, consumer, type). Self-edges are allowed but flagged
	// — a skill that produces "markdown" and consumes "markdown" wires
	// to itself, which is how iterative-refinement skills are declared.
	var edges []SkillGraphEdge
	for i := range entries {
		e := entries[i]
		extra := ExtraFromEntry(&e)
		for _, prod := range extra.Produces {
			if prod == "" {
				continue
			}
			for _, consumerName := range consumersByType[prod] {
				edges = append(edges, SkillGraphEdge{
					From:         e.Name,
					To:           consumerName,
					ArtifactType: prod,
				})
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].ArtifactType < edges[j].ArtifactType
	})

	if nodes == nil {
		nodes = []SkillGraphNode{}
	}
	if edges == nil {
		edges = []SkillGraphEdge{}
	}
	return SkillGraph{Nodes: nodes, Edges: edges}
}

// sortedCopy returns a sorted copy of in (nil → nil). Used to give
// graph nodes deterministic Produces/Consumes ordering without mutating
// the caller's ManifestExtra.
func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// Compile-time sanity: skills.ManifestExtra is the source for
// Produces/Consumes — keep the import live even when sortedCopy is the
// only consumer of skills in this file.
var _ = skills.ManifestExtra{}
