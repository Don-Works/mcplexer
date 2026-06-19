package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/netguard"
	"github.com/don-works/mcplexer/internal/oauth"
)

// addonHandler exposes addon authoring endpoints for the web UI.
//
//	POST /api/v1/addons/preview        — dry-run BuildAddonYAML, return the YAML.
//	POST /api/v1/addons                — write the YAML and hot-register it.
//	POST /api/v1/addons/import-openapi — produce an AddonSpec from an OpenAPI 3.x spec.
//	POST /api/v1/addons/preview-call   — issue one redacted live HTTP call
//	                                     against the spec (UI-only, never persisted).
//	POST /api/v1/addons/oauth-setup    — wizard: create OAuth provider + auth scope
//	                                     for an in-progress addon spec.
//
// All endpoints accept JSON bodies described by their handler types below.
type addonHandler struct {
	creator     *addon.Creator
	previewExec *addon.PreviewExecutor // optional; nil disables preview-call
	wizard      *oauth.Wizard          // optional; nil disables oauth-setup
	previewLim  *previewRateLimiter    // simple per-process limiter
	// httpClient is used to fetch spec_url for import-openapi; tests inject a stub.
	httpClient *http.Client
}

// importOpenAPIRequest is the JSON body for POST /addons/import-openapi.
type importOpenAPIRequest struct {
	SpecURL    string `json:"spec_url,omitempty"`
	SpecInline string `json:"spec_inline,omitempty"`
}

// importOpenAPI fetches/inlines an OpenAPI doc and returns an AddonSpec.
func (h *addonHandler) importOpenAPI(w http.ResponseWriter, r *http.Request) {
	var req importOpenAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	data, err := h.fetchSpec(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec, err := addon.ImportOpenAPI(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

// fetchSpec returns the raw bytes of the OpenAPI document, either from an
// inline string or by GETing the URL. Caps body reads at 8 MiB. Rejects
// URLs that resolve to private / link-local / loopback hosts so an
// authenticated UI request cannot be coerced into an SSRF probe of the
// daemon's own network (cloud metadata, internal services, etc.).
func (h *addonHandler) fetchSpec(ctx context.Context, req importOpenAPIRequest) ([]byte, error) {
	if req.SpecInline != "" {
		return []byte(req.SpecInline), nil
	}
	if req.SpecURL == "" {
		return nil, fmt.Errorf("either spec_url or spec_inline is required")
	}
	u, err := url.Parse(req.SpecURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("spec_url must be an http(s) URL")
	}
	if err := netguard.AssertPublicHost(ctx, u.Hostname()); err != nil {
		return nil, fmt.Errorf("fetch spec_url: %w", err)
	}
	client := h.httpClient
	if client == nil {
		client = netguard.NewPublicHTTPClient(15 * time.Second)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.SpecURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json, application/yaml, text/yaml")
	resp, err := client.Do(httpReq)
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

// preview validates the spec and returns the generated YAML without writing.
func (h *addonHandler) preview(w http.ResponseWriter, r *http.Request) {
	var spec addon.AddonSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid spec: "+err.Error())
		return
	}
	yamlText, err := addon.BuildAddonYAML(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"yaml": yamlText,
	})
}

// create writes the YAML and reloads the registry. When the spec carries
// auth.kind=oauth2_pending the response also tells the agent that human
// approval is required and points at the wizard's authorize URL (when
// the wizard has been wired up).
func (h *addonHandler) create(w http.ResponseWriter, r *http.Request) {
	if h.creator == nil {
		writeError(w, http.StatusServiceUnavailable, "addon creator not configured")
		return
	}
	var spec addon.AddonSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid spec: "+err.Error())
		return
	}
	path, tools, err := h.creator.Create(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := map[string]any{
		"name":  spec.Name,
		"path":  path,
		"tools": tools,
	}
	if spec.Auth.Kind == addon.AuthOAuth2Pending {
		resp["human_approval_required"] = true
		resp["message"] = "OAuth credentials still need human approval. Open the Configure-OAuth wizard at /create-mcp?addon=" + spec.Name + " to finish."
	}
	writeJSON(w, http.StatusCreated, resp)
}

// previewCall executes one ephemeral HTTP request from the in-progress
// AddonSpec and returns a redacted view of request + response. Nothing is
// persisted. Sensitive headers + JSON body keys are redacted before the
// payload leaves the server. Per-process rate-limited.
func (h *addonHandler) previewCall(w http.ResponseWriter, r *http.Request) {
	if h.previewExec == nil {
		writeError(w, http.StatusServiceUnavailable, "preview executor not configured")
		return
	}
	if h.previewLim != nil && !h.previewLim.allow(r.RemoteAddr) {
		writeError(w, http.StatusTooManyRequests, "preview rate limit exceeded")
		return
	}
	var req addon.PreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid preview request: "+err.Error())
		return
	}
	resp, err := h.previewExec.Run(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// oauthSetup runs the addon OAuth wizard: creates the provider + auth scope
// rows that the addon's parent_server will inherit. Returns the structured
// result (incl. authorize URL for human-in-the-loop authorization_code).
func (h *addonHandler) oauthSetup(w http.ResponseWriter, r *http.Request) {
	if h.wizard == nil {
		writeError(w, http.StatusServiceUnavailable, "oauth wizard not configured")
		return
	}
	var spec oauth.WizardSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid wizard spec: "+err.Error())
		return
	}
	res, err := h.wizard.Run(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
