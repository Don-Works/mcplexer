package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
)

type auditSSEHandler struct {
	bus *audit.Bus
}

func (h *auditSSEHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Disable the server's WriteTimeout for this long-lived SSE connection.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Read optional filters from query params. Parity with the /audit
	// REST surface for the dimensions that map to a scalar AuditRecord
	// field (exact-match). Latency / q / sort don't apply to a live
	// per-record push, so they're not honoured here.
	qWorkspace := r.URL.Query().Get("workspace_id")
	qTool := r.URL.Query().Get("tool_name")
	qStatus := r.URL.Query().Get("status")
	qExecution := r.URL.Query().Get("execution_id")
	qSession := r.URL.Query().Get("session_id")
	qActorKind := r.URL.Query().Get("actor_kind")
	qActorID := r.URL.Query().Get("actor_id")
	qServer := r.URL.Query().Get("downstream_server_id")
	qRouteRule := r.URL.Query().Get("route_rule_id")
	qClientType := r.URL.Query().Get("client_type")
	qErrorCode := r.URL.Query().Get("error_code")
	qTier := r.URL.Query().Get("tier")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch := h.bus.Subscribe()
	defer h.bus.Unsubscribe(ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			if !matchFilter(rec.WorkspaceID, qWorkspace) ||
				!matchFilter(rec.ToolName, qTool) ||
				!matchFilter(rec.Status, qStatus) ||
				!matchFilter(rec.ExecutionID, qExecution) ||
				!matchFilter(rec.SessionID, qSession) ||
				!matchFilter(rec.ActorKind, qActorKind) ||
				!matchFilter(rec.ActorID, qActorID) ||
				!matchFilter(rec.DownstreamServerID, qServer) ||
				!matchFilter(rec.RouteRuleID, qRouteRule) ||
				!matchFilter(rec.ClientType, qClientType) ||
				!matchFilter(rec.ErrorCode, qErrorCode) ||
				!matchFilter(rec.Tier, qTier) {
				continue
			}
			data, err := json.Marshal(rec)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			flusher.Flush()
		}
	}
}

// matchFilter returns true if the filter is empty or matches the value.
func matchFilter(value, filter string) bool {
	return filter == "" || value == filter
}
