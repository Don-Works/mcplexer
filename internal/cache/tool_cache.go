package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// ToolCallKey uniquely identifies a cached tool call response.
type ToolCallKey struct {
	ServerID    string
	AuthScopeID string
	ToolName    string
	ArgsHash    string
}

// ToolCache wraps a generic Cache for tool call responses, with
// pattern-based cacheability checks and mutation invalidation.
type ToolCache struct {
	cache    *Cache[ToolCallKey, json.RawMessage]
	configMu sync.RWMutex
	configs  map[string]ServerCacheConfig // keyed by server ID
}

// NewToolCache creates a tool cache with per-server configurations.
func NewToolCache(configs map[string]ServerCacheConfig) *ToolCache {
	maxEntries := 0
	for _, cfg := range configs {
		maxEntries += cfg.MaxEntries
	}
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	configCopy := make(map[string]ServerCacheConfig, len(configs))
	for serverID, cfg := range configs {
		configCopy[serverID] = cfg
	}
	return &ToolCache{
		cache:   New[ToolCallKey, json.RawMessage](maxEntries, 30*time.Minute),
		configs: configCopy,
	}
}

// GetConfig returns the cache config for a server, falling back to defaults.
func (tc *ToolCache) GetConfig(serverID string) ServerCacheConfig {
	tc.configMu.RLock()
	defer tc.configMu.RUnlock()
	if cfg, ok := tc.configs[serverID]; ok {
		return cfg
	}
	return DefaultServerCacheConfig()
}

// SetConfig updates the cache config for a server at runtime.
func (tc *ToolCache) SetConfig(serverID string, cfg ServerCacheConfig) {
	tc.configMu.Lock()
	defer tc.configMu.Unlock()
	tc.configs[serverID] = cfg
}

// RemoveConfig removes the per-server config, falling back to defaults.
func (tc *ToolCache) RemoveConfig(serverID string) {
	tc.configMu.Lock()
	defer tc.configMu.Unlock()
	delete(tc.configs, serverID)
}

// IsCacheable returns true if the tool call should be cached.
func (tc *ToolCache) IsCacheable(serverID, toolName string) bool {
	cfg := tc.GetConfig(serverID)
	if !cfg.Enabled {
		return false
	}
	if isBrowserAutomationServer(serverID) {
		return false
	}
	// Strip namespace prefix for pattern matching.
	bare := stripNamespace(toolName)
	// NoCacheablePatterns take precedence: if the tool matches any
	// exclusion pattern, it is never cached regardless of CacheablePatterns.
	if len(cfg.NoCacheablePatterns) > 0 && matchesAny(bare, cfg.NoCacheablePatterns) {
		return false
	}
	return matchesAny(bare, cfg.CacheablePatterns)
}

// IsMutation returns true if the tool call is a mutation that should
// trigger cache invalidation.
func (tc *ToolCache) IsMutation(serverID, toolName string) bool {
	cfg := tc.GetConfig(serverID)
	bare := stripNamespace(toolName)
	return matchesAny(bare, cfg.MutationPatterns)
}

// Get retrieves a cached tool call response.
func (tc *ToolCache) Get(key ToolCallKey) (json.RawMessage, bool) {
	return tc.cache.Get(key)
}

// GetWithAge retrieves a cached response and its age since caching.
func (tc *ToolCache) GetWithAge(key ToolCallKey) (json.RawMessage, time.Duration, bool) {
	return tc.cache.GetWithAge(key)
}

// Set stores a tool call response with the server's configured TTL.
// A ReadTTLSec of 0 means indefinite (no expiry); negative values use the default.
func (tc *ToolCache) Set(key ToolCallKey, value json.RawMessage) {
	cfg := tc.GetConfig(key.ServerID)
	ttl := tc.resolveTTL(cfg)
	tc.cache.SetWithTTL(key, value, ttl)
}

// GetOrLoad returns the cached response or calls loadFn, with singleflight.
func (tc *ToolCache) GetOrLoad(key ToolCallKey, loadFn func() (json.RawMessage, error)) (json.RawMessage, error) {
	return tc.cache.GetOrLoad(key, loadFn)
}

// resolveTTL converts a server's ReadTTLSec to a duration.
// 0 means indefinite (100 years), negative means use the 30-minute default.
func (tc *ToolCache) resolveTTL(cfg ServerCacheConfig) time.Duration {
	if cfg.ReadTTLSec == 0 {
		return 100 * 365 * 24 * time.Hour // indefinite
	}
	if cfg.ReadTTLSec < 0 {
		return 30 * time.Minute
	}
	return time.Duration(cfg.ReadTTLSec) * time.Second
}

// InvalidateForMutation removes cached entries related to a mutation.
func (tc *ToolCache) InvalidateForMutation(serverID, authScopeID, toolName string) {
	tc.cache.InvalidateFunc(func(k ToolCallKey) bool {
		return k.ServerID == serverID && k.AuthScopeID == authScopeID
	})
}

// InvalidateServer removes all cached entries for a specific server.
func (tc *ToolCache) InvalidateServer(serverID string) {
	tc.cache.InvalidateFunc(func(k ToolCallKey) bool {
		return k.ServerID == serverID
	})
}

// Flush removes all entries.
func (tc *ToolCache) Flush() {
	tc.cache.Flush()
}

// Stats returns cache performance metrics.
func (tc *ToolCache) Stats() Stats {
	return tc.cache.Stats()
}

// MakeKey creates a ToolCallKey from the call parameters.
func MakeKey(serverID, authScopeID, toolName string, args json.RawMessage) ToolCallKey {
	h := sha256.Sum256(args)
	return ToolCallKey{
		ServerID:    serverID,
		AuthScopeID: authScopeID,
		ToolName:    toolName,
		ArgsHash:    hex.EncodeToString(h[:8]),
	}
}

// stripNamespace removes the "namespace__" prefix from a tool name.
func stripNamespace(toolName string) string {
	if _, after, ok := strings.Cut(toolName, "__"); ok {
		return after
	}
	return toolName
}

func isBrowserAutomationServer(serverID string) bool {
	id := strings.ToLower(serverID)
	return id == "brw" ||
		strings.Contains(id, "brw-") ||
		strings.Contains(id, "brw_") ||
		strings.Contains(id, "browser") ||
		strings.Contains(id, "playwright") ||
		strings.Contains(id, "puppeteer")
}

// matchesAny checks if name matches any of the glob-like patterns.
// It tries the full name and each suffix after an underscore boundary,
// so server-prefixed tools like "clickup_get_task" also match "get_*".
// For patterns like "search_*", it also matches the bare word "search".
func matchesAny(name string, patterns []string) bool {
	for candidate := name; ; {
		for _, p := range patterns {
			if p == "*" {
				return true
			}
			if prefix, ok := strings.CutSuffix(p, "*"); ok {
				if strings.HasPrefix(candidate, prefix) {
					return true
				}
				// Also match the bare action word: "search" matches "search_*".
				trimmed := strings.TrimRight(prefix, "_")
				if candidate == trimmed {
					return true
				}
			} else if candidate == p {
				return true
			}
		}
		_, after, ok := strings.Cut(candidate, "_")
		if !ok {
			break
		}
		candidate = after
	}
	return false
}
