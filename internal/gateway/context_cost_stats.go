package gateway

import (
	"context"
	"encoding/json"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/config"
)

const contextCostLargeResultBytes = 16 * 1024

// ContextCostStats is an in-memory snapshot of tool-result byte pressure.
// Counts are process-local and reset on daemon restart.
type ContextCostStats struct {
	ToolResultsTotal          uint64                          `json:"tool_results_total"`
	ToolResultBytesTotal      uint64                          `json:"tool_result_bytes_total"`
	ToolResultBytesMax        uint64                          `json:"tool_result_bytes_max"`
	ToolResultBytesLast       uint64                          `json:"tool_result_bytes_last"`
	LargeToolResultsTotal     uint64                          `json:"large_tool_results_total"`
	ErrorToolResultsTotal     uint64                          `json:"error_tool_results_total"`
	BlockedToolResultsTotal   uint64                          `json:"blocked_tool_results_total"`
	LargeResultThresholdBytes int                             `json:"large_result_threshold_bytes"`
	ByTool                    map[string]ContextCostToolStats `json:"by_tool,omitempty"`
	Compression               CompressionStats                `json:"compression"`
}

type ContextCostToolStats struct {
	Calls          uint64 `json:"calls"`
	TotalBytes     uint64 `json:"total_bytes"`
	MaxBytes       uint64 `json:"max_bytes"`
	LastBytes      uint64 `json:"last_bytes"`
	LargeResults   uint64 `json:"large_results"`
	ErrorResults   uint64 `json:"error_results"`
	BlockedResults uint64 `json:"blocked_results"`
}

// CompressionStats aggregates the token-compression pipeline's effect across
// the process lifetime. In shadow (dry-run) mode the would-save figures
// accumulate while nothing is applied; in on mode the applied figures fill in.
// Mode is stamped at read time from settings. Counters reset on restart.
type CompressionStats struct {
	Mode              string                               `json:"mode"`
	Samples           uint64                               `json:"samples"`
	OrigBytesTotal    uint64                               `json:"orig_bytes_total"`
	AppliedSaveBytes  uint64                               `json:"applied_save_bytes"`
	AppliedSaveTokens uint64                               `json:"applied_save_tokens"`
	ByTransform       map[string]CompressionTransformStats `json:"by_transform,omitempty"`
}

// CompressionTransformStats is the per-transform observed effect — the numbers
// shown next to each transform's toggle in the settings UI. WouldSave* is the
// measured potential (in any non-off mode); AppliedSave* is what was actually
// used (on mode only).
type CompressionTransformStats struct {
	Lossless          bool   `json:"lossless"`
	Samples           uint64 `json:"samples"`
	Changed           uint64 `json:"changed"`
	OrigBytes         uint64 `json:"orig_bytes"`
	WouldSaveBytes    uint64 `json:"would_save_bytes"`
	WouldSaveTokens   uint64 `json:"would_save_tokens"`
	Applied           uint64 `json:"applied"`
	AppliedSaveBytes  uint64 `json:"applied_save_bytes"`
	AppliedSaveTokens uint64 `json:"applied_save_tokens"`
}

func (h *handler) recordContextCostResult(toolName string, result json.RawMessage, status string) {
	if h == nil {
		return
	}
	n := uint64(len(result))
	h.contextCostMu.Lock()
	defer h.contextCostMu.Unlock()
	if h.contextCost.ByTool == nil {
		h.contextCost.ByTool = make(map[string]ContextCostToolStats)
	}
	h.contextCost.ToolResultsTotal++
	h.contextCost.ToolResultBytesTotal += n
	h.contextCost.ToolResultBytesLast = n
	if n > h.contextCost.ToolResultBytesMax {
		h.contextCost.ToolResultBytesMax = n
	}
	if h.contextCost.LargeResultThresholdBytes == 0 {
		h.contextCost.LargeResultThresholdBytes = contextCostLargeResultBytes
	}
	toolStats := h.contextCost.ByTool[toolName]
	toolStats.Calls++
	toolStats.TotalBytes += n
	toolStats.LastBytes = n
	if n > toolStats.MaxBytes {
		toolStats.MaxBytes = n
	}
	if n >= contextCostLargeResultBytes {
		h.contextCost.LargeToolResultsTotal++
		toolStats.LargeResults++
	}
	switch status {
	case "blocked":
		h.contextCost.BlockedToolResultsTotal++
		toolStats.BlockedResults++
	case "error":
		h.contextCost.ErrorToolResultsTotal++
		toolStats.ErrorResults++
	}
	h.contextCost.ByTool[toolName] = toolStats
}

