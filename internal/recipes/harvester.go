package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// RecipeWriter abstracts the write surface for persisting harvested recipes.
type RecipeWriter interface {
	UpsertRecipe(ctx context.Context, r *store.Recipe) error
	ListRecipes(ctx context.Context, f store.RecipeFilter) ([]store.Recipe, error)
	DeleteRecipe(ctx context.Context, id string) error
}

// HarvesterConfig controls harvesting behaviour.
type HarvesterConfig struct {
	Since          time.Duration
	BatchSize      int
	MinOccurrences int
}

func (c *HarvesterConfig) defaults() {
	if c.Since <= 0 {
		c.Since = 7 * 24 * time.Hour
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.MinOccurrences <= 0 {
		c.MinOccurrences = 3
	}
}

// Harvester mines tool-call patterns from audit records and produces recipes.
type Harvester struct {
	audit  AuditQuerier
	store  RecipeWriter
	config HarvesterConfig
}

// NewHarvester creates a harvester.
func NewHarvester(audit AuditQuerier, store RecipeWriter, config HarvesterConfig) *Harvester {
	config.defaults()
	return &Harvester{audit: audit, store: store, config: config}
}

// Run performs one harvest cycle.
func (h *Harvester) Run(ctx context.Context, now time.Time) (int, error) {
	since := now.Add(-h.config.Since)

	records, err := h.audit.QueryRecentToolCalls(ctx, since, h.config.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("query recent tool calls: %w", err)
	}
	if len(records) == 0 {
		return 0, nil
	}

	groups := groupByTool(records)

	var upserted int
	for toolName, calls := range groups {
		if len(calls) < h.config.MinOccurrences {
			continue
		}
		recipe := buildRecipe(toolName, calls, now)
		if err := h.store.UpsertRecipe(ctx, recipe); err != nil {
			return upserted, fmt.Errorf("upsert recipe %q: %w", toolName, err)
		}
		upserted++
	}

	return upserted, nil
}

func groupByTool(records []AuditCall) map[string][]AuditCall {
	groups := make(map[string][]AuditCall)
	for _, r := range records {
		groups[r.ToolName] = append(groups[r.ToolName], r)
	}
	return groups
}

func buildRecipe(toolName string, calls []AuditCall, now time.Time) *store.Recipe {
	namespace := extractNamespace(toolName)

	var successCount, totalCount int
	var totalLatency int
	var maxTimestamp time.Time
	sessions := make(map[string]struct{})
	sourceIDs := make([]string, 0, len(calls))
	paramKeys := make(map[string]int)

	for _, c := range calls {
		totalCount++
		if c.Status == "success" {
			successCount++
		}
		totalLatency += c.LatencyMs
		sessions[c.SessionID] = struct{}{}
		sourceIDs = append(sourceIDs, c.ID)

		if c.Timestamp.After(maxTimestamp) {
			maxTimestamp = c.Timestamp
		}

		if len(c.Params) > 0 {
			keys := extractParamKeys(c.Params)
			for _, k := range keys {
				paramKeys[k]++
			}
		}
	}

	errorRate := 0.0
	if totalCount > 0 {
		errorRate = float64(totalCount-successCount) / float64(totalCount)
	}

	avgLatency := 0.0
	if totalCount > 0 {
		avgLatency = float64(totalLatency) / float64(totalCount)
	}

	threshold := len(calls) / 2
	if threshold < 1 {
		threshold = 1
	}
	var commonKeys, optionalKeys []string
	for k, count := range paramKeys {
		if count >= threshold {
			commonKeys = append(commonKeys, k)
		} else {
			optionalKeys = append(optionalKeys, k)
		}
	}
	sort.Strings(commonKeys)
	sort.Strings(optionalKeys)

	pattern := ParamKeys{Keys: commonKeys, Optional: optionalKeys}
	patternJSON, _ := json.Marshal(pattern)

	sourceIDsJSON, _ := json.Marshal(sourceIDs)
	tagsJSON, _ := json.Marshal(buildTags(namespace, calls))

	var lastUsed *time.Time
	if !maxTimestamp.IsZero() {
		lastUsed = &maxTimestamp
	}

	r := &store.Recipe{
		ToolName:       toolName,
		Namespace:      namespace,
		Description:    buildDescription(toolName, totalCount, errorRate),
		ParamsPattern:  patternJSON,
		SuccessCount:   successCount,
		TotalCount:     totalCount,
		AvgLatencyMs:   avgLatency,
		ErrorRate:      errorRate,
		SessionCount:   len(sessions),
		LastUsedAt:     lastUsed,
		Tags:           tagsJSON,
		SourceAuditIDs: sourceIDsJSON,
	}

	r.Score = Rank(r, now)
	return r
}

func extractNamespace(toolName string) string {
	if idx := strings.Index(toolName, "__"); idx > 0 {
		return toolName[:idx]
	}
	return "other"
}

func extractParamKeys(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func buildTags(namespace string, calls []AuditCall) []string {
	tags := make([]string, 0, 4)
	if namespace != "" && namespace != "other" {
		tags = append(tags, "ns:"+namespace)
	}
	var hasErrors, hasSuccess bool
	for _, c := range calls {
		if c.Status == "error" || c.Status == "blocked" {
			hasErrors = true
		}
		if c.Status == "success" {
			hasSuccess = true
		}
	}
	if hasErrors {
		tags = append(tags, "has-errors")
	}
	if hasSuccess {
		tags = append(tags, "successful")
	}
	return tags
}

func buildDescription(toolName string, total int, errorRate float64) string {
	quality := "reliable"
	if errorRate > 0.1 {
		quality = "moderate"
	}
	if errorRate > 0.3 {
		quality = "unreliable"
	}
	return fmt.Sprintf("%s: %d calls, %s (error rate %.0f%%)",
		toolName, total, quality, errorRate*100)
}
