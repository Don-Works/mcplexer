package hexcore

import "time"

type Event struct {
	ID        string
	Source    string
	Timestamp time.Time

	Kind     string
	Content  string
	Metadata map[string]any

	WorkspaceID string
	SessionID   string
	Audience    string
	Priority    string
	Tags        []string
	ReplyTo     string

	SenderName      string
	SenderRole      string
	IsAuthenticated bool
	PairingCode     string
}

type Action struct {
	ID        string
	Source    string
	Timestamp time.Time

	Kind     string
	Content  string
	Title    string
	Metadata map[string]any

	Target     ActionTarget
	Priority   string
	Tags       []string
	ReplyTo    string
	NotifyUser bool

	WorkerID     string
	RunID        string
	Status       string
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

type ActionTarget struct {
	Channel         string
	ChatID          string
	SpaceID         string
	SlackChannel    string
	SlackWebhook    string
	ClickUpListID   string
	GitHubRepo      string
	WebhookURL      string
	FilePath        string
	MeshAudience    string
	ToPeer          string
	Headers         map[string]string
	Mode            string
	SecretScopeID   string
	IncludeMetadata bool
}
