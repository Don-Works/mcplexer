package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
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

// meshPeerDirectory is the structured mesh__list_peers result. Agents index
// `peers[]` for to_peer routing; `text` carries the same directory as a
// human-readable render for callers that want prose.
type meshPeerDirectory struct {
	ThisDevice meshSelfDevice  `json:"this_device"`
	Peers      []meshPeerEntry `json:"peers"`
	Count      int             `json:"count"`
	Hint       string          `json:"hint"`
	Text       string          `json:"text"`
}

type meshSelfDevice struct {
	DisplayName string `json:"display_name"`
	PeerID      string `json:"peer_id"`
	PeerShort   string `json:"peer_short"`
}

type meshPeerEntry struct {
	DisplayName string   `json:"display_name"`
	PeerID      string   `json:"peer_id"`
	PeerShort   string   `json:"peer_short"`
	LastSeen    string   `json:"last_seen,omitempty"`
	LastSeenAge string   `json:"last_seen_age,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

// handleMeshListPeers returns paired peers — friendly name + peer ID — so
// agents can see who they can address with to_peer. The wire shape is a JSON
// object with a structured `peers[]` array plus a `text` render; a bare
// markdown string used to make `result.peers` silently undefined for a
// code-mode consumer.
//
// Peer DisplayName is peer-authored free text (a paired machine sets it via
// the p2p rename event). The structured display_name fields are scanned and
// wrapped in the <untrusted-content> trust marker only on a denylist hit
// (clean identifiers stay clean, mirroring mesh__receive), while `text` keeps
// the always-wrapped render. Builtin results never pass through
// sanitizeToolResult, so the handler must wrap itself. The remote rename path
// also validates the name at ingest (mesh.acceptablePeerDisplayName); this is
// the second layer.
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
	scan := h.meshFieldSanitizer(ctx, MeshPrefix+"list_peers", false)
	wrap := h.meshFieldSanitizer(ctx, MeshPrefix+"list_peers", true)
	return marshalJSONResult(buildPeerDirectory(peers, self, h.mesh.SelfPeerID(), scan, wrap))
}

// buildPeerDirectory renders paired peers as a structured directory plus the
// human `text`. Peer-authored display names are run through scan (wrap on a
// denylist hit); `text` keeps the always-wrapped render via wrap.
func buildPeerDirectory(peers []store.P2PPeer, self, selfPeer string, scan, wrap func(string) string) meshPeerDirectory {
	now := time.Now().UTC()
	dir := meshPeerDirectory{
		ThisDevice: meshSelfDevice{DisplayName: self, PeerID: selfPeer, PeerShort: shortPeer(selfPeer)},
		Peers:      make([]meshPeerEntry, 0, len(peers)),
		Count:      len(peers),
		Hint:       "Address a peer by passing to_peer:\"<name>\" (peers[].display_name) to mesh__send. peer_short is the routable short ID.",
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Mesh Peer Directory\nThis device: %s (%s)\n\n", self, shortPeer(selfPeer))
	if len(peers) == 0 {
		b.WriteString("No paired peers. Pair a peer first via the p2p pairing flow, then call this again.\n")
		dir.Text = wrap(b.String())
		return dir
	}
	fmt.Fprintf(&b, "Paired peers (%d):\n", len(peers))
	for _, p := range peers {
		name := p.DisplayName
		if name == "" {
			name = "(unnamed)"
		}
		seen, lastSeen, seenAge := "—", "", ""
		if p.LastSeen != nil {
			seen = p.LastSeen.UTC().Format("2006-01-02 15:04:05Z")
			lastSeen = p.LastSeen.UTC().Format(time.RFC3339)
			seenAge = formatRelativeAge(now.Sub(*p.LastSeen))
		}
		fmt.Fprintf(&b, "- %s  (peer:%s, last seen %s)\n", name, shortPeer(p.PeerID), seen)
		dir.Peers = append(dir.Peers, meshPeerEntry{
			DisplayName: scan(name),
			PeerID:      p.PeerID,
			PeerShort:   shortPeer(p.PeerID),
			LastSeen:    lastSeen,
			LastSeenAge: seenAge,
			Scopes:      p.Scopes,
		})
	}
	b.WriteString("\nAddress a peer by passing `to_peer: \"<name>\"` to mesh__send.\n")
	dir.Text = wrap(b.String())
	return dir
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
