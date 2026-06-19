package mesh

import (
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type authScopeSnapshot struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name,omitempty"`
	Type            string          `json:"type"`
	RedactionHints  json.RawMessage `json:"redaction_hints,omitempty"`
	OAuthProviderID string          `json:"oauth_provider_id,omitempty"`
	Source          string          `json:"source,omitempty"`
}

type oauthProviderSnapshot struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	TemplateID   string          `json:"template_id,omitempty"`
	AuthorizeURL string          `json:"authorize_url"`
	TokenURL     string          `json:"token_url"`
	ClientID     string          `json:"client_id"`
	ClientSecret string          `json:"client_secret,omitempty"`
	Scopes       json.RawMessage `json:"scopes,omitempty"`
	UsePKCE      bool            `json:"use_pkce"`
	RedirectURI  string          `json:"redirect_uri,omitempty"`
	Source       string          `json:"source,omitempty"`
}

type authSnapshotPlain struct {
	Schema     int                        `json:"schema"`
	Exported   time.Time                  `json:"exported_at"`
	Scope      authScopeSnapshot          `json:"scope"`
	Provider   *oauthProviderSnapshot     `json:"provider,omitempty"`
	Secrets    map[string]string          `json:"secrets,omitempty"`
	OAuthToken *store.OAuthTokenData      `json:"oauth_token,omitempty"`
	Servers    []downstreamServerSnapshot `json:"servers,omitempty"`
	Routes     []routeRuleSnapshot        `json:"routes,omitempty"`
}

type downstreamServerSnapshot struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Transport      string          `json:"transport"`
	Command        string          `json:"command,omitempty"`
	Args           json.RawMessage `json:"args,omitempty"`
	URL            *string         `json:"url,omitempty"`
	ToolNamespace  string          `json:"tool_namespace"`
	Discovery      string          `json:"discovery"`
	CacheConfig    json.RawMessage `json:"cache_config,omitempty"`
	IdleTimeoutSec int             `json:"idle_timeout_sec"`
	CallTimeoutSec int             `json:"call_timeout_sec,omitempty"`
	MaxInstances   int             `json:"max_instances"`
	RestartPolicy  string          `json:"restart_policy"`
	Disabled       bool            `json:"disabled"`
	Source         string          `json:"source,omitempty"`
}

type routeRuleSnapshot struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Priority           int             `json:"priority"`
	WorkspaceID        string          `json:"workspace_id"`
	PathGlob           string          `json:"path_glob"`
	ToolMatch          json.RawMessage `json:"tool_match,omitempty"`
	ScopePolicy        json.RawMessage `json:"scope_policy,omitempty"`
	DownstreamServerID string          `json:"downstream_server_id"`
	AuthScopeID        string          `json:"auth_scope_id"`
	Policy             string          `json:"policy"`
	LogLevel           string          `json:"log_level,omitempty"`
	ApprovalMode       string          `json:"approval_mode,omitempty"`
	ApprovalTimeout    int             `json:"approval_timeout,omitempty"`
	Source             string          `json:"source,omitempty"`
}
