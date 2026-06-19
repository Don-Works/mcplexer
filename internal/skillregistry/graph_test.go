package skillregistry

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// skillEntry is a test helper that builds a SkillRegistryEntry with the
// W4 ManifestExtra stashed into MetadataJSON the same way the publish
// path does (see extra.go::stashManifestExtra). Avoids reaching into
// unexported helpers from the test package.
func skillEntry(name string, produces, consumes []string) store.SkillRegistryEntry {
	extra := skills.ManifestExtra{Produces: produces, Consumes: consumes}
	encoded, _ := skills.MarshalExtra(extra)
	var typed any
	_ = json.Unmarshal(encoded, &typed)
	meta := map[string]any{ManifestExtraStashKey: typed}
	metaBytes, _ := json.Marshal(meta)
	return store.SkillRegistryEntry{
		Name:         name,
		Version:      1,
		Description:  "skill " + name,
		MetadataJSON: metaBytes,
	}
}

func TestBuildGraph_Empty(t *testing.T) {
	got := BuildGraph(nil, nil)
	if got.Nodes == nil {
		t.Error("Nodes must be non-nil empty slice for JSON [] serialisation")
	}
	if got.Edges == nil {
		t.Error("Edges must be non-nil empty slice for JSON [] serialisation")
	}
	if len(got.Nodes) != 0 || len(got.Edges) != 0 {
		t.Errorf("got %d nodes / %d edges, want 0/0", len(got.Nodes), len(got.Edges))
	}
}

