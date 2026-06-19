package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// MeshSender is the narrow mesh dependency the manager uses so tests can
// substitute a fake. In production this is *mesh.Manager.
type MeshSender interface {
	Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}

type notificationLister interface {
	ListNotifications(ctx context.Context, f notify.ListFilter) ([]notify.StoredEvent, error)
}

// Manager runs the Telegram integration: long-poll loop (via Client),
// notify-bus subscription (outbound), inbound routing into the mesh, pairing
// lifecycle, and per-chat message threading.
type Manager struct {
	store     store.Store
	mesh      MeshSender
	notifyBus *notify.Bus
	client    *Client

	cfg   RoutingConfig
	cache *sentCache
	// thinking tracks "💭 …" placeholder messages keyed by the inbound
	// mesh message id. When a worker's reply arrives with reply_to=<id>
	// and a matching entry exists, the bridge edits the placeholder
	// in-place rather than posting a fresh chat bubble.
	thinking *thinkingCache

	inbound chan IncomingMessage

	mu      sync.RWMutex
	running bool

	notifySeenMu sync.Mutex
	notifySeen   map[string]struct{}

	defaultMinPriority string
}

// Option configures a Manager at construction.
type Option func(*Manager)

// WithDefaultAudience sets the mesh audience for fresh inbound messages.
// "*" broadcasts, "role:<name>" targets a role.
func WithDefaultAudience(s string) Option {
	return func(m *Manager) { m.cfg.DefaultAudience = s }
}

// WithDefaultMinPriority applies this minimum priority to newly paired chats.
func WithDefaultMinPriority(p string) Option {
	return func(m *Manager) {
		if p != "" {
			m.defaultMinPriority = p
		}
	}
}

// NewManager constructs a Manager with no Telegram client attached yet.
// Call SetClient before Run to activate the long-poll loop.
func NewManager(s store.Store, mm MeshSender, bus *notify.Bus, opts ...Option) *Manager {
	m := &Manager{
		store:              s,
		mesh:               mm,
		notifyBus:          bus,
		cache:              newSentCache(),
		thinking:           newThinkingCache(),
		inbound:            make(chan IncomingMessage, 128),
		notifySeen:         make(map[string]struct{}),
		defaultMinPriority: "normal",
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// SetClient attaches the Telegram API client. Manager.Run fails with a clear
// error if no client is attached.
func (m *Manager) SetClient(c *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
}

// HasClient reports whether a Telegram client is attached.
func (m *Manager) HasClient() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client != nil
}

// Run starts the Telegram long-poll loop, subscribes to the notify bus, and
// drives the inbound dispatcher + pairing sweep. Returns when ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("telegram: manager already running")
	}
	client := m.client
	if client == nil {
		m.mu.Unlock()
		slog.Info("telegram: no client configured — skipping run")
		<-ctx.Done()
		return nil
	}
	m.running = true
	m.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := client.Run(ctx, m.inbound); err != nil && ctx.Err() == nil {
			slog.Warn("telegram: client stopped with error", "error", err)
		}
	}()

	var notifyCh <-chan notify.Event
	if m.notifyBus != nil {
		notifyCh = m.notifyBus.Subscribe()
		defer m.notifyBus.Unsubscribe(notifyCh)
	}
	if lister, ok := m.store.(notificationLister); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.runNotifyBackfill(ctx, lister)
		}()
	}

	sweepTick := time.NewTicker(5 * time.Minute)
	defer sweepTick.Stop()
	m.sweepPairings(ctx)

	slog.Info("telegram: running")

	for {
		select {
		case <-ctx.Done():
			if err := client.Stop(); err != nil {
				slog.Warn("telegram: client stop", "error", err)
			}
			wg.Wait()
			return nil

		case msg := <-m.inbound:
			m.handleInbound(ctx, msg)

		case evt, ok := <-notifyCh:
			if !ok {
				notifyCh = nil
				continue
			}
			m.handleNotify(ctx, evt)

		case <-sweepTick.C:
			m.sweepPairings(ctx)
		}
	}
}

