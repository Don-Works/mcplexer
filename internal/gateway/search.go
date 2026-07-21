package gateway

import (
	"context"
	"encoding/json"
	"strings"
)

const maxSearchResults = 20

// BuiltinPrefix is the namespace prefix for MCPlexer built-in tools.
const BuiltinPrefix = "mcpx__"

// MeshPrefix is the namespace prefix for agent mesh tools.
const MeshPrefix = "mesh__"

// BridgePrefix is the namespace prefix for chat bridge tools (Telegram, Google Chat, ...).
const BridgePrefix = "chat__"

// SecretPrefix is the namespace prefix for ephemeral secret-prompt tools.
const SecretPrefix = "secret__"

// EmailPrefix is the namespace prefix for the agent email tool surface.
const EmailPrefix = "email__"

// MemoryPrefix is the namespace prefix for the universal memory tools
// (memory__save / __recall / __list / __forget / __offer_memory /
// __request_memory). Cross-harness fact + note store, see migration 058.
const MemoryPrefix = "memory__"

// TaskPrefix is the namespace prefix for the universal task tools
// (task__create / __list / __get / __update / __assign / __claim /
// __delete / __append_note). Per-workspace operational primitive, see
// migration 061.
const TaskPrefix = "task__"

// SkillPrefix is the namespace prefix for the W2 skill telemetry tools
// (skill__run_start / __phase / __run_complete). One row per run in
// the skill_runs table (migration 074); optional task tree mirror.
const SkillPrefix = "skill__"

// legacyBuiltinPrefix is the old prefix, kept for backward compatibility.
const legacyBuiltinPrefix = "mcplexer__"

// legacyMcpxRenames maps the bare tool name (no prefix) of every former
// mcplexer__ builtin that was renamed to mcpx__. Without this allowlist
// we'd rewrite every modern mcplexer__* tool (list_workspaces,
// delete_route, …) into a non-existent mcpx__ name and dispatch as a
// builtin — breaking the whole self-CRUD admin surface.
var legacyMcpxRenames = map[string]bool{
	"execute_code":           true,
	"search_tools":           true,
	"provision_mcp":          true,
	"create_addon":           true,
	"import_openapi":         true,
	"approve_tool_call":      true,
	"deny_tool_call":         true,
	"list_pending_approvals": true,
	"reload_server":          true,
	"flush_cache":            true,
}

// normalizeBuiltinName converts the legacy mcplexer__-prefixed name of a
// renamed mcpx__ builtin into its modern form. mcplexer__list_workspaces
// and friends — which belong to the new self-CRUD admin namespace, not
// the legacy mcpx surface — are passed through untouched.
func normalizeBuiltinName(name string) string {
	if after, ok := strings.CutPrefix(name, legacyBuiltinPrefix); ok {
		if legacyMcpxRenames[after] {
			return BuiltinPrefix + after
		}
	}
	return name
}

// filterByWorkspaceRoutes is intentionally permissive at the LIST step:
// every tool is returned regardless of workspace routing. Routing rules are
// still enforced authoritatively at DISPATCH (handleToolsCall) — so a tool
// appearing in tools/list cannot be invoked unless an active route allows it.
//
// Why permissive: the previous behavior gated tool DISCOVERY on routing.
// In practice this caused fresh OpenCode/Codex/gemini-cli installs to see
// an empty tools/list and report "Failed to get tools" because the engine's
// per-tool route resolution was returning errors for stdio sessions whose
// clientRoot didn't slot under a registered workspace. Hiding tools at
// listing time gave false signals; gating at call time gives correct ones.
//
// TODO: when M7.x repo+workspace identity is wired, revisit whether to
// re-introduce per-tool listing scoping for federated views (cross-user
// audit clarity), but keep listings inclusive for the local-user case.
func (h *handler) filterByWorkspaceRoutes(ctx context.Context, tools []Tool) []Tool {
	return tools
}

// searchStopWords are common words filtered out of queries to improve matching.
var searchStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "can": true, "shall": true,
	"to": true, "of": true, "in": true, "for": true, "on": true,
	"with": true, "at": true, "by": true, "from": true, "as": true,
	"into": true, "about": true, "between": true, "through": true,
	"and": true, "or": true, "but": true, "not": true, "no": true,
	"it": true, "its": true, "this": true, "that": true, "these": true,
	"my": true, "me": true, "i": true, "we": true, "our": true,
	"all": true, "some": true, "any": true, "each": true,
	"get": true, "show": true, "find": true, "give": true,
}