func TestBuildGraph_LinearChain(t *testing.T) {
	// extract → draft → publish, single artifact type "markdown".
	entries := []store.SkillRegistryEntry{
		skillEntry("extract", []string{"markdown"}, nil),
		skillEntry("draft", []string{"markdown"}, []string{"markdown"}),
		skillEntry("publish", nil, []string{"markdown"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Nodes) != 3 {
		t.Fatalf("Nodes len = %d, want 3", len(got.Nodes))
	}
	// Nodes sorted by name: draft, extract, publish.
	wantNames := []string{"draft", "extract", "publish"}
	for i, n := range got.Nodes {
		if n.Name != wantNames[i] {
			t.Errorf("Nodes[%d].Name = %q, want %q", i, n.Name, wantNames[i])
		}
	}
	wantEdges := []SkillGraphEdge{
		{From: "draft", To: "draft", ArtifactType: "markdown"},
		{From: "draft", To: "publish", ArtifactType: "markdown"},
		{From: "extract", To: "draft", ArtifactType: "markdown"},
		{From: "extract", To: "publish", ArtifactType: "markdown"},
	}
	if !reflect.DeepEqual(got.Edges, wantEdges) {
		t.Errorf("Edges = %+v, want %+v", got.Edges, wantEdges)
	}
}

func TestBuildGraph_FanOut(t *testing.T) {
	// source → {a, b, c}.
	entries := []store.SkillRegistryEntry{
		skillEntry("source", []string{"data"}, nil),
		skillEntry("a", nil, []string{"data"}),
		skillEntry("b", nil, []string{"data"}),
		skillEntry("c", nil, []string{"data"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Edges) != 3 {
		t.Errorf("Edges len = %d, want 3 (fan-out)", len(got.Edges))
	}
	for _, e := range got.Edges {
		if e.From != "source" {
			t.Errorf("edge.From = %q, want all from \"source\"", e.From)
		}
	}
}

func TestBuildGraph_FanIn(t *testing.T) {
	// {x, y, z} → sink.
	entries := []store.SkillRegistryEntry{
		skillEntry("x", []string{"frag"}, nil),
		skillEntry("y", []string{"frag"}, nil),
		skillEntry("z", []string{"frag"}, nil),
		skillEntry("sink", nil, []string{"frag"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Edges) != 3 {
		t.Errorf("Edges len = %d, want 3 (fan-in)", len(got.Edges))
	}
	for _, e := range got.Edges {
		if e.To != "sink" {
			t.Errorf("edge.To = %q, want all to \"sink\"", e.To)
		}
	}
}

func TestBuildGraph_CyclesAllowed(t *testing.T) {
	// a → b → a (cycle, no detection / flagging — caller's problem).
	entries := []store.SkillRegistryEntry{
		skillEntry("a", []string{"foo"}, []string{"bar"}),
		skillEntry("b", []string{"bar"}, []string{"foo"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Edges) != 2 {
		t.Errorf("Edges len = %d, want 2 (cycle)", len(got.Edges))
	}
	// Both directions present.
	dirs := map[string]bool{}
	for _, e := range got.Edges {
		dirs[e.From+"→"+e.To] = true
	}
	if !dirs["a→b"] || !dirs["b→a"] {
		t.Errorf("cycle edges missing — got %v", dirs)
	}
}

func TestBuildGraph_IsolatedNode_Included(t *testing.T) {
	entries := []store.SkillRegistryEntry{
		skillEntry("alone", nil, nil),
		skillEntry("paired-a", []string{"x"}, nil),
		skillEntry("paired-b", nil, []string{"x"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Nodes) != 3 {
		t.Errorf("Nodes len = %d, want 3 (isolated node MUST appear)", len(got.Nodes))
	}
	if len(got.Edges) != 1 {
		t.Errorf("Edges len = %d, want 1 (only paired-a→paired-b)", len(got.Edges))
	}
}

func TestBuildGraph_MissingMatch_NoEdge(t *testing.T) {
	// foo produces "alpha" but nothing consumes "alpha" → 0 edges.
	entries := []store.SkillRegistryEntry{
		skillEntry("foo", []string{"alpha"}, nil),
		skillEntry("bar", nil, []string{"beta"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Edges) != 0 {
		t.Errorf("Edges len = %d, want 0 (no matching types)", len(got.Edges))
	}
}

func TestBuildGraph_MultipleArtifactTypes_OneEdgePerType(t *testing.T) {
	entries := []store.SkillRegistryEntry{
		skillEntry("a", []string{"x", "y"}, nil),
		skillEntry("b", nil, []string{"x", "y"}),
	}
	got := BuildGraph(entries, nil)
	if len(got.Edges) != 2 {
		t.Fatalf("Edges len = %d, want 2 (one per shared type)", len(got.Edges))
	}
	wantTypes := map[string]bool{"x": false, "y": false}
	for _, e := range got.Edges {
		if e.From != "a" || e.To != "b" {
			t.Errorf("edge = %+v, want a→b", e)
		}
		wantTypes[e.ArtifactType] = true
	}
	for k, v := range wantTypes {
		if !v {
			t.Errorf("missing edge for artifact type %q", k)
		}
	}
}

func TestBuildGraph_StatsLookupAttached(t *testing.T) {
	entries := []store.SkillRegistryEntry{
		skillEntry("a", []string{"x"}, nil),
		skillEntry("b", nil, []string{"x"}),
	}
	lookup := func(name string) *SkillStatsSummary {
		switch name {
		case "a":
			return &SkillStatsSummary{Invocations: 10, SuccessRate: 0.8}
		default:
			return nil
		}
	}
	got := BuildGraph(entries, lookup)
	var nodeA, nodeB SkillGraphNode
	for _, n := range got.Nodes {
		if n.Name == "a" {
			nodeA = n
		}
		if n.Name == "b" {
			nodeB = n
		}
	}
	if nodeA.StatsSummary == nil || nodeA.StatsSummary.Invocations != 10 {
		t.Errorf("nodeA.StatsSummary = %+v, want Invocations=10", nodeA.StatsSummary)
	}
	if nodeB.StatsSummary != nil {
		t.Errorf("nodeB.StatsSummary = %+v, want nil (lookup returned nil)", nodeB.StatsSummary)
	}
}

func TestBuildGraph_EntryWithoutManifestExtra_NoEdges(t *testing.T) {
	// A pre-W4 skill row (no MetadataJSON) should still appear as a node
	// with no Produces/Consumes — graceful degrade, no panic.
	entries := []store.SkillRegistryEntry{
		{Name: "legacy", Version: 1},
		skillEntry("modern", []string{"foo"}, nil),
	}
	got := BuildGraph(entries, nil)
	if len(got.Nodes) != 2 {
		t.Errorf("Nodes len = %d, want 2 including legacy", len(got.Nodes))
	}
	if len(got.Edges) != 0 {
		t.Errorf("Edges len = %d, want 0 (legacy has no consumes)", len(got.Edges))
	}
}
