package telegram

import (
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
)

// RoutingDecision captures how an inbound chat message should be translated
// into a mesh.SendRequest (or skipped entirely).
type RoutingDecision struct {
	// Skip is true when the message must not reach the mesh (e.g. group chat
	// messages that don't mention the bot). Content is ignored.
	Skip bool

	// SkipReason is a short string explaining why a message was skipped.
	// Useful for debug logs; not surfaced to users.
	SkipReason string

	// Send holds the mesh request to execute when Skip is false.
	Send mesh.SendRequest
}

// RoutingConfig is bridge-wide configuration driven from settings. Pure
// function inputs — no I/O dependencies — so routing is trivially testable.
type RoutingConfig struct {
	// DefaultAudience applied to fresh (non-reply, non-@role) messages.
	// "*" broadcasts to all agents in the workspace; "role:<name>" targets a
	// specific agent role; "" falls back to "*".
	DefaultAudience string
}

// DecideInbound turns a normalised IncomingMessage into a RoutingDecision.
// Pure: takes the message + config + optional resolved mesh-message-id for the
// reply target, returns what to do. No mutation, no side effects.
//
// Five cases, in priority order:
//  1. Button callback resolved to a known mesh message → direct reply.
//  2. Native reply to a known bot message → direct reply.
//  3. Fresh message starting with `@rolename` → route to that role.
//  4. Fresh message in a DM, or a group message that @-mentions the bot →
//     default audience, tagged human+platform.
//  5. Group message that neither @-mentions the bot nor replies to one → skip.
func DecideInbound(
	msg IncomingMessage,
	cfg RoutingConfig,
	resolvedMeshID string,
) RoutingDecision {
	tags := "human," + msg.Platform

	content := strings.TrimSpace(msg.Text)
	if isGroupChat(msg.ChatType) && msg.SenderName != "" {
		content = msg.SenderName + ": " + content
	}

	// Case 1: button callback with a resolved mesh message id.
	if msg.CallbackData != "" && resolvedMeshID != "" {
		return RoutingDecision{
			Send: mesh.SendRequest{
				Kind:     "reply",
				Content:  content,
				Priority: "high",
				Audience: resolvedMeshIDAsSession(resolvedMeshID), // placeholder, resolved by caller
				Tags:     tags + ",button",
				ReplyTo:  resolvedMeshID,
			},
		}
	}

	// Case 2: native reply to a known bot message.
	if msg.IsReplyToBot && resolvedMeshID != "" {
		return RoutingDecision{
			Send: mesh.SendRequest{
				Kind:     "reply",
				Content:  content,
				Priority: "high",
				Audience: resolvedMeshIDAsSession(resolvedMeshID),
				Tags:     tags,
				ReplyTo:  resolvedMeshID,
			},
		}
	}

	// Group messages with no mention and not a reply → skip.
	if isGroupChat(msg.ChatType) && !msg.MentionsBot && !msg.IsReplyToBot {
		return RoutingDecision{Skip: true, SkipReason: "group message without bot mention"}
	}

	// Case 3: @role prefix.
	if role, stripped, ok := splitRolePrefix(msg.Text); ok {
		content := strings.TrimSpace(stripped)
		if isGroupChat(msg.ChatType) && msg.SenderName != "" {
			content = msg.SenderName + ": " + content
		}
		return RoutingDecision{
			Send: mesh.SendRequest{
				Kind:     inferKind(stripped),
				Content:  content,
				Priority: "high",
				Audience: role,
				Tags:     tags,
			},
		}
	}

	// Case 4: fresh message, default audience.
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

// resolvedMeshIDAsSession is a placeholder signalling that the bridge caller
// must replace Send.Audience with the session id of the mesh message's author
// (which requires a DB lookup). Routing is kept a pure function by emitting
// this sentinel and letting the caller substitute.
func resolvedMeshIDAsSession(id string) string {
	return "mesh-msg:" + id
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

func isGroupChat(chatType string) bool {
	switch chatType {
	case "group", "supergroup", "space":
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

// splitRolePrefix checks if text starts with "@word" and returns (role, rest, true).
// Role must be alphanumeric + underscore + hyphen to avoid false positives on emails etc.
func splitRolePrefix(text string) (role, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return "", "", false
	}
	// Find the end of the @token.
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
