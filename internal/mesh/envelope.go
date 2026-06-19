package mesh

import (
	"github.com/don-works/mcplexer/internal/idtrunc"
)

// ReceiveEnvelope is the structured JSON result of mesh__receive. The
// markdown formatter (FormatReceiveResultWithOptions) used to be the wire
// shape, but code-mode consumers auto-unwrap a single text block into the
// parsed value — a markdown string made `r.messages` silently undefined in
// agent JS. The envelope is gateway-constructed (trusted); only the free-text
// fields that carry cross-peer content are individually sanitized/wrapped by
// the gateway before marshalling.
type ReceiveEnvelope struct {
	Stats    ReceiveEnvelopeStats   `json:"stats"`
	Notices  []string               `json:"notices,omitempty"`
	Agents   []ReceiveEnvelopeAgent `json:"agents"`
	Messages []ReceiveEnvelopeMsg   `json:"messages"`
	// Hint documents the hydrate path so an agent holding only this
	// envelope knows how to fetch full bodies.
	Hint string `json:"hint"`
}

// ReceiveEnvelopeStats mirrors MeshStats with stable snake_case JSON names.
type ReceiveEnvelopeStats struct {
	ActiveAgents int `json:"active_agents"`
	LiveMessages int `json:"live_messages"`
	NewForYou    int `json:"new_for_you"`
}

// ReceiveEnvelopeAgent is one row of the active-agent directory.
type ReceiveEnvelopeAgent struct {
	Name     string `json:"name"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status,omitempty"`
	Origin   string `json:"origin"`
	LastSeen string `json:"last_seen"`
	Self     bool   `json:"self,omitempty"`
}

// ReceiveEnvelopeMsg is one bounded message preview. Preview carries at most
// the configured preview byte budget; full content is an explicit
// mesh__hydrate / mesh__thread read.
type ReceiveEnvelopeMsg struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Priority string `json:"priority,omitempty"`
	// ActorKind tags what fired the send: "agent" | "worker" | "user" |
	// "peer-import" | "system". Lets consumers visually separate (or
	// filter out, via the actor_kinds receive params) worker chatter.
	ActorKind    string `json:"actor_kind,omitempty"`
	From         string `json:"from"`
	Age          string `json:"age"`
	Preview      string `json:"preview"`
	ContentBytes int    `json:"content_bytes"`
	Truncated    bool   `json:"truncated,omitempty"`
	ReplyCount   int    `json:"reply_count,omitempty"`
	ThreadRoot   string `json:"thread_root,omitempty"`
	Tags         string `json:"tags,omitempty"`
}

// ReceiveEnvelopeOptions configures BuildReceiveEnvelope. The two hooks let
// the gateway apply its sanitize/trust pipeline per-field without this
// package importing the sanitizer:
//
//   - WrapUntrusted is applied to message previews — by-definition
//     cross-peer free text, always wrapped in the trust marker.
//   - ScanText is applied to short identity fields (agent names, roles,
//     statuses, sender labels) — scanned for injection markers and wrapped
//     only on a hit, so clean identifiers stay clean.
//
// Nil hooks are identity functions (used by tests and slim builds).
type ReceiveEnvelopeOptions struct {
	ContentPreviewBytes int
	Notices             []string
	WrapUntrusted       func(string) string
	ScanText            func(string) string
}

// BuildReceiveEnvelope converts a ReceiveResult into the structured wire
// shape. Empty result sets marshal as empty arrays (never null) so agents
// can rely on `.messages.length`.
func BuildReceiveEnvelope(r *ReceiveResult, selfSessionID string, opts ReceiveEnvelopeOptions) *ReceiveEnvelope {
	wrap := opts.WrapUntrusted
	if wrap == nil {
		wrap = func(s string) string { return s }
	}
	scan := opts.ScanText
	if scan == nil {
		scan = func(s string) string { return s }
	}
	previewBytes := NormalizeReceivePreviewBytes(opts.ContentPreviewBytes)

	env := &ReceiveEnvelope{
		Stats: ReceiveEnvelopeStats{
			ActiveAgents: r.Stats.ActiveAgents,
			LiveMessages: r.Stats.LiveMessages,
			NewForYou:    r.Stats.NewForYou,
		},
		Notices:  opts.Notices,
		Agents:   make([]ReceiveEnvelopeAgent, 0, len(r.Agents)),
		Messages: make([]ReceiveEnvelopeMsg, 0, len(r.Messages)),
		Hint:     receiveEnvelopeHint(r.TaskEventsExcluded),
	}

	for _, a := range r.Agents {
		name := a.Name
		if name == "" {
			name = a.ClientType
		}
		if name == "" {
			name = idtrunc.Short(a.SessionID, 8)
		}
		env.Agents = append(env.Agents, ReceiveEnvelopeAgent{
			Name:     scan(name),
			Role:     scan(a.Role),
			Status:   scan(a.Status),
			Origin:   formatOrigin(a.Origin),
			LastSeen: formatRelativeTime(a.LastSeenAt),
			Self:     a.SessionID == selfSessionID,
		})
	}

	for _, msg := range r.Messages {
		preview, truncated := previewContent(msg.Content, previewBytes)
		env.Messages = append(env.Messages, ReceiveEnvelopeMsg{
			ID:           msg.ID,
			Kind:         msg.Kind,
			Priority:     msg.Priority,
			ActorKind:    msg.ActorKind,
			From:         scan(messageSender(&msg)),
			Age:          formatRelativeTime(msg.CreatedAt),
			Preview:      wrap(preview),
			ContentBytes: len(msg.Content),
			Truncated:    truncated,
			ReplyCount:   msg.ReplyCount,
			ThreadRoot:   msg.ThreadRoot,
			Tags:         msg.Tags,
		})
	}

	return env
}

// receiveEnvelopeHint documents (a) where the body lives — consumers kept
// reading messages[].content and getting undefined; the field is `preview` —
// (b) the hydrate path, and (c) the default task_event exclusion + opt-in
// when it was applied to this read.
func receiveEnvelopeHint(taskEventsExcluded bool) string {
	hint := "messages[].preview carries the message body (there is no .content field); " +
		"previews are bounded — mesh__hydrate({message_id}) or mesh__thread({thread_id}) for full content"
	if taskEventsExcluded {
		hint += ". kind=task_event messages are excluded by default — pass kinds:\"task_event\" to opt in"
	}
	return hint
}
