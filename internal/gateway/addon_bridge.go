package gateway

import (
	"encoding/json"

	"github.com/don-works/mcplexer/internal/addon"
)

// addonToolDefinitions converts addon registry entries into MCP Tool definitions.
func addonToolDefinitions(reg *addon.Registry) []Tool {
	all := reg.AllTools()
	tools := make([]Tool, 0, len(all))
	for _, rt := range all {
		t := Tool{
			Name:        rt.FullName,
			Description: rt.Description,
		}

		// Convert input_schema map to JSON.
		if rt.InputSchema != nil {
			data, err := json.Marshal(rt.InputSchema)
			if err == nil {
				t.InputSchema = data
			}
		}

		// Convert annotations to MCP format.
		if rt.Annotations != nil {
			t.Extras = withAnnotations(ToolAnnotations{
				ReadOnlyHint:    rt.Annotations.ReadOnlyHint,
				DestructiveHint: rt.Annotations.DestructiveHint,
				IdempotentHint:  rt.Annotations.IdempotentHint,
			})
		}

		tools = append(tools, t)
	}
	return tools
}
