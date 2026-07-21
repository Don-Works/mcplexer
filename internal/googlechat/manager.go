package googlechat

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// MeshSender is the narrow mesh dependency the manager uses so tests can
// substitute a fake. In production this is *mesh.Manager.
type MeshSender interface {
	Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}

// Manager runs the Google Chat integration: webhook-pushed inbound events,
// notify-bus subscription (outbound), mesh routing, pairing lifecycle, and
// space threading.
type Manager struct {
	store     store.Store
	mesh      MeshSender
	notifyBus *notify.Bus
	client    *Client

	cfg RoutingConfig

	inbound chan IncomingMessage

	mu      sync.RWMutex
	running bool

	defaultMinPriority string
	defaultListenMode  string
}

// Option configures a Manager at construction.
type Option func(*Manager)

// WithDefaultAudience sets the mesh audience for fresh inbound messages.
// "*" broadcasts; "role:<name>" targets a role.
func WithDefaultAudience(s string) Option {
	return func(m *Manager) { m.cfg.DefaultAudience = s }
}

// WithDefaultMinPriority applies this minimum priority to newly added spaces.
func WithDefaultMinPriority(p string) Option {
	return func(m *Manager) {
		if p != "" {
			m.defaultMinPriority = p
		}
	}
}

// WithDefaultListenMode sets the listen_mode used when the bridge auto-records
// a space (e.g. on ADDED_TO_SPACE). Defaults to "mention" — matching telegram's
// group behaviour.
func WithDefaultListenMode(s string) Option {
	return func(m *Manager) {
		if s != "" {
			m.defaultListenMode = s
		}
	}
}

