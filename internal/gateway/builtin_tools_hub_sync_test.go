package gateway

import "testing"

func TestHubSyncToolDefinitionsIncludeSearch(t *testing.T) {
	defs := hubSyncToolDefinitions()
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Name] = true
	}
	for _, want := range []string{
		"mesh__skill_hub_index",
		"mesh__skill_hub_search",
		"mesh__skill_hub_pull",
	} {
		if !names[want] {
			t.Fatalf("missing hub sync tool %s in %+v", want, names)
		}
	}
}