// recordCompression folds the pipeline's per-transform observations into the
// process-local compression stats. Safe to call with an empty slice. All
// transforms in one call measure against the same incoming payload, so the
// call's incoming size is taken from the first observation; per-transform
// would-save figures are kept separate (never summed into a misleading total).
func (h *handler) recordCompression(obs []compression.Observation) {
	if h == nil || len(obs) == 0 {
		return
	}
	h.contextCostMu.Lock()
	defer h.contextCostMu.Unlock()
	cs := &h.contextCost.Compression
	if cs.ByTransform == nil {
		cs.ByTransform = make(map[string]CompressionTransformStats)
	}
	cs.Samples++
	cs.OrigBytesTotal += uint64(obs[0].OrigBytes)
	for _, o := range obs {
		ts := cs.ByTransform[o.Transform]
		ts.Lossless = o.Lossless
		ts.Samples++
		ts.OrigBytes += uint64(o.OrigBytes)
		if o.Changed {
			ts.Changed++
		}
		if o.SavedBytes > 0 {
			ts.WouldSaveBytes += uint64(o.SavedBytes)
		}
		if o.SavedTokens > 0 {
			ts.WouldSaveTokens += uint64(o.SavedTokens)
		}
		if o.Applied {
			ts.Applied++
			ts.AppliedSaveBytes += uint64(o.SavedBytes)
			ts.AppliedSaveTokens += uint64(o.SavedTokens)
			cs.AppliedSaveBytes += uint64(o.SavedBytes)
			cs.AppliedSaveTokens += uint64(o.SavedTokens)
		}
		cs.ByTransform[o.Transform] = ts
	}
}

func (h *handler) ContextCostStats() ContextCostStats {
	if h == nil {
		return ContextCostStats{LargeResultThresholdBytes: contextCostLargeResultBytes}
	}
	h.contextCostMu.RLock()
	defer h.contextCostMu.RUnlock()
	out := h.contextCost
	if out.LargeResultThresholdBytes == 0 {
		out.LargeResultThresholdBytes = contextCostLargeResultBytes
	}
	if h.contextCost.ByTool != nil {
		out.ByTool = make(map[string]ContextCostToolStats, len(h.contextCost.ByTool))
		for k, v := range h.contextCost.ByTool {
			out.ByTool[k] = v
		}
	}
	if h.contextCost.Compression.ByTransform != nil {
		out.Compression.ByTransform = make(map[string]CompressionTransformStats, len(h.contextCost.Compression.ByTransform))
		for k, v := range h.contextCost.Compression.ByTransform {
			out.Compression.ByTransform[k] = v
		}
	}
	return out
}

func contextCostStatsToolDefinition() Tool {
	return Tool{
		Name:        "mcpx__context_cost_stats",
		Description: "Return process-local context-cost counters for MCP tool results plus the current output/preview cap settings. Counters reset on daemon restart and measure bytes after sanitization/compaction where applicable.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Context Cost Stats",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

func (h *handler) handleContextCostStats(ctx context.Context) (json.RawMessage, *RPCError) {
	settings := config.DefaultSettings()
	if h != nil && h.settingsSvc != nil {
		settings = h.settingsSvc.Load(ctx)
	}
	snap := h.ContextCostStats()
	snap.Compression.Mode = string(compression.ParseMode(settings.CompressionMode))
	return marshalJSONResult(map[string]any{
		"counters": snap,
		"settings": map[string]any{
			"slim_tools":                   settings.SlimTools,
			"slim_surface":                 settings.SlimSurface,
			"compact_responses":            settings.CompactResponses,
			"compression_mode":             string(compression.ParseMode(settings.CompressionMode)),
			"code_mode_max_output_bytes":   settings.CodeModeMaxOutputBytes,
			"code_mode_max_heap_growth_mb": settings.CodeModeMaxHeapGrowthMB,
			"mesh_receive_max_results":     settings.MeshReceiveMaxResults,
			"mesh_receive_preview_bytes":   settings.MeshReceivePreviewBytes,
			"mesh_send_max_content_bytes":  settings.MeshSendMaxContentBytes,
		},
	})
}
