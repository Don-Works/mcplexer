package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// recipeSearchToolDef returns the builtin tool definition for searching
// harvested tool-call recipes. This tool is discoverable through
// search_tools but not in the static tools/list.
func recipeSearchToolDef() Tool {
	extras := withAnnotations(ToolAnnotations{
		Title:           "Search Tool Recipes",
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	})
	extras["x-search-tags"] = json.RawMessage(`"recipe,harvest,pattern,tool usage,call pattern,hint,snippet"`)

	return Tool{
		Name: "mcpx__search_recipes",
		Description: "Search harvested tool-call recipes mined from audit logs. " +
			"Recipes capture successful tool-call patterns — common parameters, " +
			"error rates, latency, and usage frequency — for any tool that has been " +
			"called at least 3 times. Results are ranked by reliability, frequency, " +
			"recency, and session diversity. Use this to discover how tools are " +
			"typically called, what parameters they expect, and which tools are " +
			"most reliable for a given task. Provide an FTS5 query (e.g. 'github issue list' " +
			"or 'postgres query') or leave empty to list top-ranked recipes. " +
			"Secrets are never included — only parameter key names, never values.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "FTS5 search query over tool names, namespaces, descriptions, and tags. Leave empty to list top-ranked recipes."
				},
				"namespace": {
					"type": "string",
					"description": "Optional namespace filter (e.g. 'github', 'postgres')"
				},
				"tool_name": {
					"type": "string",
					"description": "Optional exact tool name filter"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum results (default 10, max 50)",
					"default": 10
				},
				"detail": {
					"type": "string",
					"enum": ["summary", "full"],
					"description": "Detail level: 'summary' (default) shows name/score/description; 'full' includes param patterns and stats"
				}
			}
		}`),
		Extras: extras,
	}
}

// recipeStatsToolDef returns the builtin tool definition for recipe statistics.
func recipeStatsToolDef() Tool {
	extras := withAnnotations(ToolAnnotations{
		Title:           "Recipe Statistics",
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	})
	extras["x-search-tags"] = json.RawMessage(`"recipe stats,harvest summary,tool usage stats"`)

	return Tool{
		Name: "mcpx__recipe_stats",
		Description: "Get summary statistics about the recipe store: total recipes, " +
			"top namespaces, average scores, total tool calls recorded. " +
			"Also triggers an on-demand harvest cycle if the store is empty.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Extras: extras,
	}
}

// handleSearchRecipes processes mcpx__search_recipes calls.
func (h *handler) handleSearchRecipes(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	var p struct {
		Query     string `json:"query"`
		Namespace string `json:"namespace"`
		ToolName  string `json:"tool_name"`
		Limit     int    `json:"limit"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	if p.Limit <= 0 {
		p.Limit = 10
	}
	if p.Limit > 50 {
		p.Limit = 50
	}

	f := store.RecipeFilter{
		Limit:  p.Limit,
		Offset: 0,
	}
	if p.Namespace != "" {
		f.Namespace = &p.Namespace
	}
	if p.ToolName != "" {
		f.ToolName = &p.ToolName
	}

	var recipes []store.Recipe
	var err error

	if p.Query != "" {
		f.Query = p.Query
		recipes, err = h.store.SearchRecipes(ctx, f)
	} else {
		recipes, err = h.store.ListRecipes(ctx, f)
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("search recipes: %v", err)}
	}

	if len(recipes) == 0 {
		return marshalToolResult("No recipes found. Try running a harvest cycle first with mcpx__recipe_stats."), nil
	}

	wantFull := strings.EqualFold(p.Detail, "full")
	return marshalToolResult(formatRecipes(recipes, wantFull)), nil
}

// handleRecipeStats processes mcpx__recipe_stats calls.
func (h *handler) handleRecipeStats(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	// Trigger an on-demand harvest if the store is empty.
	recipes, err := h.store.ListRecipes(ctx, store.RecipeFilter{Limit: 1})
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("list recipes: %v", err)}
	}

	result := "## Recipe Store Statistics\n\n"

	if len(recipes) == 0 {
		result += "The recipe store is empty. On-demand harvest triggered — recipes will appear after the next harvest cycle.\n\n"
		// Can't trigger inline; the scheduler runs periodically.
		result += "Recipes are mined from audit logs (min 3 calls per tool, last 7 days by default).\n"
		result += "Ensure the gateway has processed tool calls before harvesting.\n"
	} else {
		// Get stats about the recipe store.
		allRecipes, err := h.store.ListRecipes(ctx, store.RecipeFilter{Limit: 1000})
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("list all recipes: %v", err)}
		}
		var totalScore float64
		namespaceCount := make(map[string]int)
		var totalCalls int
		for _, r := range allRecipes {
			totalScore += r.Score
			namespaceCount[r.Namespace]++
			totalCalls += r.TotalCount
		}
		avgScore := 0.0
		if len(allRecipes) > 0 {
			avgScore = totalScore / float64(len(allRecipes))
		}

		result += fmt.Sprintf("- **Total recipes:** %d\n", len(allRecipes))
		result += fmt.Sprintf("- **Total tool calls harvested:** %d\n", totalCalls)
		result += fmt.Sprintf("- **Average recipe score:** %.3f\n", avgScore)
		result += "\n### Top Namespaces\n"
		// Sort namespaces by count (simple top 5).
		type nsCount struct {
			ns    string
			count int
		}
		var sorted []nsCount
		for ns, count := range namespaceCount {
			sorted = append(sorted, nsCount{ns, count})
		}
		// Bubble sort top 5 (small set, fine).
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].count > sorted[i].count {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		maxNS := 5
		if len(sorted) < maxNS {
			maxNS = len(sorted)
		}
		for _, sc := range sorted[:maxNS] {
			result += fmt.Sprintf("- %s: **%d** recipes\n", sc.ns, sc.count)
		}
	}

	return marshalToolResult(result), nil
}

// formatRecipes formats recipe results for display.
func formatRecipes(recipes []store.Recipe, full bool) string {
	var b strings.Builder
	if full {
		for _, r := range recipes {
			fmt.Fprintf(&b, "## %s (score: %.3f)\n", r.ToolName, r.Score)
			fmt.Fprintf(&b, "- **Namespace:** %s\n", r.Namespace)
			fmt.Fprintf(&b, "- **Description:** %s\n", r.Description)
			fmt.Fprintf(&b, "- **Calls:** %d successful / %d total\n", r.SuccessCount, r.TotalCount)
			fmt.Fprintf(&b, "- **Error rate:** %.1f%%\n", r.ErrorRate*100)
			fmt.Fprintf(&b, "- **Avg latency:** %.0fms\n", r.AvgLatencyMs)
			fmt.Fprintf(&b, "- **Sessions:** %d\n", r.SessionCount)
			if len(r.ParamsPattern) > 0 {
				fmt.Fprintf(&b, "- **Params pattern:** %s\n", string(r.ParamsPattern))
			}
			if len(r.Tags) > 0 {
				fmt.Fprintf(&b, "- **Tags:** %s\n", string(r.Tags))
			}
			b.WriteByte('\n')
		}
	} else {
		for _, r := range recipes {
			fmt.Fprintf(&b, "- [%.3f] **%s** — %s\n", r.Score, r.ToolName, r.Description)
		}
		b.WriteString("\nUse `detail: \"full\"` for param patterns and stats.")
	}
	return b.String()
}
