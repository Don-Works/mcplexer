//go:build p2p

package p2p

// DisplayNameProvider returns the local user-set display name. Set via
// SetDisplayNameProvider; nil is fine and means the display_name field
// won't be stamped on outgoing pair payloads / mesh envelopes.
//
// Cross-machine display-name propagation lives behind this provider so
// the pairing_p2p source doesn't need to grow more knobs. NOT auth-bearing.
type DisplayNameProvider func() string

// SetDisplayNameProvider stores the provider for later calls. Currently a
// stub — the underlying display_name fields aren't wired into the pair
// payload/handshake yet (deferred during M7 merge). The setter exists so
// tests + serve.go callers compile; future work will plumb the value
// through QR + handshake.
func (s *PairingService) SetDisplayNameProvider(_ DisplayNameProvider) {
	// no-op for now; see TODO above
}

// takeRemoteDisplayName is a stub — display_name propagation isn't wired
// into the responder side yet. Returns empty string so callers fall back
// to the peer-prefix label.
func (s *PairingService) takeRemoteDisplayName(_ string) string { return "" }
