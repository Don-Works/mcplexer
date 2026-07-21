package gateway

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var (
	reGHRepoQualifier = regexp.MustCompile(`(?i)\brepo:([a-z0-9_.-]+)/([a-z0-9_.-]+)\b`)
	reGHOrgQualifier  = regexp.MustCompile(`(?i)\borg:([a-z0-9_.-]+)\b`)
)

// GitHubExtractor extracts org and repo resource identifiers from GitHub
// tool call arguments. It understands owner/repo fields, GitHub URLs,
// and search query qualifiers.
type GitHubExtractor struct{}

// Extract implements ScopeExtractor for GitHub tools.
func (GitHubExtractor) Extract(args json.RawMessage) map[string]map[string]struct{} {
	targets := newGHTargets()
	if len(args) == 0 {
		return targets.asMap()
	}

	var data any
	if err := json.Unmarshal(args, &data); err != nil {
		return targets.asMap()
	}
	walkGHArgs(data, &targets)
	return targets.asMap()
}

// ghTargets collects org and repo identifiers found in tool arguments.
type ghTargets struct {
	orgs  map[string]struct{}
	repos map[string]struct{}
}

func newGHTargets() ghTargets {
	return ghTargets{
		orgs:  map[string]struct{}{},
		repos: map[string]struct{}{},
	}
}

func (t ghTargets) addOrg(org string) {
	org = strings.ToLower(strings.TrimSpace(org))
	if org == "" {
		return
	}
	t.orgs[org] = struct{}{}
}

func (t ghTargets) addRepo(repo string) {
	repo = normalizeGHRepo(repo)
	if repo == "" {
		return
	}
	t.repos[repo] = struct{}{}
	t.addOrg(ghRepoOwner(repo))
}

// asMap converts the collected targets to the ScopeExtractor return format.
func (t ghTargets) asMap() map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{}, 2)
	if len(t.orgs) > 0 {
		result["org"] = t.orgs
	}
	if len(t.repos) > 0 {
		result["repo"] = t.repos
	}
	return result
}

func walkGHArgs(v any, targets *ghTargets) {
	switch val := v.(type) {
	case map[string]any:
		extractGHFromMap(val, targets)
		for _, child := range val {
			walkGHArgs(child, targets)
		}
	case []any:
		for _, item := range val {
			walkGHArgs(item, targets)
		}
	case string:
		extractGHFromString(val, targets)
	}
}

func extractGHFromMap(m map[string]any, targets *ghTargets) {
	owner := ghAsString(m["owner"])
	repo := ghAsString(m["repo"])
	if owner != "" && repo != "" {
		targets.addRepo(owner + "/" + repo)
	}

	if org := ghAsString(m["org"]); org != "" {
		targets.addOrg(org)
	}
	if org := ghAsString(m["organization"]); org != "" {
		targets.addOrg(org)
	}

	if fullName := ghAsString(m["full_name"]); fullName != "" {
		targets.addRepo(fullName)
	}
	if repoField := ghAsString(m["repository"]); repoField != "" {
		targets.addRepo(repoField)
	}
	if repoField := ghAsString(m["repository_name"]); repoField != "" {
		targets.addRepo(repoField)
	}

	if repoObj, ok := m["repository"].(map[string]any); ok {
		rOwner := ghAsString(repoObj["owner"])
		rName := ghAsString(repoObj["name"])
		if rOwner != "" && rName != "" {
			targets.addRepo(rOwner + "/" + rName)
		}
	}

	for _, key := range []string{"url", "html_url", "repository_url", "clone_url"} {
		if raw := ghAsString(m[key]); raw != "" {
			extractGHFromURL(raw, targets)
		}
	}

	for _, key := range []string{"query", "q", "search", "text"} {
		if raw := ghAsString(m[key]); raw != "" {
			extractGHFromQueryString(raw, targets)
		}
	}
}

func extractGHFromString(s string, targets *ghTargets) {
	extractGHFromURL(s, targets)
	extractGHFromQueryString(s, targets)
	if strings.Count(strings.TrimSpace(s), "/") == 1 {
		targets.addRepo(s)
	}
}

func extractGHFromQueryString(s string, targets *ghTargets) {
	for _, m := range reGHRepoQualifier.FindAllStringSubmatch(s, -1) {
		if len(m) == 3 {
			targets.addRepo(m[1] + "/" + m[2])
		}
	}
	for _, m := range reGHOrgQualifier.FindAllStringSubmatch(s, -1) {
		if len(m) == 2 {
			targets.addOrg(m[1])
		}
	}
}

func extractGHFromURL(raw string, targets *ghTargets) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	host := strings.ToLower(u.Host)
	if host != "github.com" && host != "www.github.com" && host != "api.github.com" {
		return
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		if parts[0] == "repos" && len(parts) >= 3 {
			targets.addRepo(parts[1] + "/" + parts[2])
			return
		}
		targets.addRepo(parts[0] + "/" + parts[1])
	}
}

func normalizeGHRepo(repo string) string {
	repo = strings.ToLower(strings.TrimSpace(repo))
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return ""
	}
	owner := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func ghRepoOwner(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

func ghAsString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
