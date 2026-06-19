package gateway

// builtin_tools_memory_caps.go — capability gating for the associative
// memory tools. Three of the memory__* tools structurally return empty
// results unless their backing capability is configured on the daemon:
//
//   - memory__spreading_activation — needs an embedding provider
//     (MCPLEXER_OPENAI_API_KEY); without one SpreadingActivation
//     short-circuits to nil.
//   - memory__co_recalled — needs AR4 recall tracking
//     (MCPLEXER_RECALL_TRACKING=1); without it no events ever land.
//   - memory__suggestions — composes co-recall + semantic + related-
//     entity; the related-entity axis alone is already covered by
//     memory__related_entities, so it advertises only when at least one
//     of the two gated capabilities is live.
//
// Advertising dead tools wastes agent context and invites confusing
// empty results, so tools/list drops them when the capability is off.
// Dispatch (handler_memory.go) and the REST endpoints stay untouched —
// a cached client calling a hidden tool still gets the real handler.

// memoryToolCaps captures which associative-memory capabilities are
// live on this daemon. Derived from the memory service at list time.
type memoryToolCaps struct {
	HasEmbedder    bool
	RecallTracking bool
}

// memoryToolCapabilities reads the live capability flags off the wired
// memory service. Zero-value (all off) when the service is absent.
func (h *handler) memoryToolCapabilities() memoryToolCaps {
	if h.memorySvc == nil {
		return memoryToolCaps{}
	}
	return memoryToolCaps{
		HasEmbedder:    h.memorySvc.HasEmbedder(),
		RecallTracking: h.memorySvc.RecallTrackingEnabled(),
	}
}

// memoryToolDefinitions returns the advertisable memory__* tools for
// the given capability set. See the file comment for the gating rules.
func memoryToolDefinitions(caps memoryToolCaps) []Tool {
	all := allMemoryToolDefinitions()
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		switch t.Name {
		case "memory__spreading_activation":
			if !caps.HasEmbedder {
				continue
			}
		case "memory__co_recalled":
			if !caps.RecallTracking {
				continue
			}
		case "memory__suggestions":
			if !caps.HasEmbedder && !caps.RecallTracking {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}
