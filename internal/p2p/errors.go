// Package p2p embeds a libp2p Host inside the mcplexer daemon for cross-machine
// agent communication. The libp2p dependency is gated behind the `p2p` build
// tag — when not present, the package compiles to a thin stub that returns
// ErrP2PNotBuiltIn from any operation, so the rest of the daemon can call the
// same API in both build modes.
package p2p

import "errors"

// ErrP2PNotBuiltIn is returned by stub implementations when the binary was not
// built with the `p2p` build tag. Use this sentinel to detect whether p2p is
// available at runtime: `errors.Is(err, p2p.ErrP2PNotBuiltIn)`.
var ErrP2PNotBuiltIn = errors.New("p2p: not built in (rebuild with -tags p2p)")

// ErrPairingInvalid is returned by the pairing service when a code is
// unknown, expired, or already used. Surfaced to the user as "code invalid"
// — never include details that would help an attacker distinguish cases.
var ErrPairingInvalid = errors.New("pairing code invalid or expired")

// ErrDHTUnavailable is returned by Host.FindPeer when no DHT was wired —
// either because the host was constructed without one or because we're in
// stub mode. Reconnector callers swallow this and skip the iteration.
var ErrDHTUnavailable = errors.New("p2p: dht unavailable")

// ErrPeerNotFoundInDHT is returned by Host.FindPeer when the DHT walk
// completed but produced no addresses for the requested peer. Distinct from a
// transport error so the reconnector can decide to back off.
var ErrPeerNotFoundInDHT = errors.New("p2p: peer not found in dht")
