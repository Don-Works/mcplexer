package p2p

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"
)

// MeshProtocol is the libp2p protocol ID used for cross-machine mesh
// envelopes. Wire format is length-prefixed JSON; see MeshEnvelope.
const MeshProtocol = "/mcplexer/mesh/1.0.0"

// Recipient identifies the intended target of a mesh envelope.
//
//	Kind=="peer"     → Value is a libp2p peer ID; deliver only to that node
//	Kind=="role"     → Value is an agent role; deliver to local agents matching
//	Kind=="audience" → Value is an audience selector ("*" or session ID)
type Recipient struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// MeshEnvelope is the wire-format struct sent over the libp2p mesh
// protocol. Field order is the canonical signing order — do not reorder.
//
// SenderUserID is reserved for future multi-user installs; for v1 it's
// always empty (single-user-per-host).
type MeshEnvelope struct {
	ID                string    `json:"id"`
	SenderPeerID      string    `json:"sender_peer_id"`
	SenderUserID      string    `json:"sender_user_id,omitempty"`
	SenderDisplayName string    `json:"sender_display_name,omitempty"` // display polish (M7); NOT auth-bearing
	Recipient         Recipient `json:"recipient"`
	Kind              string    `json:"kind"`
	Content           string    `json:"content"`
	Tags              string    `json:"tags,omitempty"`
	Payload           []byte    `json:"payload,omitempty"`
	TS                int64     `json:"ts"`
	Signature         []byte    `json:"signature,omitempty"`

	// Priority is the sender's message priority ("critical" | "high" |
	// "normal" | "low"). Optional on the wire — legacy peers omit it and
	// receivers clamp unknown/empty values to "normal". The signature does
	// not cover this field (see canonicalSigningBytes), matching Kind/Tags.
	Priority string `json:"priority,omitempty"`

	// M7.3 — optional repo + branch + workspace scoping. Pre-M7.3 peers
	// omit these fields entirely; receivers fall back to empty strings
	// without erroring (handled by `omitempty` + JSON's zero-value default).
	Repo          string `json:"repo,omitempty"`
	Branch        string `json:"branch,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	RepoRemote    string `json:"repo_remote,omitempty"`

	// WorkspaceID is the sender's local workspace_id (G1 — workspace
	// scoping on the wire). Receivers resolve this against
	// workspace_peer_bindings to land the inbound message in the
	// matching LOCAL workspace; envelopes with WorkspaceID set but no
	// binding are dropped with an audit record. Empty (legacy peers or
	// explicit broadcast) lands in a global bucket that real workspaces'
	// triggers (G2) refuse to fire on.
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// MaxEnvelopeBytes caps a single envelope on the wire. Beyond this we drop
// the stream — keeps memory bounded against a malicious peer flooding a
// 4 GiB length prefix.
const MaxEnvelopeBytes = 1 << 20 // 1 MiB

// errEnvelopeTooLarge is returned by stream readers when a length prefix
// exceeds MaxEnvelopeBytes.
var errEnvelopeTooLarge = errors.New("p2p mesh: envelope exceeds max size")

// canonicalSigningBytes returns the bytes covered by an envelope's Ed25519
// signature: SHA-256(id || ts_be || payload). Keeping this small + stable
// avoids the JSON-canonicalisation rabbit-hole while still binding the
// content to the signer.
func canonicalSigningBytes(id string, ts int64, payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte(id))
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h.Write(tsBuf[:])
	h.Write(payload)
	return h.Sum(nil)
}

// envelopeAge returns how long ago the envelope was sent, in real time.
func envelopeAge(env *MeshEnvelope) time.Duration {
	if env == nil {
		return 0
	}
	return time.Since(time.UnixMilli(env.TS))
}
