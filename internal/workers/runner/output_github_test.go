package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmitGitHubIssueOutput_Shape(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		captured = req
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(201, `{"number":42}`), nil
	})
	octx := sampleOutputCtx(client)
	octx.secrets = &fakeSecretsForChannel{value: []byte("ghp_token")}
	ch := outputChannel{
		Type:          "github_issue",
		Repo:          "don-works/mcplexer",
		SecretScopeID: "scope-github",
		TitlePrefix:   "[worker]",
	}
	if err := emitGitHubIssueOutput(context.Background(), octx, ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !strings.HasSuffix(captured.URL.Path, "/repos/don-works/mcplexer/issues") {
		t.Fatalf("URL path = %q", captured.URL.Path)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer ghp_token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := captured.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := captured.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Fatalf("API-Version = %q", got)
	}
	var got githubCreateIssueRequest
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v / %s", err, string(capturedBody))
	}
	if !strings.HasPrefix(got.Title, "[worker] smoke-test") {
		t.Fatalf("Title = %q", got.Title)
	}
	if !strings.Contains(got.Body, "All clear") {
		t.Fatalf("Body missing output: %q", got.Body)
	}
	if !strings.Contains(got.Body, "Tokens: 120 in / 60 out") {
		t.Fatalf("Body missing tokens: %q", got.Body)
	}
}

func TestEmitGitHubIssueOutput_BadRepo(t *testing.T) {
	ch := outputChannel{Type: "github_issue", Repo: "nope", SecretScopeID: "x"}
	octx := sampleOutputCtx(nil)
	octx.secrets = &fakeSecretsForChannel{value: []byte("t")}
	err := emitGitHubIssueOutput(context.Background(), octx, ch)
	if err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("want owner/name error, got %v", err)
	}
}

// TestEmitGitHubIssueOutput_TraversalRejected verifies the channel
// refuses to even attempt an HTTP call when Repo contains a path
// traversal — the request must fail BEFORE we resolve the secret /
// build the URL.
func TestEmitGitHubIssueOutput_TraversalRejected(t *testing.T) {
	called := false
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		called = true
		return statusResponse(201, `{}`), nil
	})
	ch := outputChannel{Type: "github_issue", Repo: "../leak", SecretScopeID: "x"}
	octx := sampleOutputCtx(client)
	octx.secrets = &fakeSecretsForChannel{value: []byte("t")}
	err := emitGitHubIssueOutput(context.Background(), octx, ch)
	if err == nil {
		t.Fatal("want error for ../leak, got nil")
	}
	if called {
		t.Fatal("HTTP client must NOT be called when repo fails validation")
	}
}

func TestEmitGitHubIssueOutput_EmptyRepo(t *testing.T) {
	ch := outputChannel{Type: "github_issue", SecretScopeID: "x"}
	err := emitGitHubIssueOutput(context.Background(), sampleOutputCtx(nil), ch)
	if err == nil || !strings.Contains(err.Error(), "empty repo") {
		t.Fatalf("want empty-repo error, got %v", err)
	}
}

// TestValidateGitHubRepo locks down the input shape for the channel's
// Repo field. The Repo string is interpolated directly into the issues
// URL — anything that lets `..` or scheme characters through would
// allow a traversal out of the /repos/ namespace. Fail-closed at
// validation time so the operator gets a legible error instead of a
// silent GitHub 404.
func TestValidateGitHubRepo(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{"happy path owner/repo", "anthropics/claude-code", false},
		{"dots, dashes, underscores allowed", "owner.x/repo_y-z", false},
		{"empty", "", true},
		{"missing slash", "ownerrepo", true},
		{"too many slashes", "owner/x/leak", true},
		{"path traversal both halves", "../leak", true},
		{"path traversal owner side", "../leak/repo", true},
		{"path traversal repo side", "owner/../leak", true},
		{"literal .. as owner", "../..", true},
		{"trailing slash", "owner/", true},
		{"leading slash", "/owner/repo", true},
		{"space in owner", "ow ner/repo", true},
		{"scheme-ish input", "https://x/y", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubRepo(tc.repo)
			if tc.wantErr && err == nil {
				t.Fatalf("validateGitHubRepo(%q) = nil, want error", tc.repo)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateGitHubRepo(%q) = %v, want nil", tc.repo, err)
			}
		})
	}
}

func TestEmitGitHubIssueOutput_Non2xx(t *testing.T) {
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(422, `{"message":"validation"}`), nil
	})
	octx := sampleOutputCtx(client)
	octx.secrets = &fakeSecretsForChannel{value: []byte("t")}
	ch := outputChannel{Type: "github_issue", Repo: "x/y", SecretScopeID: "scope"}
	err := emitGitHubIssueOutput(context.Background(), octx, ch)
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("want 422 error, got %v", err)
	}
}