// NewManager constructs a Manager with no Google Chat client attached yet.
// Call SetClient before Run to activate the outbound pipeline.
func NewManager(s store.Store, mm MeshSender, bus *notify.Bus, opts ...Option) *Manager {
	m := &Manager{
		store:              s,
		mesh:               mm,
		notifyBus:          bus,
		inbound:            make(chan IncomingMessage, 128),
		defaultMinPriority: "normal",
		defaultListenMode:  "mention",
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// SetClient attaches the Google Chat HTTP client. Without one, outbound sends
// are no-ops and the inbound webhook returns 503.
func (m *Manager) SetClient(c *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
}

// HasClient reports whether a Google Chat client is attached.
func (m *Manager) HasClient() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client != nil
}

// Inbound returns the channel webhook handlers push events onto. Buffered;
// drops when saturated (logged).
func (m *Manager) Inbound() chan<- IncomingMessage {
	return m.inbound
}

// Run subscribes to the notify bus, drives the inbound dispatcher, and sweeps
// expired pairings on a ticker. Returns when ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("googlechat: manager already running")
	}
	m.running = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	var notifyCh <-chan notify.Event
	if m.notifyBus != nil {
		notifyCh = m.notifyBus.Subscribe()
		defer m.notifyBus.Unsubscribe(notifyCh)
	}

	sweepTick := time.NewTicker(5 * time.Minute)
	defer sweepTick.Stop()
	m.sweepPairings(ctx)

	slog.Info("googlechat: running", "client_active", m.HasClient())

	for {
		select {
		case <-ctx.Done():
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

// PushInbound is a test/webhook entry point that places a message on the
// inbound channel without blocking; drops + logs on saturation.
func (m *Manager) PushInbound(msg IncomingMessage) {
	select {
	case m.inbound <- msg:
	default:
		slog.Warn("googlechat: inbound channel saturated, dropping",
			"space", msg.SpaceName)
	}
}

// handleInbound dispatches one normalised event by its EventType.
func (m *Manager) handleInbound(ctx context.Context, msg IncomingMessage) {
	switch msg.EventType {
	case EventTypeAddedToSpace:
		m.handleAdded(ctx, msg)
	case EventTypeRemovedFromSpace:
		m.handleRemoved(ctx, msg)
	case EventTypeMessage:
		m.handleMessage(ctx, msg)
	}
}

// handleAdded records a newly added space (e.g. bot was just added to a room).
// If the user issued an inline pairing command on the welcome message, the
// space gets bound to that pairing's workspace; otherwise it sits in a
// holding state (workspace_id="") until paired explicitly.
func (m *Manager) handleAdded(ctx context.Context, msg IncomingMessage) {
	if msg.SpaceName == "" {
		return
	}
	existing, _ := m.store.GetGoogleChatSpaceByName(ctx, msg.SpaceName)
	if existing != nil {
		return
	}
	slog.Info("googlechat: added to space",
		"space_name", msg.SpaceName, "space_type", msg.SpaceType)
}

// handleRemoved marks the space inactive so we stop trying to post there.
func (m *Manager) handleRemoved(ctx context.Context, msg IncomingMessage) {
	existing, _ := m.store.GetGoogleChatSpaceByName(ctx, msg.SpaceName)
	if existing == nil {
		return
	}
	if err := m.store.DeactivateGoogleChatSpace(ctx, existing.ID); err != nil {
		slog.Warn("googlechat: deactivate space", "error", err)
	}
}

// handleMessage processes one inbound user message: pairing first, then
// authz against the bound space row, then routing into the mesh.
func (m *Manager) handleMessage(ctx context.Context, msg IncomingMessage) {
	if msg.PairingCode != "" {
		m.handlePairing(ctx, msg)
		return
	}

	space, err := m.store.GetGoogleChatSpaceByName(ctx, msg.SpaceName)
	if err != nil || space == nil || !space.Active {
		slog.Info("googlechat: rejecting unbound space", "space_name", msg.SpaceName)
		return
	}
	_ = m.store.TouchGoogleChatSpace(ctx, space.ID)

	resolvedMesh, originSession := m.resolveReplyTarget(ctx, msg)
	if resolvedMesh != "" {
		msg.IsReplyToBot = true
	}

	decision := DecideInbound(msg, m.cfg, space.ListenMode, resolvedMesh)
	if decision.Skip {
		slog.Debug("googlechat: skip inbound", "reason", decision.SkipReason)
		return
	}
	if origID := ExtractMeshMessageIDFromPlaceholder(decision.Send.Audience); origID != "" {
		if originSession != "" {
			decision.Send.Audience = originSession
		} else {
			decision.Send.Audience = "*"
		}
	}

	meta := mesh.SessionMeta{
		SessionID:    space.SessionID,
		WorkspaceIDs: []string{space.WorkspaceID},
		ClientType:   Platform,
		ModelHint:    "human",
	}
	if _, err := m.mesh.Send(ctx, meta, decision.Send); err != nil {
		slog.Warn("googlechat: mesh.Send failed", "error", err)
	}
}

// resolveReplyTarget looks up the mesh message id this incoming thread is
// replying to (if any) and returns (mesh_msg_id, origin_session_id).
// v1: we don't have a thread-indexed query on the sent-messages table — when
// the bridge sends a notify-driven message we record (space_name, thread,
// native_msg_id, mesh_msg_id). To resolve "this inbound replies to which
// mesh msg" we'd need to either index by thread or carry the parent
// native-msg-id on the inbound. Both are followup work; for now the bridge
// always treats inbound threaded replies as fresh sends to the default
// audience, which is functionally correct (just loses the thread linkage).
func (m *Manager) resolveReplyTarget(ctx context.Context, msg IncomingMessage) (string, string) {
	if msg.ThreadName == "" {
		return "", ""
	}
	// Thread-indexed lookup is a followup. See the comment above.
	_ = ctx
	return "", ""
}

//nolint:unused // retained for threaded reply routing when Google Chat enables it.
func (m *Manager) sessionForMeshMessage(ctx context.Context, meshMsgID string) string {
	msg, err := m.store.GetMeshMessage(ctx, meshMsgID)
	if err != nil || msg == nil {
		return ""
	}
	return msg.SessionID
}

func (m *Manager) handlePairing(ctx context.Context, msg IncomingMessage) {
	wsID, err := m.ConsumePairing(ctx, msg.PairingCode)
	if err != nil {
		slog.Info("googlechat: rejected pairing", "code", redactCode(msg.PairingCode))
		return
	}
	now := time.Now().UTC()
	space := &store.GoogleChatSpace{
		ID:          newULID(),
		SpaceName:   msg.SpaceName,
		Title:       msg.SpaceTitle,
		SpaceType:   msg.SpaceType,
		WorkspaceID: wsID,
		SessionID:   Platform + "-" + msg.SpaceName,
		MinPriority: m.defaultMinPriority,
		ListenMode:  m.defaultListenMode,
		Active:      true,
		CreatedAt:   now,
		LastSeenAt:  now,
	}
	if err := m.store.UpsertGoogleChatSpace(ctx, space); err != nil {
		slog.Warn("googlechat: upsert space after pairing", "error", err)
		return
	}
	slog.Info("googlechat: paired space", "space_id", space.ID, "workspace", wsID)
}
