package addon

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// ImportOpenAPI parses an OpenAPI 3.x document (JSON or YAML) and produces
// an AddonSpec the user can review and tweak in the wizard.
//
// The returned spec is NOT validated against AddonSpec.Validate — the wizard
// is expected to fill in parent_server (which OpenAPI does not describe) and
// the user can rename the namespace/endpoints before saving. Auth schemes that
// don't map cleanly to an existing AuthMode return a clear error rather than
// silently falling back to AuthNone.
func ImportOpenAPI(spec []byte) (*AddonSpec, error) {
	if len(spec) == 0 {
		return nil, fmt.Errorf("openapi spec is empty")
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(spec)
	if err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	if doc.Info == nil {
		return nil, fmt.Errorf("openapi: info section is required")
	}
	if doc.Paths == nil || doc.Paths.Len() == 0 {
		return nil, fmt.Errorf("openapi: no paths found")
	}

	out := &AddonSpec{
		Name:        slugifyName(doc.Info.Title),
		Description: firstNonEmpty(doc.Info.Title, doc.Info.Description),
		BaseURL:     pickServerURL(doc.Servers),
	}
	if out.BaseURL == "" {
		return nil, fmt.Errorf("openapi: servers[0].url is required")
	}

	auth, err := pickAuth(doc)
	if err != nil {
		return nil, err
	}
	out.Auth = auth

	out.Endpoints = collectEndpoints(doc)
	if len(out.Endpoints) == 0 {
		return nil, fmt.Errorf("openapi: no usable operations found")
	}
	return out, nil
}

// pickServerURL picks the first declared server URL, trimming trailing slash.
func pickServerURL(servers openapi3.Servers) string {
	if len(servers) == 0 {
		return ""
	}
	return strings.TrimRight(servers[0].URL, "/")
}

// pickAuth inspects security requirements + components.securitySchemes and
// returns an AuthSpec. Returns an explicit error for schemes we cannot honor
// (mTLS, OIDC, OAuth flows that need full credential management).
func pickAuth(doc *openapi3.T) (AuthSpec, error) {
	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return AuthSpec{Kind: AuthNone}, nil
	}
	// Honor first global security requirement; fall back to the first scheme.
	name := firstSecurityName(doc.Security, doc.Components.SecuritySchemes)
	ref, ok := doc.Components.SecuritySchemes[name]
	if !ok || ref == nil || ref.Value == nil {
		return AuthSpec{Kind: AuthNone}, nil
	}
	return mapSecurityScheme(name, ref.Value)
}

// firstSecurityName picks the first security requirement name that resolves
// to a known scheme; returns first scheme name otherwise.
func firstSecurityName(reqs openapi3.SecurityRequirements, schemes openapi3.SecuritySchemes) string {
	for _, req := range reqs {
		for n := range req {
			if _, ok := schemes[n]; ok {
				return n
			}
		}
	}
	for n := range schemes {
		return n
	}
	return ""
}

// mapSecurityScheme converts an OpenAPI SecurityScheme to an AuthSpec.
// Returns an actionable error for unsupported schemes — callers should
// suggest the user pick a different security scheme or add it manually.
func mapSecurityScheme(name string, ss *openapi3.SecurityScheme) (AuthSpec, error) {
	switch strings.ToLower(ss.Type) {
	case "http":
		switch strings.ToLower(ss.Scheme) {
		case "bearer":
			return AuthSpec{Kind: AuthBearer}, nil
		case "hawk":
			return AuthSpec{Kind: AuthHawk}, nil
		case "basic":
			return AuthSpec{}, fmt.Errorf(
				"security scheme %q uses HTTP Basic auth which is not supported; "+
					"convert to a Bearer token or API key, or wire it through a parent_server", name)
		}
	case "apikey":
		switch strings.ToLower(ss.In) {
		case "header":
			return AuthSpec{Kind: AuthAPIKeyHeader, HeaderName: ss.Name}, nil
		case "query":
			return AuthSpec{Kind: AuthAPIKeyQuery, QueryName: ss.Name}, nil
		case "cookie":
			return AuthSpec{}, fmt.Errorf(
				"security scheme %q uses cookie-based API keys which are not supported; "+
					"prefer header or query placement", name)
		}
	case "oauth2":
		// OAuth2 is handled by the OAuth wizard agent; mark it as pending so
		// the UI can route the user to that flow. Pull the URLs and scopes off
		// whichever flow is declared so the wizard hint surfaces something
		// useful — without this the agent gets `auth_url= token_url= scopes=`
		// and has no way to point the human at the right credential.
		spec := AuthSpec{Kind: AuthKind("oauth2_pending")}
		if ss.Flows != nil {
			var f *openapi3.OAuthFlow
			grant := ""
			switch {
			case ss.Flows.AuthorizationCode != nil:
				f = ss.Flows.AuthorizationCode
				grant = "authorization_code"
			case ss.Flows.Implicit != nil:
				f = ss.Flows.Implicit
				grant = "authorization_code" // closest supported analogue
			case ss.Flows.ClientCredentials != nil:
				f = ss.Flows.ClientCredentials
				grant = "client_credentials"
			case ss.Flows.Password != nil:
				f = ss.Flows.Password
			}
			if f != nil {
				spec.AuthURL = f.AuthorizationURL
				spec.TokenURL = f.TokenURL
				if f.Scopes != nil {
					for k := range f.Scopes {
						spec.Scopes = append(spec.Scopes, k)
					}
				}
			}
			if grant != "" {
				spec.GrantType = OAuth2GrantType(grant)
			}
		}
		return spec, nil
	case "openidconnect":
		return AuthSpec{}, fmt.Errorf(
			"security scheme %q uses OpenID Connect which is not supported; "+
				"use Bearer JWT directly, or wire OIDC through the OAuth wizard", name)
	case "mutualtls":
		return AuthSpec{}, fmt.Errorf(
			"security scheme %q uses mutual TLS which is not supported; "+
				"client certificates must be configured outside the addon system", name)
	}
	return AuthSpec{}, fmt.Errorf(
		"security scheme %q has unsupported type=%q (scheme=%q in=%q); "+
			"pick a different scheme or contact the addon maintainers",
		name, ss.Type, ss.Scheme, ss.In)
}
