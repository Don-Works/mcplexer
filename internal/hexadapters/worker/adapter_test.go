package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/hexcore"
)

func TestMeshOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches mesh channel", "mesh", true},
		{"rejects file channel", "file", false},
		{"rejects empty channel", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewMeshOutputPort(func(ctx context.Context, content, priority, tags string, opts ...any) (string, error) {
				return "", nil
			})
			if p.Name() != "mesh" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "mesh")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestMeshOutputPort_Deliver(t *testing.T) {
	var gotContent, gotPriority, gotTags string
	p := NewMeshOutputPort(func(ctx context.Context, content, priority, tags string, opts ...any) (string, error) {
		gotContent = content
		gotPriority = priority
		gotTags = tags
		return "msg-1", nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Content:  "hello mesh",
		Priority: "high",
		Tags:     []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotContent != "hello mesh" {
		t.Fatalf("content = %q, want %q", gotContent, "hello mesh")
	}
	if gotPriority != "high" {
		t.Fatalf("priority = %q, want %q", gotPriority, "high")
	}
	if gotTags != "worker,output,alpha,beta" {
		t.Fatalf("tags = %q, want %q", gotTags, "worker,output,alpha,beta")
	}
}

func TestFileOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches file channel", "file", true},
		{"rejects mesh channel", "mesh", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewFileOutputPort(func(path, content, mode string) error { return nil })
			if p.Name() != "file" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "file")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestFileOutputPort_Deliver(t *testing.T) {
	var gotPath, gotContent, gotMode string
	p := NewFileOutputPort(func(path, content, mode string) error {
		gotPath = path
		gotContent = content
		gotMode = mode
		return nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Content: "file data",
		Target: hexcore.ActionTarget{
			FilePath: "/tmp/out.txt",
			Mode:     "0644",
		},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotPath != "/tmp/out.txt" {
		t.Fatalf("path = %q, want %q", gotPath, "/tmp/out.txt")
	}
	if gotContent != "file data" {
		t.Fatalf("content = %q, want %q", gotContent, "file data")
	}
	if gotMode != "0644" {
		t.Fatalf("mode = %q, want %q", gotMode, "0644")
	}
}

func TestFileOutputPort_DeliverEmptyPath(t *testing.T) {
	p := NewFileOutputPort(func(path, content, mode string) error { return nil })
	err := p.Deliver(context.Background(), hexcore.Action{Content: "data"})
	if err == nil {
		t.Fatal("Deliver() expected error for empty path")
	}
}

func TestWebhookOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches webhook channel", "webhook", true},
		{"rejects slack channel", "slack_webhook", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewWebhookOutputPort(func(ctx context.Context, url string, headers map[string]string, body any) error {
				return nil
			})
			if p.Name() != "webhook" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "webhook")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestWebhookOutputPort_DeliverWithMetadata(t *testing.T) {
	var gotBody map[string]any
	p := NewWebhookOutputPort(func(ctx context.Context, url string, headers map[string]string, body any) error {
		gotBody = body.(map[string]any)
		return nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Content:        "payload",
		WorkerID:       "w-1",
		RunID:          "r-1",
		Status:         "done",
		CostUSD:        0.1,
		InputTokens:    100,
		OutputTokens:   200,
		Target: hexcore.ActionTarget{
			WebhookURL:      "https://example.com/hook",
			IncludeMetadata: true,
			Headers:         map[string]string{"X-Token": "abc"},
		},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotBody["output"] != "payload" {
		t.Fatalf("body[output] = %v, want %q", gotBody["output"], "payload")
	}
	if gotBody["worker_id"] != "w-1" {
		t.Fatalf("body[worker_id] = %v, want %q", gotBody["worker_id"], "w-1")
	}
	if gotBody["cost_usd"] != 0.1 {
		t.Fatalf("body[cost_usd] = %v, want 0.1", gotBody["cost_usd"])
	}
}

func TestWebhookOutputPort_DeliverEmptyURL(t *testing.T) {
	p := NewWebhookOutputPort(func(ctx context.Context, url string, headers map[string]string, body any) error {
		return nil
	})
	err := p.Deliver(context.Background(), hexcore.Action{Content: "data"})
	if err == nil {
		t.Fatal("Deliver() expected error for empty URL")
	}
}

func TestSlackWebhookOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches slack_webhook", "slack_webhook", true},
		{"rejects webhook", "webhook", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewSlackWebhookOutputPort(func(ctx context.Context, url, channel, prefix, content string) error {
				return nil
			})
			if p.Name() != "slack_webhook" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "slack_webhook")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestSlackWebhookOutputPort_Deliver(t *testing.T) {
	var gotURL, gotChannel, gotContent string
	p := NewSlackWebhookOutputPort(func(ctx context.Context, url, channel, prefix, content string) error {
		gotURL = url
		gotChannel = channel
		gotContent = content
		return nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Content: "slack message",
		Target: hexcore.ActionTarget{
			SlackWebhook: "https://hooks.slack.com/test",
			SlackChannel: "#general",
		},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotURL != "https://hooks.slack.com/test" {
		t.Fatalf("url = %q, want %q", gotURL, "https://hooks.slack.com/test")
	}
	if gotChannel != "#general" {
		t.Fatalf("channel = %q, want %q", gotChannel, "#general")
	}
	if gotContent != "slack message" {
		t.Fatalf("content = %q, want %q", gotContent, "slack message")
	}
}

func TestSlackWebhookOutputPort_DeliverEmptyURL(t *testing.T) {
	p := NewSlackWebhookOutputPort(func(ctx context.Context, url, channel, prefix, content string) error {
		return nil
	})
	err := p.Deliver(context.Background(), hexcore.Action{Content: "data"})
	if err == nil {
		t.Fatal("Deliver() expected error for empty webhook URL")
	}
}

func TestClickUpOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches clickup_task", "clickup_task", true},
		{"rejects github_issue", "github_issue", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewClickUpOutputPort(func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error {
				return nil
			})
			if p.Name() != "clickup_task" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "clickup_task")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestClickUpOutputPort_Deliver(t *testing.T) {
	var gotListID, gotName, gotDesc string
	p := NewClickUpOutputPort(func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error {
		gotListID = listID
		gotName = name
		gotDesc = description
		return nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Title:   "New Task",
		Content: "task body",
		Target: hexcore.ActionTarget{
			ClickUpListID: "list-123",
			Headers:       map[string]string{"Authorization": "Bearer tok"},
		},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotListID != "list-123" {
		t.Fatalf("listID = %q, want %q", gotListID, "list-123")
	}
	if gotName != "New Task" {
		t.Fatalf("name = %q, want %q", gotName, "New Task")
	}
	if gotDesc != "task body" {
		t.Fatalf("description = %q, want %q", gotDesc, "task body")
	}
}

func TestClickUpOutputPort_DeliverEmptyListID(t *testing.T) {
	p := NewClickUpOutputPort(func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error {
		return nil
	})
	err := p.Deliver(context.Background(), hexcore.Action{Title: "t", Content: "c"})
	if err == nil {
		t.Fatal("Deliver() expected error for empty list ID")
	}
}

func TestGitHubIssueOutputPort(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		wantMatch bool
	}{
		{"matches github_issue", "github_issue", true},
		{"rejects clickup_task", "clickup_task", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewGitHubIssueOutputPort(func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error {
				return nil
			})
			if p.Name() != "github_issue" {
				t.Fatalf("Name() = %q, want %q", p.Name(), "github_issue")
			}
			got := p.CanDeliver(hexcore.Action{Target: hexcore.ActionTarget{Channel: tt.channel}})
			if got != tt.wantMatch {
				t.Fatalf("CanDeliver() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestGitHubIssueOutputPort_Deliver(t *testing.T) {
	var gotRepo, gotTitle, gotBody string
	p := NewGitHubIssueOutputPort(func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error {
		gotRepo = repo
		gotTitle = title
		gotBody = body
		return nil
	})

	err := p.Deliver(context.Background(), hexcore.Action{
		Title:   "Bug Report",
		Content: "it broke",
		Target: hexcore.ActionTarget{
			GitHubRepo: "owner/repo",
			Headers:    map[string]string{"Authorization": "Bearer ghp_xxx"},
		},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if gotRepo != "owner/repo" {
		t.Fatalf("repo = %q, want %q", gotRepo, "owner/repo")
	}
	if gotTitle != "Bug Report" {
		t.Fatalf("title = %q, want %q", gotTitle, "Bug Report")
	}
	if gotBody != "it broke" {
		t.Fatalf("body = %q, want %q", gotBody, "it broke")
	}
}

func TestGitHubIssueOutputPort_DeliverEmptyRepo(t *testing.T) {
	p := NewGitHubIssueOutputPort(func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error {
		return nil
	})
	err := p.Deliver(context.Background(), hexcore.Action{Title: "t", Content: "c"})
	if err == nil {
		t.Fatal("Deliver() expected error for empty repo")
	}
}

func TestPortErrors(t *testing.T) {
	wantErr := errors.New("network error")

	t.Run("mesh", func(t *testing.T) {
		p := NewMeshOutputPort(func(ctx context.Context, content, priority, tags string, opts ...any) (string, error) {
			return "", wantErr
		})
		err := p.Deliver(context.Background(), hexcore.Action{Content: "x"})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})

	t.Run("webhook", func(t *testing.T) {
		p := NewWebhookOutputPort(func(ctx context.Context, url string, headers map[string]string, body any) error {
			return wantErr
		})
		err := p.Deliver(context.Background(), hexcore.Action{Target: hexcore.ActionTarget{WebhookURL: "https://x"}})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})

	t.Run("slack", func(t *testing.T) {
		p := NewSlackWebhookOutputPort(func(ctx context.Context, url, channel, prefix, content string) error {
			return wantErr
		})
		err := p.Deliver(context.Background(), hexcore.Action{Target: hexcore.ActionTarget{SlackWebhook: "https://x"}})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})

	t.Run("clickup", func(t *testing.T) {
		p := NewClickUpOutputPort(func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error {
			return wantErr
		})
		err := p.Deliver(context.Background(), hexcore.Action{Target: hexcore.ActionTarget{ClickUpListID: "x"}})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})

	t.Run("github", func(t *testing.T) {
		p := NewGitHubIssueOutputPort(func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error {
			return wantErr
		})
		err := p.Deliver(context.Background(), hexcore.Action{Target: hexcore.ActionTarget{GitHubRepo: "x/y"}})
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})
}
