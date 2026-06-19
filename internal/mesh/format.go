package mesh

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/store"
)

// formatOrigin renders a MeshAgent.Origin for human-readable output.
//
//	"local"             → "local"
//	"peer:12D3...XYZ"   → "peer:XYZ" (last 10 chars)
//	"" (legacy rows)    → "local"
func formatOrigin(origin string) string {
	if origin == "" || origin == store.MeshAgentOriginLocal {
		return store.MeshAgentOriginLocal
	}
	if strings.HasPrefix(origin, store.MeshAgentOriginPeerPrefix) {
		peerID := origin[len(store.MeshAgentOriginPeerPrefix):]
		if len(peerID) > 10 {
			peerID = peerID[len(peerID)-10:]
		}
		return store.MeshAgentOriginPeerPrefix + peerID
	}
	return origin
}

// FormatSendResult formats a send result as a compact one-liner.
func FormatSendResult(msg *store.MeshMessage) string {
	return fmt.Sprintf("Sent %s [%s] — expires %s | id: %s",
		msg.Kind, msg.Priority, formatRelativeTime(msg.ExpiresAt), msg.ID)
}

// FormatReceiveResult formats a receive result as compact text.
func FormatReceiveResult(r *ReceiveResult, selfSessionID string) string {
	return FormatReceiveResultWithOptions(r, selfSessionID, ReceiveFormatOptions{
		ContentPreviewBytes: DefaultReceivePreviewBytes,
	})
}

type ReceiveFormatOptions struct {
	ContentPreviewBytes int
	Notices             []string
}

// FormatReceiveResultWithOptions formats a receive result using bounded
// per-message previews. Full content is available via explicit hydrate/thread
// reads, which have their own hard byte caps.
func FormatReceiveResultWithOptions(r *ReceiveResult, selfSessionID string, opts ReceiveFormatOptions) string {
	previewBytes := NormalizeReceivePreviewBytes(opts.ContentPreviewBytes)
	var b strings.Builder

	// Status line.
	fmt.Fprintf(&b, "## Mesh Status\n")
	fmt.Fprintf(&b, "%d agents active | %d live messages | %d new for you\n",
		r.Stats.ActiveAgents, r.Stats.LiveMessages, r.Stats.NewForYou)
	for _, notice := range opts.Notices {
		notice = strings.TrimSpace(notice)
		if notice != "" {
			fmt.Fprintf(&b, "%s\n", notice)
		}
	}

	// Active agents.
	if len(r.Agents) > 0 {
		fmt.Fprintf(&b, "\n## Active Agents\n")
		for _, a := range r.Agents {
			name := a.Name
			if name == "" {
				name = a.ClientType
			}
			if name == "" {
				name = idtrunc.Short(a.SessionID, 8)
			}
			suffix := ""
			if a.Role != "" {
				suffix = "/" + a.Role
			}
			self := ""
			if a.SessionID == selfSessionID {
				self = " (you)"
			}
			ago := formatRelativeTime(a.LastSeenAt)
			fmt.Fprintf(&b, "- %s%s [%s] — last seen %s%s\n",
				name, suffix, formatOrigin(a.Origin), ago, self)
		}
	}

	// Messages.
	if len(r.Messages) > 0 {
		fmt.Fprintf(&b, "\n## Messages (%d)\n", len(r.Messages))
		for _, msg := range r.Messages {
			icon := kindIcon(msg.Kind, msg.Priority)
			from := msg.AgentName
			if from == "" {
				from = idtrunc.Short(msg.SessionID, 8)
			}
			ago := formatRelativeTime(msg.CreatedAt)
			content, truncated := previewContent(msg.Content, previewBytes)
			if truncated {
				content = fmt.Sprintf("%s... [truncated %d/%d bytes; hydrate: %s]",
					content, previewBytes, len(msg.Content), msg.ID)
			}
			fmt.Fprintf(&b, "%s %s %q — from %s, %s [%s]\n",
				icon, strings.ToUpper(msg.Kind), content, from, ago, msg.ID)
			if msg.ReplyCount > 0 {
				fmt.Fprintf(&b, "   %d replies\n", msg.ReplyCount)
			}
		}
	} else {
		fmt.Fprintf(&b, "\nNo new messages.\n")
	}

	return b.String()
}

