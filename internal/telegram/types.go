package telegram

// IncomingMessage is the normalised shape the parse layer produces from a
// raw Telegram update. The manager decides what to do with it (authz, mesh
// routing, pairing consumption).
type IncomingMessage struct {
	Platform        string
	ChatNativeID    string
	ChatType        string // "private" | "group" | "supergroup"
	ChatTitle       string
	SenderName      string
	Text            string
	MentionsBot     bool
	IsReplyToBot    bool
	RepliedNativeID string
	NativeID        string
	PairingCode     string
	CallbackData    string
}

// OutgoingMessage is the shape handed to the render layer for MarkdownV2
// formatting and transmission via the Telegram API.
type OutgoingMessage struct {
	MeshMessageID string
	Title         string
	Body          string
	Priority      string
	Kind          string
	Tags          string
	Buttons       []Button
}

// Button is an inline action we attach to outgoing messages (Telegram
// renders these as inline-keyboard callback buttons).
type Button struct {
	Label        string
	CallbackData string
}
