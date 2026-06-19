package compact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// makeGitHubIssue builds a realistic GitHub issue object with ~30 fields.
func makeGitHubIssue(num int, state string) map[string]any {
	return map[string]any{
		"id":      float64(10000 + num),
		"node_id": fmt.Sprintf("I_kwDOABC%d", num),
		"number":  float64(num),
		"title":   fmt.Sprintf("Issue #%d: something needs fixing", num),
		"state":   state,
		"body":    fmt.Sprintf("Description for issue %d", num),
		"user": map[string]any{
			"login":               "octocat",
			"id":                  float64(1),
			"node_id":             "MDQ6VXNlcjE=",
			"avatar_url":          "https://avatars.githubusercontent.com/u/1",
			"gravatar_id":         "",
			"url":                 "https://api.github.com/users/octocat",
			"html_url":            "https://github.com/octocat",
			"followers_url":       "https://api.github.com/users/octocat/followers",
			"following_url":       "https://api.github.com/users/octocat/following{/other_user}",
			"gists_url":           "https://api.github.com/users/octocat/gists{/gist_id}",
			"starred_url":         "https://api.github.com/users/octocat/starred{/owner}{/repo}",
			"subscriptions_url":   "https://api.github.com/users/octocat/subscriptions",
			"organizations_url":   "https://api.github.com/users/octocat/orgs",
			"repos_url":           "https://api.github.com/users/octocat/repos",
			"events_url":          "https://api.github.com/users/octocat/events{/privacy}",
			"received_events_url": "https://api.github.com/users/octocat/received_events",
			"type":                "User",
			"site_admin":          false,
		},
		"labels":             []any{},
		"assignee":           nil,
		"assignees":          []any{},
		"milestone":          nil,
		"locked":             false,
		"comments":           float64(num % 5),
		"created_at":         "2024-01-15T10:00:00Z",
		"updated_at":         "2024-01-16T14:30:00Z",
		"closed_at":          nil,
		"author_association": "MEMBER",
		"active_lock_reason": nil,
		"draft":              false,
		"pull_request":       nil,
		"state_reason":       nil,
		"timeline_url":       fmt.Sprintf("https://api.github.com/repos/o/r/issues/%d/timeline", num),
		"performed_via_app":  nil,
		"reactions": map[string]any{
			"url":         fmt.Sprintf("https://api.github.com/repos/o/r/issues/%d/reactions", num),
			"total_count": float64(0),
			"+1":          float64(0), "-1": float64(0),
			"laugh": float64(0), "hooray": float64(0), "confused": float64(0),
			"heart": float64(0), "rocket": float64(0), "eyes": float64(0),
		},
	}
}

func makeSlackMessage(i int) map[string]any {
	return map[string]any{
		"type":          "message",
		"subtype":       nil,
		"ts":            fmt.Sprintf("1700000%03d.000000", i),
		"user":          fmt.Sprintf("U%05d", i),
		"text":          fmt.Sprintf("Message number %d from the team", i),
		"thread_ts":     "",
		"reply_count":   float64(0),
		"reply_users":   []any{},
		"subscribed":    false,
		"last_read":     "",
		"is_locked":     false,
		"blocks":        []any{},
		"attachments":   []any{},
		"client_msg_id": fmt.Sprintf("uuid-%d", i),
		"team":          "T12345",
		"edited":        nil,
		"reactions":     nil,
		"pinned_to":     nil,
		"pinned_info":   nil,
	}
}

func makeLinearIssue(i int) map[string]any {
	return map[string]any{
		"id":              fmt.Sprintf("issue-%d", i),
		"identifier":      fmt.Sprintf("ENG-%d", 100+i),
		"title":           fmt.Sprintf("Linear issue %d", i),
		"description":     nil,
		"priority":        float64(i%4 + 1),
		"state":           map[string]any{"name": "In Progress", "type": "started"},
		"assignee":        nil,
		"labels":          []any{},
		"project":         nil,
		"cycle":           nil,
		"estimate":        nil,
		"dueDate":         nil,
		"completedAt":     nil,
		"canceledAt":      nil,
		"archivedAt":      nil,
		"autoClosedAt":    nil,
		"autoArchivedAt":  nil,
		"snoozedUntilAt":  nil,
		"startedTriageAt": nil,
		"triagedAt":       nil,
		"createdAt":       "2024-03-01T10:00:00Z",
		"updatedAt":       "2024-03-02T14:00:00Z",
		"teamId":          "team-abc",
	}
}

