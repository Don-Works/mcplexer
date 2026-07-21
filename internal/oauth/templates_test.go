package oauth

import "testing"

func TestSlackTemplateUsesConfidentialOAuth(t *testing.T) {
	tpl := GetTemplate("slack")
	if tpl == nil {
		t.Fatal("missing slack template")
	}
	if tpl.AuthorizeURL != "https://slack.com/oauth/v2_user/authorize" {
		t.Fatalf("AuthorizeURL = %q", tpl.AuthorizeURL)
	}
	if tpl.TokenURL != "https://slack.com/api/oauth.v2.user.access" {
		t.Fatalf("TokenURL = %q", tpl.TokenURL)
	}
	if !tpl.NeedsSecret {
		t.Fatal("Slack MCP requires a confidential OAuth client secret")
	}
	if tpl.SupportsAutoDiscovery {
		t.Fatal("Slack MCP should not be treated as DCR/one-click OAuth")
	}
	if len(tpl.Scopes) == 0 {
		t.Fatal("Slack template should request MCP user-token scopes")
	}
}
