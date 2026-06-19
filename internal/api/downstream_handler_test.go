package api

import (
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/store"
)

func TestDownstreamHandlerApplyCacheConfigHotAppliesAndInvalidates(t *testing.T) {
	tc := cache.NewToolCache(map[string]cache.ServerCacheConfig{
		"srv": cache.DefaultServerCacheConfig(),
	})
	h := &downstreamHandler{toolCache: tc}

	key := cache.MakeKey("srv", "auth", "get_task", json.RawMessage(`{}`))
	tc.Set(key, json.RawMessage(`{"cached":true}`))
	if _, ok := tc.Get(key); !ok {
		t.Fatal("expected cache entry before hot apply")
	}
	if !tc.IsCacheable("srv", "get_task") {
		t.Fatal("get_task should be cacheable before hot apply")
	}

	h.applyCacheConfig(&store.DownstreamServer{
		ID:          "srv",
		CacheConfig: json.RawMessage(`{"enabled":false}`),
	})
	if tc.IsCacheable("srv", "get_task") {
		t.Fatal("get_task should not be cacheable after enabled:false")
	}
	if _, ok := tc.Get(key); ok {
		t.Fatal("server cache entries should be invalidated after hot apply")
	}

	h.applyCacheConfig(&store.DownstreamServer{
		ID:          "srv",
		CacheConfig: json.RawMessage(`{"read_ttl_sec":5}`),
	})
	cfg := tc.GetConfig("srv")
	if !cfg.Enabled {
		t.Fatal("partial cache_config should inherit enabled:true")
	}
	if cfg.ReadTTLSec != 5 {
		t.Fatalf("ReadTTLSec = %d; want 5", cfg.ReadTTLSec)
	}
	if !tc.IsCacheable("srv", "get_task") {
		t.Fatal("partial cache_config should inherit default cacheable patterns")
	}
}