// FormatHydrateResult formats one explicit message read. Content is still
// bounded because historical rows may predate the send-side cap.
func FormatHydrateResult(msg *store.MeshMessage, maxContentBytes int) string {
	if msg == nil {
		return "Message not found.\n"
	}
	contentBytes := NormalizeHydrateContentBytes(maxContentBytes)
	content, truncated := previewContent(msg.Content, contentBytes)
	var b strings.Builder
	fmt.Fprintf(&b, "## Mesh Message %s\n", msg.ID)
	fmt.Fprintf(&b, "kind: %s | priority: %s | from: %s | created: %s | content_bytes: %d\n",
		msg.Kind, msg.Priority, messageSender(msg), msg.CreatedAt.Format(time.RFC3339), len(msg.Content))
	if msg.ReplyTo != "" {
		fmt.Fprintf(&b, "reply_to: %s\n", msg.ReplyTo)
	}
	if msg.ThreadRoot != "" {
		fmt.Fprintf(&b, "thread_root: %s\n", msg.ThreadRoot)
	}
	if truncated {
		fmt.Fprintf(&b, "truncated: true (%d/%d bytes)\n", contentBytes, len(msg.Content))
	}
	fmt.Fprintf(&b, "\n%s\n", content)
	return b.String()
}

// FormatThreadResult formats an explicit thread read with one shared content
// budget across the thread.
func FormatThreadResult(msgs []store.MeshMessage, maxContentBytes int) string {
	contentBudget := NormalizeHydrateContentBytes(maxContentBytes)
	var b strings.Builder
	fmt.Fprintf(&b, "## Mesh Thread\n")
	fmt.Fprintf(&b, "%d message(s) | content_budget_bytes: %d\n", len(msgs), contentBudget)
	if len(msgs) == 0 {
		return b.String()
	}
	remaining := contentBudget
	for _, msg := range msgs {
		allow := remaining
		if allow < 0 {
			allow = 0
		}
		content, truncated := previewContent(msg.Content, allow)
		used := len(content)
		remaining -= used
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(&b, "\n### %s %s [%s]\n", strings.ToUpper(msg.Kind), msg.ID, msg.Priority)
		fmt.Fprintf(&b, "from: %s | created: %s | content_bytes: %d\n",
			messageSender(&msg), msg.CreatedAt.Format(time.RFC3339), len(msg.Content))
		if msg.ReplyTo != "" {
			fmt.Fprintf(&b, "reply_to: %s\n", msg.ReplyTo)
		}
		if truncated {
			fmt.Fprintf(&b, "truncated: true (thread content budget exhausted; hydrate: %s)\n", msg.ID)
		}
		fmt.Fprintf(&b, "\n%s\n", content)
	}
	return b.String()
}

func messageSender(msg *store.MeshMessage) string {
	if msg == nil {
		return ""
	}
	if msg.AgentName != "" {
		return msg.AgentName
	}
	return idtrunc.Short(msg.SessionID, 8)
}

func previewContent(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", s != ""
	}
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	if cut <= 0 {
		return "", true
	}
	return s[:cut], true
}

func kindIcon(kind, priority string) string {
	if priority == "critical" {
		return "[!!!]"
	}
	if priority == "low" {
		return "[.]"
	}
	switch kind {
	case "question":
		return "[?]"
	case "finding":
		return "[>]"
	case "task":
		return "[=]"
	case "result":
		return "[<]"
	case "event":
		return "[~]"
	case "alert":
		return "[!]"
	case "reply":
		return "[@]"
	default:
		return "[~]"
	}
}

func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
