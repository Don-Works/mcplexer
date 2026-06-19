// Package telegram is the Telegram adapter for the chat
package telegram

import (
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
)

// ParseMessage translates an inbound Telegram Message into the bridge's
// normalised IncomingMessage shape. `botUsername` is the bot's @username
// (without the @), used to detect mentions. Returns (msg, true) for regular
// text; (IncomingMessage{}, false) for unparseable updates (stickers, media).
func ParseMessage(m *models.Message, botUsername string) (IncomingMessage, bool) {
	if m == nil || m.Text == "" {
		return IncomingMessage{}, false
	}

	text, mentionsBot := stripBotMention(m.Text, m.Entities, botUsername)

	out := IncomingMessage{
		Platform:     "telegram",
		ChatNativeID: strconv.FormatInt(m.Chat.ID, 10),
		ChatType:     string(m.Chat.Type),
		ChatTitle:    chatTitle(m.Chat),
		SenderName:   senderName(m.From),
		Text:         text,
		MentionsBot:  mentionsBot,
		NativeID:     strconv.Itoa(m.ID),
	}

	// Detect pairing: `/start <code>` or `/pair <code>` (private), or the bot
	// was @-mentioned followed by `pair <code>` (groups).
	if code, ok := extractPairingCode(text, m.Chat.Type == models.ChatTypePrivate); ok {
		out.PairingCode = code
	}

	// Native reply tracking.
	if m.ReplyToMessage != nil {
		out.RepliedNativeID = strconv.Itoa(m.ReplyToMessage.ID)
		if m.ReplyToMessage.From != nil && m.ReplyToMessage.From.IsBot &&
			strings.EqualFold(m.ReplyToMessage.From.Username, botUsername) {
			out.IsReplyToBot = true
		}
	}

	return out, true
}

// ParseCallback translates an inbound CallbackQuery into an IncomingMessage.
// The message is resolved by the bridge via CallbackData and we only need the
// chat/sender metadata + the callback payload itself.
func ParseCallback(cb *models.CallbackQuery) (IncomingMessage, bool) {
	if cb == nil || cb.Data == "" {
		return IncomingMessage{}, false
	}
	chat := chatFromCallback(cb)
	if chat.ID == 0 {
		return IncomingMessage{}, false
	}

	return IncomingMessage{
		Platform:     "telegram",
		ChatNativeID: strconv.FormatInt(chat.ID, 10),
		ChatType:     string(chat.Type),
		ChatTitle:    chatTitle(chat),
		SenderName:   senderName(&cb.From),
		Text:         "",
		CallbackData: cb.Data,
	}, true
}

// stripBotMention removes a leading "@botname" (with/without a bot_command
// entity preceding it) from the text and reports whether one was present.
func stripBotMention(text string, entities []models.MessageEntity, botUsername string) (string, bool) {
	if botUsername == "" {
		return text, false
	}
	mentionsBot := false
	needle := "@" + botUsername

	// Entity-based detection: covers text-mentions and @mentions reliably.
	for _, e := range entities {
		if e.Type == models.MessageEntityTypeMention {
			if e.Offset+e.Length > len(text) {
				continue
			}
			if strings.EqualFold(text[e.Offset:e.Offset+e.Length], needle) {
				mentionsBot = true
			}
		}
	}
	if !mentionsBot {
		// Fallback for clients that don't emit the entity.
		if strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
			mentionsBot = true
		}
	}

	// Strip leading mention for cleaner routing, preserving the rest.
	trimmed := strings.TrimSpace(text)
	if idx := strings.Index(strings.ToLower(trimmed), strings.ToLower(needle)); idx == 0 {
		trimmed = strings.TrimSpace(trimmed[len(needle):])
	}
	return trimmed, mentionsBot
}

// extractPairingCode looks for `/start <code>` or `/pair <code>` commands and
// returns the code. Private chats also accept a bare "pair <code>" form for
// convenience; groups require the explicit slash command to avoid false matches.
func extractPairingCode(text string, privateChat bool) (string, bool) {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)

	if after, ok := strings.CutPrefix(lower, "/start "); ok {
		return strings.TrimSpace(text[len(text)-len(after):]), true
	}
	if after, ok := strings.CutPrefix(lower, "/pair "); ok {
		return strings.TrimSpace(text[len(text)-len(after):]), true
	}
	// Handle commands with bot username suffix like `/start@mybot <code>`.
	if strings.HasPrefix(lower, "/start@") || strings.HasPrefix(lower, "/pair@") {
		// Trim up to first whitespace.
		if idx := strings.IndexAny(text, " \t"); idx > 0 {
			return strings.TrimSpace(text[idx+1:]), true
		}
	}
	if privateChat {
		if after, ok := strings.CutPrefix(lower, "pair "); ok {
			return strings.TrimSpace(text[len(text)-len(after):]), true
		}
	}
	return "", false
}

func chatTitle(c models.Chat) string {
	if c.Title != "" {
		return c.Title
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if name != "" {
		return name
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return ""
}

func senderName(u *models.User) string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	if u.Username != "" {
		return u.Username
	}
	return strconv.FormatInt(u.ID, 10)
}

func chatFromCallback(cb *models.CallbackQuery) models.Chat {
	// MaybeInaccessibleMessage wraps either an accessible message or an
	// InaccessibleMessage; both carry the chat we need.
	if cb.Message.Type == models.MaybeInaccessibleMessageTypeMessage && cb.Message.Message != nil {
		return cb.Message.Message.Chat
	}
	if cb.Message.Type == models.MaybeInaccessibleMessageTypeInaccessibleMessage && cb.Message.InaccessibleMessage != nil {
		return cb.Message.InaccessibleMessage.Chat
	}
	return models.Chat{}
}
