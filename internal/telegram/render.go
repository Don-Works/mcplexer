package telegram

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

// markdownV2Special lists every character Telegram requires to be escaped
// when parse_mode=MarkdownV2 is set. Incorrect escaping gets the whole
// message rejected with a 400, so we escape paranoidly.
const markdownV2Special = `_*[]()~` + "`" + `>#+-=|{}.!\`

// EscapeMarkdownV2 escapes every MarkdownV2 special char in a string so it
// can be embedded into a formatted message without breaking the parser.
func EscapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		if strings.ContainsRune(markdownV2Special, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// RenderText formats an OutgoingMessage into a MarkdownV2 string suitable for
// SendMessage with parse_mode=MarkdownV2.
//
// Layout:
//
//	*<title>*
//	<body>
func RenderText(msg OutgoingMessage) string {
	var b strings.Builder
	if title := strings.TrimSpace(msg.Title); title != "" {
		b.WriteString("*")
		b.WriteString(EscapeMarkdownV2(title))
		b.WriteString("*\n")
	}
	if body := CleanOutboundBody(msg.Body); body != "" {
		b.WriteString(EscapeMarkdownV2(body))
	}
	return b.String()
}

// RenderPlainText formats the same payload without Telegram parse mode.
// The client uses this as a delivery fallback when Telegram rejects
// MarkdownV2, commonly because escaping expands a near-limit worker output
// beyond Telegram's post-parse message cap.
func RenderPlainText(msg OutgoingMessage) string {
	var b strings.Builder
	if title := strings.TrimSpace(msg.Title); title != "" {
		b.WriteString(title)
	}
	if body := CleanOutboundBody(msg.Body); body != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(body)
	}
	return b.String()
}

// RenderKeyboard converts the bridge's platform-agnostic button list into a
// Telegram inline keyboard. Returns nil when there are no buttons.
func RenderKeyboard(buttons []Button) *models.InlineKeyboardMarkup {
	if len(buttons) == 0 {
		return nil
	}
	row := make([]models.InlineKeyboardButton, 0, len(buttons))
	for _, b := range buttons {
		row = append(row, models.InlineKeyboardButton{
			Text:         b.Label,
			CallbackData: b.CallbackData,
		})
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{row},
	}
}
