package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// validAPINameRE matches the namespace constraint enforced on the api_name
// argument; mirrors addon.nameRE so generated YAML passes downstream validation.
var validAPINameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// provisionMCPToolDefinition is the high-level "self-serve a new MCP server"
// tool. The agent calls it once with a spec URL or inline endpoints; the
// orchestrator handles spec import, secret capture (via the human, never
// shown to the agent), parent_server + auth_scope creation, addon YAML write,
// and gateway hot-reload. The new tools become callable in the same session
// via mcpx__execute_code as `<api_name>.<tool>(args)`.
func provisionMCPToolDefinition() Tool {
	return Tool{
		Name: "mcpx__provision_mcp",
		Description: "Self-serve a new MCP tool surface for an external API in one shot. " +
			"Provide either spec_url (an OpenAPI 3.x JSON/YAML doc URL) or an " +
			"inline endpoints array. The orchestrator imports the spec, prompts " +
			"the human for any required API token (you never see the value — it " +
			"is captured into an encrypted auth scope), provisions a parent " +
			"server + route rule, writes the addon YAML, and hot-reloads the " +
			"gateway. The new tools are callable from mcpx__execute_code as " +
			"`<api_name>.<tool>(args)` in the same session. Re-running with the " +
			"same api_name updates the spec and (if auth.rotate=true) re-prompts " +
			"for a fresh token. For OAuth2-protected APIs, the tool returns " +
			"early with a wizard URL — that flow needs human UI interaction " +
			"before the addon can authorize.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"api_name": {
					"type": "string",
					"description": "Lowercase namespace for the new addon, e.g. \"linear\". Becomes the tool prefix: linear__list_issues, linear__create_issue, ..."
				},
				"description": {
					"type": "string",
					"description": "Short human-readable description of the API."
				},
				"spec_url": {
					"type": "string",
					"description": "Optional https URL to an OpenAPI 3.x JSON or YAML doc. When set, endpoints are extracted from the spec."
				},
				"spec_inline": {
					"type": "string",
					"description": "Optional inline OpenAPI 3.x JSON or YAML string. Use when the spec is small enough to embed."
				},
				"base_url": {
					"type": "string",
					"description": "Override base URL (otherwise taken from the OpenAPI servers[0].url). Required when neither spec_url nor spec_inline is provided."
				},
				"auth": {
					"type": "object",
					"description": "Auth configuration. If omitted, the auth scheme is derived from the OpenAPI spec.",
					"properties": {
						"kind": {
							"type": "string",
							"enum": ["none", "bearer", "api_key_header", "hawk"],
							"description": "Bearer token, API key in a header, Hawk/HMAC Authorization, or no auth. OAuth2 is detected from the spec and returns a wizard URL — it cannot be self-served by the agent."
						},
						"header_name": {
							"type": "string",
							"description": "Header name for api_key_header (e.g. \"X-Api-Key\"). Required when kind=api_key_header."
						},
						"rotate": {
							"type": "boolean",
							"description": "If true and the auth scope already has a value, re-prompt the human for a fresh credential."
						}
					}
				},
				"secret_label": {
					"type": "string",
					"description": "Short label shown in the human secret prompt (e.g. \"Linear API token\"). Defaults to <api_name> + \" credential\"."
				},
				"secret_reason": {
					"type": "string",
					"description": "Justification shown to the human in the secret prompt UI. Be specific about why the agent needs this credential."
				},
				"endpoints": {
					"type": "array",
					"description": "Optional inline endpoint list, used when no OpenAPI spec is provided. Same shape as mcpx__create_addon endpoints.",
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
			"required": ["api_name"]
		}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "Provision MCP for API",
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(false),
			OpenWorldHint:   boolPtr(true),
		}),
	}
}

// provisionMCPArgs is the parsed input to handleProvisionMCP.
type provisionMCPArgs struct {
	APIName      string                 `json:"api_name"`
	Description  string                 `json:"description,omitempty"`
	SpecURL      string                 `json:"spec_url,omitempty"`
	SpecInline   string                 `json:"spec_inline,omitempty"`
	BaseURL      string                 `json:"base_url,omitempty"`
	Auth         *provisionAuthOverride `json:"auth,omitempty"`
	SecretLabel  string                 `json:"secret_label,omitempty"`
	SecretReason string                 `json:"secret_reason,omitempty"`
	Endpoints    []addon.EndpointSpec   `json:"endpoints,omitempty"`
}

