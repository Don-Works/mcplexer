// memory_graph_handler_test.go — exercises the GET /api/v1/memory/graph
// surface end-to-end, including the buildGraph node/edge construction.
package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestMemoryGraphHandlerCoTagEdges seeds three memories with overlapping
// tags and asserts the graph response includes the expected co-tag edges
// (and that disjoint memories don't link).
func TestMemoryGraphHandlerCoTagEdges(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)

	// A & B share "ops"; A & C share "learning"; B & C share nothing.
	idA := seedMemory(t, svc, "alpha", "first graph memory", store.MemoryKindFact, []string{"ops", "learning"})
	idB := seedMemory(t, svc, "beta", "second graph memory", store.MemoryKindNote, []string{"ops"})
	idC := seedMemory(t, svc, "gamma", "third graph memory", store.MemoryKindNote, []string{"learning"})

	resp, err := http.Get(srv.URL + "/api/v1/memory/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	var out struct {
		Nodes []struct {
			ID    string   `json:"id"`
			Title string   `json:"title"`
			Kind  string   `json:"kind"`
			Tags  []string `json:"tags"`
			Size  int      `json:"size"`
		} `json:"nodes"`
		Edges []struct {
			Source string  `json:"source"`
			Target string  `json:"target"`
			Weight float64 `json:"weight"`
			Reason string  `json:"reason"`
		} `json:"edges"`
		Truncated bool `json:"truncated"`
		NodeCap   int  `json:"node_cap"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Nodes) != 3 {
		t.Fatalf("nodes=%d want 3", len(out.Nodes))
	}
	if out.NodeCap != maxGraphNodes {
		t.Errorf("node_cap=%d want %d", out.NodeCap, maxGraphNodes)
	}
	if out.Truncated {
		t.Errorf("unexpected truncated=true for tiny input")
	}

	// Expect exactly two co-tag edges: A-B and A-C. B-C share nothing.
	if len(out.Edges) != 2 {
		t.Fatalf("edges=%d want 2: %+v", len(out.Edges), out.Edges)
	}
	pairKey := func(a, b string) string {
		if a < b {
			return a + "|" + b
		}
		return b + "|" + a
	}
	got := map[string]bool{}
	for _, e := range out.Edges {
		got[pairKey(e.Source, e.Target)] = true
		if e.Reason != "co_tag" {
			t.Errorf("edge %s-%s reason=%q want co_tag", e.Source, e.Target, e.Reason)
		}
		if e.Weight <= 0 || e.Weight > 1 {
			t.Errorf("edge weight out of range: %f", e.Weight)
		}
	}
	if !got[pairKey(idA, idB)] {
		t.Errorf("missing A-B edge")
	}
	if !got[pairKey(idA, idC)] {
		t.Errorf("missing A-C edge")
	}
	if got[pairKey(idB, idC)] {
		t.Errorf("unexpected B-C edge (no shared tags)")
	}

	// Degree (node.size) should be 2 for A (centre), 1 for B and C.
	for _, n := range out.Nodes {
		switch n.ID {
		case idA:
			if n.Size != 2 {
				t.Errorf("A degree=%d want 2", n.Size)
			}
		case idB, idC:
			if n.Size != 1 {
				t.Errorf("leaf degree=%d want 1", n.Size)
			}
		}
	}
}

// TestMemoryGraphHandlerWikilinkEdges asserts [[Name]] tokens in content
// produce explicit wikilink edges that override co-tag reason.
func TestMemoryGraphHandlerWikilinkEdges(t *testing.T) {
	srv, _, svc := newMemoryTestServer(t)

	idA := seedMemory(t, svc, "alpha", "see [[beta]] for context", store.MemoryKindNote, nil)
	idB := seedMemory(t, svc, "beta", "the target", store.MemoryKindNote, nil)

	resp, err := http.Get(srv.URL + "/api/v1/memory/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Edges []struct {
			Source string  `json:"source"`
			Target string  `json:"target"`
			Weight float64 `json:"weight"`
			Reason string  `json:"reason"`
		} `json:"edges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Edges) != 1 {
		t.Fatalf("edges=%d want 1: %+v", len(out.Edges), out.Edges)
	}
	e := out.Edges[0]
	if e.Reason != "wikilink" {
		t.Errorf("reason=%q want wikilink", e.Reason)
	}
	if e.Weight != 1.0 {
		t.Errorf("weight=%f want 1.0", e.Weight)
	}
	// Either direction acceptable (undirected canonicalisation).
	if (e.Source != idA || e.Target != idB) && (e.Source != idB || e.Target != idA) {
		t.Errorf("unexpected edge endpoints: %s -> %s", e.Source, e.Target)
	}
}

// TestMemoryGraphHandlerEmpty confirms the handler is well-defined on an
// empty store — must return {nodes:[], edges:[]} not null.
func TestMemoryGraphHandlerEmpty(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/memory/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var out struct {
		Nodes []graphNode `json:"nodes"`
		Edges []graphEdge `json:"edges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Nodes) != 0 {
		t.Errorf("nodes=%d want 0", len(out.Nodes))
	}
	if len(out.Edges) != 0 {
		t.Errorf("edges=%d want 0", len(out.Edges))
	}
}
