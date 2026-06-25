package gateway

import (
	"context"
	"encoding/json"

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
	return marshalJSONResult(map[string]any{
		"counters": h.ContextCostStats(),
		"settings": map[string]any{
			"slim_tools":                   settings.SlimTools,
			"slim_surface":                 settings.SlimSurface,
			"compact_responses":            settings.CompactResponses,
			"code_mode_max_output_bytes":   settings.CodeModeMaxOutputBytes,
			"code_mode_max_heap_growth_mb": settings.CodeModeMaxHeapGrowthMB,
			"mesh_receive_max_results":     settings.MeshReceiveMaxResults,
			"mesh_receive_preview_bytes":   settings.MeshReceivePreviewBytes,
			"mesh_send_max_content_bytes":  settings.MeshSendMaxContentBytes,
		},
	})
}
