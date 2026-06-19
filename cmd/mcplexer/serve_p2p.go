package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// startP2P boots the embedded libp2p host if cfg.P2PEnabled is true. Returns
// (nil, nil) when disabled — callers must treat a nil host as "feature off".
//
// In binaries built without `-tags p2p` this is a stub: when P2PEnabled is
// true it returns ErrP2PNotBuiltIn so the daemon can log a helpful error and
// keep running. When P2PEnabled is false the call is identical in both
// modes (a no-op).
//
// The on-disk identity key is encrypted with age (via enc) and written to
// {IdentityPath}.age. Pass nil enc to fall back to cleartext at IdentityPath.
func startP2P(ctx context.Context, cfg *Config, enc p2p.Encryptor) (*p2p.Host, error) {
	if !cfg.P2PEnabled {
		return nil, nil
	}
	pcfg := p2p.Config{
		Enabled:           true,
		IdentityPath:      cfg.P2PIdentityPath,
		ListenAddrs:       p2pListenAddrs(),
		EnableMDNS:        true,
		EnableHolePunch:   true,
		EnableRelayClient: true,
		EnableAutoNAT:     true,
		EnableDHT:         true,
	}
	return p2p.NewHost(ctx, pcfg, enc, slog.Default())
}

// fixedP2PPort is the preferred libp2p listen port. Pinning it (instead of an
// OS-assigned ephemeral port) keeps a peer's address stable across its own
// restarts, which is what lets static-dial reconnection survive the *remote*
// side rebooting. Ephemeral fallbacks are also listed so the host still binds
// (just without a stable address) if the fixed port is already taken.
const fixedP2PPort = "14001"

func p2pListenAddrs() []string {
	return []string{
		"/ip4/0.0.0.0/tcp/" + fixedP2PPort,
		"/ip4/0.0.0.0/udp/" + fixedP2PPort + "/quic-v1",
		"/ip6/::/tcp/" + fixedP2PPort,
		"/ip6/::/udp/" + fixedP2PPort + "/quic-v1",
		// Ephemeral fallbacks — always bind something even if 14001 is taken.
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
		"/ip6/::/tcp/0",
		"/ip6/::/udp/0/quic-v1",
	}
}