func makeDBRow(i int) map[string]any {
	return map[string]any{
		"id":         float64(i),
		"name":       fmt.Sprintf("user_%d", i),
		"email":      fmt.Sprintf("user%d@example.com", i),
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-01T00:00:00Z",
		"deleted_at": nil,
		"role":       "member",
		"avatar_url": nil,
		"bio":        "",
		"settings":   map[string]any{},
	}
}

func TestRealisticGitHubIssues(t *testing.T) {
	issues := make([]map[string]any, 10)
	for i := range issues {
		issues[i] = makeGitHubIssue(i+1, "open")
	}

	inputJSON, _ := json.Marshal(issues)
	c := New()
	compacted := c.CompactJSON(inputJSON)

	// Must be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal(compacted, &parsed); err != nil {
		t.Fatalf("compacted output not valid JSON: %v\n%s", err, truncate(string(compacted), 500))
	}

	// Must be columnar.
	if _, ok := parsed["_cols"]; !ok {
		t.Fatal("expected columnar _cols")
	}
	rows := parsed["_rows"].([]any)
	if len(rows) != 10 {
		t.Fatalf("expected 10 rows, got %d", len(rows))
	}

	// Null/empty columns should be gone.
	colsStr := colNamesFromParsed(t, parsed)
	droppedCols := []string{"assignee", "milestone", "closed_at",
		"active_lock_reason", "pull_request", "state_reason", "performed_via_app"}
	for _, dc := range droppedCols {
		if sliceContains(colsStr, dc) {
			t.Errorf("null column %q should be pruned", dc)
		}
	}

	// state=open should be in _fixed since all issues are open.
	if fixed, ok := parsed["_fixed"].(map[string]any); ok {
		if fixed["state"] != "open" {
			t.Error("state should be fixed as 'open'")
		}
	}

	// Token savings: compacted should be significantly smaller.
	ratio := float64(len(compacted)) / float64(len(inputJSON))
	t.Logf("GitHub issues: %d bytes → %d bytes (%.0f%% reduction)",
		len(inputJSON), len(compacted), (1-ratio)*100)
	if ratio > 0.6 {
		t.Errorf("expected >40%% byte reduction, got %.0f%%", (1-ratio)*100)
	}
}

