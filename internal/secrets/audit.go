// Package secrets — audit.go owns the per-operation audit emission for the
// secrets Manager (Get/Put/Delete/List). The auditor field is nil-safe; when
// unset every helper here is a no-op, so the package keeps working in tests
// and bootstrap paths that don't wire an audit pipeline.
//
// Plaintext secret values NEVER appear in any field of the emitted
// AuditRecord. ParamsRedacted carries only {scope_id, key}; values are
// deliberately omitted at the source so even a misconfigured redactor
// can't leak them.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// Auditor is the narrow surface of *audit.Logger that the secrets package
// needs. Defined here (rather than importing internal/audit) to keep the
// dependency direction one-way and let tests pass a slice-collecting fake.
type Auditor interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// Event constants — kebab-cased, period-separated. Match the dashboard's
// group-by-tool_name convention so secret traffic surfaces as its own
// class alongside worker_* and mesh_* events.
const (
	auditEventSecretRead   = "secret.read"
	auditEventSecretWrite  = "secret.write"
	auditEventSecretDelete = "secret.delete"
	auditEventSecretList   = "secret.list"
)

// auditStatusOK / auditStatusError are the two values dashboards filter on.
const (
	auditStatusOK    = "ok"
	auditStatusError = "error"
)

// SetAuditor wires an audit logger so every Get/Put/Delete/List call
// emits an audit row. Nil disables audit (no-op fire-and-forget).
func (m *Manager) SetAuditor(a Auditor) {
	if m == nil {
		return
	}
	m.auditor = a
}

// emitAudit records one secrets operation. Best-effort: a failed Record
// is logged but never propagated to the caller — audit failures must
// NEVER break the user's secret access path.
//
// SECURITY: this helper deliberately accepts only scopeID + key + event
// metadata. Callers MUST NOT pass the plaintext value at any layer; the
// signature gives them no way to.
func (m *Manager) emitAudit(ctx context.Context, scopeID, key, event, status, errMsg string) {
	if m == nil || m.auditor == nil {
		return
	}
	payload := map[string]string{"scope_id": scopeID}
	if key != "" {
		payload["key"] = key
	}
	raw, _ := json.Marshal(payload)

	now := time.Now().UTC()
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      now,
		ClientType:     "secrets",
		SessionID:      "scope:" + scopeID,
		ToolName:       event,
		ParamsRedacted: raw,
		AuthScopeID:    scopeID,
		Status:         normalizeAuditStatus(status),
		ErrorMessage:   errMsg,
		CorrelationID:  audit.FromCtx(ctx),
		CreatedAt:      now,
		ActorKind:      "secrets",
		ActorID:        scopeID,
	}

	// Attribute the row to the triggering caller when the gateway stamped
	// one onto ctx — an MCP agent calling secret__list_refs, or a downstream
	// dispatch resolving a `secret://` ref. This makes the audit table's
	// Workspace + Session columns line up with every other tool row instead
	// of showing "-" / "scope:<id>". AuthScopeID stays set regardless, so
	// the scope name still surfaces as the row's badge enrichment.
	//
	// No attribution (dashboard / API / CLI initiated, no MCP session) keeps
	// the scope-attributed placeholder above, which is the honest
	// representation for those detached emissions.
	if attr, ok := audit.AttributionFromCtx(ctx); ok {
		rec.SessionID = attr.SessionID
		rec.ClientType = attr.ClientType
		rec.Model = attr.Model
		rec.WorkspaceID = attr.WorkspaceID
		rec.WorkspaceName = attr.WorkspaceName
		rec.Subpath = attr.Subpath
		if attr.ActorKind != "" {
			rec.ActorKind = attr.ActorKind
			rec.ActorID = attr.ActorID
		}
	}

	if err := m.auditor.Record(ctx, rec); err != nil {
		// SECURITY: never log `key` here. The slog destination is not
		// guaranteed to be redacted (0644 log files, journald, stderr),
		// while the audit row's ParamsRedacted goes through the audit
		// redaction layer. The key name itself ("STRIPE_LIVE_KEY",
		// "PROD_DB_PASSWORD") is sensitive and must stay out of plain-
		// text logs.
		slog.Warn("secrets audit record failed",
			"event", event, "scope_id", scopeID, "error", err)
	}
}

// normalizeAuditStatus folds empty status into "ok" so dashboards that
// filter on status= can still find these rows.
func normalizeAuditStatus(s string) string {
	if s == "" {
		return auditStatusOK
	}
	return s
}

// auditStatusFor maps an operation error to (status, message). nil err =>
// ok; ErrNotFound surfaces with a stable phrase the UI can group on.
func auditStatusFor(err error) (string, string) {
	if err == nil {
		return auditStatusOK, ""
	}
	if isNotFound(err) {
		return auditStatusError, "key not found"
	}
	return auditStatusError, err.Error()
}

// isNotFound matches store.ErrNotFound directly or via fmt.Errorf("%w")
// wrapping. Kept as a tiny helper so the audit path stays readable.
func isNotFound(err error) bool {
	return err != nil && errors.Is(err, store.ErrNotFound)
}
