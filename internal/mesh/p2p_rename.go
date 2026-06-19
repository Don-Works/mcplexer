package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// Rename plumbing for cross-machine display_name updates. Split out of
// p2p_bridge.go to keep that file under the 300-line limit.
//
// Wire shape: kind="event" + tag="display_name_change" + content
// `{"new_name":"…"}`. Receivers update their local p2p_peers.display_name
// row for the sender. NOT auth-bearing — the cryptographic identity of the
// rename is still the libp2p PeerID and the envelope signature.

// DisplayNameChangedKind is the mesh envelope kind broadcast when the user
// updates Settings.DisplayName. Receivers update p2p_peers.display_name for
// the sender. NOT auth-bearing — the rename is keyed off the libp2p PeerID.
const DisplayNameChangedKind = "event"

// DisplayNameChangedTag is appended to the envelope tags so receivers can
// filter the event without doing content sniffing.
const DisplayNameChangedTag = "display_name_change"

// DisplayNameUpdater is the narrow store surface the bridge uses when a
// peer broadcasts a display_name_changed event. Wires to the sqlite-backed
// P2PPeerStore in production; tests pass an in-memory fake.
type DisplayNameUpdater interface {
	UpdateDisplayName(ctx context.Context, peerID, newName string) error
}

// PeerLister is the narrow read surface used to resolve a friendly device
// name (e.g. "elliot") to a libp2p peer ID for to_peer routing, and to back
// the mesh__list_peers MCP tool. Wires to P2PPeerStore in production; tests
// pass an in-memory fake.
type PeerLister interface {
	ListPeers(ctx context.Context) ([]store.P2PPeer, error)
}

// SetPeerRenamer wires the store-side update hook used by the
// display_name_changed event handler. nil-safe; absent updater means rename
// events are logged + ignored.
func (m *Manager) SetPeerRenamer(u DisplayNameUpdater) {
	if m == nil {
		return
	}
	m.peerRenamer = u
}

// SetPeerLister wires the read hook used to resolve friendly device names
// → peer IDs and to back the mesh__list_peers MCP tool. nil-safe; absent
// lister means name lookup falls back to "no match" (caller treats input
// as a literal peer ID).
func (m *Manager) SetPeerLister(l PeerLister) {
	if m == nil {
		return
	}
	m.peerLister = l
}

