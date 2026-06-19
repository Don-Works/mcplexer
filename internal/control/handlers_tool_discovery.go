// handlers_tool_discovery.go (M0.7) — mcplexer__list_available_tools.
//
// Surfaces every tool currently advertised by every registered
// downstream MCP server, in the same projection /api/v1/tools serves.
// Lets an MCP-only admin discover names for tool_allowlist_json without
// dropping to HTTP.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/writeclass"
)

// availableTool is the JSON shape returned to MCP callers. Matches the
// HTTP /api/v1/tools row to keep both surfaces in lockstep.
type availableTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Namespace   string `json:"namespace"`
	WriteClass  bool   `json:"write_class"`
}

// handleListAvailableTools is the regular store-based handler entry.
// Accepts no input. Returns one row per (name, namespace, description,
// write_class).
func handleListAvailableTools(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	servers, err := s.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downstreams: %w", err)
	}
	out := make([]availableTool, 0)
	for _, srv := range servers {
		out = append(out, projectTools(srv.CapabilitiesCache, srv.ToolNamespace)...)
	}
	return jsonResult(out)
}

// projectTools decodes one server's CapabilitiesCache JSON into the
// API-row shape. Servers whose cache hasn't been populated (never
// discovered, or in-flight discovery) are silently skipped. Namespace
// prefixing matches what the HTTP /api/v1/tools handler does.
func projectTools(cache json.RawMessage, namespace string) []availableTool {
	if len(cache) == 0 {
		return nil
	}
	var payload struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(cache, &payload); err != nil {
		return nil
	}
	out := make([]availableTool, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		fullName := t.Name
		if namespace != "" && !strings.Contains(fullName, "__") {
			fullName = namespace + "__" + t.Name
		}
		out = append(out, availableTool{
			Name:        fullName,
			Description: t.Description,
			Namespace:   namespace,
			WriteClass:  writeclass.IsWriteClass(fullName),
		})
	}
	return out
}
