package runner

import (
	"context"
	"time"
)

// recordOutputAudit emits the worker_output.emitted audit record for
// one channel dispatch. Failure→status=error with the underlying
// message attached. Computes the destination identifier from the
// channel config so the audit row can fingerprint where the output
// went without storing the raw URL/token (see destinationHash).
func (r *Runner) recordOutputAudit(ctx context.Context, octx outputContext, ch outputChannel, started time.Time, emitErr error) {
	durationMS := r.clock.Now().Sub(started).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	success := emitErr == nil
	errMsg := ""
	if emitErr != nil {
		errMsg = emitErr.Error()
	}
	r.emitAuditOutputEmitted(ctx, octx.workerID, octx.runID, ch.Type, channelDestination(ch), durationMS, success, errMsg)
}

// channelDestination returns the per-channel-type destination string
// that gets hashed into the audit row. Each branch picks the most
// identifying field that distinguishes "this destination" from "a
// different destination" — webhook URL for HTTP sinks, list_id for
// ClickUp, owner/repo for GitHub, etc. Unknown channel types and
// channels with no remote target (file, mesh) → empty string, which
// translates to an empty destination_hash in the audit row.
func channelDestination(ch outputChannel) string {
	switch ch.Type {
	case "webhook":
		return ch.URL
	case "slack_webhook":
		// Prefer the channel name when present (Slack incoming
		// webhooks can post to whichever channel the payload
		// references), else fall back to the webhook URL itself.
		if ch.Channel != "" {
			return ch.Channel
		}
		return ch.URL
	case "clickup_task":
		return ch.ListID
	case "github_issue":
		return ch.Repo
	default:
		// file / mesh / unknown channels have no addressable remote
		// destination; empty hash communicates "not applicable".
		return ""
	}
}
