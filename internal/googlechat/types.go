// Package googlechat is the Google Chat adapter for the chat bridge.
// Mirrors internal/telegram in shape: pure parse + routing functions feed
// a Manager that subscribes to the notify bus and dispatches to mesh.Send.
package googlechat

// Platform is the stable identifier used in mesh tags, sessions, audit
// rows, and the GoogleChatSpace.session_id prefix.
const Platform = "googlechat"

// IncomingMessage is the normalised shape the parse layer produces from a
// raw Google Chat event. The manager decides what to do with it (authz,
// mesh routing, pairing consumption, space lifecycle).
type IncomingMessage struct {
	// EventType is the original Google Chat event verb. Manager dispatches
	// MESSAGE through routing; ADDED_TO_SPACE / REMOVED_TO_SPACE through
	// space-lifecycle handlers.
	EventType string

	// SpaceName is the Google Chat resource name, e.g. "spaces/AAAA...".
	SpaceName string

	// SpaceType is "dm" | "group" | "space" (Google Workspace's modern term
	// for a multi-person room).
	SpaceType string

	// SpaceTitle is the human-readable space name (empty for DMs).
	SpaceTitle string

	// SenderName is the human-friendly display name for the human or bot
	// that posted the inbound message.
	SenderName string

	// SenderType is "HUMAN" | "BOT". We never route BOT messages back into
	// the mesh — that's the bridge's outgoing feedback loop.
	SenderType string

	// Text is the message body with any leading "@bot" mention stripped.
	Text string

	// MentionsBot is true when the inbound message @-mentioned the bot
	// (entity-detected; falls back to text-substring as last resort).
	MentionsBot bool

	// ThreadName is the Google Chat thread resource name (when this message
	// is part of a thread). Empty for DMs or unthreaded messages.
	ThreadName string

	// NativeMessageID is the Google Chat message resource name (last
	// segment of "spaces/.../messages/...").
	NativeMessageID string

	// IsReplyToBot is set when the inbound message threads under a message
	// we previously sent. Resolved by the manager against
	// googlechat_sent_messages, not by parse.
	IsReplyToBot bool

	// RepliedNativeID is the native_message_id of the message being
	// threaded under (== last bot message in the same thread, when known).
	RepliedNativeID string

	// PairingCode is set when the inbound message looks like a pairing
	// command (e.g. "/pair ABC12345" or "pair ABC12345" in a DM).
	PairingCode string
}

// OutgoingMessage is the shape handed to the render + client layers for
// delivery via the Google Chat REST API.
type OutgoingMessage struct {
	MeshMessageID string
	Title         string
	Body          string
	Priority      string
	Kind          string
	Tags          string

	// ThreadName, when non-empty, makes the reply land in the same thread
	// as the inbound message that triggered it. Empty for fresh sends.
	ThreadName string
}