// filterStopWords removes common stop words from query tokens, keeping at
// least one token so queries like "get issues" become "issues".
func filterStopWords(tokens []string) []string {
	filtered := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !searchStopWords[t] {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		return tokens // keep original if all words are stop words
	}
	return filtered
}

// extractSearchTags returns search tags from a tool's Extras if present.
func extractSearchTags(t Tool) string {
	if t.Extras == nil {
		return ""
	}
	raw, ok := t.Extras["x-search-tags"]
	if !ok {
		return ""
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err == nil {
		return strings.ToLower(strings.Join(tags, " "))
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return strings.ToLower(single)
	}
	return ""
}

// extractSearchDescription returns the first paragraph of the description,
// capped at 200 chars, for scoring purposes.
func extractSearchDescription(desc string) string {
	if idx := strings.Index(desc, "\n\n"); idx > 0 {
		desc = desc[:idx]
	}
	if len(desc) > 200 {
		desc = desc[:200]
	}
	return strings.ToLower(desc)
}

// buildSearchText creates a searchable string from a tool's name, description,
// and search tags. Expands namespace separators, underscores, and hyphens into
// spaces so that token-based queries like "linear tasks" match tools named
// "linear__list_tasks".
func buildSearchText(t Tool) string {
	nameLower := strings.ToLower(t.Name)
	descLower := extractSearchDescription(t.Description)

	expanded := strings.ReplaceAll(nameLower, "__", " ")
	expanded = strings.ReplaceAll(expanded, "_", " ")
	expanded = strings.ReplaceAll(expanded, "-", " ")

	text := nameLower + " " + expanded + " " + descLower

	if tags := extractSearchTags(t); tags != "" {
		text += " " + tags
	}

	return text
}

// matchesQuery checks if a tool matches the search query. It first tries an
// exact substring match, then falls back to multi-token matching where every
// non-stop-word in the query must appear somewhere in the tool's searchable text.
// Synonym expansion is applied so that "make customer" matches a tool whose
// description mentions "create".
func matchesQuery(t Tool, queryLower string) bool {
	if queryLower == "" {
		return true
	}

	searchText := buildSearchText(t)

	if strings.Contains(searchText, queryLower) {
		return true
	}

	tokens := filterStopWords(strings.Fields(queryLower))
	if len(tokens) == 0 {
		return false
	}

	syn := defaultSynonyms()
	for _, tok := range tokens {
		if !anySynonymContained(searchText, tok, syn) {
			return false
		}
	}
	return true
}

// anySynonymContained returns true when the searchText contains the token or
// any term in the token's synonym cluster.
func anySynonymContained(searchText, token string, syn *synonymTable) bool {
	if strings.Contains(searchText, token) {
		return true
	}
	for _, s := range syn.expandTerm(token) {
		if s == token {
			continue
		}
		if strings.Contains(searchText, s) {
			return true
		}
	}
	return false
}

// scoreMatch returns a relevance score for sorting search results (higher = better).
func scoreMatch(t Tool, queryLower string) int {
	nameLower := strings.ToLower(t.Name)
	descLower := extractSearchDescription(t.Description)
	nameExpanded := strings.ReplaceAll(strings.ReplaceAll(nameLower, "__", " "), "_", " ")
	tags := extractSearchTags(t)

	score := 0

	if nameLower == queryLower || nameExpanded == queryLower {
		score += 100
	}
	if strings.Contains(nameLower, queryLower) || strings.Contains(nameExpanded, queryLower) {
		score += 50
	}
	if strings.Contains(descLower, queryLower) {
		score += 20
	}
	if tags != "" && strings.Contains(tags, queryLower) {
		score += 15
	}

	tokens := filterStopWords(strings.Fields(queryLower))
	for _, tok := range tokens {
		if strings.Contains(nameLower, tok) || strings.Contains(nameExpanded, tok) {
			score += 10
		}
		if strings.Contains(descLower, tok) {
			score += 5
		}
		if tags != "" && strings.Contains(tags, tok) {
			score += 3
		}
	}
	return score
}
