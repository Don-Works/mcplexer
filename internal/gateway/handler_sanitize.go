package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// sanitizeToolResult walks the MCP CallToolResult, runs each text content
// item through sanitize.Process, and replaces the text with the
// (possibly enveloped) body.
//
// Audit events are emitted (one per denylist hit) carrying the matched
// pattern name and byte offsets — NEVER the matched snippet, to avoid
// leaking secrets-shaped content into the audit log.
//
// Returns the original result unchanged on any unmarshalling error; we
// would rather pass through unsanitized than fail a successful tool call
// because of a malformed downstream payload (this matches the existing
// posture in injectCacheMeta).
func (h *handler) sanitizeToolResult(
	ctx context.Context,
	result json.RawMessage,
	namespacedToolName string,
) json.RawMessage {
	if len(result) == 0 {
		return result
	}

	// Trust classification — trusted internal namespaces (mcpx__/task__/
	// memory__/secret__/mcplexer__ + safe mesh__ tools) skip sanitize
	// entirely. The envelope is only load-bearing on payloads that
	// ingest third-party / cross-peer / network content, so spending
	// CPU + tokens enveloping the gateway's own admin reads is pure tax.
	// See trust_classifier.go for the full table.
	trust := classifyTrust(namespacedToolName)
	if !trust.NeedsSanitize {
		return result
	}

	var parsed CallToolResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		// Not a CallToolResult shape — leave it alone.
		return result
	}
	if len(parsed.Content) == 0 {
		return result
	}

	mutated := false

	// Read the toggle from settings each call so the dashboard PUT
	// takes effect without restart. SettingsService.Load is cheap
	// (in-memory cache + DB fallback) so calling per tool-result is
	// fine. nil settingsSvc (test fixtures) → default false.
	envelopeAlways := false
	if h.settingsSvc != nil {
		envelopeAlways = h.settingsSvc.Load(ctx).SanitizerEnvelopeAlways
	}
	// Tools classified as peer-origin (mesh__receive) MUST be enveloped
	// regardless of the dashboard toggle — the wrapper is the marker
	// that the payload arrived over the mesh.
	if trust.ForceEnvelope {
		envelopeAlways = true
	}

	for i, item := range parsed.Content {
		if item.Type != "text" || item.Text == "" {
			continue
		}
		out := sanitize.Process(sanitize.ProcessOptions{
			Denylist:       h.sanitizer,
			Source:         trust.Source,
			Trust:          trust.TrustLevel,
			Body:           item.Text,
			EnvelopeAlways: envelopeAlways,
		})

		for _, m := range out.Matches {
			h.recordSanitizeEvent(ctx, namespacedToolName, m)
		}

		if out.Action == sanitize.ActionEnveloped && out.Body != item.Text {
			parsed.Content[i].Text = out.Body
			mutated = true
		}
	}

	if !mutated {
		return result
	}

	data, err := json.Marshal(parsed)
	if err != nil {
		slog.Warn("sanitize: re-marshal failed; returning unsanitized result",
			"tool", namespacedToolName, "error", err)
		return result
	}
	return data
}

// recordSanitizeEvent emits a single audit row for a denylist hit.
//
// We reuse the existing AuditRecord shape — ErrorMessage carries the
// human-readable summary, ParamsRedacted carries the structured detail
// (pattern name, byte offsets, source). The matched snippet is
// deliberately omitted; the snippet field on sanitize.Match exists for
// in-process diagnostics, not for persistence.
func (h *handler) recordSanitizeEvent(
	ctx context.Context,
	namespacedToolName string,
	m sanitize.Match,
) {
	if h.auditor == nil {
		return
	}

	detail, err := json.Marshal(map[string]any{
		"event":        sanitize.EventInjectionDetected,
		"pattern_name": m.Pattern,
		"start":        m.Start,
		"end":          m.End,
		"source":       "tool:" + namespacedToolName,
	})
	if err != nil {
		// Audit-event marshalling failure is non-fatal; the result is
		// still enveloped, the user is still protected.
		slog.Warn("sanitize: audit detail marshal failed", "error", err)
		return
	}

	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      time.Now(),
		SessionID:      h.sessions.sessionID(),
		ClientType:     h.sessions.clientType(),
		Model:          h.sessions.modelHint(),
		WorkspaceID:    h.currentWorkspaceID(ctx),
		WorkspaceName:  h.currentWorkspaceName(ctx),
		ToolName:       namespacedToolName,
		ParamsRedacted: detail,
		Status:         "sanitized",
		ErrorCode:      sanitize.EventInjectionDetected,
		ErrorMessage:   fmt.Sprintf("injection marker %q detected in tool output", m.Pattern),
		ExecutionID:    executionIDFromContext(ctx),
		SkillID:        skillIDPtr(ctx),
		ActorKind:      "user",
		ActorID:        h.sessions.sessionID(),
		CorrelationID:  executionIDFromContext(ctx),
	}

	if err := h.auditor.Record(ctx, rec); err != nil {
		slog.Error("sanitize: audit record failed", "error", err)
	}
}