// handleInbound processes one normalised Telegram message: authz, pairing
// consumption, mesh routing.
func (m *Manager) handleInbound(ctx context.Context, msg IncomingMessage) {
	if msg.PairingCode != "" {
		m.handlePairing(ctx, msg)
		return
	}

	chat, err := m.store.GetTelegramChatByNative(ctx, msg.Platform, msg.ChatNativeID)
	if err != nil || chat == nil || !chat.Active {
		slog.Info("telegram: rejecting unbound chat", "native_chat_id", msg.ChatNativeID)
		return
	}
	_ = m.store.TouchTelegramChat(ctx, chat.ID)

	resolvedMesh, originSession := m.resolveReplyTarget(ctx, msg)

	decision := DecideInbound(msg, m.cfg, resolvedMesh)
	if decision.Skip {
		slog.Debug("telegram: skip inbound", "reason", decision.SkipReason)
		return
	}
	if origID := ExtractMeshMessageIDFromPlaceholder(decision.Send.Audience); origID != "" {
		if originSession != "" {
			decision.Send.Audience = originSession
		} else {
			decision.Send.Audience = "*"
		}
	}

	// Empty content reaches us when a callback (Reply button tap) has no
	// accompanying text. Substituting a placeholder keeps the mesh
	// thread continuous — the worker pulls history and infers what the
	// user meant — instead of mesh.Send failing with "content is
	// required" and the user staring at a stuck thinking spinner.
	if strings.TrimSpace(decision.Send.Content) == "" {
		if msg.CallbackData != "" {
			decision.Send.Content = "[reply button tapped — continue the thread]"
		} else {
			decision.Send.Content = "[empty message]"
		}
	}

	meta := mesh.SessionMeta{
		SessionID:    chat.SessionID,
		WorkspaceIDs: []string{chat.WorkspaceID},
		ClientType:   "telegram",
		ModelHint:    "human",
	}
	sent, err := m.mesh.Send(ctx, meta, decision.Send)
	if err != nil {
		slog.Warn("telegram: mesh.Send failed", "error", err)
		return
	}
	if sent != nil && m.client != nil && m.thinking != nil {
		m.startThinkingPlaceholder(ctx, *chat, sent.ID)
	}
}

// startThinkingPlaceholder posts a "💭 thinking…" message and kicks off
// the typing-indicator renewal goroutine. The placeholder + its
// cancel-func are recorded against the inbound mesh message id so a
// later worker reply (with reply_to=<that id>) can edit the placeholder
// in place instead of posting a fresh message.
//
// Best-effort: any failure swallows + logs. The user just sees a
// regular fresh reply when the worker finishes.
func (m *Manager) startThinkingPlaceholder(ctx context.Context, chat store.TelegramChat, meshMsgID string) {
	out := OutgoingMessage{
		Title: "",
		Body:  ThinkingPlaceholderText,
	}
	placeholderID, err := m.client.Send(ctx, chat, out)
	if err != nil || placeholderID == "" {
		slog.Debug("telegram: thinking placeholder skipped", "error", err)
		return
	}
	typingCtx, cancel := context.WithTimeout(context.Background(), thinkingTTL)
	startTyping(typingCtx, m.client, chat)
	m.thinking.Put(meshMsgID, thinkingRecord{
		chat:          chat,
		placeholderID: placeholderID,
		cancel:        cancel,
	})
}

