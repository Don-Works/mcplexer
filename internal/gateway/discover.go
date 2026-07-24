package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/codemode"
	"github.com/don-works/mcplexer/internal/downstream"
)

const maxDiscoverQueries = 10

// Caps on the detail:"full" Code-API block. Without these, a broad
// multi-query search can match up to maxDiscoverQueries*maxSearchResults
// (200) tools and render every full TypeScript signature — ~81k chars in the
// wild, which blows the MCP result token cap and gets force-saved to a file.
// We render full signatures only for the top-scored tools and stop once the
// byte budget is hit; the rest still appear by name+description in the
// per-query lists, and the agent can fetch any one with tool:"<exact_name>".
const (
	maxFullSignatures  = 40
	maxFullDetailBytes = 24000
)

// discoverQueryResult holds search results for a single query. `matches` is
// the ranked tool list (kept for backwards-compatible formatters); `ranked`
// carries the per-hit hybrid score breakdown for the new compact output.
type discoverQueryResult struct {
	query   string
	matches []Tool
	ranked  []rankedTool
}

// searchToolsDefinition returns the built-in mcpx__search_tools Tool.
//
// This is one of only three tools workers ever see in their top-level tool
// list (alongside mcpx__call_tool and mcpx__execute_code), so the description is
// deliberately long — it's the single entry point through which every
// other capability is discovered, and the cost of context bytes here is
// amortised against all the tool schemas that are NOT in the worker's
// surface.
func searchToolsDefinition() Tool {
	return Tool{
		Name: "mcpx__search_tools",
		Description: "Search for any callable function reachable from mcplexer — " +
			"downstream MCP servers (github, linear, slack, customer-specific, …) AND " +
			"mcplexer's built-in namespaces (mesh, memory, secret, mcpx admin, skill registry). " +
			"This is the ONLY discovery surface: downstream tools are not exposed directly in " +
			"tools/list, so if a capability exists, it is found here and then invoked with " +
			"mcpx__call_tool or mcpx__execute_code.\n\n" +
			"How to use:\n" +
			"- Infer search terms from the user's request — don't ask what to search for, just guess.\n" +
			"- Pass `queries: [\"send message to peer\", \"check pending mesh\", ...]` to search several " +
			"intents in one call. A singular `query: \"send message\"` (or query array) is also accepted. " +
			"Results are returned grouped by query.\n" +
			"- Results are ranked by a hybrid keyword + semantic score and grouped by namespace, with a " +
			"score and snippet per hit.\n" +
			"- Default `summary` is the cheap discovery mode. Use `tool: \"mesh__send\"` for one exact " +
			"TypeScript signature. Use `detail: \"full\"` only for a narrow query/namespace when you need " +
			"several signatures at once.\n" +
			"- Use `namespaces: [\"mesh\", \"memory\"]` to scope to specific surfaces when you already " +
			"know roughly where the function lives.\n" +
			"- Use `tool: \"mesh__send\"` for a single tool's full signature — bypasses ranking entirely.\n\n" +
			"Common namespaces to try (not exhaustive; downstream namespaces vary per install):\n" +
			"- mesh — talk to other agents on this machine or paired peer machines\n" +
			"- memory — persistent agent memory across runs\n" +
			"- secret — request a secret from the user (blocks until they respond)\n" +
			"- mcpx — gateway operations and the skills registry (`mcpx.skill_search`, `mcpx.skill_get`)\n" +
			"- skill — skill-run telemetry only (`skill.run_start`, `skill.phase`, `skill.run_complete`)\n" +
			"- github, linear, slack, postgres, … — downstream MCP servers configured by the operator\n\n" +
			"Workflow: search in summary mode to find the function and fetch the exact signature if needed. " +
			"Use mcpx__call_tool for one small independent call. Use mcpx__execute_code for batching, " +
			"dependent calls, filtering/aggregation, polling, branching, or transforms.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"queries": {
					"type": "array",
					"items": { "type": "string" },
					"maxItems": 10,
					"description": "Search queries to match against tool names and descriptions. Each query is searched independently and results are grouped."
				},
				"query": {
					"oneOf": [
						{ "type": "string" },
						{ "type": "array", "items": { "type": "string" }, "maxItems": 10 }
					],
					"description": "Compatibility alias for queries. Accepts one string or an array of strings."
				},
				"limit": {
					"type": "integer",
					"minimum": 1,
					"maximum": 20,
					"description": "Optional cap on results per query and full signatures rendered. Default 20, max 20."
				},
				"max_results": {
					"type": "integer",
					"minimum": 1,
					"maximum": 20,
					"description": "Compatibility alias for limit. Default 20, max 20."
				},
				"detail": {
					"type": "string",
					"enum": ["summary", "full"],
					"description": "Level of detail: 'summary' (default) returns ranked names + snippets + scores; 'full' includes TypeScript signatures for narrow searches."
				},
				"namespaces": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Optional namespace filter, e.g. [\"github\", \"linear\"]. When set, only tools whose name begins with one of these namespaces are searched."
				},
				"namespace": {
					"oneOf": [
						{ "type": "string" },
						{ "type": "array", "items": { "type": "string" } }
					],
					"description": "Compatibility alias for namespaces."
				},
				"tool": {
					"type": "string",
					"description": "Fetch a single tool's full TypeScript signature by exact name (e.g. 'postgres__query'). Ignores queries when set."
				}
			}
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Search Tools",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

