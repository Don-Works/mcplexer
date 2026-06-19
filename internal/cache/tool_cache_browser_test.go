package cache

import "testing"

func TestIsCacheable_BrowserToolsNotCachedByDefault(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"agent_browser": DefaultServerCacheConfig(),
		"browser":       DefaultServerCacheConfig(),
		"playwright":    DefaultServerCacheConfig(),
		"puppeteer":     DefaultServerCacheConfig(),
	})

	tests := []struct {
		name     string
		serverID string
		tool     string
		want     bool
	}{
		{"agent_browser list tabs", "agent_browser", "agent_browser__browser_list_tabs", false},
		{"agent_browser navigate", "agent_browser", "agent_browser__browser_navigate", false},
		{"agent_browser screenshot", "agent_browser", "agent_browser__browser_screenshot", false},
		{"browser list tabs", "browser", "browser__browser_list_tabs", false},
		{"playwright list tabs", "playwright", "playwright__browser_list_tabs", false},
		{"puppeteer navigate", "puppeteer", "puppeteer__browser_navigate", false},
		{"normal get on browser server", "agent_browser", "agent_browser__get_status", false},
		{"clickup list tasks", "clickup", "clickup__list_tasks", true},
		{"clickup get task", "clickup", "clickup__get_task", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tc.IsCacheable(tt.serverID, tt.tool)
			if got != tt.want {
				t.Errorf("IsCacheable(%q, %q) = %v; want %v",
					tt.serverID, tt.tool, got, tt.want)
			}
		})
	}
}

func TestIsCacheable_NoCacheablePatternsOverride(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"s1": {
			Enabled:             true,
			CacheablePatterns:   DefaultCacheablePatterns,
			NoCacheablePatterns: nil,
		},
	})
	if !tc.IsCacheable("s1", "s1__list_tabs") {
		t.Fatal("list_tabs should be cacheable when NoCacheablePatterns is empty")
	}

	tc.SetConfig("s1", ServerCacheConfig{
		Enabled:             true,
		CacheablePatterns:   DefaultCacheablePatterns,
		NoCacheablePatterns: []string{"list_tabs"},
	})
	if tc.IsCacheable("s1", "s1__list_tabs") {
		t.Fatal("list_tabs should not be cacheable when excluded")
	}
	if !tc.IsCacheable("s1", "s1__list_tasks") {
		t.Fatal("list_tasks should still be cacheable")
	}
}

func TestParseServerCacheConfigMergesDefaults(t *testing.T) {
	cfg, err := ParseServerCacheConfig([]byte(`{"read_ttl_sec":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatal("missing enabled should inherit the default true value")
	}
	if cfg.ReadTTLSec != 5 {
		t.Fatalf("ReadTTLSec = %d; want 5", cfg.ReadTTLSec)
	}
	if !matchesAny("get_task", cfg.CacheablePatterns) {
		t.Fatal("missing cacheable_patterns should inherit defaults")
	}
	if !matchesAny("browser_list_tabs", cfg.NoCacheablePatterns) {
		t.Fatal("missing no_cacheable_patterns should inherit browser defaults")
	}

	cfg, err = ParseServerCacheConfig([]byte(`{"enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("explicit enabled:false should be preserved")
	}
	if !matchesAny("get_task", cfg.CacheablePatterns) {
		t.Fatal("explicit enabled:false should not erase default patterns")
	}
}

func TestRemoveConfig_FallbackToDefaults(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"custom": {
			Enabled:             true,
			CacheablePatterns:   []string{"custom_only"},
			NoCacheablePatterns: nil,
		},
	})
	if !tc.IsCacheable("custom", "custom_only") {
		t.Fatal("custom_only should be cacheable before removal")
	}

	tc.RemoveConfig("custom")

	if !tc.IsCacheable("custom", "custom__get_task") {
		t.Fatal("get_task should be cacheable after removal")
	}
	if tc.IsCacheable("custom", "custom__create_task") {
		t.Fatal("create_task should not be cacheable after removal")
	}
}

func TestInvalidateServer(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"s1": DefaultServerCacheConfig(),
		"s2": DefaultServerCacheConfig(),
	})

	key1 := MakeKey("s1", "auth1", "get_task", []byte(`{}`))
	key2 := MakeKey("s2", "auth1", "get_task", []byte(`{}`))

	tc.Set(key1, []byte(`"result1"`))
	tc.Set(key2, []byte(`"result2"`))

	tc.InvalidateServer("s1")

	if _, ok := tc.Get(key1); ok {
		t.Error("expected s1 entries to be invalidated")
	}
	if _, ok := tc.Get(key2); !ok {
		t.Error("expected s2 entries to survive")
	}
}
