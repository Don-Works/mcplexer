package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"

	"github.com/google/uuid"
)

// notifyBusAdapter forwards an ephemeral.Notifier Publish call into a
// notify.Bus event so the existing UI toast + native OS notification path
// fires for new secret prompts.
type notifyBusAdapter struct{ bus *notify.Bus }

func (a *notifyBusAdapter) Publish(id, label, reason string) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(notify.Event{
		MessageID: id,
		Source:    "secret",
		AgentName: "mcplexer",
		Role:      "secret-prompt",
		Kind:      "secret_prompt",
		Priority:  "high",
		Title:     "Secret requested: " + label,
		Body:      reason,
		Tags:      "secret",
		CreatedAt: time.Now().UTC(),
	})
}

// buildSecretPromptManager constructs an ephemeral.Manager backed by the
// SQLite store. dataDir is the directory holding the database; the manager
// creates dataDir/secrets/ephemeral with 0700 perms. Returns (nil, nil, nil)
// on failure (the feature is optional; the daemon keeps running).
func buildSecretPromptManager(
	ctx context.Context,
	db *sqlite.DB,
	notifyBus *notify.Bus,
	auditor *audit.Logger,
	dbDSN string,
) (*ephemeral.Manager, *ephemeral.Bus) {
	dataDir := filepath.Dir(dbDSN)
	bus := ephemeral.NewBus()
	notifier := &notifyBusAdapter{bus: notifyBus}

	hook := makeSecretPromptAuditHook(auditor)

	mgr, err := ephemeral.New(ctx, db, dataDir, notifier, bus, hook)
	if err != nil {
		slog.Warn("secret prompts: manager init failed", "error", err)
		return nil, nil
	}
	mgr.Start(ctx)
	slog.Info("secret prompts: manager started", "dir",
		filepath.Join(dataDir, "secrets", "ephemeral"))
	return mgr, bus
}

// makeSecretPromptAuditHook records lifecycle events to the audit log. Only
// metadata is recorded — never the secret value or the file path.
func makeSecretPromptAuditHook(auditor *audit.Logger) ephemeral.AuditHook {
	if auditor == nil {
		return nil
	}
	return func(event string, req ephemeral.PromptRequest, id string) {
		now := time.Now().UTC()
		status := "pending"
		errMsg := ""
		switch event {
		case "submitted":
			status = "success"
		case "cancelled":
			status = "blocked"
			errMsg = "user_cancelled"
		case "timeout":
			status = "blocked"
			errMsg = "timeout"
		}
		// reason+label are non-sensitive metadata; nothing here can leak
		// the secret value or the file path.
		params := []byte(`{"reason":` + jsonString(req.Reason) +
			`,"label":` + jsonString(req.Label) +
			`,"requester":` + jsonString(req.Requester) +
			`,"delete_on_read":` + boolStr(req.DeleteOnRead) +
			`,"event":` + jsonString(event) +
			`,"prompt_id":` + jsonString(id) + `}`)
		rec := &store.AuditRecord{
			ID:             uuid.NewString(),
			Timestamp:      now,
			SessionID:      req.Requester,
			ToolName:       "secret__prompt",
			ParamsRedacted: params,
			Status:         status,
			ErrorMessage:   errMsg,
			CreatedAt:      now,
		}
		if err := auditor.Record(context.Background(), rec); err != nil {
			slog.Warn("secret prompt audit", "event", event, "error", err)
		}
	}
}

// jsonString renders s as a JSON string literal without leaking edge cases.
// kept inline to avoid an extra import in this small helper.
func jsonString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				out = append(out, '?')
			} else {
				out = append(out, []byte(string(r))...)
			}
		}
	}
	out = append(out, '"')
	return string(out)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
