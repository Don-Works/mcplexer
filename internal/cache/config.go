package cache

import (
	"encoding/json"
	"strings"
)

// ServerCacheConfig holds per-downstream-server caching configuration.
type ServerCacheConfig struct {
	Enabled             bool               `json:"enabled"`
	ReadTTLSec          int                `json:"read_ttl_sec"`
	CacheablePatterns   []string           `json:"cacheable_patterns"`
	NoCacheablePatterns []string           `json:"no_cacheable_patterns,omitempty"`
	MutationPatterns    []string           `json:"mutation_patterns"`
	InvalidationRules   []InvalidationRule `json:"invalidation_rules,omitempty"`
	MaxEntries          int                `json:"max_entries"`
}

// InvalidationRule defines a targeted cache invalidation: when a mutation
// matching MutationPattern is called, entries matching InvalidatePattern
// for the same server+scope are evicted.
type InvalidationRule struct {
	MutationPattern   string `json:"mutation_pattern"`
	InvalidatePattern string `json:"invalidate_pattern"`
}

// DefaultCacheablePatterns are tool name prefixes that indicate read operations.
var DefaultCacheablePatterns = []string{
	"get_*", "list_*", "search_*", "read_*", "fetch_*", "query_*", "find_*",
}

// DefaultMutationPatterns are tool name prefixes that indicate write operations.
var DefaultMutationPatterns = []string{
	"create_*", "update_*", "delete_*", "send_*", "post_*",
	"put_*", "set_*", "add_*", "remove_*",
}

// DefaultNoCacheablePatterns are tool name prefixes that must never be cached
// even if they also match a cacheable pattern. Browser automation and
// stateful session tools return ephemeral state that changes between calls.
var DefaultNoCacheablePatterns = []string{
	"brw_*", "browser_*", "agent_browser_*", "playwright_*", "puppeteer_*",
	"navigate_*", "screenshot_*",
}

// DefaultServerCacheConfig returns the default caching config for a server.
func DefaultServerCacheConfig() ServerCacheConfig {
	return ServerCacheConfig{
		Enabled:             true,
		ReadTTLSec:          1800,
		CacheablePatterns:   append([]string(nil), DefaultCacheablePatterns...),
		NoCacheablePatterns: append([]string(nil), DefaultNoCacheablePatterns...),
		MutationPatterns:    append([]string(nil), DefaultMutationPatterns...),
		MaxEntries:          1000,
	}
}

// ParseServerCacheConfig overlays JSON onto the default cache config.
// Missing fields inherit defaults; explicit values such as enabled:false,
// read_ttl_sec:0, or no_cacheable_patterns:[] are preserved.
func ParseServerCacheConfig(raw json.RawMessage) (ServerCacheConfig, error) {
	cfg := DefaultServerCacheConfig()
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ServerCacheConfig{}, err
	}
	return cfg, nil
}
