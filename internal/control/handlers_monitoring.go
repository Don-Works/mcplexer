// handlers_monitoring.go — admin handlers for the Monitoring feature
// (migration 128): remote hosts, log sources, alert channels. Same
// (ctx, store, args) shape as the workspace handlers; validation
// (selector charset, secrets rule) lives in the store layer so REST
// and MCP get identical rejections.
package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

func requireWorkspaceID(args json.RawMessage) (string, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if in.WorkspaceID == "" {
		return "", fmt.Errorf("workspace_id is required")
	}
	return in.WorkspaceID, nil
}

// --- remote hosts ---

func handleCreateRemoteHost(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	var h store.RemoteHost
	if err := json.Unmarshal(args, &h); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	h.Enabled = true
	// Re-read the explicit flag so {"enabled": false} is honoured.
	var flags struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(args, &flags); err == nil && flags.Enabled != nil {
		h.Enabled = *flags.Enabled
	}
	if err := s.CreateRemoteHost(ctx, &h); err != nil {
		return nil, err
	}
	return jsonResult(&h)
}

func handleListRemoteHosts(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	wsID, err := requireWorkspaceID(args)
	if err != nil {
		return nil, err
	}
	hosts, err := s.ListRemoteHosts(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("list remote hosts: %w", err)
	}
	return jsonResult(map[string]any{"remote_hosts": hosts})
}

func handleGetRemoteHost(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	h, err := s.GetRemoteHost(ctx, id)
	if err != nil {
		return nil, err
	}
	return jsonResult(h)
}

func handleUpdateRemoteHost(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	h, err := s.GetRemoteHost(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, h); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	h.ID = id
	if err := s.UpdateRemoteHost(ctx, h); err != nil {
		return nil, err
	}
	return jsonResult(h)
}

func handleDeleteRemoteHost(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteRemoteHost(ctx, id); err != nil {
		return nil, err
	}
	return textResult("deleted"), nil
}

// handleRepinRemoteHost clears the TOFU host-key pin so the next dial
// re-records it. Explicit operator action per ADR 0007 §3 — never
// automatic.
func handleRepinRemoteHost(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.SetRemoteHostPin(ctx, id, ""); err != nil {
		return nil, err
	}
	return textResult("pin cleared — next successful dial re-records it"), nil
}

// --- log sources ---

func handleCreateLogSource(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	var src store.LogSource
	if err := json.Unmarshal(args, &src); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	src.Enabled = true
	var flags struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(args, &flags); err == nil && flags.Enabled != nil {
		src.Enabled = *flags.Enabled
	}
	if err := s.CreateLogSource(ctx, &src); err != nil {
		return nil, err
	}
	return jsonResult(&src)
}

func handleListLogSources(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	wsID, err := requireWorkspaceID(args)
	if err != nil {
		return nil, err
	}
	sources, err := s.ListLogSources(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("list log sources: %w", err)
	}
	return jsonResult(map[string]any{"log_sources": sources})
}

func handleGetLogSource(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	src, err := s.GetLogSource(ctx, id)
	if err != nil {
		return nil, err
	}
	return jsonResult(src)
}

func handleUpdateLogSource(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	src, err := s.GetLogSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, src); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	src.ID = id
	if err := s.UpdateLogSource(ctx, src); err != nil {
		return nil, err
	}
	return jsonResult(src)
}

func handleDeleteLogSource(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteLogSource(ctx, id); err != nil {
		return nil, err
	}
	return textResult("deleted"), nil
}

// --- monitoring channels ---

func handleCreateMonitoringChannel(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	var c store.MonitoringChannel
	if err := json.Unmarshal(args, &c); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	c.Enabled = true
	var flags struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(args, &flags); err == nil && flags.Enabled != nil {
		c.Enabled = *flags.Enabled
	}
	if err := s.CreateMonitoringChannel(ctx, &c); err != nil {
		return nil, err
	}
	return jsonResult(&c)
}

func handleListMonitoringChannels(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	wsID, err := requireWorkspaceID(args)
	if err != nil {
		return nil, err
	}
	channels, err := s.ListMonitoringChannels(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("list monitoring channels: %w", err)
	}
	return jsonResult(map[string]any{"channels": channels})
}

func handleGetMonitoringChannel(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	c, err := s.GetMonitoringChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	return jsonResult(c)
}

func handleUpdateMonitoringChannel(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	c, err := s.GetMonitoringChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(args, c); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	c.ID = id
	if err := s.UpdateMonitoringChannel(ctx, c); err != nil {
		return nil, err
	}
	return jsonResult(c)
}

func handleDeleteMonitoringChannel(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteMonitoringChannel(ctx, id); err != nil {
		return nil, err
	}
	return textResult("deleted"), nil
}
