package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/don-works/mcplexer/internal/store"
)

// Client is the Telegram API transport — thin wrapper around go-telegram/bot
// with the long-poll loop, message send, and callback-ack plumbing.
type Client struct {
	token string

	mu          sync.Mutex
	bot         *tgbot.Bot
	botUsername string
	inbound     chan<- IncomingMessage
	cancelRun   context.CancelFunc
}

// NewClient constructs a Client bound to the given bot token. The token is
// not validated here; errors surface on Run when GetMe is called.
func NewClient(token string) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram: empty token")
	}
	return &Client{token: token}, nil
}

// Run starts the long-poll loop. Blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context, inbound chan<- IncomingMessage) error {
	c.mu.Lock()
	c.inbound = inbound
	c.mu.Unlock()

	b, err := tgbot.New(c.token, tgbot.WithDefaultHandler(c.defaultHandler))
	if err != nil {
		return fmt.Errorf("telegram new: %w", err)
	}
	c.mu.Lock()
	c.bot = b
	c.mu.Unlock()

	if me, err := b.GetMe(ctx); err == nil && me != nil {
		c.mu.Lock()
		c.botUsername = me.Username
		c.mu.Unlock()
		slog.Info("telegram: ready", "username", me.Username)
	} else {
		slog.Warn("telegram: GetMe failed, mention detection limited", "error", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancelRun = cancel
	c.mu.Unlock()

	b.Start(runCtx)
	return nil
}

// Stop cancels the long-poll loop. Idempotent.
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelRun != nil {
		c.cancelRun()
		c.cancelRun = nil
	}
	return nil
}

// SendChatAction calls Telegram's sendChatAction so users see the
// "typing…" indicator while a worker is composing a reply. Telegram
// clears the indicator after ~5s, so callers that want it to persist
// should call this on a ticker. action is one of the strings telegram
// accepts (typing | upload_photo | record_voice | ...). Non-fatal: a
// failed action call is logged and swallowed since the indicator is
// purely a UX nicety.
func (c *Client) SendChatAction(ctx context.Context, chat store.TelegramChat, action string) error {
	c.mu.Lock()
	b := c.bot
	c.mu.Unlock()
	if b == nil {
		return fmt.Errorf("telegram: client not running")
	}
	chatID, err := strconv.ParseInt(chat.NativeChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid chat id %q: %w", chat.NativeChatID, err)
	}
	_, err = b.SendChatAction(ctx, &tgbot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatAction(action),
	})
	if err != nil {
		return fmt.Errorf("telegram: chat action: %w", err)
	}
	return nil
}

// EditMessage updates an already-sent message's text in place. Used to
// transform a "💭 thinking…" placeholder into the worker's actual reply
// so the user sees ONE message that updates, rather than placeholder +
// reply as two separate chat bubbles.
func (c *Client) EditMessage(ctx context.Context, chat store.TelegramChat, messageID string, msg OutgoingMessage) error {
	c.mu.Lock()
	b := c.bot
	c.mu.Unlock()
	if b == nil {
		return fmt.Errorf("telegram: client not running")
	}
	chatID, err := strconv.ParseInt(chat.NativeChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid chat id %q: %w", chat.NativeChatID, err)
	}
	msgID, err := strconv.Atoi(messageID)
	if err != nil {
		return fmt.Errorf("telegram: invalid message id %q: %w", messageID, err)
	}
	params := &tgbot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      RenderText(msg),
		ParseMode: models.ParseModeMarkdown,
	}
	if _, err = b.EditMessageText(ctx, params); err == nil {
		return nil
	}
	markdownErr := err
	plain := RenderPlainText(msg)
	if strings.TrimSpace(plain) == "" || plain == params.Text {
		return fmt.Errorf("telegram: edit: %w", markdownErr)
	}
	params.Text = plain
	params.ParseMode = ""
	if _, err = b.EditMessageText(ctx, params); err != nil {
		return fmt.Errorf("telegram: edit: %w; plain fallback: %v", markdownErr, err)
	}
	return nil
}

// Send delivers an OutgoingMessage to a bound chat, returning the native
// message id for threading.
func (c *Client) Send(ctx context.Context, chat store.TelegramChat, msg OutgoingMessage) (string, error) {
	c.mu.Lock()
	b := c.bot
	c.mu.Unlock()
	if b == nil {
		return "", fmt.Errorf("telegram: client not running")
	}

	chatID, err := strconv.ParseInt(chat.NativeChatID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("telegram: invalid chat id %q: %w", chat.NativeChatID, err)
	}

	params := &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      RenderText(msg),
		ParseMode: models.ParseModeMarkdown,
	}
	if kb := RenderKeyboard(msg.Buttons); kb != nil {
		params.ReplyMarkup = kb
	}

	sent, err := b.SendMessage(ctx, params)
	if err != nil {
		markdownErr := err
		plain := RenderPlainText(msg)
		if strings.TrimSpace(plain) == "" || plain == params.Text {
			return "", fmt.Errorf("telegram: send: %w", markdownErr)
		}
		params.Text = plain
		params.ParseMode = ""
		sent, err = b.SendMessage(ctx, params)
		if err != nil {
			return "", fmt.Errorf("telegram: send: %w; plain fallback: %v", markdownErr, err)
		}
	}
	if sent == nil {
		return "", nil
	}
	return strconv.Itoa(sent.ID), nil
}

func (c *Client) defaultHandler(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	c.mu.Lock()
	inbound := c.inbound
	botUsername := c.botUsername
	c.mu.Unlock()
	if inbound == nil {
		return
	}

	if u.CallbackQuery != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
			CallbackQueryID: u.CallbackQuery.ID,
			Text:            "Received — relaying to agent.",
		})
		if msg, ok := ParseCallback(u.CallbackQuery); ok {
			pushOrDrop(inbound, msg)
		}
		return
	}

	var tgMsg *models.Message
	switch {
	case u.Message != nil:
		tgMsg = u.Message
	case u.EditedMessage != nil:
		tgMsg = u.EditedMessage
	default:
		return
	}
	if msg, ok := ParseMessage(tgMsg, botUsername); ok {
		pushOrDrop(inbound, msg)
	}
}

func pushOrDrop(inbound chan<- IncomingMessage, msg IncomingMessage) {
	select {
	case inbound <- msg:
	default:
		slog.Warn("telegram: inbound channel saturated, dropping message",
			"chat", msg.ChatNativeID)
	}
}