// handleDiscoverTools searches for tools across all servers and returns
// results grouped by query. The detail parameter controls output verbosity:
// "summary" (default) returns a compact ranked list with scores + snippets
// grouped by namespace; "full" includes TypeScript signatures. The tool
// parameter fetches a single tool's full signature by exact name. The
// namespaces parameter filters the candidate pool.
func (h *handler) handleDiscoverTools(
	ctx context.Context, queries []string, detail, tool string, namespaces []string, limit int,
) (json.RawMessage, *RPCError) {
	// Gather all available tools (static + dynamic, via cache).
	allTools, err := h.gatherCodeModeTools(ctx)
	if err != nil {
		return nil, &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("gather tools: %v", err),
		}
	}

	// Filter by workspace routes (before adding searchable builtins,
	// since builtins don't go through routing).
	allTools = h.filterByWorkspaceRoutes(ctx, allTools)

	// Include searchable builtins that aren't in the static tools/list.
	// Dedupe afterwards: under slim-surface mode, the same built-in
	// (e.g. mesh__send) is contributed by both gatherCodeModeTools (via
	// codeModeBuiltinTools) and searchableBuiltins (via buildAllBuiltinTools).
	// Without dedup the agent sees every workflow tool twice in search results.
	allTools = append(allTools, h.searchableBuiltins(ctx)...)
	allTools = dedupeToolsByName(allTools)
	allTools = filterByWorkerToolAllowlist(ctx, allTools)
	allTools = filterByWorkerCapability(ctx, allTools)
	allTools = filterByWorkerFilesystemContract(ctx, allTools)
	allTools = h.filterAdminToolsForContext(ctx, allTools)

	// Single-tool lookup: return just that tool's full TS signature.
	if tool != "" {
		for _, t := range allTools {
			if t.Name == tool {
				defs := toolsToToolDefs([]Tool{t})
				ts := codemode.GenerateTypeScript(defs)
				return marshalToolResult(fmt.Sprintf("## %s\n%s\n\n%s", t.Name, t.Description, ts)), nil
			}
		}
		return marshalToolResult(fmt.Sprintf("Tool %q not found.", tool)), nil
	}

	if len(queries) > maxDiscoverQueries {
		return marshalErrorResult(
			fmt.Sprintf("Too many queries (max %d).", maxDiscoverQueries),
		), nil
	}
	if limit < 0 {
		return marshalErrorResult("limit must be a positive integer."), nil
	}
	if limit > maxSearchResults {
		return marshalErrorResult(fmt.Sprintf("limit too high (max %d).", maxSearchResults)), nil
	}
	resultLimit := maxSearchResults
	signatureLimit := maxFullSignatures
	if limit > 0 {
		resultLimit = limit
		signatureLimit = limit
	}

	// Require at least one search query — no "list all" mode.
	if len(queries) == 0 {
		return marshalErrorResult(
			"Provide search queries to find tools. Infer keywords from the user's intent — " +
				"e.g. [\"issues\", \"pull requests\"] or [\"send message\", \"channel\"]. " +
				"Use tool: \"name\" for a single tool's full signature.",
		), nil
	}

	// Apply namespace scoping before indexing so synonyms+IDF are
	// computed against the same candidate pool the user will see.
	if len(namespaces) > 0 {
		allTools = filterByNamespaces(allTools, namespaces)
	}

	// Rebuild the semantic index with the latest tool set.
	h.semIndex.rebuild(allTools)

	wantFull := strings.EqualFold(detail, "full")

	// Search each query independently using the hybrid ranker.
	var results []discoverQueryResult
	allMatched := make(map[string]Tool) // deduplicated union for TypeScript

	for _, q := range queries {
		ranked := hybridSearch(allTools, q, &h.semIndex, resultLimit)
		matches := make([]Tool, 0, len(ranked))
		for _, r := range ranked {
			matches = append(matches, r.Tool)
		}
		results = append(results, discoverQueryResult{
			query: q, matches: matches, ranked: ranked,
		})
		for _, t := range matches {
			allMatched[t.Name] = t
		}
	}

	// Check if we got any results at all.
	if len(allMatched) == 0 {
		queryStr := strings.Join(queries, ", ")
		return marshalToolResult(fmt.Sprintf(
			"No tools found matching: %s", queryStr,
		)), nil
	}

	// Speculative prefetch: pre-warm downstream servers for matched tools
	// so they're ready when execute_code follows.
	h.prefetchServers(ctx, allMatched)

	if wantFull {
		return marshalToolResult(formatDiscoverResults(results, allMatched, signatureLimit)), nil
	}
	return marshalToolResult(formatDiscoverResultsSummary(results)), nil
}

