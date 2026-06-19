package routing

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// parsedRule holds a RouteRule with pre-parsed fields for matching.
type parsedRule struct {
	store.RouteRule
	toolPatterns    []string
	specificity     int
	toolSpecificity int
	namespace       string // tool_namespace from downstream server
}

// parseRules converts store RouteRules into parsedRules.
func parseRules(rules []store.RouteRule) []parsedRule {
	out := make([]parsedRule, 0, len(rules))
	for _, r := range rules {
		pr := parsedRule{
			RouteRule:   r,
			specificity: GlobSpecificity(r.PathGlob),
		}
		pr.toolPatterns = parseToolMatch(r.ToolMatch)
		pr.toolSpecificity = calculateToolSpecificity(pr.toolPatterns)
		out = append(out, pr)
	}
	return out
}

// calculateToolSpecificity returns a score for tool match specificity.
// "*" -> 0
// "prefix__*" -> 1
// "exact" -> 2
func calculateToolSpecificity(patterns []string) int {
	max := 0
	for _, p := range patterns {
		s := 0
		if p == "*" {
			s = 0
		} else if strings.HasSuffix(p, "*") {
			s = 1
		} else {
			s = 2
		}
		if s > max {
			max = s
		}
	}
	return max
}

// parseToolMatch decodes the JSON tool_match array.
func parseToolMatch(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{"*"}
	}
	var patterns []string
	if err := json.Unmarshal(raw, &patterns); err != nil {
		return []string{"*"}
	}
	if len(patterns) == 0 {
		return []string{"*"}
	}
	return patterns
}

// sortRules sorts parsed rules by:
// 1. Glob specificity DESC (most specific path always wins)
// 2. Tool specificity DESC
// 3. Priority DESC (tiebreak among equal specificity)
// 4. ID ASC (stable tiebreak)
func sortRules(rules []parsedRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].specificity != rules[j].specificity {
			return rules[i].specificity > rules[j].specificity
		}
		if rules[i].toolSpecificity != rules[j].toolSpecificity {
			return rules[i].toolSpecificity > rules[j].toolSpecificity
		}
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
}

// matchTool checks if toolName matches any of the tool patterns.
// Patterns support trailing wildcard: "github__*" matches "github__create_issue".
func matchTool(toolName string, patterns []string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(toolName, prefix) {
				return true
			}
		} else if p == toolName {
			return true
		}
	}
	return false
}