func (m *Manager) resolveReplyTarget(ctx context.Context, msg IncomingMessage) (string, string) {
	if msg.CallbackData != "" {
		if action, arg, ok := ParseCallbackData(msg.CallbackData); ok && action == "reply" && arg != "" {
			if sess := m.sessionForMeshMessage(ctx, arg); sess != "" {
				return arg, sess
			}
		}
		return "", ""
	}
	if msg.IsReplyToBot && msg.RepliedNativeID != "" {
		if id := m.cache.Get(msg.Platform, msg.ChatNativeID, msg.RepliedNativeID); id != "" {
			if sess := m.sessionForMeshMessage(ctx, id); sess != "" {
				return id, sess
			}
		}
		if rec, err := m.store.GetTelegramSentMessage(ctx, msg.Platform, msg.ChatNativeID, msg.RepliedNativeID); err == nil && rec != nil {
			m.cache.Put(msg.Platform, msg.ChatNativeID, msg.RepliedNativeID, rec.MeshMessageID)
			if sess := m.sessionForMeshMessage(ctx, rec.MeshMessageID); sess != "" {
				return rec.MeshMessageID, sess
			}
		}
	}
	return "", ""
}

func (m *Manager) sessionForMeshMessage(ctx context.Context, meshMsgID string) string {
	msg, err := m.store.GetMeshMessage(ctx, meshMsgID)
	if err != nil || msg == nil {
		return ""
	}
	return msg.SessionID
}

func (m *Manager) handlePairing(ctx context.Context, msg IncomingMessage) {
	wsID, err := m.ConsumePairing(ctx, msg.Platform, msg.PairingCode)
	if err != nil {
		slog.Info("telegram: rejected pairing", "code", redactCode(msg.PairingCode))
		return
	}
	now := time.Now().UTC()
	chat := &store.TelegramChat{
		ID:           newULID(),
		Platform:     msg.Platform,
		NativeChatID: msg.ChatNativeID,
		ChatType:     msg.ChatType,
		Title:        msg.ChatTitle,
		WorkspaceID:  wsID,
		SessionID:    msg.Platform + "-" + msg.ChatNativeID,
		MinPriority:  m.defaultMinPriority,
		Active:       true,
		CreatedAt:    now,
		LastSeenAt:   now,
	}
	if err := m.store.UpsertTelegramChat(ctx, chat); err != nil {
		slog.Warn("telegram: upsert chat after pairing", "error", err)
		return
	}
	slog.Info("telegram: paired chat", "chat_id", chat.ID, "workspace", wsID)
}

func (m *Manager) handleNotify(ctx context.Context, evt notify.Event) {
	if !shouldForwardNotifyToTelegram(evt) {
		return
	}
	if m.notifyAlreadySeen(evt.MessageID) {
		return
	}
	workspaceID, err := m.workspaceForMeshMessage(ctx, evt.MessageID)
	if err != nil || workspaceID == "" {
		return
	}
	// Look up the full mesh message so we can read ReplyTo — used to
	// match an in-flight thinking placeholder for in-place edit.
	var replyTo string
	if msg, err := m.store.GetMeshMessage(ctx, evt.MessageID); err == nil && msg != nil {
		replyTo = msg.ReplyTo
	}
	chats, err := m.store.ListActiveTelegramChatsByWorkspace(ctx, workspaceID)
	if err != nil {
		return
	}
	if len(chats) == 0 {
		// Cross-workspace fallback: no Telegram chat is bound to this
		// workspace. Only page every active chat for critical events; high
		// worker/system chatter should stay in the dashboard/mesh unless
		// the operator explicitly paired that workspace.
		if !allowCrossWorkspaceTelegramFallback(evt) {
			m.markNotifySeen(evt.MessageID)
			return
		}
		chats = m.allActiveChats(ctx)
	}
	if len(chats) == 0 {
		m.markNotifySeen(evt.MessageID)
		return
	}
	chunks := SplitBody(CleanOutboundBody(evt.Body))
	attempted := false
	delivered := false
	for ci := range chats {
		c := chats[ci]
		if !allowPriority(c.MinPriority, evt.Priority) {
			continue
		}
		attempted = true
		if m.dispatchOutboundChunks(ctx, c, dispatchEnvelope{
			meshMessageID: evt.MessageID,
			replyTo:       replyTo,
			title:         evt.Title,
			priority:      evt.Priority,
			kind:          evt.Kind,
			tags:          evt.Tags,
			chunks:        chunks,
		}) {
			delivered = true
		}
	}
	if delivered || !attempted {
		m.markNotifySeen(evt.MessageID)
	}
}

