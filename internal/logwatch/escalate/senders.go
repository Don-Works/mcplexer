// senders.go — channel senders for the dispatcher. v1 wires
// gchat_webhook (zero-setup HTTP POST, webhook URL held as a
// scope-bound secret ref) and mesh (priority-mapped alert that also
// feeds googlechat space bindings + worker wake). telegram/whatsapp
// senders land with the M4 wiring.
package escalate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// SecretReader resolves scope-bound secret refs at send time only.
// Satisfied by *secrets.Manager.
type SecretReader interface {
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

var secretRefKeyRe = regexp.MustCompile(`^secret://([A-Za-z0-9_.-]+)$`)

// gchatChannelConfig is the config_json shape for kind=gchat_webhook:
// {"auth_scope_id": "<scope>", "webhook_ref": "secret://GCHAT_WEBHOOK_X"}.
type gchatChannelConfig struct {
	AuthScopeID string `json:"auth_scope_id"`
	WebhookRef  string `json:"webhook_ref"`
}

// GChatWebhookSender POSTs {"text": message} to a Google Chat
// incoming webhook. The URL is a credential: it exists only inside
// this Send call, resolved from the secrets store.
type GChatWebhookSender struct {
	Secrets SecretReader
	// Client defaults to a 10s-timeout client.
	Client *http.Client
}

func (s *GChatWebhookSender) Send(ctx context.Context, ch *store.MonitoringChannel, _ /* severity */, message string) error {
	var cfg gchatChannelConfig
	if err := json.Unmarshal([]byte(ch.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("escalate: channel %s config: %w", ch.Name, err)
	}
	m := secretRefKeyRe.FindStringSubmatch(cfg.WebhookRef)
	if m == nil || cfg.AuthScopeID == "" {
		return fmt.Errorf("escalate: channel %s needs auth_scope_id + webhook_ref (secret://KEY)", ch.Name)
	}
	url, err := s.Secrets.Get(ctx, cfg.AuthScopeID, m[1])
	if err != nil {
		return fmt.Errorf("escalate: resolve webhook ref: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"text": message})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSpace(string(url)), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("escalate: webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The webhook URL embeds key+token in its query and IS the credential.
		// A *url.Error stringifies it verbatim and it would land in slog (which
		// runs no redaction). Strip the resolved URL out of the error text.
		return fmt.Errorf("escalate: webhook post failed: %s",
			strings.ReplaceAll(err.Error(), strings.TrimSpace(string(url)), "[redacted-webhook-url]"))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("escalate: webhook status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// MeshPoster is the slice of *mesh.Manager the mesh sender needs.
type MeshPoster interface {
	Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}

// MeshSender emits the alert onto the workspace mesh: it wakes the
// log-watch worker (mesh trigger on tag:logwatch), reaches paired
// peers, and feeds googlechat space bindings via the notify bus.
type MeshSender struct {
	Mesh MeshPoster
	// WorkspaceMeta builds the session meta binding the message to the
	// channel's workspace. Wired at daemon boot.
	WorkspaceMeta func(workspaceID string) mesh.SessionMeta
}

// severityPriority maps monitoring severity onto mesh priority
// vocabulary (also what googlechat space MinPriority filters on).
func severityPriority(sev string) string {
	switch sev {
	case store.SeverityCritical:
		return "critical"
	case store.SeverityError:
		return "high"
	case store.SeverityWarn:
		return "normal"
	default:
		return "low"
	}
}

func (s *MeshSender) Send(ctx context.Context, ch *store.MonitoringChannel, severity, message string) error {
	_, err := s.Mesh.Send(ctx, s.WorkspaceMeta(ch.WorkspaceID), mesh.SendRequest{
		Kind:      "alert",
		Content:   message,
		Priority:  severityPriority(severity),
		Audience:  "*",
		Tags:      "logwatch," + severity,
		ActorKind: "system",
	})
	return err
}
