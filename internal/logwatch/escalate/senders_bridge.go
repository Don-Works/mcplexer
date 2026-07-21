// senders_bridge.go — channel senders that ride other mcplexer
// subsystems: telegram (built-in manager) and whatsapp (openwa
// downstream via the gateway's internal tool-dispatch path, so the
// chat id stays a secret:// ref that the gateway substitutes at
// dispatch — plaintext never touches this package).
package escalate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// TelegramBridge is the slice of *telegram.Manager the sender needs.
type TelegramBridge interface {
	SendByChatID(ctx context.Context, chatID, text, priority string) error
}

// telegramChannelConfig: {"chat_id": "<internal mcplexer chat id>"} —
// an internal binding id, not a credential.
type telegramChannelConfig struct {
	ChatID string `json:"chat_id"`
}

// TelegramSender delivers to a workspace-bound telegram chat.
type TelegramSender struct {
	Bridge TelegramBridge
}

func (s *TelegramSender) Send(ctx context.Context, ch *store.MonitoringChannel, severity, message string) error {
	var cfg telegramChannelConfig
	if err := json.Unmarshal([]byte(ch.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("escalate: channel %s config: %w", ch.Name, err)
	}
	if cfg.ChatID == "" {
		return fmt.Errorf("escalate: channel %s needs chat_id", ch.Name)
	}
	return s.Bridge.SendByChatID(ctx, cfg.ChatID, message, severityPriority(severity))
}

// ToolCaller dispatches one downstream tool call through the gateway
// pipeline (secret substitution, audit, routing included).
type ToolCaller interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// whatsappChannelConfig: chat_id_ref MUST be a secret:// ref (the chat
// id is the personal number — PII); session_id names the OpenWA
// session; tool defaults to openwa__send_text.
type whatsappChannelConfig struct {
	ChatIDRef string `json:"chat_id_ref"`
	SessionID string `json:"session_id"`
	Tool      string `json:"tool"`
}

// WhatsAppSender delivers via the OpenWA downstream. The secret:// ref
// is passed VERBATIM as the chat_id argument — the gateway resolves it
// against the downstream's auth scope at dispatch, so the number never
// exists in this process's monitoring path.
type WhatsAppSender struct {
	Caller ToolCaller
}

func (s *WhatsAppSender) Send(ctx context.Context, ch *store.MonitoringChannel, _ /* severity */, message string) error {
	var cfg whatsappChannelConfig
	if err := json.Unmarshal([]byte(ch.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("escalate: channel %s config: %w", ch.Name, err)
	}
	if !secretRefKeyRe.MatchString(cfg.ChatIDRef) {
		return fmt.Errorf("escalate: channel %s chat_id_ref must be a secret:// ref", ch.Name)
	}
	tool := strings.TrimSpace(cfg.Tool)
	if tool == "" {
		tool = "openwa__send_text"
	}
	if tool != "openwa__send_text" {
		return fmt.Errorf("escalate: channel %s has unsupported whatsapp tool", ch.Name)
	}
	args, err := json.Marshal(map[string]string{
		"chat_id":    cfg.ChatIDRef,
		"session_id": cfg.SessionID,
		"text":       message,
	})
	if err != nil {
		return err
	}
	if _, err := s.Caller.CallTool(ctx, tool, args); err != nil {
		return fmt.Errorf("escalate: whatsapp send: %w", err)
	}
	return nil
}