func (m *Manager) notifyAlreadySeen(messageID string) bool {
	if strings.TrimSpace(messageID) == "" {
		return false
	}
	m.notifySeenMu.Lock()
	defer m.notifySeenMu.Unlock()
	_, ok := m.notifySeen[messageID]
	return ok
}

func (m *Manager) markNotifySeen(messageID string) bool {
	if strings.TrimSpace(messageID) == "" {
		return true
	}
	m.notifySeenMu.Lock()
	defer m.notifySeenMu.Unlock()
	if _, ok := m.notifySeen[messageID]; ok {
		return false
	}
	m.notifySeen[messageID] = struct{}{}
	return true
}

func (m *Manager) runNotifyBackfill(ctx context.Context, lister notificationLister) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	since := time.Now().UTC().Add(-2 * time.Minute)
	for {
		m.backfillNotifyOnce(ctx, lister, since)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) backfillNotifyOnce(ctx context.Context, lister notificationLister, since time.Time) {
	events, err := lister.ListNotifications(ctx, notify.ListFilter{
		Source: "mesh",
		Limit:  100,
	})
	if err != nil {
		slog.Debug("telegram: notify backfill list failed", "error", err)
		return
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.CreatedAt.Before(since) {
			continue
		}
		m.handleNotify(ctx, notify.Event{
			MessageID: e.MessageID,
			Source:    e.Source,
			AgentName: e.AgentName,
			Role:      e.Role,
			Kind:      e.Kind,
			Priority:  e.Priority,
			Title:     e.Title,
			Body:      e.Body,
			Tags:      e.Tags,
			Link:      e.Link,
			CreatedAt: e.CreatedAt,
		})
	}
}

