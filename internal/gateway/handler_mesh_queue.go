package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/store"
)

// meshOutboundQueue is the structured mesh__list_queue result. Every field is
// locally generated (message/peer IDs, our own dial errors) — no peer-authored
// free text — so nothing here is trust-wrapped. Agents index `queue[]`; `text`
// carries the same listing as a human render.
type meshOutboundQueue struct {
	Wired bool             `json:"wired"`
	Queue []meshQueueEntry `json:"queue"`
	Count int              `json:"count"`
	Peers int              `json:"peers"`
	Hint  string           `json:"hint"`
	Text  string           `json:"text"`
}

type meshQueueEntry struct {
	MessageID       string `json:"message_id"`
	TargetPeerID    string `json:"target_peer_id"`
	TargetPeerShort string `json:"target_peer_short"`
	Attempts        int    `json:"attempts"`
	EnqueuedAt      string `json:"enqueued_at"`
	EnqueuedAge     string `json:"enqueued_age"`
	NextAttemptAt   string `json:"next_attempt_at"`
	ExpiresAt       string `json:"expires_at"`
	LastError       string `json:"last_error,omitempty"`
}

// handleMeshListQueue surfaces the offline-delivery queue contents — every
// targeted `to_peer` mesh message that's been parked because the remote
// peer was unreachable at dispatch time. Admin-style read-only output:
// peer + age + attempts + next_attempt_at + last_error so an operator
// (or another agent) can triage what's been delayed. The wire shape is a
// JSON object with a structured `queue[]` array plus a `text` render.
//
// Empty queue is the steady state — an empty queue[] with a friendly text,
// not an error.
func (h *handler) handleMeshListQueue(ctx context.Context) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	q := h.mesh.OutboundQueue()
	if q == nil {
		return marshalJSONResult(meshOutboundQueue{
			Wired: false,
			Queue: []meshQueueEntry{},
			Hint:  "No offline-delivery queue is wired on this daemon (p2p disabled or no transport configured).",
			Text: "## Mesh Outbound Queue\n\nNo offline-delivery queue is wired " +
				"on this daemon (p2p disabled or no transport configured).\n",
		})
	}
	rows, err := q.ListPending(ctx)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(buildOutboundQueue(rows))
}

// buildOutboundQueue renders queued rows as a structured listing plus the
// human `text`. Rows are ordered by target peer then enqueue time so the
// array is deterministic and matches the prose grouping.
func buildOutboundQueue(rows []store.MeshOutbound) meshOutboundQueue {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TargetPeerID != rows[j].TargetPeerID {
			return rows[i].TargetPeerID < rows[j].TargetPeerID
		}
		return rows[i].EnqueuedAt.Before(rows[j].EnqueuedAt)
	})
	out := meshOutboundQueue{
		Wired: true,
		Queue: make([]meshQueueEntry, 0, len(rows)),
		Count: len(rows),
		Hint:  "Read-only triage. Messages drain automatically when the target peer reconnects or on the 30s background sweep.",
		Text:  formatMeshOutboundQueue(rows),
	}
	peers := make(map[string]struct{}, len(rows))
	now := time.Now().UTC()
	for _, r := range rows {
		peers[r.TargetPeerID] = struct{}{}
		out.Queue = append(out.Queue, meshQueueEntry{
			MessageID:       r.MessageID,
			TargetPeerID:    r.TargetPeerID,
			TargetPeerShort: shortPeerForQueue(r.TargetPeerID),
			Attempts:        r.Attempts,
			EnqueuedAt:      r.EnqueuedAt.UTC().Format(time.RFC3339),
			EnqueuedAge:     formatRelativeAge(now.Sub(r.EnqueuedAt)),
			NextAttemptAt:   r.NextAttemptAt.UTC().Format(time.RFC3339),
			ExpiresAt:       r.ExpiresAt.UTC().Format(time.RFC3339),
			LastError:       r.LastError,
		})
	}
	out.Peers = len(peers)
	return out
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
