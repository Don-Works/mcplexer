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

func TestDownstreamRuntimeChanged(t *testing.T) {
	urlA := "http://example.test/a"
	urlB := "http://example.test/b"
	base := &store.DownstreamServer{
		ID:             "srv",
		Transport:      "stdio",
		Command:        "old",
		Args:           json.RawMessage(`["--old"]`),
		URL:            &urlA,
		IdleTimeoutSec: 30,
		CallTimeoutSec: 60,
		MaxInstances:   1,
		RestartPolicy:  "on-failure",
	}
	same := *base
	if downstreamRuntimeChanged(base, &same) {
		t.Fatal("identical runtime should not be marked changed")
	}

	renamed := *base
	renamed.Name = "new display name"
	if downstreamRuntimeChanged(base, &renamed) {
		t.Fatal("display-only change should not be marked runtime changed")
	}

	cases := map[string]func(*store.DownstreamServer){
		"transport":        func(ds *store.DownstreamServer) { ds.Transport = "http" },
		"command":          func(ds *store.DownstreamServer) { ds.Command = "new" },
		"args":             func(ds *store.DownstreamServer) { ds.Args = json.RawMessage(`["--new"]`) },
		"url":              func(ds *store.DownstreamServer) { ds.URL = &urlB },
		"idle_timeout":     func(ds *store.DownstreamServer) { ds.IdleTimeoutSec = 31 },
		"call_timeout":     func(ds *store.DownstreamServer) { ds.CallTimeoutSec = 61 },
		"max_instances":    func(ds *store.DownstreamServer) { ds.MaxInstances = 2 },
		"restart_policy":   func(ds *store.DownstreamServer) { ds.RestartPolicy = "always" },
		"disabled":         func(ds *store.DownstreamServer) { ds.Disabled = true },
		"url_nil_to_value": func(ds *store.DownstreamServer) { ds.URL = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := *base
			mutate(&changed)
			if !downstreamRuntimeChanged(base, &changed) {
				t.Fatal("runtime change was not detected")
			}
		})
	}
}
