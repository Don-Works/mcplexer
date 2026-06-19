package runner

import (
	"context"
	"fmt"
	"log/slog"
)

// signalKind values used in the worker.* lifecycle stream.
const (
	signalStarted          = "worker.started"
	signalToolCall         = "worker.tool_call"
	signalFinished         = "worker.finished"
	signalAwaitingApproval = "worker.awaiting_approval"
)

// emitSignal pushes one lifecycle signal through the mesh sender and
// returns the resulting mesh message ID. Errors are logged but never
// propagated: a degraded mesh must not fail a successful worker run.
// Returns an empty string when the mesh isn't wired.
func (r *Runner) emitSignal(ctx context.Context, workerID, runID string, msg MeshOutbound) string {
	if r.mesh == nil {
		return ""
	}
	msg.WorkerID = workerID
	msg.RunID = runID
	id, err := r.mesh.Send(ctx, msg)
	if err != nil {
		slog.Warn("worker mesh signal failed",
			"worker_id", workerID,
			"run_id", runID,
			"kind", msg.Kind,
			"tags", msg.Tags,
			"error", err,
		)
		return ""
	}
	return id
}

// emitStarted fires the worker.started event at the top of a run.
//
//nolint:unused // start signal hook retained for re-enabling live worker telemetry.
func (r *Runner) emitStarted(ctx context.Context, workerID, runID, workerName string) string {
	return r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "event",
		Priority: "low",
		Tags:     "worker,started," + signalKindTag(signalStarted),
		Content:  fmt.Sprintf("worker %q started (run %s)", workerName, runID),
	})
}

// emitStartedFromState fires worker.started enriched with trigger
// metadata so observers can attribute the run. Schedule-driven runs
// emit the same content as emitStarted; mesh / manual runs get the
// "triggered by …" suffix and a chain-depth tag where applicable.
func (r *Runner) emitStartedFromState(
	ctx context.Context, workerID, runID, workerName string, s *loopState,
) string {
	content := fmt.Sprintf("worker %q started (run %s)", workerName, runID)
	tags := "worker,started," + signalKindTag(signalStarted)
	if s != nil && s.triggerKind != "" && s.triggerKind != "schedule" {
		tags += ",trigger_" + safeTag(s.triggerKind)
		switch s.triggerKind {
		case "mesh":
			source := s.triggerSourcePeer
			if source == "" {
				source = "local"
			}
			content += fmt.Sprintf(" — mesh-triggered by %s (msg %s, depth %d)",
				source, s.triggerMessageID, s.triggerChainDepth)
		case "manual":
			content += " — manually triggered"
		}
		if s.triggerChainDepth > 0 {
			tags += ",chain-depth:" + safeTag(formatInt(s.triggerChainDepth))
		}
	}
	return r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "event",
		Priority: "low",
		Tags:     tags,
		Content:  content,
	})
}

// formatInt is a tiny helper kept here so signals.go stays a tight unit
// without dragging in strconv across files that wrap many helpers.
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	neg := n < 0
	if neg {
		n = -n
	}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// emitToolCall fires per dispatched tool call.
func (r *Runner) emitToolCall(ctx context.Context, workerID, runID, toolName string) string {
	return r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "event",
		Priority: "low",
		Tags:     "worker,tool_call," + signalKindTag(signalToolCall) + "," + safeTag(toolName),
		Content:  fmt.Sprintf("worker tool call: %s", toolName),
	})
}

// emitFinished fires at the end of a run. Failures escalate to
// kind=alert+priority=high and set NotifyUser=true so dashboard/SSE
// notification surfaces can show them; chat bridges may still triage
// lifecycle chatter before paging humans. Success / cap_exceeded stay at
// event/low and don't notify (output-channel emissions handle the
// success-path notify_user flag explicitly).
func (r *Runner) emitFinished(ctx context.Context, workerID, runID, status, workerName, summary string) string {
	kind := "event"
	priority := "low"
	notifyUser := false
	if status == StatusFailure {
		kind = "alert"
		priority = "high"
		notifyUser = true
	}
	content := fmt.Sprintf("worker %q finished (run %s) status=%s", workerName, runID, status)
	if summary != "" {
		content += " — " + summary
	}
	return r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:       kind,
		Priority:   priority,
		Tags:       "worker,finished," + signalKindTag(signalFinished) + "," + safeTag(status),
		Content:    content,
		NotifyUser: notifyUser,
	})
}

// emitAwaitingApproval fires when a propose-mode worker hits a
// WriteClass tool — high priority because it blocks progress.
func (r *Runner) emitAwaitingApproval(ctx context.Context, workerID, runID, workerName, toolName string) string {
	return r.emitSignal(ctx, workerID, runID, MeshOutbound{
		Kind:     "alert",
		Priority: "high",
		Tags:     "worker,awaiting_approval," + signalKindTag(signalAwaitingApproval) + "," + safeTag(toolName),
		Content: fmt.Sprintf("worker %q is awaiting approval for write tool %q (run %s)",
			workerName, toolName, runID),
	})
}

// safeTag flattens user-controlled strings into a tag-safe slug. We
// strip commas (the tag separator) and whitespace; everything else
// passes through so operators can still grep for the original value.
func safeTag(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' || c == ' ' || c == '\t' || c == '\n' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// signalKindTag returns the dotted signal kind ("worker.started") as a
// flat tag fragment ("worker_started") so it sorts cleanly in the mesh
// tag column without colliding with the comma separator.
func signalKindTag(kind string) string {
	out := make([]byte, 0, len(kind))
	for i := 0; i < len(kind); i++ {
		c := kind[i]
		if c == '.' {
			c = '_'
		}
		out = append(out, c)
	}
	return string(out)
}
