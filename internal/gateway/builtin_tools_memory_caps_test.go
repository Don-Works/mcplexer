// builtin_tools_memory_caps_test.go — the associative memory tools
// (spreading_activation / co_recalled / suggestions) must only be
// advertised when their backing capability is live; advertising tools
// that structurally return empty wastes agent context.
package gateway

import (
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
)

func memoryToolNameSet(tools []Tool) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, t := range tools {
		out[t.Name] = true
	}
	return out
}

func TestMemoryToolDefinitionsCapabilityGate(t *testing.T) {
	cases := []struct {
		name         string
		caps         memoryToolCaps
		wantSpread   bool
		wantCoRecall bool
		wantSugg     bool
	}{
		{name: "all_off", caps: memoryToolCaps{}},
		{name: "embedder_only", caps: memoryToolCaps{HasEmbedder: true},
			wantSpread: true, wantSugg: true},
		{name: "tracking_only", caps: memoryToolCaps{RecallTracking: true},
			wantCoRecall: true, wantSugg: true},
		{name: "both", caps: memoryToolCaps{HasEmbedder: true, RecallTracking: true},
			wantSpread: true, wantCoRecall: true, wantSugg: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryToolNameSet(memoryToolDefinitions(tc.caps))
			checks := []struct {
				tool string
				want bool
			}{
				{tool: "memory__spreading_activation", want: tc.wantSpread},
				{tool: "memory__co_recalled", want: tc.wantCoRecall},
				{tool: "memory__suggestions", want: tc.wantSugg},
			}
			for _, c := range checks {
				if got[c.tool] != c.want {
					t.Errorf("%s advertised=%v, want %v", c.tool, got[c.tool], c.want)
				}
			}
			// The universal surface never disappears.
			for _, always := range []string{
				"memory__save", "memory__recall", "memory__list",
				"memory__related_entities", "memory__forget",
			} {
				if !got[always] {
					t.Errorf("%s must always be advertised", always)
				}
			}
		})
	}
}

func TestMemoryToolCapabilitiesFromService(t *testing.T) {
	h := &handler{}
	if caps := h.memoryToolCapabilities(); caps.HasEmbedder || caps.RecallTracking {
		t.Fatalf("nil service must yield zero caps, got %+v", caps)
	}

	svc := memory.NewService(nil, memory.NoopEmbedder{}, nil)
	h.memorySvc = svc
	if caps := h.memoryToolCapabilities(); caps.HasEmbedder || caps.RecallTracking {
		t.Fatalf("noop embedder + tracking off must yield zero caps, got %+v", caps)
	}

	svc.EnableRecallTrackingForTest()
	if caps := h.memoryToolCapabilities(); !caps.RecallTracking {
		t.Fatal("recall tracking enabled but caps.RecallTracking=false")
	}
}
