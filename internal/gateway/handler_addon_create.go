package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/addon"
)

// AddonCreator writes a new addon YAML to disk and reloads the registry,
// then returns the path written and the new full tool names.
type AddonCreator interface {
	Create(ctx context.Context, spec addon.AddonSpec) (path string, newTools []string, err error)
}

// createAddonToolDefinition returns the built-in MCP tool that lets agents
// scaffold a new custom MCP addon at runtime. The new tools are immediately
// callable via mcpx__execute_code; a tools/list_changed notification is sent.
func createAddonToolDefinition() Tool {
	return Tool{
		Name: "mcpx__create_addon",
		Description: "Scaffold a new custom MCP addon from a high-level spec and " +
			"register it with the running gateway. Provide a name (becomes the " +
			"tool namespace), description, base_url, parent_server (existing " +
			"downstream server ID for auth), an auth block, and a list of REST " +
			"endpoints. The generated YAML is written to addons/<name>.yaml, " +
			"the registry is hot-reloaded, and a notifications/tools/list_changed " +
			"notification is sent. Returns the file path and the new tool names.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Addon namespace, e.g. 'weatherco'. Lowercase, alphanumeric + underscore."
				},
				"description": {
					"type": "string",
					"description": "Short description of the API the addon wraps."
				},
				"base_url": {
					"type": "string",
					"description": "Base URL of the API, e.g. https://api.weather.co/v1."
				},
				"parent_server": {
					"type": "string",
					"description": "Existing downstream server ID whose auth scope the addon inherits."
				},
				"auth_scope": {
					"type": "string",
					"description": "Optional named auth_scope override (defaults to the parent_server's route auth)."
				},
				"auth": {
					"type": "object",
					"properties": {
						"kind": {
							"type": "string",
							"enum": ["none", "bearer", "api_key_header", "api_key_query", "hawk", "oauth2", "oauth2_pending"],
							"description": "Use hawk for Hawk/HMAC Authorization headers. Use oauth2_pending when scaffolding from OpenAPI; the human will complete the OAuth wizard separately. oauth2 means the wizard has already produced a provider+scope you can reference."
						},
						"header_name": {"type": "string"},
						"query_name": {"type": "string"},
						"auth_url": {"type": "string", "description": "OAuth2 authorization endpoint (oauth2/oauth2_pending only)."},
						"token_url": {"type": "string", "description": "OAuth2 token endpoint (oauth2/oauth2_pending only)."},
						"scopes": {"type": "array", "items": {"type": "string"}},
						"client_id": {"type": "string"},
						"use_pkce": {"type": "boolean"},
						"grant_type": {"type": "string", "enum": ["authorization_code", "client_credentials"]}
					},
					"required": ["kind"]
				},
				"endpoints": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"name": {"type": "string"},
							"description": {"type": "string"},
							"method": {"type": "string", "enum": ["GET", "POST", "PUT", "PATCH", "DELETE"]},
							"path": {"type": "string"},
							"params": {
								"type": "array",
								"items": {
									"type": "object",
									"properties": {
										"name": {"type": "string"},
										"type": {"type": "string", "enum": ["string", "integer", "number", "boolean"]},
										"in": {"type": "string", "enum": ["path", "query", "body"]},
										"description": {"type": "string"},
										"required": {"type": "boolean"}
									},
									"required": ["name", "type", "in"]
								}
							}
						},
						"required": ["name", "description", "method", "path"]
					}
				}
			},
			"required": ["name", "description", "base_url", "parent_server", "auth", "endpoints"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Create Custom MCP Addon",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}

// handleCreateAddon parses the args into an addon.AddonSpec, calls the
// configured AddonCreator, and reports the result + sends list_changed.
func (h *handler) handleCreateAddon(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.addonCreator == nil {
		return marshalErrorResult(
			"Addon creation is not enabled — the daemon was started without an addons directory.",
		), nil
	}

	var spec addon.AddonSpec
	if err := json.Unmarshal(args, &spec); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if err := spec.Validate(); err != nil {
		return marshalErrorResult(fmt.Sprintf("Invalid spec: %s", err)), nil
	}

	path, newTools, err := h.addonCreator.Create(ctx, spec)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("create addon: %s", err)), nil
	}

	// Notify connected clients so their tool surface refreshes.
	h.sendToolsListChanged()

	// AI accessibility: oauth2_pending specs cannot dispatch tools yet —
	// the human must finish the Configure-OAuth wizard. Tell the agent
	// explicitly so it doesn't read the "Created" message and immediately
	// try to call a tool that 401s.
	if spec.Auth.Kind == addon.AuthOAuth2Pending {
		msg := fmt.Sprintf(
			"Created addon %q at %s with %d tool(s): %s.\n"+
				"HUMAN APPROVAL REQUIRED: this addon uses oauth2 and the wizard "+
				"has not been completed yet. Ask the user to open the MCPlexer "+
				"UI > Create Custom MCP > Configure OAuth step for %q before "+
				"calling any of these tools. Calls will fail with 401 until "+
				"authorization is finished.",
			spec.Name, path, len(newTools), strings.Join(newTools, ", "), spec.Name,
		)
		return marshalToolResult(msg), nil
	}

	msg := fmt.Sprintf(
		"Created addon %q at %s with %d tool(s): %s.\n"+
			"They are now available to mcpx__execute_code as %s.<tool>(args).",
		spec.Name, path, len(newTools), strings.Join(newTools, ", "), spec.Name,
	)
	return marshalToolResult(msg), nil
}

// SetAddonCreator wires an AddonCreator onto the running gateway. Called by
// the daemon during startup once the addons dir + DB-aware resolvers are
// available.
func (s *Server) SetAddonCreator(c AddonCreator) {
	if s == nil || s.handler == nil {
		return
	}
	s.handler.addonCreator = c
}