// mustStartP2P calls startP2P and translates ErrP2PNotBuiltIn into a warning
// log line so a daemon launched with --p2p in a slim build keeps running with
// the rest of its features intact. Any other error is logged and the daemon
// continues without p2p — failure to bind a libp2p socket should never take
// down the gateway.
func mustStartP2P(ctx context.Context, cfg *Config, enc p2p.Encryptor) *p2p.Host {
	h, err := startP2P(ctx, cfg, enc)
	if err == nil {
		if h != nil {
			slog.Info("p2p host ready", "peer_id", h.PeerID(), "addrs", h.Addrs())
			// Re-dial direct addresses recorded via POST /api/p2p/connect so
			// links to DHT-unreachable peers (e.g. a peer reachable only on a
			// Tailscale IP it doesn't advertise) survive a restart of either
			// side. Runs shortly after boot, then on a timer; RedialStatic
			// skips already-connected peers so steady state is quiet.
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				ticker := time.NewTicker(60 * time.Second)
				defer ticker.Stop()
				for {
					if n := h.RedialStatic(ctx); n > 0 {
						slog.Info("p2p: re-dialed static peers", "count", n)
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		return h
	}
	if errors.Is(err, p2p.ErrP2PNotBuiltIn) {
		slog.Warn("p2p requested but not built in", "hint", "rebuild with -tags p2p")
		return nil
	}
	slog.Error("p2p startup failed", "error", err)
	return nil
}

// buildPairingService returns a *p2p.PairingService bound to host + the
// shared store. Returns nil when the host itself is nil (p2p disabled or
// startup failed) so the API router can register a 501 handler.
//
// We also attach the P2PPeerStore as a PeerPersister so the responder side
// of a successful handshake inserts the initiator into our p2p_peers table
// (fixes the asymmetric pairing bug: ClickUp 86c9kfhhb).
func buildPairingService(host *p2p.Host, st store.P2PPeerStore) *p2p.PairingService {
	if host == nil {
		return nil
	}
	svc := p2p.NewPairingService(host, p2p.StorePairingAdapter{S: st})
	svc.SetPeerPersister(st)
	return svc
}

// attachSelfIdentity wires the bootstrapped local user into the pairing
// service so the QR payload + handshake stream carry the self user_id +
// display_name (M7.1). Safe to call with a nil pairing service (stub
// builds, p2p disabled) — it's a no-op.
func attachSelfIdentity(p *p2p.PairingService, users store.UserStore, self *store.User) {
	if p == nil || self == nil {
		return
	}
	p.SetSelfIdentity(self.UserID, self.DisplayName)
	p.SetUserLinker(userLinkerAdapter{users: users})
}

// userLinkerAdapter bridges store.UserStore to p2p.UserLinker. Defined here
// rather than in the p2p package so the p2p layer can stay free of the
// composite store.Store dependency.
type userLinkerAdapter struct {
	users store.UserStore
}

func (a userLinkerAdapter) UpsertUser(ctx context.Context, userID, displayName string) error {
	return a.users.UpsertUser(ctx, userID, displayName)
}

func (a userLinkerAdapter) LinkPeerToUser(ctx context.Context, peerID, userID string) error {
	return a.users.LinkPeerToUser(ctx, peerID, userID)
}

// resolveP2PEnabled merges runtime/CLI/env config (cfg.P2PEnabled) with the
// persisted settings row so the UI toggle has effect on next start without
// requiring an env-var change. CLI/env takes precedence over the DB row.
func resolveP2PEnabled(ctx context.Context, cfg *Config, settingsSvc *config.SettingsService) {
	if cfg.P2PEnabled {
		return
	}
	if settingsSvc == nil {
		return
	}
	if settingsSvc.Load(ctx).P2PEnabled {
		cfg.P2PEnabled = true
	}
}

// agentBroadcasterAdapter bridges mesh.Manager's AgentBroadcaster
// interface (which speaks store.MeshAgent for cleanliness) to the p2p
// layer's AgentDirectoryService (which speaks p2p.AgentRecord on the
// wire). Conversion is cheap and isolates the wire format from the
// store package — peer:* origin filtering happens here so no peer-origin
// row ever leaks back across the wire.
type agentBroadcasterAdapter struct {
	svc *p2p.AgentDirectoryService
}

func (a agentBroadcasterAdapter) BroadcastDelta(
	ctx context.Context, added []store.MeshAgent, removed []string,
) {
	if a.svc == nil {
		return
	}
	records := make([]p2p.AgentRecord, 0, len(added))
	for _, ag := range added {
		// Safety: never echo peer-origin agents back over the wire.
		// Same belt + braces as LocalAgentSource.
		if isPeerOrigin(ag.Origin) {
			continue
		}
		records = append(records, p2p.AgentRecord{
			SessionID:  ag.SessionID,
			Name:       ag.Name,
			Role:       ag.Role,
			ClientType: ag.ClientType,
			LastSeenAt: ag.LastSeenAt,
		})
	}
	a.svc.BroadcastDelta(ctx, records, removed)
}

func isPeerOrigin(origin string) bool {
	const prefix = "peer:"
	return len(origin) >= len(prefix) && origin[:len(prefix)] == prefix
}

// makeDisplayNameProvider returns a closure that resolves the local
// Settings.DisplayName at call time. Wired into PairingService + mesh
// Manager so the live UI value is always used (no stale snapshot).
//
// We pass a long-lived ctx so the closure isn't tied to a single HTTP
// request; SettingsService.Load uses a SQLite read which is cheap. NOT
// auth-bearing — see Settings.DisplayName + MeshEnvelope.SenderDisplayName.
func makeDisplayNameProvider(
	ctx context.Context, settingsSvc *config.SettingsService,
) func() string {
	return func() string {
		if settingsSvc == nil {
			return ""
		}
		return settingsSvc.Load(ctx).DisplayName
	}
}