// allActiveChats returns every active Telegram chat across all
// workspaces, de-duplicated by chat id. Used as the cross-workspace
// fallback in handleNotify when no chat is bound to the notifying
// workspace. Built from existing store primitives (ListWorkspaces +
// per-workspace chat lookup) so it needs no new store method.
func (m *Manager) allActiveChats(ctx context.Context) []store.TelegramChat {
	workspaces, err := m.store.ListWorkspaces(ctx)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []store.TelegramChat
	for _, ws := range workspaces {
		cs, err := m.store.ListActiveTelegramChatsByWorkspace(ctx, ws.ID)
		if err != nil {
			continue
		}
		for _, c := range cs {
			if _, ok := seen[c.ID]; ok {
				continue
			}
			seen[c.ID] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// dispatchEnvelope packages everything dispatchOutboundChunks needs in
// one struct so the iteration stays parameter-light.
type dispatchEnvelope struct {
	meshMessageID string
	replyTo       string
	title         string
	priority      string
	kind          string
	tags          string
	chunks        []string
}

// dispatchOutboundChunks sends each chunk as a telegram message. The
// first chunk:
//   - if its reply_to matches a tracked "💭 thinking" placeholder for
//     this chat, edits the placeholder in place + cancels the typing
//     renewal goroutine.
//   - otherwise posts as a new message (with the Reply inline button).
//
// Subsequent chunks always post as new messages with no buttons and no
// title — they're continuation bubbles for one logical response.
func (m *Manager) dispatchOutboundChunks(
	ctx context.Context, chat store.TelegramChat, env dispatchEnvelope,
) bool {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return false
	}
	delivered := false
	for i, body := range env.chunks {
		first := i == 0
		out := OutgoingMessage{
			MeshMessageID: env.meshMessageID,
			Body:          body,
			Priority:      env.priority,
			Kind:          env.kind,
			Tags:          env.tags,
		}
		if first {
			out.Title = env.title
			out.Buttons = []Button{{Label: "Reply", CallbackData: "reply:" + env.meshMessageID}}
		}
		var nativeID string
		var err error
		if first && env.replyTo != "" && m.thinking != nil {
			if rec, ok := m.thinking.Pop(env.replyTo); ok && rec.chat.ID == chat.ID {
				if rec.cancel != nil {
					rec.cancel()
				}
				if editErr := client.EditMessage(ctx, chat, rec.placeholderID, out); editErr == nil {
					nativeID = rec.placeholderID
				} else {
					slog.Debug("telegram: edit placeholder failed, falling back to new send", "error", editErr)
				}
			}
		}
		if nativeID == "" {
			nativeID, err = client.Send(ctx, chat, out)
			if err != nil {
				slog.Warn("telegram: send failed", "chat_id", chat.ID, "error", err)
				return delivered
			}
		}
		if nativeID == "" {
			continue
		}
		delivered = true
		m.cache.Put(chat.Platform, chat.NativeChatID, nativeID, env.meshMessageID)
		rec := &store.TelegramSentMessage{
			ID:              newULID(),
			Platform:        chat.Platform,
			NativeChatID:    chat.NativeChatID,
			NativeMessageID: nativeID,
			MeshMessageID:   env.meshMessageID,
			CreatedAt:       time.Now().UTC(),
		}
		if err := m.store.InsertTelegramSentMessage(ctx, rec); err != nil {
			slog.Warn("telegram: record sent", "error", err)
		}
	}
	return delivered
}

// dispatchOutbound is the legacy single-message dispatcher, kept for
// callers that compose OutgoingMessage directly (e.g. pairing
// confirmation). Routes through dispatchOutboundChunks with one
// chunk so the editing + tracking paths stay unified.
//
//nolint:unused // kept as an internal compatibility shim for older call sites.
func (m *Manager) dispatchOutbound(ctx context.Context, chat store.TelegramChat, msg OutgoingMessage) {
	m.dispatchOutboundChunks(ctx, chat, dispatchEnvelope{
		meshMessageID: msg.MeshMessageID,
		title:         msg.Title,
		priority:      msg.Priority,
		kind:          msg.Kind,
		tags:          msg.Tags,
		chunks:        []string{msg.Body},
	})
}

func (m *Manager) workspaceForMeshMessage(ctx context.Context, id string) (string, error) {
	msg, err := m.store.GetMeshMessage(ctx, id)
	if err != nil {
		return "", err
	}
	return msg.WorkspaceID, nil
}

// SendByChatID sends a plain text message to a specific bound chat. Used by
// MCP tools so agents can target a known chat without going through notify.
// Long inputs are split via SplitBody so the user receives the whole
// payload across multiple chat bubbles rather than a truncated first chunk.
func (m *Manager) SendByChatID(ctx context.Context, chatID, text, priority string) error {
	if chatID == "" {
		return fmt.Errorf("chat_id is required")
	}
	chat, err := m.store.GetTelegramChat(ctx, chatID)
	if err != nil {
		return fmt.Errorf("lookup chat: %w", err)
	}
	if !chat.Active {
		return fmt.Errorf("chat is inactive")
	}
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("telegram client not configured")
	}
	if err := sendChunked(ctx, client, *chat, "Agent message", text, priority); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	_ = m.store.TouchTelegramChat(ctx, chat.ID)
	m.recordOutboundToMesh(ctx, *chat, text)
	return nil
}

// BroadcastWorkspace delivers plain text to every active chat in a
// workspace. Each chat receives the full payload, split across multiple
// messages when it exceeds Telegram's per-message cap.
func (m *Manager) BroadcastWorkspace(ctx context.Context, workspaceID, text, priority string) (int, error) {
	chats, err := m.store.ListActiveTelegramChatsByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return 0, fmt.Errorf("telegram client not configured")
	}
	count := 0
	for _, c := range chats {
		if err := sendChunked(ctx, client, c, "Agent broadcast", text, priority); err != nil {
			slog.Warn("telegram: broadcast send failed", "chat_id", c.ID, "error", err)
			continue
		}
		count++
		m.recordOutboundToMesh(ctx, c, text)
	}
	return count, nil
}

