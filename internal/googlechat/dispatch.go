package googlechat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// handleNotify fans an outbound notify event out to every active space in
// the mesh message's workspace, filtering by per-space MinPriority.
func (m *Manager) handleNotify(ctx context.Context, evt notify.Event) {
	workspaceID, err := m.workspaceForMeshMessage(ctx, evt.MessageID)
	if err != nil || workspaceID == "" {
		return
	}
	spaces, err := m.store.ListActiveGoogleChatSpacesByWorkspace(ctx, workspaceID)
	if err != nil || len(spaces) == 0 {
		return
	}

	outgoing := OutgoingMessage{
		MeshMessageID: evt.MessageID,
		Title:         evt.Title,
		Body:          TruncateBody(evt.Body),
		Priority:      evt.Priority,
		Kind:          evt.Kind,
		Tags:          evt.Tags,
	}

	for _, s := range spaces {
		s := s
		if !allowPriority(s.MinPriority, evt.Priority) {
			continue
		}
		m.dispatchOutbound(ctx, s, outgoing)
	}
}

// dispatchOutbound performs one Google Chat send and records the native id
// against the mesh message id so future native replies can thread back.
func (m *Manager) dispatchOutbound(ctx context.Context, space store.GoogleChatSpace, msg OutgoingMessage) {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		slog.Warn("googlechat: client not configured, outbound message dropped",
			"space_id", space.ID)
		return
	}
	nativeID, err := client.Send(ctx, space, msg)
	if err != nil {
		slog.Warn("googlechat: send failed", "space_id", space.ID, "error", err)
		return
	}
	if nativeID == "" {
		return
	}
	rec := &store.GoogleChatSentMessage{
		ID:              newULID(),
		SpaceName:       space.SpaceName,
		ThreadName:      msg.ThreadName,
		NativeMessageID: nativeID,
		MeshMessageID:   msg.MeshMessageID,
		CreatedAt:       time.Now().UTC(),
	}
	if err := m.store.InsertGoogleChatSentMessage(ctx, rec); err != nil {
		slog.Warn("googlechat: record sent", "error", err)
	}
}

func (m *Manager) workspaceForMeshMessage(ctx context.Context, id string) (string, error) {
	msg, err := m.store.GetMeshMessage(ctx, id)
	if err != nil {
		return "", err
	}
	return msg.WorkspaceID, nil
}

// SendBySpaceID sends a plain text message to a specific bound space. Used by
// REST + future MCP tools so agents can target a known space directly.
func (m *Manager) SendBySpaceID(ctx context.Context, spaceID, text, priority string) error {
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	space, err := m.store.GetGoogleChatSpace(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("lookup space: %w", err)
	}
	if !space.Active {
		return fmt.Errorf("space is inactive")
	}
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("googlechat client not configured")
	}
	out := OutgoingMessage{
		Title:    "Agent message",
		Body:     TruncateBody(text),
		Priority: priority,
		Kind:     "event",
	}
	if _, err := client.Send(ctx, *space, out); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	_ = m.store.TouchGoogleChatSpace(ctx, space.ID)
	return nil
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
