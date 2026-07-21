// Package worker provides hexagonal adapters that bridge the worker runner's
// output channel dispatch to hexcore.OutputPort interfaces.
//
// Each output type (mesh, file, webhook, slack, clickup, github) becomes
// an OutputPort that CanDeliver checks by Action.Target.Channel.
package worker

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/hexcore"
)

// MeshOutputPort delivers Actions to the mesh network.
type MeshOutputPort struct {
	sendFunc func(ctx context.Context, content, priority, tags string, opts ...any) (string, error)
}

func NewMeshOutputPort(sendFunc func(ctx context.Context, content, priority, tags string, opts ...any) (string, error)) *MeshOutputPort {
	return &MeshOutputPort{sendFunc: sendFunc}
}

func (p *MeshOutputPort) Name() string { return "mesh" }

func (p *MeshOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "mesh"
}

func (p *MeshOutputPort) Deliver(ctx context.Context, action hexcore.Action) error {
	tags := "worker,output"
	for _, t := range action.Tags {
		tags += "," + t
	}
	_, err := p.sendFunc(ctx, action.Content, action.Priority, tags)
	return err
}

// FileOutputPort delivers Actions to the filesystem.
type FileOutputPort struct {
	writeFunc func(path, content, mode string) error
}

func NewFileOutputPort(writeFunc func(path, content, mode string) error) *FileOutputPort {
	return &FileOutputPort{writeFunc: writeFunc}
}

func (p *FileOutputPort) Name() string { return "file" }

func (p *FileOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "file"
}

func (p *FileOutputPort) Deliver(_ context.Context, action hexcore.Action) error {
	if action.Target.FilePath == "" {
		return fmt.Errorf("file output: empty path")
	}
	return p.writeFunc(action.Target.FilePath, action.Content, action.Target.Mode)
}

// WebhookOutputPort delivers Actions via HTTP POST.
type WebhookOutputPort struct {
	postFunc func(ctx context.Context, url string, headers map[string]string, body any) error
}

func NewWebhookOutputPort(postFunc func(ctx context.Context, url string, headers map[string]string, body any) error) *WebhookOutputPort {
	return &WebhookOutputPort{postFunc: postFunc}
}

func (p *WebhookOutputPort) Name() string { return "webhook" }

func (p *WebhookOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "webhook"
}

func (p *WebhookOutputPort) Deliver(ctx context.Context, action hexcore.Action) error {
	if action.Target.WebhookURL == "" {
		return fmt.Errorf("webhook output: empty URL")
	}
	body := map[string]any{"output": action.Content}
	if action.Target.IncludeMetadata {
		body["worker_id"] = action.WorkerID
		body["run_id"] = action.RunID
		body["status"] = action.Status
		body["cost_usd"] = action.CostUSD
		body["input_tokens"] = action.InputTokens
		body["output_tokens"] = action.OutputTokens
	}
	return p.postFunc(ctx, action.Target.WebhookURL, action.Target.Headers, body)
}

// SlackWebhookOutputPort delivers Actions as Slack messages.
type SlackWebhookOutputPort struct {
	postFunc func(ctx context.Context, url, channel, prefix, content string) error
}

func NewSlackWebhookOutputPort(postFunc func(ctx context.Context, url, channel, prefix, content string) error) *SlackWebhookOutputPort {
	return &SlackWebhookOutputPort{postFunc: postFunc}
}

func (p *SlackWebhookOutputPort) Name() string { return "slack_webhook" }

func (p *SlackWebhookOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "slack_webhook"
}

func (p *SlackWebhookOutputPort) Deliver(ctx context.Context, action hexcore.Action) error {
	if action.Target.SlackWebhook == "" {
		return fmt.Errorf("slack output: empty webhook URL")
	}
	return p.postFunc(ctx, action.Target.SlackWebhook, action.Target.SlackChannel, "", action.Content)
}

// ClickUpOutputPort delivers Actions as ClickUp tasks.
type ClickUpOutputPort struct {
	createFunc func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error
}

func NewClickUpOutputPort(createFunc func(ctx context.Context, listID, name, description, prefix string, headers map[string]string) error) *ClickUpOutputPort {
	return &ClickUpOutputPort{createFunc: createFunc}
}

func (p *ClickUpOutputPort) Name() string { return "clickup_task" }

func (p *ClickUpOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "clickup_task"
}

func (p *ClickUpOutputPort) Deliver(ctx context.Context, action hexcore.Action) error {
	if action.Target.ClickUpListID == "" {
		return fmt.Errorf("clickup output: empty list ID")
	}
	return p.createFunc(ctx, action.Target.ClickUpListID, action.Title, action.Content, "", action.Target.Headers)
}

// GitHubIssueOutputPort delivers Actions as GitHub issues.
type GitHubIssueOutputPort struct {
	createFunc func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error
}

func NewGitHubIssueOutputPort(createFunc func(ctx context.Context, repo, title, body, prefix string, headers map[string]string) error) *GitHubIssueOutputPort {
	return &GitHubIssueOutputPort{createFunc: createFunc}
}

func (p *GitHubIssueOutputPort) Name() string { return "github_issue" }

func (p *GitHubIssueOutputPort) CanDeliver(action hexcore.Action) bool {
	return action.Target.Channel == "github_issue"
}

func (p *GitHubIssueOutputPort) Deliver(ctx context.Context, action hexcore.Action) error {
	if action.Target.GitHubRepo == "" {
		return fmt.Errorf("github output: empty repo")
	}
	return p.createFunc(ctx, action.Target.GitHubRepo, action.Title, action.Content, "", action.Target.Headers)
}