// recordOutboundToMesh persists an agent-initiated outbound Telegram message
// (sent via the telegram__send_message / broadcast MCP tools) into the mesh so
// the bundled telegram-responder can see it in {mesh_history}. Without this the
// responder only ever observed inbound human turns and its own worker replies —
// never messages other agents pushed to the user — so it answered with no idea
// what had already been said.
//
// Tagged "telegram,agent-outbound": the substring "telegram" matches the
// responder's history filter, while the absence of "human" keeps it from
// re-firing the responder, whose mesh trigger is scoped to tag_match='human'
// (migration 110). LocalOnly keeps these daemon-local echoes off the peer
// broadcast. Best-effort: a failure logs a warning and never fails the
// user-facing send.
func (m *Manager) recordOutboundToMesh(
	ctx context.Context, chat store.TelegramChat, text string,
) {
	if m.mesh == nil || strings.TrimSpace(text) == "" {
		return
	}
	meta := mesh.SessionMeta{
		SessionID:    chat.SessionID,
		WorkspaceIDs: []string{chat.WorkspaceID},
		ClientType:   "telegram-bot",
		ModelHint:    "agent",
	}
	req := mesh.SendRequest{
		Kind:    "event",
		Content: text,
		// Force "high" so agent-outbound rows aren't archived by
		// ArchiveLowestPriority ahead of the high-priority telegram
		// conversation they belong to. The telegram SEND priority (in
		// SendByChatID / BroadcastWorkspace) is unaffected — this only sets
		// the persisted mesh-row priority.
		Priority:  "high",
		Audience:  "*",
		Tags:      "telegram,agent-outbound",
		ActorKind: "agent",
		LocalOnly: true,
	}
	if _, err := m.mesh.Send(ctx, meta, req); err != nil {
		slog.Warn("telegram: record agent-outbound to mesh failed",
			"chat_id", chat.ID, "error", err)
	}
}

// sendChunked splits text via SplitBody and posts each chunk as a
// separate telegram message. The first chunk carries the title; later
// continuation bubbles drop it so users see one logical response.
// Returns the first send error encountered (best-effort: later chunks
// still attempt to send so partial delivery doesn't strand the user
// without the bulk of the message). Empty text is a no-op.
func sendChunked(ctx context.Context, client *Client, chat store.TelegramChat, title, text, priority string) error {
	text = CleanOutboundBody(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var firstErr error
	for i, chunk := range SplitBody(text) {
		out := OutgoingMessage{
			Body:     chunk,
			Priority: priority,
			Kind:     "event",
		}
		if i == 0 {
			out.Title = title
		}
		if _, err := client.Send(ctx, chat, out); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func newULID() string {
	return ulid.Make().String()
}

func redactCode(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

func priorityRank(p string) int {
	switch strings.ToLower(p) {
	case "critical":
		return 4
	case "high":
		return 3
	case "normal":
		return 2
	case "low":
		return 1
	}
	return 0
}

func allowPriority(minPriority, evt string) bool {
	return priorityRank(evt) >= priorityRank(minPriority)
}

func shouldForwardNotifyToTelegram(evt notify.Event) bool {
	return !isWorkerLifecycleNotify(evt.Tags)
}

func allowCrossWorkspaceTelegramFallback(evt notify.Event) bool {
	return priorityRank(evt.Priority) >= priorityRank("critical")
}

func isWorkerLifecycleNotify(tags string) bool {
	seen := make(map[string]bool)
	for _, raw := range strings.Split(tags, ",") {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" {
			continue
		}
		switch tag {
		case "worker_started", "worker_finished", "worker_tool_call":
			return true
		}
		seen[tag] = true
	}
	return seen["worker"] && (seen["started"] || seen["finished"] || seen["tool_call"])
}