type provisionAuthOverride struct {
	Kind       string `json:"kind"`
	HeaderName string `json:"header_name,omitempty"`
	Rotate     bool   `json:"rotate,omitempty"`
}

// handleProvisionMCP runs the orchestrator. It is intentionally chatty in its
// return string so the agent can see exactly which steps happened — especially
// the one human-in-the-loop step (secret prompt) which they cannot observe
// directly.
func (h *handler) handleProvisionMCP(
	ctx context.Context, rawArgs json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.addonCreator == nil {
		return marshalErrorResult(
			"Addon provisioning is not enabled — the daemon was started without an addons directory.",
		), nil
	}
	if h.secretPrompts == nil {
		return marshalErrorResult(
			"Provisioning requires the secret-prompt subsystem, which is disabled on this instance.",
		), nil
	}
	if h.secretsManager == nil {
		return marshalErrorResult(
			"Provisioning requires the secrets manager (encrypted auth-scope storage), which is not configured.",
		), nil
	}

	var args provisionMCPArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	args.APIName = strings.TrimSpace(args.APIName)
	v := newValidator()
	if !validAPINameRE.MatchString(args.APIName) {
		if args.APIName == "" {
			v.requireStringWithHint("api_name", "",
				"lowercase letter then alphanumeric/underscore, 2-63 chars (e.g. \"linear\", \"weatherco\")")
		} else {
			v.addFieldErr("invalid_value", "api_name", args.APIName,
				"api_name must match ^[a-z][a-z0-9_]{1,62}$",
				"lowercase letter then alphanumeric/underscore, 2-63 chars (e.g. \"linear\", \"weatherco\")")
		}
	}
	// spec source: at least one of spec_url, spec_inline, or endpoints
	// must be present — flag it here so the user sees all the missing
	// fields together rather than failing in buildProvisionSpec.
	if args.SpecURL == "" && args.SpecInline == "" && len(args.Endpoints) == 0 {
		v.addFieldErr("required_field_missing", "spec_url|spec_inline|endpoints", "",
			"provide one of spec_url, spec_inline, or endpoints (with base_url)",
			"OpenAPI URL is easiest; inline OpenAPI works for small specs; explicit endpoints are for ad-hoc APIs")
	}
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	spec, err := h.buildProvisionSpec(ctx, args)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}

	// OAuth2 cannot be self-served by the agent — bail out clearly.
	if spec.Auth.Kind == addon.AuthOAuth2 || spec.Auth.Kind == addon.AuthOAuth2Pending {
		return marshalToolResult(fmt.Sprintf(
			"Cannot self-provision %q: the API requires OAuth2 (auth_url=%s, token_url=%s, scopes=%s). "+
				"This flow needs an interactive human UI step. "+
				"Ask the user to open the MCPlexer UI > Create Custom MCP > Configure OAuth wizard "+
				"and complete it; the addon can then be created with mcpx__create_addon.",
			args.APIName, spec.Auth.AuthURL, spec.Auth.TokenURL,
			strings.Join(spec.Auth.Scopes, ","),
		)), nil
	}

	// Provision the parent server, auth scope, and route rule (idempotent).
	provInfo, err := h.ensureProvisionInfra(ctx, args.APIName, spec.Description, spec.Auth.Kind)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("provision infrastructure: %s", err)), nil
	}
	spec.ParentServer = provInfo.ServerID
	spec.AuthScope = provInfo.AuthScopeName

	// If auth requires a credential, capture it from the human and persist it
	// into the auth scope's encrypted store. The agent never sees the value.
	authSummary := "auth=none"
	if spec.Auth.Kind == addon.AuthBearer || spec.Auth.Kind == addon.AuthAPIKeyHeader || spec.Auth.Kind == addon.AuthHawk {
		needPrompt, err := h.shouldPromptForSecret(ctx, provInfo.AuthScopeID, spec.Auth, args.Auth)
		if err != nil {
			return marshalErrorResult(fmt.Sprintf("inspect existing credential: %s", err)), nil
		}
		if needPrompt {
			label := args.SecretLabel
			if label == "" {
				label = args.APIName + " credential"
				if spec.Auth.Kind == addon.AuthHawk {
					label = args.APIName + " Hawk credentials"
				}
			}
			reason := args.SecretReason
			if reason == "" {
				reason = fmt.Sprintf("Set up %s API access for an MCP addon", args.APIName)
				if spec.Auth.Kind == addon.AuthHawk {
					reason += `. Paste JSON {"id":"...","key":"..."} or two lines: id then key.`
				}
			}
			if err := h.captureSecretIntoAuthScope(
				ctx, provInfo.AuthScopeID, spec.Auth, label, reason,
			); err != nil {
				return marshalErrorResult(err.Error()), nil
			}
			authSummary = fmt.Sprintf("auth=%s (captured from human, stored encrypted)", spec.Auth.Kind)
		} else {
			authSummary = fmt.Sprintf("auth=%s (reused existing credential)", spec.Auth.Kind)
		}
	}

	// Create the addon YAML and hot-reload the registry.
	path, newTools, err := h.addonCreator.Create(ctx, *spec)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("create addon: %s", err)), nil
	}

	// Notify clients so the tool surface refreshes mid-session.
	h.sendToolsListChanged()

	msg := fmt.Sprintf(
		"Provisioned %q with %d tool(s) (%s).\n"+
			"YAML: %s\n"+
			"Tools: %s\n"+
			"They are now callable via mcpx__execute_code as %s.<tool>(args).",
		args.APIName, len(newTools), authSummary, path,
		strings.Join(newTools, ", "), args.APIName,
	)
	return marshalToolResult(msg), nil
}