func TestRealisticSlackMessages(t *testing.T) {
	msgs := make([]map[string]any, 8)
	for i := range msgs {
		msgs[i] = makeSlackMessage(i + 1)
	}

	inputJSON, _ := json.Marshal(msgs)
	c := New()
	compacted := c.CompactJSON(inputJSON)

	var parsed any
	if err := json.Unmarshal(compacted, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	m, ok := parsed.(map[string]any)
	if !ok {
		t.Fatal("expected columnar output")
	}

	colsStr := colNamesFromParsed(t, m)
	// Empty fields should be pruned.
	for _, dropped := range []string{"thread_ts", "last_read", "blocks", "attachments", "reply_users"} {
		if sliceContains(colsStr, dropped) {
			// Check it's not in _fixed either (it's empty, should just vanish).
			if fixed, ok := m["_fixed"].(map[string]any); ok {
				if _, inFixed := fixed[dropped]; !inFixed {
					t.Errorf("empty column %q should be pruned", dropped)
				}
			} else {
				t.Errorf("empty column %q should be pruned", dropped)
			}
		}
	}

	ratio := float64(len(compacted)) / float64(len(inputJSON))
	t.Logf("Slack messages: %d → %d bytes (%.0f%% reduction)",
		len(inputJSON), len(compacted), (1-ratio)*100)
}

func TestRealisticLinearIssues(t *testing.T) {
	issues := make([]map[string]any, 5)
	for i := range issues {
		issues[i] = makeLinearIssue(i + 1)
	}

	inputJSON, _ := json.Marshal(issues)
	c := New()
	compacted := c.CompactJSON(inputJSON)

	var parsed map[string]any
	if err := json.Unmarshal(compacted, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := parsed["_cols"]; !ok {
		t.Fatal("expected columnar format")
	}

	// Many null fields should be pruned.
	colsStr := colNamesFromParsed(t, parsed)
	nullCols := []string{"description", "assignee", "project", "cycle",
		"estimate", "dueDate", "completedAt", "canceledAt", "archivedAt"}
	for _, nc := range nullCols {
		if sliceContains(colsStr, nc) {
			t.Errorf("null column %q should be pruned", nc)
		}
	}
}

func TestRealisticDatabaseRows(t *testing.T) {
	rows := make([]map[string]any, 6)
	for i := range rows {
		rows[i] = makeDBRow(i + 1)
	}

	inputJSON, _ := json.Marshal(rows)
	c := New()
	compacted := c.CompactJSON(inputJSON)

	var parsed map[string]any
	if err := json.Unmarshal(compacted, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// role=member, created_at, updated_at should be in _fixed.
	if fixed, ok := parsed["_fixed"].(map[string]any); ok {
		if fixed["role"] != "member" {
			t.Error("role should be fixed as 'member'")
		}
	}

	// avatar_url (null), bio (""), settings ({}) should be pruned.
	colsStr := colNamesFromParsed(t, parsed)
	for _, dropped := range []string{"avatar_url", "bio", "settings", "deleted_at"} {
		if sliceContains(colsStr, dropped) {
			t.Errorf("empty column %q should be pruned", dropped)
		}
	}
}

func TestRealisticMCPEnvelope(t *testing.T) {
	issues := make([]map[string]any, 5)
	for i := range issues {
		issues[i] = makeGitHubIssue(i+1, "open")
	}
	issuesJSON, _ := json.Marshal(issues)
	envelope := fmt.Sprintf(
		`{"content":[{"type":"text","text":%s}]}`,
		mustQuoteJSON(string(issuesJSON)),
	)

	c := New()
	result := c.CompactToolResult(json.RawMessage(envelope))

	var env struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("invalid result: %v", err)
	}

	text := env.Content[0]["text"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("compacted text not valid JSON: %v", err)
	}
	if _, ok := parsed["_cols"]; !ok {
		t.Error("expected columnar format in MCP envelope")
	}

	ratio := float64(len(result)) / float64(len(envelope))
	t.Logf("MCP envelope: %d → %d bytes (%.0f%% reduction)",
		len(envelope), len(result), (1-ratio)*100)
}

func TestDataPreservationRoundTrip(t *testing.T) {
	c := New()
	issues := make([]map[string]any, 5)
	for i := range issues {
		issues[i] = map[string]any{
			"id":     float64(i + 1),
			"title":  fmt.Sprintf("Issue %d", i+1),
			"score":  float64(i)*1.5 + 0.5,
			"active": i%2 == 0,
			"tags":   []any{"bug", fmt.Sprintf("p%d", i)},
		}
	}

	inputJSON, _ := json.Marshal(issues)
	compacted := c.CompactJSON(inputJSON)

	var m map[string]any
	if err := json.Unmarshal(compacted, &m); err != nil {
		t.Fatal(err)
	}

	cols := m["_cols"].([]any)
	rows := m["_rows"].([]any)
	fixed, _ := m["_fixed"].(map[string]any)

	for i, r := range rows {
		row := r.([]any)
		rebuilt := make(map[string]any)
		for j, col := range cols {
			rebuilt[fmt.Sprintf("%v", col)] = row[j]
		}
		for k, v := range fixed {
			rebuilt[k] = v
		}

		orig := issues[i]
		for k, want := range orig {
			got, exists := rebuilt[k]
			if want == nil {
				continue
			}
			if !exists {
				t.Errorf("row %d: key %q lost", i, k)
				continue
			}
			wj, _ := json.Marshal(want)
			gj, _ := json.Marshal(got)
			if string(wj) != string(gj) {
				t.Errorf("row %d key %q: want %s, got %s", i, k, wj, gj)
			}
		}
	}
}

func mustQuoteJSON(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func colNamesFromParsed(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["_cols"]
	if !ok {
		return nil
	}
	arr := raw.([]any)
	out := make([]string, len(arr))
	for i, v := range arr {
		out[i] = fmt.Sprintf("%v", v)
	}
	return out
}

func TestFormatColumnarGitHubOutput(t *testing.T) {
	issues := make([]map[string]any, 5)
	for i := range issues {
		issues[i] = makeGitHubIssue(i+1, "open")
	}

	result := CompactArray(issues)
	table := FormatColumnar(result)
	if table == "" {
		t.Fatal("expected table output")
	}

	// Should have pipe-separated columns.
	lines := strings.Split(table, "\n")
	if len(lines) < 2 {
		t.Fatal("expected at least header + 1 row")
	}
	if !strings.Contains(lines[0], "[all:") && !strings.Contains(lines[1], "|") {
		t.Error("expected pipe-delimited table")
	}

	t.Logf("Formatted output (%d lines):\n%s", len(lines), table)
}
