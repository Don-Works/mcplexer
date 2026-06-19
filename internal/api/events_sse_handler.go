package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/session"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// eventsSSEHandler multiplexes the always-on dashboard event streams onto a
// SINGLE SSE connection at GET /api/v1/events/stream.
//
// Why this exists: the gateway is served over plain http://, so browsers
// talk HTTP/1.1 and are capped at ~6 connections per origin. The dashboard
// used to open a SEPARATE EventSource per stream — notifications, approvals,
// sessions, secret-prompts, tasks — each permanently holding one of those
// slots. Combined with page-level streams (audit, worker-run) and the
// sidebar's polls, the pool exhausted and every subsequent request hung as
// "pending" with no recovery. Folding the always-on, low-volume, unfiltered
// streams into one connection leaves the rest of the budget free.
//
// Each event is written as a NAMED SSE event (`event: <channel>`) so the
// client can route it to the right per-channel subscribers. The payload is
// byte-for-byte the same JSON the per-stream endpoints emit, so client
// handlers are unchanged apart from where they attach.
//
// Deliberately NOT multiplexed here:
//   - audit  — page-scoped (audit page only) AND server-side filtered by
//     workspace/tool/status/execution/session; can be high-volume. Stays on
//     /api/v1/audit/stream.
//   - worker-run transcript tails — parameterized per {id}/{run_id}, include
//     text deltas, and self-terminate. Stays on
//     /api/v1/workers/{id}/runs/{run_id}/events. The low-volume run status /
//     usage events ARE multiplexed as the "workers" channel so list-level UI
//     can stay realtime without opening a second long-lived connection.
//
// All buses are optional. A nil bus yields a nil channel, and a receive on a
// nil channel never fires in select — so missing buses are simply absent
// channels, no guard needed.
type eventsSSEHandler struct {
	notifyBus   *notify.Bus
	approvalBus *approval.Bus
	sessionBus  *session.Bus
	secretBus   *ephemeral.Bus
	tasksBus    *tasks.Bus
	workerBus   *runner.RunBus
}

func (h *eventsSSEHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// SSE connections are long-lived; clear the write deadline so the
	// server's WriteTimeout (0 here, but be explicit) never kills them.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	emit := func(channel string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", channel, data)
		flusher.Flush()
	}

	var (
		notifyCh   <-chan notify.Event
		approvalCh <-chan approval.ApprovalEvent
		sessionCh  <-chan session.Event
		secretCh   <-chan ephemeral.Event
		tasksCh    <-chan tasks.Event
		workerCh   <-chan *runner.RunEvent
	)
	if h.notifyBus != nil {
		notifyCh = h.notifyBus.Subscribe()
		defer h.notifyBus.Unsubscribe(notifyCh)
	}
	if h.approvalBus != nil {
		approvalCh = h.approvalBus.Subscribe()
		defer h.approvalBus.Unsubscribe(approvalCh)
	}
	if h.sessionBus != nil {
		sessionCh = h.sessionBus.Subscribe()
		defer h.sessionBus.Unsubscribe(sessionCh)
	}
	if h.secretBus != nil {
		secretCh = h.secretBus.Subscribe()
		defer h.secretBus.Unsubscribe(secretCh)
	}
	if h.tasksBus != nil {
		var unsub func()
		tasksCh, unsub = h.tasksBus.Subscribe()
		defer unsub()
	}
	if h.workerBus != nil {
		workerCh = h.workerBus.Subscribe()
		defer h.workerBus.Unsubscribe(workerCh)
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-notifyCh:
			if !ok {
				notifyCh = nil
				continue
			}
			emit("notifications", evt)
		case evt, ok := <-approvalCh:
			if !ok {
				approvalCh = nil
				continue
			}
			emit("approvals", evt)
		case evt, ok := <-sessionCh:
			if !ok {
				sessionCh = nil
				continue
			}
			emit("sessions", evt)
		case evt, ok := <-secretCh:
			if !ok {
				secretCh = nil
				continue
			}
			emit("secrets", evt)
		case evt, ok := <-tasksCh:
			if !ok {
				tasksCh = nil
				continue
			}
			emit("tasks", evt)
		case evt, ok := <-workerCh:
			if !ok {
				workerCh = nil
				continue
			}
			if evt == nil || evt.Kind == runner.RunEventKindTextDelta {
				continue
			}
			// RunEvents (status/usage/tool_call) plus lightweight delegation_updated
			// signals (published by workers handler on create/review) are emitted
			// on the "workers" channel. DelegationsPage subscribes and refetches
			// the list so the UI stays live without manual refresh.
			emit("workers", evt)
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			flusher.Flush()
		}
	}
}
