package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// Logger writes audit records with parameter redaction.
type Logger struct {
	store store.AuditStore
	scope store.AuthScopeStore
	bus   *Bus
}

// NewLogger creates an audit Logger. The bus parameter is optional (nil-safe).
func NewLogger(auditStore store.AuditStore, scopeStore store.AuthScopeStore, bus *Bus) *Logger {
	return &Logger{store: auditStore, scope: scopeStore, bus: bus}
}

// Record redacts sensitive parameters and inserts the audit record.
// When rec.CorrelationID is empty the value is populated from ctx (via
// FromCtx) so callers that didn't explicitly set it inherit the
// ambient id for free.
//
// The CorrelationID field is also stamped into ParamsRedacted under
// "correlation_id" as a defensive fallback — should the audit_records
// column be missing (older schema, parallel migration not yet landed)
// the value still survives in the JSON payload and dashboards can
// recover it without a re-emit.
func (l *Logger) Record(ctx context.Context, rec *store.AuditRecord) error {
	if rec.CorrelationID == "" {
		rec.CorrelationID = FromCtx(ctx)
	}
	if rec.CorrelationID != "" {
		rec.ParamsRedacted = ensureCorrelationInParams(rec.ParamsRedacted, rec.CorrelationID)
	}

	hints, err := l.loadRedactionHints(ctx, rec.AuthScopeID)
	if err != nil {
		return fmt.Errorf("load redaction hints: %w", err)
	}

	if len(rec.ParamsRedacted) > 0 {
		rec.ParamsRedacted = Redact(rec.ParamsRedacted, hints)
	}
	// ErrorMessage often carries adapter stderr (claude_cli wraps up
	// to 256 bytes of subprocess stderr into the wrapped error). A
	// prompt-injected OAuth-refresh failure or API-key error would
	// otherwise land verbatim in audit_records.error_message and
	// persist until pruning — H1 in the security audit.
	if rec.ErrorMessage != "" {
		rec.ErrorMessage = RedactString(rec.ErrorMessage, hints)
	}

	if err := l.store.InsertAuditRecord(ctx, rec); err != nil {
		return fmt.Errorf("insert audit record: %w", err)
	}
	if l.bus != nil {
		l.bus.Publish(rec)
	}
	return nil
}

// ensureCorrelationInParams folds correlation_id into the
// ParamsRedacted JSON. Idempotent — if the key is already present the
// payload is returned unchanged. A malformed (non-object) payload is
// preserved as-is so we never lose data on a fallback path.
func ensureCorrelationInParams(params json.RawMessage, id string) json.RawMessage {
	if id == "" {
		return params
	}
	if len(params) == 0 {
		out, _ := json.Marshal(map[string]string{"correlation_id": id})
		return out
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil || obj == nil {
		return params // not an object — leave it alone
	}
	if _, ok := obj["correlation_id"]; ok {
		return params
	}
	enc, err := json.Marshal(id)
	if err != nil {
		return params
	}
	obj["correlation_id"] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return params
	}
	return out
}

// loadRedactionHints fetches per-scope redaction hints from the auth scope.
func (l *Logger) loadRedactionHints(ctx context.Context, authScopeID string) ([]string, error) {
	if authScopeID == "" {
		return nil, nil
	}

	scope, err := l.scope.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return nil, nil // scope not found is non-fatal for audit
	}

	if len(scope.RedactionHints) == 0 {
		return nil, nil
	}

	var hints []string
	if err := json.Unmarshal(scope.RedactionHints, &hints); err != nil {
		return nil, nil // malformed hints is non-fatal
	}
	return hints, nil
}
