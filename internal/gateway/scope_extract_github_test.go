package gateway

import (
	"encoding/json"
	"testing"
)

func TestGitHubExtractor_OwnerRepo(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"owner":"acme","repo":"mcplexer"}`))

	if _, ok := result["repo"]["acme/mcplexer"]; !ok {
		t.Fatalf("expected repo acme/mcplexer, got %v", result["repo"])
	}
	if _, ok := result["org"]["acme"]; !ok {
		t.Fatalf("expected org acme, got %v", result["org"])
	}
}

func TestGitHubExtractor_URL(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"url":"https://github.com/acme/mcplexer/issues/1"}`))

	if _, ok := result["repo"]["acme/mcplexer"]; !ok {
		t.Fatalf("expected repo from URL, got %v", result["repo"])
	}
}

func TestGitHubExtractor_QueryQualifier(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"query":"is:issue repo:evil/private bug"}`))

	if _, ok := result["repo"]["evil/private"]; !ok {
		t.Fatalf("expected repo from query, got %v", result["repo"])
	}
}

func TestGitHubExtractor_OrgField(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"organization":"acme-corp"}`))

	if _, ok := result["org"]["acme-corp"]; !ok {
		t.Fatalf("expected org from organization field, got %v", result["org"])
	}
}

func TestGitHubExtractor_EmptyArgs(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(nil)

	if len(result) != 0 {
		t.Fatalf("expected empty result for nil args, got %v", result)
	}
}

func TestGitHubExtractor_FullIntegration_OrgAllowlist(t *testing.T) {
	ext := GitHubExtractor{}
	policy, err := NewScopePolicy(json.RawMessage(`{"org": ["acme"]}`))
	if err != nil {
		t.Fatal(err)
	}

	// Allowed: acme org
	extracted := ext.Extract(json.RawMessage(`{"owner":"acme","repo":"mcplexer"}`))
	if err := policy.Enforce(extracted); err != nil {
		t.Fatalf("expected allow for acme org: %v", err)
	}

	// Denied: evil org
	extracted = ext.Extract(json.RawMessage(`{"owner":"evil","repo":"x"}`))
	if err := policy.Enforce(extracted); err == nil {
		t.Fatal("expected deny for non-allowlisted org")
	}
}

func TestGitHubExtractor_FullIntegration_RepoAllowlist(t *testing.T) {
	ext := GitHubExtractor{}
	policy, err := NewScopePolicy(json.RawMessage(`{"repo": ["acme/mcplexer"]}`))
	if err != nil {
		t.Fatal(err)
	}

	// Allowed: URL extraction
	extracted := ext.Extract(json.RawMessage(`{"url":"https://github.com/acme/mcplexer/issues/1"}`))
	if err := policy.Enforce(extracted); err != nil {
		t.Fatalf("expected allow from URL: %v", err)
	}

	// Denied: query extraction
	extracted = ext.Extract(json.RawMessage(`{"query":"is:issue repo:evil/private bug"}`))
	if err := policy.Enforce(extracted); err == nil {
		t.Fatal("expected deny from query extraction")
	}
}

func TestGitHubExtractor_APIRepoURL(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"url":"https://api.github.com/repos/acme/mcplexer"}`))

	if _, ok := result["repo"]["acme/mcplexer"]; !ok {
		t.Fatalf("expected repo from API URL, got %v", result["repo"])
	}
}

func TestGitHubExtractor_FullNameField(t *testing.T) {
	ext := GitHubExtractor{}
	result := ext.Extract(json.RawMessage(`{"full_name":"acme/mcplexer"}`))

	if _, ok := result["repo"]["acme/mcplexer"]; !ok {
		t.Fatalf("expected repo from full_name, got %v", result["repo"])
	}
}
