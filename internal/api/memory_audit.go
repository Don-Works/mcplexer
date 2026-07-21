// memory_audit.go — REST↔MCP audit-parity emit helper for the memory
// handler. Lifted out of memory_handler.go to keep that file under the
// 300-line cap. The whole point of this helper is bug F053JE: REST
// memory mutations were silently bypassing the audit ledger because
// only the gateway's MCP dispatch path called audit.Logger.Record.
//
// LOAD-BEARING: the redactor (internal/audit/Redact) runs inside
// Logger.Record over rec.ParamsRedacted. Callers MUST pass the full
// argument payload (including the free-form `content` body on
// memory__save) so secret-shaped substrings are scrubbed before the
// row hits the store. Bypassing recordAudit and inserting directly
// into store.AuditStore would skip redaction entirely — don't.
package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/store"
)

// recordAudit emits an audit row mirroring the MCP-side shape. tool_name
// must be one of the memory__* vocab (memory__save, memory__invalidate,
// memory__forget, memory__pin, memory__unpin, memory__forget_by_source,
// memory__link_entity, memory__unlink_entity). params are marshalled
// to JSON and handed to Logger.Record — the redactor inside Record
// will scrub secret-looking substrings before the row hits the store.
//
// status: "success" or "error". errMsg is folded into ErrorMessage on
// error; the redactor scrubs that too (handler_audit.go comment H1).
//
// Best-effort: a failed audit insert is logged at warn-level by the
// Logger and we swallow the error here — the HTTP call has already
// returned by the time we record.
func (h *memoryHandler) recordAudit(
	ctx context.Context, toolName, status, errMsg string, params map[string]any, start time.Time,
) {
	if h.auditor == nil {
		return
	}
	paramsJSON, _ := json.Marshal(params)
	_ = h.auditor.Record(ctx, &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      start.UTC(),
		ClientType:     "api",
		ToolName:       toolName,
		ParamsRedacted: paramsJSON,
		Status:         status,
		ErrorMessage:   errMsg,
		LatencyMs:      int(time.Since(start) / time.Millisecond),
		CreatedAt:      time.Now().UTC(),
		ActorKind:      "api",
	})
}
