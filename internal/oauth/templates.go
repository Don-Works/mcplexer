package oauth

// ProviderTemplate is a built-in OAuth provider configuration template.
type ProviderTemplate struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	AuthorizeURL          string   `json:"authorize_url"`
	TokenURL              string   `json:"token_url"`
	Scopes                []string `json:"scopes"`
	UsePKCE               bool     `json:"use_pkce"`
	NeedsSecret           bool     `json:"needs_secret"`
	SupportsAutoDiscovery bool     `json:"supports_auto_discovery"`
	SetupURL              string   `json:"setup_url"`
	HelpText              string   `json:"help_text"`
	CallbackURL           string   `json:"callback_url,omitempty"`
}

var templates = map[string]ProviderTemplate{
	"github": {
		ID:                    "github",
		Name:                  "GitHub",
		AuthorizeURL:          "https://github.com/login/oauth/authorize",
		TokenURL:              "https://github.com/login/oauth/access_token",
		Scopes:                []string{"repo", "read:org", "gist", "workflow", "read:user", "user:email", "project"},
		UsePKCE:               true,
		NeedsSecret:           true,
		SupportsAutoDiscovery: false,
		SetupURL:              "https://github.com/settings/developers",
		HelpText:              "Create an OAuth App under Settings > Developer settings > OAuth Apps",
	},
	"linear": {
		ID:                    "linear",
		Name:                  "Linear",
		AuthorizeURL:          "https://linear.app/oauth/authorize",
		TokenURL:              "https://api.linear.app/oauth/token",
		Scopes:                []string{"read", "write"},
		UsePKCE:               true,
		NeedsSecret:           false,
		SupportsAutoDiscovery: true,
		SetupURL:              "https://linear.app/settings/api",
		HelpText:              "This integration connects automatically. Just click Connect and authenticate.",
	},
	"slack": {
		ID:           "slack",
		Name:         "Slack",
		AuthorizeURL: "https://slack.com/oauth/v2_user/authorize",
		TokenURL:     "https://slack.com/api/oauth.v2.user.access",
		Scopes: []string{
			"search:read.public",
			"search:read.private",
			"search:read.mpim",
			"search:read.im",
			"search:read.files",
			"search:read.users",
			"chat:write",
			"channels:history",
			"groups:history",
			"mpim:history",
			"im:history",
			"canvases:read",
			"canvases:write",
			"users:read",
			"users:read.email",
			"reactions:write",
			"reactions:read",
			"emoji:read",
			"files:read",
			"channels:write",
			"groups:write",
			"im:write",
			"mpim:write",
			"channels:read",
			"groups:read",
			"mpim:read",
		},
		UsePKCE:               true,
		NeedsSecret:           true,
		SupportsAutoDiscovery: false,
		SetupURL:              "https://api.slack.com/apps",
		HelpText:              "Create or reuse an internal Slack app with MCP enabled. Slack MCP uses confidential OAuth: paste the app client ID and client secret, and register the callback URL shown here.",
	},
	"clickup": {
		ID:                    "clickup",
		Name:                  "ClickUp",
		AuthorizeURL:          "https://app.clickup.com/api",
		TokenURL:              "https://api.clickup.com/api/v2/oauth/token",
		Scopes:                []string{},
		UsePKCE:               false,
		NeedsSecret:           true,
		SupportsAutoDiscovery: false,
		SetupURL:              "https://clickup.com/integrations/manage/apps",
		HelpText:              "Create an OAuth App at clickup.com/integrations/manage/apps. This provider gives full API access (including Chat v3) using the standard ClickUp OAuth flow.",
	},
	"google": {
		ID:                    "google",
		Name:                  "Google",
		AuthorizeURL:          "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:              "https://oauth2.googleapis.com/token",
		Scopes:                []string{"openid", "email", "profile"},
		UsePKCE:               true,
		NeedsSecret:           true,
		SupportsAutoDiscovery: false,
		SetupURL:              "https://console.cloud.google.com/apis/credentials",
		HelpText:              "Create OAuth 2.0 credentials in Google Cloud Console",
	},
	"freeagent": {
		ID:                    "freeagent",
		Name:                  "FreeAgent",
		AuthorizeURL:          "https://api.freeagent.com/v2/approve_app",
		TokenURL:              "https://api.freeagent.com/v2/token_endpoint",
		Scopes:                []string{},
		UsePKCE:               false,
		NeedsSecret:           true,
		SupportsAutoDiscovery: false,
		SetupURL:              "https://dev.freeagent.com/dashboard",
		HelpText:              "Register an OAuth app at dev.freeagent.com/dashboard. FreeAgent does not use scopes — API access is bounded by the connecting user's permission level (0–8). For read-only outstanding-invoice lookups, use a user with at least Level 4 permissions.",
	},
}

// ListTemplates returns all built-in provider templates.
func ListTemplates() []ProviderTemplate {
	out := make([]ProviderTemplate, 0, len(templates))
	for _, t := range templates {
		out = append(out, t)
	}
	return out
}

// GetTemplate returns a template by ID, or nil if not found.
func GetTemplate(id string) *ProviderTemplate {
	t, ok := templates[id]
	if !ok {
		return nil
	}
	return &t
}
