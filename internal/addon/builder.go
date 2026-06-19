package addon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AuthKind identifies how a custom addon authenticates with its API.
type AuthKind string

const (
	AuthNone          AuthKind = "none"
	AuthBearer        AuthKind = "bearer"
	AuthAPIKeyHeader  AuthKind = "api_key_header"
	AuthAPIKeyQuery   AuthKind = "api_key_query"
	AuthHawk          AuthKind = "hawk"
	AuthOAuth2        AuthKind = "oauth2"
	AuthOAuth2Pending AuthKind = "oauth2_pending"
)

// OAuth2GrantType identifies the OAuth2 grant flow to use.
type OAuth2GrantType string

const (
	OAuth2GrantAuthorizationCode OAuth2GrantType = "authorization_code"
	OAuth2GrantClientCredentials OAuth2GrantType = "client_credentials"
)

// AuthSpec describes how the new addon authenticates.
// Token/key values are NOT inlined into the generated YAML — the user wires
// them up via a parent_server's auth_scope. The kind is used to drop helpful
// comments and (for api_key_header/_query) to scaffold the header/param name.
//
// For OAuth2 (kind=oauth2 or oauth2_pending), the additional fields describe
// the auth/token URLs, scopes, client ID/secret, and grant type. Pending
// indicates the spec was produced by OpenAPI import and still needs an
// interactive Configure-OAuth wizard step before it can authorize.
type AuthSpec struct {
	Kind       AuthKind `json:"kind" yaml:"kind"`
	HeaderName string   `json:"header_name,omitempty" yaml:"header_name,omitempty"`
	QueryName  string   `json:"query_name,omitempty" yaml:"query_name,omitempty"`

	// OAuth2 fields. Empty unless kind is oauth2 or oauth2_pending.
	AuthURL   string          `json:"auth_url,omitempty" yaml:"auth_url,omitempty"`
	TokenURL  string          `json:"token_url,omitempty" yaml:"token_url,omitempty"`
	Scopes    []string        `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	ClientID  string          `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	UsePKCE   bool            `json:"use_pkce,omitempty" yaml:"use_pkce,omitempty"`
	GrantType OAuth2GrantType `json:"grant_type,omitempty" yaml:"grant_type,omitempty"`
}

// EndpointSpec is one HTTP endpoint that becomes one addon tool.
type EndpointSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Method      string      `json:"method"`
	Path        string      `json:"path"` // relative path, joined with BaseURL
	Params      []ParamSpec `json:"params,omitempty"`
}

// ParamSpec describes one input parameter.
type ParamSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string, integer, number, boolean
	In          string `json:"in"`   // path, query, body
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// AddonSpec is the high-level user-facing description of a custom addon.
type AddonSpec struct {
	Name         string         `json:"name"` // namespace, e.g. "weatherco"
	Description  string         `json:"description"`
	BaseURL      string         `json:"base_url"`             // e.g. https://api.weather.co
	ParentServer string         `json:"parent_server"`        // existing downstream server ID for auth
	AuthScope    string         `json:"auth_scope,omitempty"` // optional override
	Auth         AuthSpec       `json:"auth"`
	Endpoints    []EndpointSpec `json:"endpoints"`
}

// BuildAddonYAML converts an AddonSpec into a YAML string that the loader
// can parse. The output is deterministic for a given input.
func BuildAddonYAML(spec AddonSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}

	af := AddonFile{
		ParentServer: spec.ParentServer,
		AuthScope:    spec.AuthScope,
		Tools:        make([]ToolDef, 0, len(spec.Endpoints)),
	}

	base := strings.TrimRight(spec.BaseURL, "/")
	for _, ep := range spec.Endpoints {
		af.Tools = append(af.Tools, buildToolDef(base, spec.Auth, ep))
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "# %s — custom MCP addon\n", spec.Name)
	fmt.Fprintf(&buf, "# %s\n", spec.Description)
	fmt.Fprintf(&buf, "# Auth: %s\n\n", spec.Auth.Kind)

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&af); err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("close yaml encoder: %w", err)
	}
	return buf.String(), nil
}

