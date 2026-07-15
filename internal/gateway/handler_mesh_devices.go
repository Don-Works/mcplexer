package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
)

// handleMeshSetDeviceName updates Settings.DisplayName and broadcasts the
// rename to every paired peer so they show "morgan" instead of "peer:xyz".
// Idempotent — re-running with the same name still re-broadcasts (cheap
// nudge for peers that may have just paired).
func (h *handler) handleMeshSetDeviceName(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	if h.settingsSvc == nil {
		return marshalErrorResult("Settings service unavailable — cannot persist device name."), nil
	}

	var req struct {
		Name string `json:"name"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	v := newValidator()
	v.requireStringWithHint("name", req.Name,
		"a short, human-friendly device name (letters, digits, hyphens; <= 64 chars)")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if err := config.ValidateDisplayName(req.Name); err != nil {
		return marshalErrorResult(fmt.Sprintf("invalid name: %v", err)), nil
	}

	settings := h.settingsSvc.Load(ctx)
	prev := settings.DisplayName
	if prev == req.Name {
		// Re-broadcast anyway — costs ~one envelope per peer and ensures any
		// peer that came online since the last rename picks up the name.
		_ = h.mesh.BroadcastDisplayNameChange(ctx, req.Name)
		return marshalToolResult(fmt.Sprintf("Device name already '%s' — re-broadcast to paired peers.", req.Name)), nil
	}
	settings.DisplayName = req.Name
	if err := h.settingsSvc.Save(ctx, settings); err != nil {
		return marshalErrorResult(fmt.Sprintf("save settings: %v", err)), nil
	}
	if err := h.mesh.BroadcastDisplayNameChange(ctx, req.Name); err != nil {
		// Persisted locally but the broadcast leg failed — surface that so
		// the caller can retry; their peers will see the old name until.
		return marshalToolResult(fmt.Sprintf(
			"Device name set to '%s' locally, but broadcast to peers failed: %v",
			req.Name, err,
		)), nil
	}

	return marshalToolResult(fmt.Sprintf(
		"Device name updated: '%s' → '%s'. Broadcast to all paired peers; they can now route to_peer='%s'.",
		prev, req.Name, req.Name,
	)), nil
}

// handleMeshListPeers returns paired peers — friendly name + short peer ID
// — so agents can see who they can address with to_peer.
func (h *handler) handleMeshListPeers(ctx context.Context) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	peers, err := h.mesh.ListPeers(ctx)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("list peers: %v", err)), nil
	}

	self := h.mesh.LocalDisplayName()
	if self == "" {
		self = "(unset)"
	}
	selfPeer := h.mesh.SelfPeerID()

	var b strings.Builder
	fmt.Fprintf(&b, "## Mesh Peer Directory\nThis device: %s (%s)\n\n", self, shortPeer(selfPeer))
	if len(peers) == 0 {
		b.WriteString("No paired peers. Pair a peer first via the p2p pairing flow, then call this again.\n")
		return marshalToolResult(b.String()), nil
	}
	fmt.Fprintf(&b, "Paired peers (%d):\n", len(peers))
	for _, p := range peers {
		name := p.DisplayName
		if name == "" {
			name = "(unnamed)"
		}
		seen := "—"
		if p.LastSeen != nil {
			seen = p.LastSeen.UTC().Format("2006-01-02 15:04:05Z")
		}
		fmt.Fprintf(&b, "- %s  (peer:%s, last seen %s)\n", name, shortPeer(p.PeerID), seen)
	}
	b.WriteString("\nAddress a peer by passing `to_peer: \"<name>\"` to mesh__send.\n")
	return marshalToolResult(b.String()), nil
}

// shortPeer is a log-friendly tail of a libp2p peer ID. Mirrors the helper
// in internal/mesh/p2p_bridge.go but lives here to keep the gateway free
// of cross-package noise.
func shortPeer(peerID string) string {
	if len(peerID) <= 10 {
		return peerID
	}
	return peerID[len(peerID)-10:]
}