// buildProvisionSpec resolves the AddonSpec from spec_url, spec_inline, or
// inline endpoints. Overrides from args.Auth and args.BaseURL are applied
// after spec import.
func (h *handler) buildProvisionSpec(
	ctx context.Context, args provisionMCPArgs,
) (*addon.AddonSpec, error) {
	var spec *addon.AddonSpec

	switch {
	case args.SpecURL != "" || args.SpecInline != "":
		data, err := fetchOpenAPIBytes(ctx, args.SpecURL, args.SpecInline)
		if err != nil {
			return nil, err
		}
		s, err := addon.ImportOpenAPI(data)
		if err != nil {
			return nil, fmt.Errorf("import openapi: %w", err)
		}
		spec = s
	case len(args.Endpoints) > 0:
		if args.BaseURL == "" {
			return nil, errors.New("base_url is required when no OpenAPI spec is provided")
		}
		spec = &addon.AddonSpec{
			Description: args.Description,
			BaseURL:     args.BaseURL,
			Auth:        addon.AuthSpec{Kind: addon.AuthNone},
			Endpoints:   args.Endpoints,
		}
	default:
		return nil, errors.New("provide spec_url, spec_inline, or endpoints (with base_url)")
	}

	// Override spec name + description with the agent's preferred values.
	spec.Name = args.APIName
	if args.Description != "" {
		spec.Description = args.Description
	}
	if spec.Description == "" {
		spec.Description = args.APIName + " API"
	}
	if args.BaseURL != "" {
		spec.BaseURL = strings.TrimRight(args.BaseURL, "/")
	}

	// Apply auth override.
	if args.Auth != nil && args.Auth.Kind != "" {
		switch args.Auth.Kind {
		case "none":
			spec.Auth = addon.AuthSpec{Kind: addon.AuthNone}
		case "bearer":
			spec.Auth = addon.AuthSpec{Kind: addon.AuthBearer}
		case "api_key_header":
			if args.Auth.HeaderName == "" {
				return nil, errors.New("auth.header_name is required for api_key_header")
			}
			spec.Auth = addon.AuthSpec{
				Kind:       addon.AuthAPIKeyHeader,
				HeaderName: args.Auth.HeaderName,
			}
		case "hawk":
			spec.Auth = addon.AuthSpec{Kind: addon.AuthHawk}
		default:
			return nil, fmt.Errorf("auth.kind %q is not supported by provision_mcp (use mcpx__create_addon for OAuth2)", args.Auth.Kind)
		}
	}

	// AuthAPIKeyQuery is not wired through HeadersForDownstream, refuse it
	// so we don't silently produce a non-functional addon.
	if spec.Auth.Kind == addon.AuthAPIKeyQuery {
		return nil, errors.New("api_key_query auth is not supported by provision_mcp; use api_key_header, bearer, or hawk")
	}

	return spec, nil
}

