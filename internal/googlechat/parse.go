package googlechat

import (
	"encoding/json"
	"strings"
)

// Event types Google Chat sends our webhook. ADDED / REMOVED let us track
// space lifecycle so the bridge can record a newly added space (and stop
// posting to one we've been kicked from) without a separate provisioning
// step.
const (
	EventTypeMessage          = "MESSAGE"
	EventTypeAddedToSpace     = "ADDED_TO_SPACE"
	EventTypeRemovedFromSpace = "REMOVED_FROM_SPACE"
)

// EventEnvelope is the subset of the Google Chat event payload we read.
// Google sends a JSON envelope per event with `type` + `space` + `message`
// (when type=MESSAGE) + `user` (the sender).
type EventEnvelope struct {
	Type      string        `json:"type"`
	EventTime string        `json:"eventTime"`
	Space     EventSpace    `json:"space"`
	User      EventUser     `json:"user"`
	Message   *EventMessage `json:"message,omitempty"`
}

// EventSpace mirrors google.chat.v1.Space.
type EventSpace struct {
	Name        string `json:"name"`        // "spaces/AAAA..."
	DisplayName string `json:"displayName"` // empty for DMs
	Type        string `json:"type"`        // "DM" | "ROOM" (legacy) | "GROUP_CHAT"
	SpaceType   string `json:"spaceType"`   // "DIRECT_MESSAGE" | "SPACE" | "GROUP_CHAT" (newer)
}

// EventUser mirrors google.chat.v1.User.
type EventUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"` // "HUMAN" | "BOT"
	IsAnonymous bool   `json:"isAnonymous"`
}

// EventMessage mirrors google.chat.v1.Message (subset we care about).
type EventMessage struct {
	Name          string             `json:"name"` // "spaces/.../messages/MSG_ID"
	Text          string             `json:"text"`
	ArgumentText  string             `json:"argumentText"` // text with bot @-mention removed
	Sender        EventUser          `json:"sender"`
	Thread        EventThread        `json:"thread"`
	AnnotatedText string             `json:"-"` // we compute this
	Annotations   []EventAnnotation  `json:"annotations"`
	SlashCommand  *EventSlashCommand `json:"slashCommand,omitempty"`
}

type EventThread struct {
	Name string `json:"name"` // "spaces/.../threads/THREAD_ID"
}

type EventAnnotation struct {
	Type        string            `json:"type"` // "USER_MENTION" | "SLASH_COMMAND"
	StartIndex  int               `json:"startIndex"`
	Length      int               `json:"length"`
	UserMention *EventUserMention `json:"userMention,omitempty"`
}

type EventUserMention struct {
	User EventUser `json:"user"`
	Type string    `json:"type"` // "MENTION" | "ADD"
}

type EventSlashCommand struct {
	CommandID string `json:"commandId"`
}

// ParseEvent translates a raw event payload into an IncomingMessage. botName,
// when non-empty, is the bot's resource name (e.g. "users/12345") — used to
// detect mentions of THIS bot vs. mentions of other users.
//
// Returns (msg, true) for events the bridge wants to handle; (zero, false)
// for unsupported event types or malformed payloads.
func ParseEvent(raw []byte, botName string) (IncomingMessage, bool) {
	var env EventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return IncomingMessage{}, false
	}
	if env.Type == "" {
		return IncomingMessage{}, false
	}

	out := IncomingMessage{
		EventType:  env.Type,
		SpaceName:  env.Space.Name,
		SpaceTitle: env.Space.DisplayName,
		SpaceType:  normalizeSpaceType(env.Space),
		SenderName: env.User.DisplayName,
		SenderType: env.User.Type,
	}
	if out.SenderType == "" {
		out.SenderType = "HUMAN"
	}

	switch env.Type {
	case EventTypeMessage:
		if env.Message == nil {
			return IncomingMessage{}, false
		}
		// Reject bot-authored messages — those are our outbound feedback.
		if env.Message.Sender.Type == "BOT" {
			return IncomingMessage{}, false
		}
		out.NativeMessageID = lastSegment(env.Message.Name)
		out.ThreadName = env.Message.Thread.Name
		if env.Message.Sender.DisplayName != "" {
			out.SenderName = env.Message.Sender.DisplayName
		}
		out.MentionsBot = detectBotMention(env.Message, botName)
		out.Text = cleanMessageText(env.Message)
		if code, ok := extractPairingCode(out.Text, out.SpaceType == "dm"); ok {
			out.PairingCode = code
		}
		return out, true

	case EventTypeAddedToSpace, EventTypeRemovedFromSpace:
		// Lifecycle events carry space + user but no message body.
		return out, true
	}
	return IncomingMessage{}, false
}

// normalizeSpaceType collapses Google's evolving SpaceType / Type fields onto
// the bridge's "dm" | "group" | "space" vocabulary.
func normalizeSpaceType(s EventSpace) string {
	switch strings.ToUpper(s.SpaceType) {
	case "DIRECT_MESSAGE":
		return "dm"
	case "GROUP_CHAT":
		return "group"
	case "SPACE":
		return "space"
	}
	switch strings.ToUpper(s.Type) {
	case "DM":
		return "dm"
	case "GROUP_CHAT":
		return "group"
	case "ROOM", "SPACE":
		return "space"
	}
	return "space"
}

// detectBotMention scans annotations for a USER_MENTION whose user matches
// botName. Falls back to substring match on the raw text when annotations
// are absent.
func detectBotMention(m *EventMessage, botName string) bool {
	if m == nil {
		return false
	}
	if botName != "" {
		for _, a := range m.Annotations {
			if a.Type == "USER_MENTION" && a.UserMention != nil &&
				a.UserMention.User.Name == botName {
				return true
			}
		}
	}
	// Fallback heuristic: if argumentText differs from text, a mention was
	// stripped (Google strips the leading bot mention into argumentText).
	if strings.TrimSpace(m.ArgumentText) != "" &&
		strings.TrimSpace(m.ArgumentText) != strings.TrimSpace(m.Text) {
		return true
	}
	return false
}

// cleanMessageText prefers Google's argumentText (text with the leading bot
// @-mention stripped) and falls back to the raw text otherwise.
func cleanMessageText(m *EventMessage) string {
	if m == nil {
		return ""
	}
	if t := strings.TrimSpace(m.ArgumentText); t != "" {
		return t
	}
	return strings.TrimSpace(m.Text)
}

// lastSegment returns the last "/"-delimited segment of a resource name.
// "spaces/AAA/messages/BBB" → "BBB". Empty string in, empty string out.
func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// extractPairingCode looks for "/pair <code>" or (in DMs) a bare "pair <code>"
// command and returns the code. Mirrors telegram's extractPairingCode for
// parity; groups require the slash command to avoid false matches on regular
// conversation.
func extractPairingCode(text string, privateChat bool) (string, bool) {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	if after, ok := strings.CutPrefix(lower, "/pair "); ok {
		return strings.TrimSpace(text[len(text)-len(after):]), true
	}
	if after, ok := strings.CutPrefix(lower, "/start "); ok {
		return strings.TrimSpace(text[len(text)-len(after):]), true
	}
	if privateChat {
		if after, ok := strings.CutPrefix(lower, "pair "); ok {
			return strings.TrimSpace(text[len(text)-len(after):]), true
		}
	}
	return "", false
}
