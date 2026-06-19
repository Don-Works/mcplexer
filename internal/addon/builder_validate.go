package addon

import (
	"fmt"
	"regexp"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// Validate returns a non-nil error if the spec is malformed. The returned
// error joins all problems found so the user can fix them in one pass.
func (s AddonSpec) Validate() error {
	var errs []string
	if !nameRE.MatchString(s.Name) {
		errs = append(errs, "name must match ^[a-z][a-z0-9_]{1,62}$")
	}
	if strings.TrimSpace(s.Description) == "" {
		errs = append(errs, "description is required")
	}
	if !strings.HasPrefix(s.BaseURL, "http://") && !strings.HasPrefix(s.BaseURL, "https://") {
		errs = append(errs, "base_url must start with http:// or https://")
	}
	if strings.TrimSpace(s.ParentServer) == "" {
		errs = append(errs, "parent_server is required")
	}
	switch s.Auth.Kind {
	case AuthNone, AuthBearer, AuthAPIKeyHeader, AuthAPIKeyQuery, AuthHawk, AuthOAuth2, AuthOAuth2Pending:
	default:
		errs = append(errs, fmt.Sprintf("auth.kind %q is not supported", s.Auth.Kind))
	}
	if s.Auth.Kind == AuthAPIKeyHeader && s.Auth.HeaderName == "" {
		errs = append(errs, "auth.header_name is required for api_key_header")
	}
	if s.Auth.Kind == AuthAPIKeyQuery && s.Auth.QueryName == "" {
		errs = append(errs, "auth.query_name is required for api_key_query")
	}
	if s.Auth.Kind == AuthOAuth2 || s.Auth.Kind == AuthOAuth2Pending {
		if e := validateOAuth2(s.Auth); e != "" {
			errs = append(errs, e)
		}
	}
	if len(s.Endpoints) == 0 {
		errs = append(errs, "at least one endpoint is required")
	}
	for i, ep := range s.Endpoints {
		if e := validateEndpoint(ep); e != "" {
			errs = append(errs, fmt.Sprintf("endpoints[%d]: %s", i, e))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// validateOAuth2 enforces wizard-required fields for OAuth2 specs. Pending
// specs (produced by OpenAPI import) only need the URLs/scopes filled in;
// client_id/grant come later from the wizard.
func validateOAuth2(a AuthSpec) string {
	if a.AuthURL == "" && a.Kind != AuthOAuth2Pending {
		return "auth.auth_url is required for oauth2"
	}
	if a.TokenURL == "" {
		return "auth.token_url is required for oauth2"
	}
	switch a.GrantType {
	case "", OAuth2GrantAuthorizationCode, OAuth2GrantClientCredentials:
		// allowed; "" means pending
	default:
		return fmt.Sprintf("auth.grant_type %q must be authorization_code or client_credentials", a.GrantType)
	}
	if a.Kind == AuthOAuth2 {
		if a.GrantType == "" {
			return "auth.grant_type is required for oauth2 (authorization_code or client_credentials)"
		}
		if a.ClientID == "" && !a.UsePKCE {
			return "auth.client_id is required for oauth2 unless use_pkce is true"
		}
	}
	return ""
}

func validateEndpoint(ep EndpointSpec) string {
	if !nameRE.MatchString(ep.Name) {
		return "name must match ^[a-z][a-z0-9_]{1,62}$"
	}
	if strings.TrimSpace(ep.Description) == "" {
		return "description is required"
	}
	if !validMethods[strings.ToUpper(ep.Method)] {
		return fmt.Sprintf("method %q is not supported", ep.Method)
	}
	if !strings.HasPrefix(ep.Path, "/") {
		return "path must start with /"
	}
	for j, p := range ep.Params {
		if !nameRE.MatchString(p.Name) {
			return fmt.Sprintf("params[%d].name must match ^[a-z][a-z0-9_]{1,62}$", j)
		}
		switch p.Type {
		case "string", "integer", "number", "boolean":
		default:
			return fmt.Sprintf("params[%d].type %q is not supported", j, p.Type)
		}
		switch p.In {
		case "path", "query", "body":
		default:
			return fmt.Sprintf("params[%d].in %q must be path|query|body", j, p.In)
		}
	}
	return ""
}
