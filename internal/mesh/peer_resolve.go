package mesh

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrPeerNotPaired is returned by ResolvePeer when the input does not map
// to any currently-paired peer. Callers should render a uniform message
// nudging the user to mesh__list_peers.
var ErrPeerNotPaired = errors.New("mesh: peer not paired")

// ErrAmbiguousPeer is returned by ResolvePeer when the input matches more
// than one paired peer (e.g. two devices share a display_name). Callers
// should ask the user to disambiguate via the full peer ID.
var ErrAmbiguousPeer = errors.New("mesh: peer name is ambiguous")

// shortPeerIDLen is the suffix length used by mesh__list_peers and other
// human-facing surfaces to render a short, copy-pasteable peer ID. Must
// stay in sync with the formatter in handler_mesh_devices.go (shortPeer).
const shortPeerIDLen = 10

// ResolvePeer maps any of {full libp2p peer ID, short 10-char suffix,
// device display_name} to the full libp2p peer ID of a currently-paired,
// non-revoked peer. Returns ErrPeerNotPaired when nothing matches and
// ErrAmbiguousPeer when the input matches multiple peers (display-name
// collision; the full or short peer ID is always unique).
//
// Comparison rules:
//   - Full peer ID: case-sensitive exact match against P2PPeer.PeerID.
//   - Short ID (input shape: shortPeerIDLen non-whitespace chars, no
//     base58 prefix): case-sensitive suffix match against P2PPeer.PeerID.
//   - Display name: case-insensitive, whitespace-trimmed match against
//     P2PPeer.DisplayName.
//
// Revoked peers are never returned. Empty input returns ErrPeerNotPaired.
func (m *Manager) ResolvePeer(ctx context.Context, input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", fmt.Errorf("%w: empty input", ErrPeerNotPaired)
	}
	if m == nil || m.peerLister == nil {
		return "", fmt.Errorf("%w: no peer lister configured", ErrPeerNotPaired)
	}
	peers, err := m.peerLister.ListPeers(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: list peers: %v", ErrPeerNotPaired, err)
	}

	// Pass 1: exact peer-ID match. Cheapest + most specific; a paired peer
	// is always uniquely identified by its full libp2p ID so this short-
	// circuits before we have to think about ambiguity.
	for _, p := range peers {
		if p.RevokedAt != nil {
			continue
		}
		if p.PeerID == s {
			return p.PeerID, nil
		}
	}

	// Pass 2: short-ID suffix match. The short form is the last
	// shortPeerIDLen chars of the full peer ID (see shortPeer() in
	// handler_mesh_devices.go); two paired peers cannot share a suffix
	// of that length in practice but we still check for collisions to
	// surface ErrAmbiguousPeer if they ever do.
	if looksLikeShortID(s) {
		matches := matchShortID(peers, s)
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("%w: short id %q matches %d peers",
				ErrAmbiguousPeer, s, len(matches))
		}
	}

	// Pass 3: display_name match (case-insensitive). Names are user-set
	// and not guaranteed unique, so an ambiguous hit is a real error.
	matches := matchDisplayName(peers, s)
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%w: name %q matches %d peers",
			ErrAmbiguousPeer, s, len(matches))
	}

	return "", fmt.Errorf("%w: %q", ErrPeerNotPaired, s)
}

// looksLikeShortID returns true when s has the shape of a short peer ID
// suffix as rendered by mesh__list_peers — exactly shortPeerIDLen chars
// and no whitespace. We deliberately do NOT require base58 because the
// suffix can contain any base58 alphabet char including digits.
func looksLikeShortID(s string) bool {
	if len(s) != shortPeerIDLen {
		return false
	}
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return false
		}
	}
	return true
}

// matchShortID returns the full peer IDs of every non-revoked peer whose
// peer_id ends with the given suffix. Used by ResolvePeer.
func matchShortID(peers []store.P2PPeer, suffix string) []string {
	var out []string
	for _, p := range peers {
		if p.RevokedAt != nil {
			continue
		}
		if strings.HasSuffix(p.PeerID, suffix) {
			out = append(out, p.PeerID)
		}
	}
	return out
}

// matchDisplayName returns the full peer IDs of every non-revoked peer
// whose display_name matches the input (case-insensitive, whitespace-
// trimmed on both sides). Used by ResolvePeer.
func matchDisplayName(peers []store.P2PPeer, input string) []string {
	target := strings.ToLower(strings.TrimSpace(input))
	if target == "" {
		return nil
	}
	var out []string
	for _, p := range peers {
		if p.RevokedAt != nil {
			continue
		}
		stored := strings.ToLower(strings.TrimSpace(p.DisplayName))
		if stored == target {
			out = append(out, p.PeerID)
		}
	}
	return out
}

// FormatPeerNotPairedError renders a uniform, agent-friendly error for
// the common case where ResolvePeer returned ErrPeerNotPaired or
// ErrAmbiguousPeer. Centralised so every mesh__* tool gives the same
// guidance.
func FormatPeerNotPairedError(input string, err error) string {
	switch {
	case errors.Is(err, ErrAmbiguousPeer):
		return fmt.Sprintf(
			"%q matches multiple paired peers — disambiguate by passing the full peer id (try mesh__list_peers to see paired devices)",
			input)
	case errors.Is(err, ErrPeerNotPaired):
		return fmt.Sprintf(
			"no paired peer matches %q (try mesh__list_peers to see paired devices)",
			input)
	default:
		return err.Error()
	}
}
