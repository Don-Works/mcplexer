package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// githubAPIBase is the v3 REST API root. Package-private constant for
// the same reason as clickupAPIBase — easy to override later if we
// need GHE compatibility, stable for now.
const githubAPIBase = "https://api.github.com"

// githubRepoPattern is the validator for the channel's Repo field. We
// allow the same character set GitHub allows in owner / repo segments
// (alphanumeric + dot + dash + underscore) and REJECT path traversal
// segments like `..` — the Repo value gets interpolated directly into
// the issues URL, so anything outside this shape is either an operator
// typo or an attempt to break out of the /repos/ namespace.
var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// githubTitleLimit is the soft ceiling for issue titles. GitHub itself
// accepts up to 256 chars; this is the visual-fit threshold for the
// repo issues page.
const githubTitleLimit = 100

// githubCreateIssueRequest is the JSON body we POST to
// /repos/{owner}/{repo}/issues. Labels would be a nice future addition
// — leaving them out keeps the channel config flat.
type githubCreateIssueRequest struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// emitGitHubIssueOutput creates a GitHub issue in ch.Repo (owner/repo),
// reading the PAT or fine-grained token from ch.SecretScopeID key
// "api_key". Same fault-tolerance contract as the other HTTP channels:
// non-2xx → wrapped error → mesh alert via reportChannelError.
func emitGitHubIssueOutput(ctx context.Context, octx outputContext, ch outputChannel) error {
	repo := strings.TrimSpace(ch.Repo)
	if err := validateGitHubRepo(repo); err != nil {
		return err
	}
	token, err := resolveChannelSecret(ctx, octx, ch.SecretScopeID, "github_issue")
	if err != nil {
		return err
	}
	body, err := json.Marshal(buildGitHubRequest(octx, ch))
	if err != nil {
		return fmt.Errorf("github_issue channel: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/issues", githubAPIBase, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github_issue channel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := octx.httpClient
	if client == nil {
		return fmt.Errorf("github_issue channel: nil http client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github_issue channel: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github_issue channel: http %d: %s",
			resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

// validateGitHubRepo rejects any Repo value that doesn't look like a
// strict `owner/repo` pair. Called BEFORE we interpolate Repo into the
// issues URL — a stray `..` here would otherwise produce paths like
// /repos/../leak/issues that the GitHub API rejects with an opaque 404,
// hiding the actual operator error. Fail-fast at config-validation time.
func validateGitHubRepo(repo string) error {
	if repo == "" {
		return fmt.Errorf("github_issue channel: empty repo")
	}
	if !githubRepoPattern.MatchString(repo) {
		return fmt.Errorf("github_issue channel: repo %q must be owner/name "+
			"(alphanumeric, dot, dash, underscore only — no path segments)", repo)
	}
	// Belt-and-braces: the regex character class already excludes "..",
	// but explicit rejection of "../" / "/.." substrings keeps the
	// intent legible to a future reviewer.
	if strings.Contains(repo, "..") {
		return fmt.Errorf("github_issue channel: repo %q must not contain '..'", repo)
	}
	return nil
}

// buildGitHubRequest renders the issue body. Title gets a smart prefix
// + the worker name; body gets the full output plus the same metadata
// footer as the ClickUp channel so cross-tool tracing stays easy.
func buildGitHubRequest(octx outputContext, ch outputChannel) githubCreateIssueRequest {
	prefix := strings.TrimSpace(ch.TitlePrefix)
	title := fmt.Sprintf("%s · run %s", octx.workerName, shortRunID(octx.runID))
	if prefix != "" {
		title = fmt.Sprintf("%s %s", prefix, title)
	}
	return githubCreateIssueRequest{
		Title: snippet(title, githubTitleLimit),
		Body:  githubIssueBody(octx),
	}
}

// githubIssueBody renders the issue body markdown. Identical shape to
// the ClickUp variant so operators can mentally diff a worker that
// fans out to both surfaces.
func githubIssueBody(octx outputContext) string {
	var b strings.Builder
	b.WriteString(octx.output)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "- Worker: `%s`\n", octx.workerName)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", octx.runID)
	fmt.Fprintf(&b, "- Status: `%s`\n", octx.status)
	fmt.Fprintf(&b, "- Cost: $%.4f\n", octx.costUSD)
	fmt.Fprintf(&b, "- Tokens: %d in / %d out\n", octx.inputTokens, octx.outputTokens)
	return b.String()
}