// provisionInfra carries the IDs/names produced when ensuring the parent
// server, auth scope, and route rule exist for an addon namespace.
type provisionInfra struct {
	ServerID      string
	AuthScopeID   string
	AuthScopeName string
}

func provisionAuthScopeType(kind addon.AuthKind) string {
	if kind == addon.AuthHawk {
		return "hawk"
	}
	return "generic"
}

// ensureProvisionInfra creates (or finds) the DownstreamServer, AuthScope,
// and RouteRule that anchor the addon. Idempotent: re-running with the same
// api_name reuses existing rows and only fills in what is missing.
func (h *handler) ensureProvisionInfra(
	ctx context.Context, apiName, description string, authKind addon.AuthKind,
) (*provisionInfra, error) {
	info := &provisionInfra{}

	// Parent server: look up by name first, then fall back to a stable ID.
	serverName := apiName + " (auto-provisioned)"
	stableServerID := "addon-host-" + apiName

	if existing, err := h.store.GetDownstreamServerByName(ctx, serverName); err == nil && existing != nil {
		info.ServerID = existing.ID
	} else if existing, err := h.store.GetDownstreamServer(ctx, stableServerID); err == nil && existing != nil {
		info.ServerID = existing.ID
	} else {
		now := time.Now().UTC()
		srv := &store.DownstreamServer{
			ID:             stableServerID,
			Name:           serverName,
			Transport:      "internal",
			ToolNamespace:  apiName,
			Discovery:      "static",
			IdleTimeoutSec: 0,
			MaxInstances:   1,
			RestartPolicy:  "never",
			Source:         "agent_provisioned",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := h.store.CreateDownstreamServer(ctx, srv); err != nil {
			return nil, fmt.Errorf("create downstream server: %w", err)
		}
		info.ServerID = srv.ID
	}

	// Auth scope: name-keyed for stable lookup.
	scopeName := apiName + "-credential"
	scopeType := provisionAuthScopeType(authKind)
	info.AuthScopeName = scopeName
	if existing, err := h.store.GetAuthScopeByName(ctx, scopeName); err == nil && existing != nil {
		info.AuthScopeID = existing.ID
		if existing.Type != scopeType {
			existing.Type = scopeType
			existing.UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateAuthScope(ctx, existing); err != nil {
				return nil, fmt.Errorf("update auth scope type: %w", err)
			}
		}
	} else {
		now := time.Now().UTC()
		scope := &store.AuthScope{
			ID:        uuid.NewString(),
			Name:      scopeName,
			Type:      scopeType,
			Source:    "agent_provisioned",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := h.store.CreateAuthScope(ctx, scope); err != nil {
			return nil, fmt.Errorf("create auth scope: %w", err)
		}
		info.AuthScopeID = scope.ID
	}

	// Route rule: ensure tool calls in the apiName namespace route to our
	// shell server with our auth scope. We pick the first available
	// workspace — most installs have a single global workspace.
	if err := h.ensureProvisionRoute(ctx, apiName, info.ServerID, info.AuthScopeID); err != nil {
		return nil, err
	}

	// Refresh the routing engine so the new rule takes effect immediately.
	if h.engine != nil {
		h.engine.InvalidateAllRoutes()
	}

	return info, nil
}

// ensureProvisionRoute creates a RouteRule matching <namespace>__* if one
// doesn't already exist for this namespace + downstream server.
func (h *handler) ensureProvisionRoute(
	ctx context.Context, apiName, serverID, authScopeID string,
) error {
	// Pick a workspace. The session's workspace is preferred; fall back to
	// the first workspace found.
	workspaceID := h.currentWorkspaceID(ctx)
	if workspaceID == "" {
		all, err := h.store.ListWorkspaces(ctx)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		if len(all) == 0 {
			return errors.New("no workspaces configured — create one before provisioning addons")
		}
		workspaceID = all[0].ID
	}
	if rpc := h.requireWorkspaceWrite(ctx, workspaceID); rpc != nil {
		return errors.New(rpc.Message)
	}

	existing, err := h.store.ListRouteRules(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list route rules: %w", err)
	}
	wantPattern := apiName + "__*"
	for _, r := range existing {
		if r.DownstreamServerID == serverID && string(r.ToolMatch) != "" {
			var pats []string
			if json.Unmarshal(r.ToolMatch, &pats) == nil {
				for _, p := range pats {
					if p == wantPattern {
						// Ensure auth scope is current; update if drifted.
						if r.AuthScopeID != authScopeID {
							r.AuthScopeID = authScopeID
							r.UpdatedAt = time.Now().UTC()
							if err := h.store.UpdateRouteRule(ctx, &r); err != nil {
								return fmt.Errorf("update route rule: %w", err)
							}
						}
						return nil
					}
				}
			}
		}
	}

	now := time.Now().UTC()
	tm, _ := json.Marshal([]string{wantPattern})
	rule := &store.RouteRule{
		ID:                 uuid.NewString(),
		Name:               apiName + " (provisioned)",
		Priority:           50,
		WorkspaceID:        workspaceID,
		PathGlob:           "**",
		ToolMatch:          tm,
		DownstreamServerID: serverID,
		AuthScopeID:        authScopeID,
		Policy:             "allow",
		Source:             "agent_provisioned",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := h.store.CreateRouteRule(ctx, rule); err != nil {
		return fmt.Errorf("create route rule: %w", err)
	}
	return nil
}

// shouldPromptForSecret returns true when the auth scope has no usable secret
// yet, or when the agent explicitly requested rotation. Reading the keys via
// secretsManager.List is safe — it returns the key names but never the values.
func (h *handler) shouldPromptForSecret(
	ctx context.Context, authScopeID string, auth addon.AuthSpec, override *provisionAuthOverride,
) (bool, error) {
	if override != nil && override.Rotate {
		return true, nil
	}
	keys, err := h.secretsManager.List(ctx, authScopeID)
	if err != nil {
		return false, err
	}
	wantKey := secretKeyForAuth(auth)
	wantKeys := secretKeysForAuth(auth)
	if wantKey == "" && len(wantKeys) == 0 {
		return false, nil
	}
	if len(wantKeys) == 0 {
		wantKeys = []string{wantKey}
	}
	found := make(map[string]bool, len(keys))
	for _, k := range keys {
		found[k] = true
	}
	for _, k := range wantKeys {
		if !found[k] {
			return true, nil
		}
	}
	return false, nil
}

// captureSecretIntoAuthScope drives the human-in-the-loop secret prompt and
// pipes the result into the auth scope's encrypted store. The flow:
//
//  1. Create a pending prompt with delete_on_read=false. We need to read the
//     file ourselves — if the on-read watcher fired, we'd race the read.
//  2. Block waiting for the human to submit (or cancel/timeout).
//  3. Read the 0600 file once.
//  4. Encrypt + persist into the auth scope under the appropriate key.
//  5. Hard-delete the file. The sweeper would catch it on expiry anyway,
//     but we don't want plaintext on disk a moment longer than necessary.
//
// The agent never sees the value. The auth scope never appears in the result.
func (h *handler) captureSecretIntoAuthScope(
	ctx context.Context, authScopeID string, auth addon.AuthSpec, label, reason string,
) error {
	created, err := h.secretPrompts.RequestPrompt(ctx, ephemeral.PromptRequest{
		Reason:       reason,
		Label:        label,
		Requester:    h.sessions.sessionID(),
		Timeout:      5 * time.Minute,
		DeleteOnRead: false,
	})
	if err != nil {
		return fmt.Errorf("request secret prompt: %w", err)
	}

	res, err := h.secretPrompts.Wait(ctx, created.ID)
	if err != nil {
		switch {
		case errors.Is(err, ephemeral.ErrUserCancelled):
			return errors.New("secret prompt cancelled by the human; provisioning aborted")
		case errors.Is(err, ephemeral.ErrPromptTimeout):
			return errors.New("secret prompt timed out (no response within 5 minutes); provisioning aborted")
		default:
			return fmt.Errorf("secret prompt failed: %w", err)
		}
	}

	value, err := os.ReadFile(res.Path)
	if err != nil {
		return fmt.Errorf("read captured secret: %w", err)
	}
	// Best-effort cleanup regardless of subsequent errors.
	defer func() {
		if err := os.Remove(res.Path); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to delete ephemeral secret file after ingestion",
				"path", res.Path, "error", err)
		}
	}()

	storedValue := string(value)
	if auth.Kind == addon.AuthHawk {
		creds, err := parseHawkPromptSecret(storedValue)
		if err != nil {
			return err
		}
		for k, v := range creds {
			if err := h.secretsManager.Put(ctx, authScopeID, k, []byte(v)); err != nil {
				return fmt.Errorf("persist hawk secret %s into auth scope: %w", k, err)
			}
		}
		return nil
	}
	if auth.Kind == addon.AuthBearer {
		// Auto-prefix Bearer if the human pasted the raw token. We accept
		// pre-prefixed values too (`Bearer abc`) so the human can paste
		// either form.
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(storedValue)), "bearer ") {
			storedValue = "Bearer " + strings.TrimSpace(storedValue)
		}
	}

	key := secretKeyForAuth(auth)
	if key == "" {
		return fmt.Errorf("internal: cannot determine secret key for auth kind %q", auth.Kind)
	}
	if err := h.secretsManager.Put(ctx, authScopeID, key, []byte(storedValue)); err != nil {
		return fmt.Errorf("persist secret into auth scope: %w", err)
	}
	return nil
}