// WriteAndRegister writes the generated YAML to <dir>/<name>.yaml and reloads
// the registry from that directory. The new tools are immediately available.
func WriteAndRegister(reg *Registry, dir string, spec AddonSpec, resolve NamespaceResolver, opts ...LoadOption) (string, error) {
	yamlText, err := BuildAddonYAML(spec)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	path := filepath.Join(dir, spec.Name+".yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	if reg != nil {
		if err := reg.Reload(dir, resolve, opts...); err != nil {
			return path, fmt.Errorf("reload registry: %w", err)
		}
	}
	return path, nil
}

// Creator binds a Registry, an addons directory, and the resolvers needed to
// load YAML files into a single object that the gateway handler can call to
// hot-create new addons. It implements gateway.AddonCreator without importing
// the gateway package.
type Creator struct {
	Registry         *Registry
	Dir              string
	Resolve          NamespaceResolver
	AuthScopeResolve AuthScopeResolver
}

// Create writes the spec to <Dir>/<spec.Name>.yaml, hot-reloads the registry,
// and returns the path written and the new fully-namespaced tool names. The
// context is accepted to satisfy the gateway.AddonCreator interface but is
// not used (file IO + in-memory reload only).
func (c *Creator) Create(_ context.Context, spec AddonSpec) (string, []string, error) {
	if c == nil || c.Registry == nil {
		return "", nil, fmt.Errorf("addon creator not configured")
	}
	var opts []LoadOption
	if c.AuthScopeResolve != nil {
		opts = append(opts, WithAuthScopeResolver(c.AuthScopeResolve))
	}
	path, err := WriteAndRegister(c.Registry, c.Dir, spec, c.Resolve, opts...)
	if err != nil {
		return path, nil, err
	}
	names := make([]string, 0, len(spec.Endpoints))
	for _, ep := range spec.Endpoints {
		names = append(names, spec.Name+"__"+ep.Name)
	}
	return path, names, nil
}

// Reload re-reads dir and replaces the registry's contents in place under a
// write lock. Existing pointers to *Registry remain valid.
func (r *Registry) Reload(dir string, resolve NamespaceResolver, opts ...LoadOption) error {
	fresh, err := LoadDir(dir, resolve, opts...)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byFullName = fresh.byFullName
	r.byNamespace = fresh.byNamespace
	r.all = fresh.all
	return nil
}

// buildToolDef converts an EndpointSpec to a ToolDef, including auth-derived
// static headers/query params and a JSON Schema input_schema.
func buildToolDef(baseURL string, auth AuthSpec, ep EndpointSpec) ToolDef {
	td := ToolDef{
		Name:        ep.Name,
		Description: ep.Description,
		Method:      strings.ToUpper(ep.Method),
		URL:         rewriteURLPlaceholders(baseURL+ep.Path, ep.Params),
		InputSchema: buildInputSchema(ep.Params),
	}

	// Wire query params; placeholders match input_schema property names.
	qp := map[string]string{}
	for _, p := range ep.Params {
		if p.In == "query" {
			qp[p.Name] = "{{" + p.Name + "}}"
		}
	}
	if auth.Kind == AuthAPIKeyQuery && auth.QueryName != "" {
		// Use an underscore-prefixed placeholder so the user fills it via
		// auth_scope environment, not as a per-call argument.
		qp[auth.QueryName] = "{{_api_key}}"
	}
	if len(qp) > 0 {
		td.QueryParams = qp
	}

	// Static headers. The Authorization/X-Api-Key value uses a placeholder
	// that must be supplied by the parent_server's auth_scope at runtime.
	headers := map[string]string{}
	if auth.Kind == AuthAPIKeyHeader && auth.HeaderName != "" {
		headers[auth.HeaderName] = "{{_api_key}}"
	}
	if len(headers) > 0 {
		td.Headers = headers
	}

	// Disable body mapping for methods without a body.
	if !methodTakesBody(td.Method) {
		td.BodyMapping = "none"
	}
	return td
}

// methodTakesBody mirrors executor.methodHasBody for the encoder.
func methodTakesBody(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// buildInputSchema returns a minimal JSON Schema describing the params.
func buildInputSchema(params []ParamSpec) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range params {
		entry := map[string]any{"type": p.Type}
		if p.Description != "" {
			entry["description"] = p.Description
		}
		props[p.Name] = entry
		if p.Required {
			required = append(required, p.Name)
		}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
