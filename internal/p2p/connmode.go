package p2p

// ConnectionMode describes how a peer is currently reachable. The four states are
// returned to callers (REST API, debug UI, audit logs) as plain strings so
// downstream code doesn't need to import this package.
//
// Defined in the build-tag-agnostic file so callers in cmd/, internal/api/,
// and the UI handler can reference these without requiring `-tags p2p`. The
// actual classification logic lives in connmode_p2p.go (p2p tag only).
type ConnectionMode string

const (
	// ModeNone — no active connection.
	ModeNone ConnectionMode = "none"
	// ModeDirect — direct dial succeeded; no NAT traversal needed (LAN, public
	// IP, or successful UPnP/PCP port mapping).
	ModeDirect ConnectionMode = "direct"
	// ModeHolePunched — DCUtR succeeded; the connection looks direct now but
	// was upgraded from a relay-mediated rendezvous via STUN-style hole-punch.
	ModeHolePunched ConnectionMode = "hole-punched"
	// ModeRelay — connection is going through a circuit-v2 relay. Limited
	// (bandwidth/time-capped) and slower; used as fallback only.
	ModeRelay ConnectionMode = "relay"
)

// PeerMode pairs a peer ID string with its current connection mode. Returned
// by Host.PeerModes for the optional debug panel. JSON-tagged so it can be
// embedded directly in REST responses.
type PeerMode struct {
	Peer string         `json:"peer"`
	Mode ConnectionMode `json:"mode"`
}
