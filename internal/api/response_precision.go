// response_precision.go — precision-aware JSON response writer for the
// task surface. Wraps writeJSON with a `?precision=ns` opt-in so an
// agent that needs the full RFC3339Nano form can ask for it explicitly
// while the default response stays seconds-precision for LLM
// readability.
//
// Why a separate writer: the task structs (store.Task, store.TaskNote,
// store.TaskOffer) carry a custom MarshalJSON that truncates every
// time.Time to seconds. That gives "noiseless by default". A caller
// that opts into nanos calls writeJSONForRequest and we re-encode via
// the alias-typed bypass exposed from internal/store.
//
// Currently used directly only by handlers that hold a request handle
// and want the opt-in available; tasks_handler.go integration is
// scheduled to happen alongside the parallel agent's surface work and
// is intentionally not done here to avoid stepping on that worktree.

package api

import (
	"log/slog"
	"net/http"

	"github.com/don-works/mcplexer/internal/store"
)

// precisionNanos returns true when the request asks for full
// nanosecond precision via `?precision=ns`. Any other value (including
// "seconds", "s", missing, "") falls through to the seconds-truncating
// default.
func precisionNanos(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.URL.Query().Get("precision") {
	case "ns", "nano", "nanos", "nanoseconds":
		return true
	}
	return false
}

// writeJSONForRequest is the precision-aware analogue of writeJSON.
// It consults `?precision=ns` and dispatches to either the truncating
// default (custom MarshalJSON) or the alias-typed nanosecond bypass.
// Identical wire shape to writeJSON otherwise — same Content-Type,
// status, encoder.
func writeJSONForRequest(w http.ResponseWriter, r *http.Request, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, err := store.MarshalJSONWithPrecision(data, precisionNanos(r))
	if err != nil {
		slog.Error("failed to encode response", "error", err)
		return
	}
	if _, err := w.Write(body); err != nil {
		slog.Error("failed to write response", "error", err)
	}
}
