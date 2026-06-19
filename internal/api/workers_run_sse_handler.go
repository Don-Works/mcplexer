// workers_run_sse_handler.go streams a single WorkerRun's mid-flight
// events over Server-Sent Events. Subscribes to the runner's RunBus
// when one is wired (live status / text_delta / tool_call / usage
// frames as the runner publishes them) and falls back to a one-shot
// status snapshot when no bus is available.
//
// Backwards compatibility: every consumer of the v1 endpoint expected
// `event: status\ndata: <WorkerRun JSON>\n\n` frames on every status
// transition. That contract is preserved — additional event names
// (text_delta, tool_call, usage) are layered on top, and clients that
// only listen for "status" continue to work.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// workerRunSSEMaxDuration caps how long one subscription stays open
// for. After this the handler closes — the client reconnects, gets a
// fresh snapshot of the (potentially now-terminal) state, and naturally
// stops. Prevents pathological never-finalizing runs from holding a
// connection open forever.
const workerRunSSEMaxDuration = 15 * time.Minute

// streamRun serves GET /api/v1/workers/{id}/runs/{run_id}/events.
// On connect, emits one `event: status` frame with the persisted row so
// reconnecting clients always have a starting point. Then either:
//
//   - subscribes to the runner's RunBus and forwards every matching
//     event live (the wired path), or
//   - if no bus is wired or the run is already terminal at connect time,
//     closes the connection after the initial snapshot.
func (h *workersHandler) streamRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Read the persisted row first so we can 404 cleanly before any SSE
	// headers go out (once they're written the status code is locked).
	run, err := h.svc.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrWorkerRunNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "failed to read run", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	writeRunStatusEvent(w, flusher, run)
	if isTerminalRunStatus(run.Status) {
		return
	}

	// Subscribe to the live bus. When the bus is unwired (CLI tests,
	// stdio mode) we close after the snapshot — no live updates, but
	// the client at least sees the starting state.
	bus := h.svc.RunBus()
	if bus == nil {
		return
	}
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	deadline := time.NewTimer(workerRunSSEMaxDuration)
	defer deadline.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.RunID != runID {
				continue
			}
			writeRunBusEvent(w, flusher, ev)
			// Terminal status closes the stream — the client's
			// EventSource onclose fires and downstream code stops the
			// hook.
			if ev.Kind == runner.RunEventKindStatus && ev.Run != nil && isTerminalRunStatus(ev.Run.Status) {
				return
			}
		}
	}
}

// writeRunStatusEvent serialises the persisted run row as a status
// frame. Used for the initial snapshot before bus subscription.
func writeRunStatusEvent(w http.ResponseWriter, flusher http.Flusher, run *store.WorkerRun) {
	data, err := json.Marshal(run)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
	flusher.Flush()
}

// writeRunBusEvent emits one bus event as an SSE frame. The event
// name on the wire matches RunEvent.Kind so frontend listeners can
// addEventListener("status" | "text_delta" | "tool_call" | "usage").
// For status events the data payload is the inner WorkerRun JSON
// (backwards compat with v1 listeners); for everything else it's the
// full RunEvent envelope.
func writeRunBusEvent(w http.ResponseWriter, flusher http.Flusher, ev *runner.RunEvent) {
	var payload []byte
	var err error
	if ev.Kind == runner.RunEventKindStatus && ev.Run != nil {
		payload, err = json.Marshal(ev.Run)
	} else {
		payload, err = json.Marshal(ev)
	}
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload)
	flusher.Flush()
}

// isTerminalRunStatus returns true when the run status indicates the
// loop has stopped (the SSE subscription can close). Mirrors the
// runner package's status constants so we don't import the runner
// here for a tiny string-set check.
func isTerminalRunStatus(status string) bool {
	switch status {
	case "success", "failure", "cap_exceeded", "rejected", "awaiting_approval", "cancelled":
		return true
	default:
		return false
	}
}
