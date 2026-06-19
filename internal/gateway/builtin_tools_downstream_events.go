package gateway

import "encoding/json"

func downstreamEventToolDefinitions() []Tool {
	readOnly := boolPtr(true)
	return []Tool{
		{
			Name: "mcpx__downstream_events_since",
			Description: "Read the bounded downstream notification journal for one MCP server instance. " +
				"Returns events with seq > since_seq. Use after synchronous browser/downstream tool calls " +
				"to collect progress/logging/list-change notifications without live-streaming tool results.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"server_id": {"type":"string","description":"Downstream server id, e.g. playwright."},
					"auth_scope_id": {"type":"string","description":"Optional auth scope when the route uses one."},
					"since_seq": {"type":"integer","description":"Return events with seq greater than this. Default 0."},
					"limit": {"type":"integer","description":"Max events to return. Default 50, max 1024."},
					"methods": {
						"type":"array",
						"items":{"type":"string"},
						"description":"Optional notification method filter, e.g. notifications/progress."
					}
				},
				"required": ["server_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:          "Downstream Events Since",
				ReadOnlyHint:   readOnly,
				IdempotentHint: readOnly,
			}),
		},
		{
			Name: "mcpx__downstream_events_wait",
			Description: "Block until the downstream notification journal has matching new events after since_seq, " +
				"or until timeout_seconds elapses. Timeout is reported as timed_out:true, not an error.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"server_id": {"type":"string","description":"Downstream server id."},
					"auth_scope_id": {"type":"string","description":"Optional auth scope when the route uses one."},
					"since_seq": {"type":"integer","description":"Wake when seq is greater than this. Default 0."},
					"timeout_seconds": {"type":"integer","description":"Max seconds to wait. Default 25, max 3600."},
					"limit": {"type":"integer","description":"Max events to return once woken. Default 50."},
					"methods": {
						"type":"array",
						"items":{"type":"string"},
						"description":"Optional notification method filter."
					}
				},
				"required": ["server_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:          "Downstream Events Wait",
				ReadOnlyHint:   readOnly,
				IdempotentHint: readOnly,
			}),
		},
		{
			Name: "mcpx__downstream_events_batch",
			Description: "Fetch since-deltas for multiple downstream instance journals in one call. " +
				"Use when driving several browser/downstream surfaces and polling notification deltas together.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"streams": {
						"type":"array",
						"items":{
							"type":"object",
							"properties":{
								"server_id":{"type":"string"},
								"auth_scope_id":{"type":"string"},
								"since_seq":{"type":"integer"}
							},
							"required":["server_id"]
						},
						"description":"Journal streams to read."
					},
					"limit": {"type":"integer","description":"Per-stream event cap. Default 50."},
					"methods": {
						"type":"array",
						"items":{"type":"string"},
						"description":"Optional notification method filter applied to every stream."
					}
				},
				"required": ["streams"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:          "Downstream Events Batch",
				ReadOnlyHint:   readOnly,
				IdempotentHint: readOnly,
			}),
		},
	}
}
