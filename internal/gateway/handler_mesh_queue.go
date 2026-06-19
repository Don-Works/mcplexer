package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/store"
)

// handleMeshListQueue surfaces the offline-delivery queue contents — every
// targeted `to_peer` mesh message that's been parked because the remote
// peer was unreachable at dispatch time. Admin-style read-only output:
// peer + age + attempts + next_attempt_at + last_error so an operator
// (or another agent) can triage what's been delayed.
//
// Empty queue is the steady state — render a friendly "nothing pending"
// rather than an error.
func (h *handler) handleMeshListQueue(ctx context.Context) ([]byte, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	q := h.mesh.OutboundQueue()
	if q == nil {
		return marshalToolResult(
			"## Mesh Outbound Queue\n\nNo offline-delivery queue is wired " +
				"on this daemon (p2p disabled or no transport configured).\n",
		), nil
	}
	rows, err := q.ListPending(ctx)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalToolResult(formatMeshOutboundQueue(rows)), nil
}

// formatMeshOutboundQueue renders the queue as a human-readable Markdown
// table grouped by target peer. Stable ordering: peer ID alphabetic, then
// enqueued_at ascending (oldest first within a peer).
func formatMeshOutboundQueue(rows []store.MeshOutbound) string {
	if len(rows) == 0 {
		return "## Mesh Outbound Queue\n\nNo messages queued for offline peers.\n"
	}

	byPeer := make(map[string][]store.MeshOutbound)
	for _, r := range rows {
		byPeer[r.TargetPeerID] = append(byPeer[r.TargetPeerID], r)
	}
	peers := make([]string, 0, len(byPeer))
	for p := range byPeer {
		peers = append(peers, p)
	}
	sort.Strings(peers)

	now := time.Now().UTC()
	var b strings.Builder
	fmt.Fprintf(&b, "## Mesh Outbound Queue\n\n%d message(s) queued across %d peer(s).\n",
		len(rows), len(peers))
	for _, p := range peers {
		peerRows := byPeer[p]
		sort.Slice(peerRows, func(i, j int) bool {
			return peerRows[i].EnqueuedAt.Before(peerRows[j].EnqueuedAt)
		})
		fmt.Fprintf(&b, "\n### %s (%d)\n", shortPeerForQueue(p), len(peerRows))
		for _, r := range peerRows {
			writeQueueRow(&b, r, now)
		}
	}
	b.WriteString("\nMessages drain automatically when the target peer reconnects, or on the 30s background sweep.\n")
	return b.String()
}

func writeQueueRow(b *strings.Builder, r store.MeshOutbound, now time.Time) {
	age := formatRelativeAge(now.Sub(r.EnqueuedAt))
	nextIn := r.NextAttemptAt.Sub(now)
	nextWhen := "now"
	if nextIn > 0 {
		nextWhen = fmt.Sprintf("in %s", formatRelativeAge(nextIn))
	}
	expiresIn := formatRelativeAge(r.ExpiresAt.Sub(now))
	lastErr := r.LastError
	if lastErr == "" {
		lastErr = "—"
	}
	fmt.Fprintf(b,
		"- %s — queued %s ago, %d attempt(s), next retry %s, expires in %s, last error: %s\n",
		r.MessageID, age, r.Attempts, nextWhen, expiresIn, truncateOneLine(lastErr, 120),
	)
}

// shortPeerForQueue renders a peer ID with a short, readable middle ellipsis.
func shortPeerForQueue(p string) string {
	return idtrunc.Ellipsis(p, 8, 8)
}

// truncateOneLine trims to a max length + collapses internal whitespace so
// a multi-line dial error renders cleanly in the queue listing.
func truncateOneLine(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