func (h *handler) filterAdminToolsForContext(ctx context.Context, tools []Tool) []Tool {
	if h.adminGate == nil || IsInProcessWorkerCall(ctx) || h.sessions.isAdminTrusted() {
		return tools
	}
	return h.adminGate.FilterAdminTools(
		tools,
		h.sessions.clientRoot(),
		h.sessions.workspaceRoots(),
	)
}

// prefetchServers fires off background pre-warm calls for servers that own
// matched tools, so downstream processes are running when execute_code arrives.
func (h *handler) prefetchServers(ctx context.Context, matched map[string]Tool) {
	// Isolated worker discovery is intentionally gateway-owned. Even when a
	// configured downstream reuses a builtin namespace, search must not wake
	// that process merely because the builtin matched.
	if _, isolated := workerFilesystemScopeFromContext(ctx); isolated {
		return
	}
	pf, ok := h.manager.(Prefetcher)
	if !ok {
		return
	}

	// Extract unique server namespaces from matched tools.
	namespaces := make(map[string]struct{})
	for _, t := range matched {
		if ns, _, ok := splitNamespace(t.Name); ok {
			namespaces[ns] = struct{}{}
		}
	}

	// Resolve namespace → serverID from configured servers.
	servers, err := h.store.ListDownstreamServers(ctx)
	if err != nil {
		return
	}

	bgCtx := h.bgCtx
	if bgCtx == nil {
		bgCtx = context.Background()
	}

	for _, srv := range servers {
		if _, ok := namespaces[srv.ToolNamespace]; !ok {
			continue
		}
		if srv.Transport == "internal" || srv.Disabled || downstream.IsAutoStartUnsafeServer(srv) {
			continue
		}
		go pf.EnsureRunning(bgCtx, srv.ID, "")
	}
}

// formatDiscoverAll formats all tools with TypeScript definitions (no query grouping).
func formatDiscoverAll(tools []Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d tools:\n", len(tools))

	for _, t := range tools {
		fmt.Fprintf(&b, "- %s — %s\n", t.Name, t.Description)
	}

	// Generate TypeScript.
	defs := toolsToToolDefs(tools)
	ts := codemode.GenerateTypeScript(defs)
	fmt.Fprintf(&b, "\n## Code API\n\n%s", ts)

	return b.String()
}

