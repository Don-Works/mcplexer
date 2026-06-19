package cache

import (
	"testing"
)

func TestIsCacheable(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"s1": DefaultServerCacheConfig(),
		"s2": {Enabled: false},
	})

	tests := []struct {
		name     string
		serverID string
		tool     string
		want     bool
	}{
		{"get_ prefix", "s1", "clickup__get_workspace", true},
		{"list_ prefix", "s1", "clickup__list_tasks", true},
		{"search_ prefix", "s1", "clickup__search_tasks", true},
		{"read_ prefix", "s1", "github__read_file", true},
		{"fetch_ prefix", "s1", "api__fetch_data", true},
		{"query_ prefix", "s1", "db__query_records", true},
		{"find_ prefix", "s1", "app__find_user", true},
		{"create is not cacheable", "s1", "clickup__create_task", false},
		{"update is not cacheable", "s1", "clickup__update_task", false},
		{"random tool not cacheable", "s1", "clickup__do_something", false},
		{"disabled server", "s2", "clickup__get_workspace", false},
		{"unknown server uses defaults", "s3", "api__get_data", true},
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

func TestIsMutation(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"s1": DefaultServerCacheConfig(),
	})

	tests := []struct {
		name     string
		serverID string
		tool     string
		want     bool
	}{
		{"create_", "s1", "clickup__create_task", true},
		{"update_", "s1", "clickup__update_task", true},
		{"delete_", "s1", "clickup__delete_task", true},
		{"send_", "s1", "slack__send_message", true},
		{"post_", "s1", "api__post_data", true},
		{"put_", "s1", "api__put_data", true},
		{"set_", "s1", "config__set_value", true},
		{"add_", "s1", "clickup__add_tag_to_task", true},
		{"remove_", "s1", "clickup__remove_tag", true},
		{"get_ is not mutation", "s1", "clickup__get_task", false},
		{"list_ is not mutation", "s1", "clickup__list_tasks", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tc.IsMutation(tt.serverID, tt.tool)
			if got != tt.want {
				t.Errorf("IsMutation(%q, %q) = %v; want %v",
					tt.serverID, tt.tool, got, tt.want)
			}
		})
	}
}

func TestStripNamespace(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"clickup__get_task", "get_task"},
		{"github__create_issue", "create_issue"},
		{"plain_tool", "plain_tool"},
		{"a__b__c", "b__c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripNamespace(tt.input)
			if got != tt.want {
				t.Errorf("stripNamespace(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchesAny(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		patterns []string
		want     bool
	}{
		{"wildcard", "anything", []string{"*"}, true},
		{"prefix match", "get_workspace", []string{"get_*"}, true},
		{"exact match", "custom_tool", []string{"custom_tool"}, true},
		{"no match", "something", []string{"get_*", "list_*"}, false},
		{"multiple patterns", "list_tasks", []string{"get_*", "list_*"}, true},
		{"server-prefixed get", "clickup_get_task", []string{"get_*"}, true},
		{"server-prefixed search", "clickup_search", []string{"search_*"}, true},
		{"server-prefixed list", "github_list_repos", []string{"list_*"}, true},
		{"server-prefixed create", "clickup_create_task", []string{"create_*"}, true},
		{"deeply prefixed", "my_svc_get_item", []string{"get_*"}, true},
		{"server-prefixed no match", "clickup_do_thing", []string{"get_*", "list_*"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAny(tt.tool, tt.patterns)
			if got != tt.want {
				t.Errorf("matchesAny(%q, %v) = %v; want %v",
					tt.tool, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestMakeKey(t *testing.T) {
	k1 := MakeKey("s1", "auth1", "get_task", []byte(`{"id":"123"}`))
	k2 := MakeKey("s1", "auth1", "get_task", []byte(`{"id":"123"}`))
	k3 := MakeKey("s1", "auth1", "get_task", []byte(`{"id":"456"}`))

	if k1 != k2 {
		t.Error("same args should produce same key")
	}
	if k1 == k3 {
		t.Error("different args should produce different keys")
	}
}

func TestInvalidateForMutation(t *testing.T) {
	tc := NewToolCache(map[string]ServerCacheConfig{
		"s1": DefaultServerCacheConfig(),
	})

	key1 := MakeKey("s1", "auth1", "get_task", []byte(`{}`))
	key2 := MakeKey("s1", "auth1", "list_tasks", []byte(`{}`))
	key3 := MakeKey("s2", "auth1", "get_task", []byte(`{}`))

	tc.Set(key1, []byte(`"result1"`))
	tc.Set(key2, []byte(`"result2"`))
	tc.Set(key3, []byte(`"result3"`))

	// Mutation on s1/auth1 should invalidate key1 and key2 but not key3.
	tc.InvalidateForMutation("s1", "auth1", "create_task")

	if _, ok := tc.Get(key1); ok {
		t.Error("expected key1 to be invalidated")
	}
	if _, ok := tc.Get(key2); ok {
		t.Error("expected key2 to be invalidated")
	}
	if _, ok := tc.Get(key3); !ok {
		t.Error("expected key3 to survive (different server)")
	}
}