// secretKeyForAuth returns the auth-scope key name under which the captured
// credential is stored. The injector turns each (key, value) pair in an auth
// scope into one HTTP header at request time.
func secretKeyForAuth(auth addon.AuthSpec) string {
	switch auth.Kind {
	case addon.AuthBearer:
		return "Authorization"
	case addon.AuthAPIKeyHeader:
		return auth.HeaderName
	default:
		return ""
	}
}

func secretKeysForAuth(auth addon.AuthSpec) []string {
	if auth.Kind == addon.AuthHawk {
		return []string{"HAWK_ID", "HAWK_KEY"}
	}
	return nil
}

func parseHawkPromptSecret(raw string) (map[string]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("hawk credential prompt was empty")
	}

	var obj map[string]string
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return nil, fmt.Errorf("parse hawk credential JSON: %w", err)
		}
		return normalizeHawkPromptSecrets(obj)
	}

	lines := strings.Split(trimmed, "\n")
	lineVals := map[string]string{}
	var positional []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			lineVals[strings.TrimSpace(k)] = strings.TrimSpace(v)
			continue
		}
		positional = append(positional, line)
	}
	if len(lineVals) > 0 {
		return normalizeHawkPromptSecrets(lineVals)
	}
	if len(positional) >= 2 {
		return normalizeHawkPromptSecrets(map[string]string{
			"id":  positional[0],
			"key": positional[1],
		})
	}
	if id, key, ok := strings.Cut(trimmed, ":"); ok {
		return normalizeHawkPromptSecrets(map[string]string{
			"id":  strings.TrimSpace(id),
			"key": strings.TrimSpace(key),
		})
	}
	return nil, errors.New("hawk credential must be JSON, KEY=value lines, two lines (id then key), or id:key")
}

func normalizeHawkPromptSecrets(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		value := strings.TrimSpace(v)
		if value == "" {
			continue
		}
		switch key {
		case "hawk_id", "hawk_key_id", "api_key_id", "id":
			out["HAWK_ID"] = value
		case "hawk_key", "hawk_secret", "api_key", "key":
			out["HAWK_KEY"] = value
		case "hawk_algorithm", "algorithm", "alg":
			out["HAWK_ALGORITHM"] = value
		}
	}
	if out["HAWK_ID"] == "" {
		return nil, errors.New("hawk credential missing id / HAWK_ID")
	}
	if out["HAWK_KEY"] == "" {
		return nil, errors.New("hawk credential missing key / HAWK_KEY")
	}
	if out["HAWK_ALGORITHM"] == "" {
		out["HAWK_ALGORITHM"] = "sha256"
	}
	return out, nil
}
