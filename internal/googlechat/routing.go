package googlechat

import (
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
)

// RoutingDecision captures how an inbound Google Chat message should be
// translated into a mesh.SendRequest (or skipped entirely). Mirrors
// telegram.RoutingDecision for parity.
type RoutingDecision struct {
	Skip       bool
	SkipReason string
	Send       mesh.SendRequest
}

// RoutingConfig is bridge-wide configuration driven from settings.
type RoutingConfig struct {
	// DefaultAudience applied to fresh (non-reply, non-@role) messages.
	// "*" broadcasts to all agents in the workspace; "role:<name>" targets a
	// specific agent role; "" falls back to "*".
	DefaultAudience string
}

// SessionPlaceholderPrefix is the sentinel used in RoutingDecision.Send.Audience
// when the caller must substitute the author's session id.
const SessionPlaceholderPrefix = "mesh-msg:"

// ExtractMeshMessageIDFromPlaceholder returns the mesh message id embedded in
// the placeholder, or "" if the audience isn't a placeholder.
func ExtractMeshMessageIDFromPlaceholder(audience string) string {
	if after, ok := strings.CutPrefix(audience, SessionPlaceholderPrefix); ok {
		return after
	}
	return ""
}

// DecideInbound turns a normalised IncomingMessage + listenMode + resolved
// reply target into a RoutingDecision. Pure: no I/O. listenMode is "mention"
// (default — only forward when @-mentioned or replied-to in groups) or
// "all" (every group message forwards).
//
// Cases, in priority order:
//  1. Native reply to a known bot message → direct reply (audience sentinel
//     gets substituted by the caller via DB lookup).
//  2. Fresh message starting with "@rolename" → route to that role.
//  3. DM, OR group with @-mention, OR group with listen_mode="all" →
//     default audience, sender prefixed onto content when group.
//  4. Group message that neither @-mentions the bot nor replies to one
//     AND listen_mode != "all" → skip.
func DecideInbound(
	msg IncomingMessage,
	cfg RoutingConfig,
	listenMode string,
	resolvedMeshID string,
) RoutingDecision {
	tags := "human," + Platform

	content := strings.TrimSpace(msg.Text)
	if isGroupChat(msg.SpaceType) && msg.SenderName != "" {
		content = msg.SenderName + ": " + content
	}

	// Case 1: native reply to a known bot message.
	if msg.IsReplyToBot && resolvedMeshID != "" {
		return RoutingDecision{
			Send: mesh.SendRequest{
				Kind:     "reply",
				Content:  content,
				Priority: "high",
				Audience: SessionPlaceholderPrefix + resolvedMeshID,
				Tags:     tags,
				ReplyTo:  resolvedMeshID,
			},
		}
	}

	// Skip group messages with no mention, no reply, no all-mode.
	if isGroupChat(msg.SpaceType) && !msg.MentionsBot && !msg.IsReplyToBot && listenMode != "all" {
		return RoutingDecision{Skip: true, SkipReason: "group message without bot mention"}
	}

	// Case 2: @role prefix.
	if role, stripped, ok := splitRolePrefix(msg.Text); ok {
		c := strings.TrimSpace(stripped)
		if isGroupChat(msg.SpaceType) && msg.SenderName != "" {
			c = msg.SenderName + ": " + c
		}
		return RoutingDecision{
			Send: mesh.SendRequest{
				Kind:     inferKind(stripped),
				Content:  c,
				Priority: "high",
				Audience: role,
				Tags:     tags,
			},
		}
	}

	// Case 3: fresh message, default audience.
	audience := cfg.DefaultAudience
	if audience == "" {
		audience = "*"
	}
	if after, ok := strings.CutPrefix(audience, "role:"); ok {
		audience = after
	}
	return RoutingDecision{
		Send: mesh.SendRequest{
			Kind:     inferKind(msg.Text),
			Content:  content,
			Priority: "high",
			Audience: audience,
			Tags:     tags,
		},
	}
}

func isGroupChat(spaceType string) bool {
	switch spaceType {
	case "group", "space":
		return true
	}
	return false
}

func inferKind(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasSuffix(text, "?") {
		return "question"
	}
	return "task"
}

// splitRolePrefix returns (role, rest, true) if text starts with "@word".
// Role must be alphanumeric + underscore + hyphen.
func splitRolePrefix(text string) (role, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return "", "", false
	}
	end := 1
	for end < len(text) {
		c := text[end]
		isIdent := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-'
		if !isIdent {
			break
		}
		end++
	}
	if end == 1 {
		return "", "", false
	}
	role = strings.ToLower(text[1:end])
	rest = strings.TrimSpace(text[end:])
	if rest == "" {
		return "", "", false
	}
	return role, rest, true
}