// formatDiscoverResults formats query-grouped results with a combined TypeScript block.
func formatDiscoverResults(results []discoverQueryResult, allMatched map[string]Tool, signatureLimit int) string {
	var b strings.Builder
	if signatureLimit <= 0 || signatureLimit > maxFullSignatures {
		signatureLimit = maxFullSignatures
	}

	for _, r := range results {
		fmt.Fprintf(&b, "## Results for %q (%d tools)\n", r.query, len(r.matches))
		for _, t := range r.matches {
			fmt.Fprintf(&b, "- %s — %s\n", t.Name, snippet(t.Description))
		}
		b.WriteByte('\n')
	}

	// Rank matched tools by their best score across all queries so the
	// (capped) full-signature block carries the most relevant ones first.
	bestScore := make(map[string]float64, len(allMatched))
	for _, r := range results {
		for _, rt := range r.ranked {
			if rt.Score > bestScore[rt.Tool.Name] {
				bestScore[rt.Tool.Name] = rt.Score
			}
		}
	}
	toolList := make([]Tool, 0, len(allMatched))
	for _, t := range allMatched {
		toolList = append(toolList, t)
	}
	sort.Slice(toolList, func(i, j int) bool {
		si, sj := bestScore[toolList[i].Name], bestScore[toolList[j].Name]
		if si != sj {
			return si > sj // higher score first
		}
		return toolList[i].Name < toolList[j].Name
	})

	total := len(toolList)
	rendered := toolList
	if len(rendered) > signatureLimit {
		rendered = rendered[:signatureLimit]
	}

	// Generate signatures, shrinking the set until the block fits the byte
	// budget. Verbose schemas vary 10x in size, so a count cap alone isn't
	// enough — we proportionally trim and regenerate (cheap; the set is small).
	ts := codemode.GenerateTypeScript(toolsToToolDefs(rendered))
	for len(ts) > maxFullDetailBytes && len(rendered) > 1 {
		keep := len(rendered) * maxFullDetailBytes / len(ts)
		if keep >= len(rendered) {
			keep = len(rendered) - 1
		}
		if keep < 1 {
			keep = 1
		}
		rendered = rendered[:keep]
		ts = codemode.GenerateTypeScript(toolsToToolDefs(rendered))
	}

	fmt.Fprintf(&b, "## Code API")
	if omitted := total - len(rendered); omitted > 0 {
		fmt.Fprintf(&b, " (full signatures for top %d of %d tools — output capped)\n", len(rendered), total)
		fmt.Fprintf(&b, "_%d more matched (listed by name above). Narrow your query, scope with namespaces:[…], "+
			"or use tool:\"<exact_name>\" for any one signature._\n", omitted)
	}
	fmt.Fprintf(&b, "\n\n%s", ts)

	return b.String()
}

// formatDiscoverResultsSummary formats query-grouped results as a compact
// namespace-grouped ranked list with per-hit scores and snippets. Falls back
// to plain name + description when the ranked breakdown is missing (so older
// callers/tests that build discoverQueryResult by hand still produce output).
func formatDiscoverResultsSummary(results []discoverQueryResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "## Results for %q (%d tools)\n", r.query, len(r.matches))
		writeRankedSummary(&b, r)
		b.WriteByte('\n')
	}
	b.WriteString("Use detail: \"full\" for TypeScript signatures, or tool: \"name\" for a single tool.")
	return b.String()
}

// writeRankedSummary writes one query's ranked hits, grouped by namespace.
func writeRankedSummary(b *strings.Builder, r discoverQueryResult) {
	if len(r.ranked) == 0 {
		for _, t := range r.matches {
			fmt.Fprintf(b, "- %s — %s\n", t.Name, t.Description)
		}
		return
	}
	for _, group := range groupByNamespace(r.ranked) {
		fmt.Fprintf(b, "### %s\n", group.Namespace)
		for _, hit := range group.Hits {
			fmt.Fprintf(b, "- [%.2f] %s — %s\n",
				hit.Score, hit.Tool.Name, snippet(hit.Tool.Description))
		}
	}
}
