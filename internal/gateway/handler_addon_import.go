package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
)

// importOpenAPIToolDefinition returns the built-in MCP tool that lets agents
// scaffold an addon from an OpenAPI 3.x spec in one shot. The produced
// AddonSpec is returned but NOT saved — the agent calls mcpx__create_addon
// (or the user reviews via the wizard) to register it.
func importOpenAPIToolDefinition() Tool {
	return Tool{
		Name: "mcpx__import_openapi",
		Description: "Import an OpenAPI 3.x spec and produce a draft AddonSpec ready " +
			"for review. Provide either spec_url (https URL to the JSON/YAML doc) or " +
			"spec_inline (the doc as a string). Returns the AddonSpec — pass it to " +
			"mcpx__create_addon after filling in parent_server. Auth schemes that " +
			"don't map (mTLS, OIDC, HTTP Basic) are reported as errors with " +
			"actionable suggestions.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"spec_url": {
					"type": "string",
					"description": "https URL to an OpenAPI 3.x JSON or YAML doc."
				},
				"spec_inline": {
					"type": "string",
					"description": "Inline OpenAPI 3.x JSON or YAML content."
				}
			}
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Import OpenAPI Spec",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

// handleImportOpenAPI fetches/loads the spec, runs the importer, and returns
// the AddonSpec as a JSON string in the tool result.
func (h *handler) handleImportOpenAPI(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	var req struct {
		SpecURL    string `json:"spec_url,omitempty"`
		SpecInline string `json:"spec_inline,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	data, err := fetchOpenAPIBytes(ctx, req.SpecURL, req.SpecInline)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	spec, err := addon.ImportOpenAPI(data)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("import openapi: %s", err)), nil
	}

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(out)), nil
}

// fetchOpenAPIBytes resolves spec_url or spec_inline to raw doc bytes, with
// the same 8 MiB cap and content-type accept header as the REST handler.
func fetchOpenAPIBytes(ctx context.Context, specURL, specInline string) ([]byte, error) {
	if specInline != "" {
		return []byte(specInline), nil
	}
	if specURL == "" {
		return nil, fmt.Errorf("either spec_url or spec_inline is required")
	}
	u, err := url.Parse(specURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("spec_url must be an http(s) URL")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch spec_url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch spec_url: status %s", resp.Status)
	}
	const maxBytes = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read spec body: %w", err)
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("spec_url body too large (>%d bytes)", maxBytes)
	}
	return body, nil
}
