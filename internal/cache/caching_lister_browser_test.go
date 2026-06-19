package cache

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCachingLister_BrowserServerPassthrough(t *testing.T) {
	inner := &mockLister{result: json.RawMessage(`{"tabs":[]}`)}
	tc := NewToolCache(map[string]ServerCacheConfig{
		"agent_browser": DefaultServerCacheConfig(),
	})
	cl := NewCachingToolLister(inner, tc)

	ctx := context.Background()
	args := json.RawMessage(`{}`)

	r1, err := cl.CallWithMeta(ctx, "agent_browser", "", "browser_list_tabs", args, false)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := cl.CallWithMeta(ctx, "agent_browser", "", "browser_list_tabs", args, false)
	if err != nil {
		t.Fatal(err)
	}
	if r1.CacheHit || r2.CacheHit {
		t.Fatalf("browser calls should never report cache hits: r1=%v r2=%v", r1.CacheHit, r2.CacheHit)
	}
	if inner.callCount != 2 {
		t.Fatalf("callCount = %d; want 2", inner.callCount)
	}
}

func TestCachingLister_BrowserToolPassthroughButNormalToolCached(t *testing.T) {
	inner := &mockLister{result: json.RawMessage(`{"data":"ok"}`)}
	tc := NewToolCache(map[string]ServerCacheConfig{
		"myapp": DefaultServerCacheConfig(),
	})
	cl := NewCachingToolLister(inner, tc)

	ctx := context.Background()
	args := json.RawMessage(`{}`)

	cl.Call(ctx, "myapp", "auth1", "agent_browser__browser_list_tabs", args) //nolint:errcheck
	cl.Call(ctx, "myapp", "auth1", "agent_browser__browser_list_tabs", args) //nolint:errcheck
	cl.Call(ctx, "myapp", "auth1", "clickup__get_task", args)                //nolint:errcheck
	cl.Call(ctx, "myapp", "auth1", "clickup__get_task", args)                //nolint:errcheck

	if inner.callCount != 3 {
		t.Fatalf("callCount = %d; want 3", inner.callCount)
	}
}