// ListPeers returns paired peers (excluding revoked rows). Empty slice
// when no peer lister is wired or the store is empty. Used by the
// mesh__list_peers MCP tool.
func (m *Manager) ListPeers(ctx context.Context) ([]store.P2PPeer, error) {
	if m == nil || m.peerLister == nil {
		return nil, nil
	}
	all, err := m.peerLister.ListPeers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.P2PPeer, 0, len(all))
	for _, p := range all {
		if p.RevokedAt != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// LocalDisplayName returns this device's user-set display name, or "" if no
// provider is wired. Used by mesh__list_peers to render "(this device)" and
// by the gateway when it needs to report the local label.
func (m *Manager) LocalDisplayName() string {
	return m.localDisplayName()
}

// SelfPeerIDForTest is exported for tests in the mesh_test package. The
// production caller is mesh.Manager.SelfPeerID() in p2p_bridge.go.
// (Intentionally empty when no transport is wired.)

// ResolveDeviceName maps a friendly name (e.g. "elliot") to a libp2p peer
// ID by consulting the paired-peers directory. If the input already looks
// like a peer ID (starts with "12D" or "Qm"), it is returned unchanged.
// Empty/unknown inputs return "" — caller decides whether that's an error
// or a broadcast intent.
//
// The stored display_name is trim-and-lowercased on the comparison side
// because legacy rows pre-dating the resolveSelfDisplayName sanitiser fix
// can carry trailing whitespace ("example-host.invalid ") from a raw OS hostname.
// Without this trim a user typing the visible name still gets "does not
// match any paired device" until they re-pair. The trim is read-only —
// callers that round-trip the DisplayName back to the wire still see the
// stored value verbatim; this only affects name → peer-id lookup.
func (m *Manager) ResolveDeviceName(ctx context.Context, nameOrPeerID string) string {
	s := strings.TrimSpace(nameOrPeerID)
	if s == "" {
		return ""
	}
	if looksLikePeerID(s) {
		return s
	}
	if m == nil || m.peerLister == nil {
		return ""
	}
	peers, err := m.peerLister.ListPeers(ctx)
	if err != nil {
		return ""
	}
	target := strings.ToLower(s)
	for _, p := range peers {
		if p.RevokedAt != nil {
			continue
		}
		stored := strings.ToLower(strings.TrimSpace(p.DisplayName))
		if stored == target {
			return p.PeerID
		}
	}
	return ""
}

// looksLikePeerID is a cheap shape check for libp2p peer IDs: base58btc
// strings starting with "12D" (Ed25519) or "Qm" (RSA legacy), at least 40
// chars. Not a strict validator — just enough to distinguish a peer ID
// from a friendly name like "elliot" without doing a full multihash decode.
func looksLikePeerID(s string) bool {
	if len(s) < 40 {
		return false
	}
	return strings.HasPrefix(s, "12D") || strings.HasPrefix(s, "Qm")
}

// SetDisplayNameProvider wires a fn that returns the local user-set
// display name. The Manager stamps every outgoing libp2p envelope with the
// returned label so receivers can render "from peer-laptop" instead of the
// peer-prefix fallback. nil-safe; returning "" yields no SenderDisplayName.
//
// IMPORTANT: NOT auth-bearing. The cryptographic identity of an envelope
// is the libp2p PeerID + signature; SenderDisplayName is a UX hint only.
func (m *Manager) SetDisplayNameProvider(fn func() string) {
	if m == nil {
		return
	}
	m.displayNameFn = fn
}

// applyDisplayNameChange persists a peer rename received over the mesh.
// Best-effort: logs and swallows errors so a flaky DB doesn't poison the
// inbound bridge. The new name is parsed from envelope.Content as JSON
// {"new_name": "..."} (see BroadcastDisplayNameChange).
func (m *Manager) applyDisplayNameChange(ctx context.Context, env p2p.MeshEnvelope) {
	if m.peerRenamer == nil {
		return
	}
	newName := parseRenameContent(env.Content)
	if newName == "" {
		return
	}
	if err := m.peerRenamer.UpdateDisplayName(ctx, env.SenderPeerID, newName); err != nil {
		slog.Default().Debug("p2p: rename paired peer",
			"peer", env.SenderPeerID, "new_name", newName, "err", err)
	}
}

// BroadcastDisplayNameChange ships a `display_name_changed` mesh event to
// every paired peer. Receivers update their local p2p_peers.display_name
// for THIS host. Idempotent + best-effort: a transport error is logged
// but does not roll the rename back on this side. nil-safe.
//
// NOT auth-bearing: the rename is keyed off the libp2p PeerID; the
// envelope signature still proves the rename came from us.
func (m *Manager) BroadcastDisplayNameChange(ctx context.Context, newName string) error {
	if m == nil || m.p2p == nil {
		return nil
	}
	if newName == "" {
		return errors.New("BroadcastDisplayNameChange: new name required")
	}
	body, err := json.Marshal(map[string]string{"new_name": newName})
	if err != nil {
		return err
	}
	env := &p2p.MeshEnvelope{
		ID:                newULID(),
		SenderPeerID:      m.selfPeerID,
		SenderDisplayName: newName,
		Kind:              DisplayNameChangedKind,
		Tags:              DisplayNameChangedTag,
		Content:           string(body),
		Recipient:         p2p.Recipient{Kind: "audience", Value: "*"},
		TS:                time.Now().UnixMilli(),
	}
	if _, err := m.p2p.SendBroadcast(ctx, env); err != nil {
		if errors.Is(err, p2p.ErrP2PNotBuiltIn) {
			return nil
		}
		return err
	}
	return nil
}

// isDisplayNameChange returns true when env carries the rename signal
// (kind=event + tag=display_name_change). Cheap string check; the content
// is parsed only on a positive match.
func isDisplayNameChange(env p2p.MeshEnvelope) bool {
	if env.Kind != DisplayNameChangedKind {
		return false
	}
	for _, tag := range splitTags(env.Tags) {
		if tag == DisplayNameChangedTag {
			return true
		}
	}
	return false
}

// splitTags is a tiny CSV splitter used by isDisplayNameChange.
func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	out := []string{}
	cur := ""
	for _, r := range tags {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// parseRenameContent extracts the new display name from a rename envelope's
// Content field. Tolerant of malformed input — returns "" so the caller
// no-ops instead of crashing.
func parseRenameContent(s string) string {
	if s == "" {
		return ""
	}
	var body struct {
		NewName string `json:"new_name"`
	}
	if err := json.Unmarshal([]byte(s), &body); err != nil {
		return ""
	}
	return body.NewName
}
