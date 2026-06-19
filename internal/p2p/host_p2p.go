//go:build p2p

package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
)

// Host wraps a libp2p host with the discovery, ping, and lifecycle hooks
// the mcplexer daemon needs. Construct with NewHost; close with Close.
type Host struct {
	cfg       Config
	h         host.Host
	pinger    *ping.PingService
	mdns      mdns.Service
	dht       *dht.IpfsDHT      // optional; set when EnableDHT is true
	discovery *DiscoveryService // optional; set by NewDiscoveryService
	tracker   *holePunchTracker // optional; set when hole-punch tracing enabled
	logger    *slog.Logger
	closeMu   sync.Mutex
	closed    bool
}

// NewHost boots a libp2p Host using cfg. If cfg.Enabled is false, returns
// (nil, nil). If enc is non-nil, the on-disk identity key is encrypted at
// rest using age (file path = cfg.IdentityPath + ".age"); otherwise the key
// is stored in cleartext at cfg.IdentityPath. The returned Host owns its
// lifecycle (close with Close); the ctx is used only for setup-time work.
func NewHost(ctx context.Context, cfg Config, enc Encryptor, logger *slog.Logger) (*Host, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}

	priv, err := loadIdentity(cfg.IdentityPath, enc)
	if err != nil {
		return nil, err
	}

	tracker := newHolePunchTracker(logger)
	opts, err := buildLibp2pOptions(cfg, priv, tracker)
	if err != nil {
		return nil, err
	}
	lh, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("p2p: libp2p.New: %w", err)
	}

	h := &Host{cfg: cfg, h: lh, tracker: tracker, logger: logger}
	if err := h.startServices(ctx); err != nil {
		_ = lh.Close()
		return nil, err
	}
	if cfg.EnableDHT {
		bootstraps, perr := parseRelays(cfg.BootstrapRelays)
		if perr != nil {
			logger.Warn("p2p dht: bootstrap parse failed; continuing without DHT", "err", perr)
		} else {
			kdht, derr := wireDHT(ctx, lh, bootstraps, logger)
			if derr != nil {
				logger.Warn("p2p dht: init failed; continuing without DHT", "err", derr)
			} else {
				h.dht = kdht
			}
		}
	}
	logger.Info("p2p host started",
		"peer_id", lh.ID().String(),
		"addrs", multiaddrsAsStrings(lh.Addrs()),
		"mdns", cfg.EnableMDNS,
		"holepunch", cfg.EnableHolePunch,
		"relay_client", cfg.EnableRelayClient,
		"autonat", cfg.EnableAutoNAT,
		"dht", h.dht != nil,
		"static_relays", len(cfg.BootstrapRelays),
	)
	return h, nil
}

// buildLibp2pOptions converts our Config into a slice of libp2p.Options.
// AutoNAT v1 is always wired up by libp2p as part of basic_host setup; we
// explicitly enable AutoNAT v2 (the more reliable, RPC-based variant) and
// optionally enable hole-punch + relay-client based on cfg.
func buildLibp2pOptions(cfg Config, priv libp2pPrivKey, tr holepunch.EventTracer) ([]libp2p.Option, error) {
	cm, err := connmgr.NewConnManager(cfg.ConnMgrLowWater, cfg.ConnMgrHighWater)
	if err != nil {
		return nil, fmt.Errorf("p2p: connection manager: %w", err)
	}
	opts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(cfg.ListenAddrs...),
		libp2p.DefaultTransports,
		libp2p.DefaultSecurity,
		libp2p.DefaultMuxers,
		libp2p.NATPortMap(),
		libp2p.ConnectionManager(cm),
	}
	if cfg.EnableAutoNAT {
		// EnableNATService lets us answer AutoNAT dial-back probes from peers.
		// EnableAutoNATv2 turns on the more reliable v2 protocol on our side.
		opts = append(opts, libp2p.EnableNATService(), libp2p.EnableAutoNATv2())
	}
	opts = appendHolePunchOpts(opts, cfg, tr)
	relayOpts, err := buildRelayOpts(cfg)
	if err != nil {
		return nil, err
	}
	return append(opts, relayOpts...), nil
}

// appendHolePunchOpts appends DCUtR options when EnableHolePunch is set. The
// tracer is wired so we can record per-peer hole-punch outcomes for the
// connection-mode reporter and the optional debug UI.
func appendHolePunchOpts(opts []libp2p.Option, cfg Config, tr holepunch.EventTracer) []libp2p.Option {
	if !cfg.EnableHolePunch {
		return opts
	}
	return append(opts, libp2p.EnableHolePunching(holepunch.WithTracer(tr)))
}

// buildRelayOpts wires the libp2p relay-client subsystem. We never run a
// relay server ourselves — only consume the public bootstrap relays as
// fallback when hole-punching fails.
func buildRelayOpts(cfg Config) ([]libp2p.Option, error) {
	if !cfg.EnableRelayClient {
		return nil, nil
	}
	out := []libp2p.Option{libp2p.EnableRelay()}
	relays, err := parseRelays(cfg.BootstrapRelays)
	if err != nil {
		return nil, err
	}
	if len(relays) == 0 {
		return out, nil
	}
	return append(out, libp2p.EnableAutoRelayWithStaticRelays(relays)), nil
}

// startServices wires ping + mDNS onto the booted host.
func (h *Host) startServices(_ context.Context) error {
	h.pinger = ping.NewPingService(h.h)
	if h.cfg.EnableMDNS {
		svc := mdns.NewMdnsService(h.h, h.cfg.MDNSServiceTag, &mdnsNotifee{h: h})
		if err := svc.Start(); err != nil {
			return fmt.Errorf("p2p: start mDNS: %w", err)
		}
		h.mdns = svc
	}
	return nil
}

// ID returns the host's libp2p peer ID.
func (h *Host) ID() peer.ID { return h.h.ID() }

// PeerID returns the host's libp2p peer ID as a string. Available in both
// build modes (stubs return "").
func (h *Host) PeerID() string {
	if h == nil {
		return ""
	}
	return h.h.ID().String()
}

// Addrs returns the host's listen multiaddrs as strings.
func (h *Host) Addrs() []string {
	if h == nil {
		return nil
	}
	return multiaddrsAsStrings(h.h.Addrs())
}

// Inner exposes the underlying libp2p Host for advanced callers (tests,
// future protocol handlers). Treat as unstable. Not available in stub builds.
func (h *Host) Inner() host.Host { return h.h }

// ConnectionMode returns the current connection mode for the given peer.
// Returns ModeNone when there is no live connection. The mode is computed
// fresh from the active conn(s) — relay/limited connections take precedence
// over direct, hole-punched is detected by consulting the holepunch tracker.
func (h *Host) ConnectionMode(p peer.ID) ConnectionMode {
	if h == nil {
		return ModeNone
	}
	conns := h.h.Network().ConnsToPeer(p)
	return classifyConns(conns, h.tracker.wasHolePunched(p))
}

// PeerModes returns a snapshot of connection mode for every peer the host is
// currently connected to. Useful for the optional debug panel.
func (h *Host) PeerModes() []PeerMode {
	if h == nil {
		return nil
	}
	peers := h.h.Network().Peers()
	out := make([]PeerMode, 0, len(peers))
	for _, p := range peers {
		out = append(out, PeerMode{Peer: p.String(), Mode: h.ConnectionMode(p)})
	}
	return out
}

// Pinger returns the ping service for round-trip checks. Not available in
// stub builds.
func (h *Host) Pinger() *ping.PingService { return h.pinger }

// Self returns the host's libp2p peer ID. The reconnector uses this to skip
// the trivial "find myself" iteration when a paired-peer list contains us.
func (h *Host) Self() peer.ID {
	if h == nil {
		return ""
	}
	return h.h.ID()
}

// IsConnected reports whether libp2p currently has at least one open
// connection to peerID. Used by the reconnector to skip already-live peers.
func (h *Host) IsConnected(peerID peer.ID) bool {
	if h == nil {
		return false
	}
	return len(h.h.Network().ConnsToPeer(peerID)) > 0
}

// ConnectAddrInfo dials the given AddrInfo, adding its addrs to the peerstore
// first so libp2p has somewhere to try. Returns nil on success — the caller
// can verify with IsConnected.
func (h *Host) ConnectAddrInfo(ctx context.Context, info peer.AddrInfo) error {
	if h == nil {
		return errors.New("p2p: nil host")
	}
	if len(info.Addrs) > 0 {
		h.h.Peerstore().AddAddrs(info.ID, info.Addrs, peerstoreTTL)
	}
	return h.h.Connect(ctx, info)
}

// Connect dials the given peer (multiaddr-with-/p2p/ID) and adds it to the
// peerstore. Useful in tests and for static peer wiring.
func (h *Host) Connect(ctx context.Context, addr string) (peer.ID, error) {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return "", fmt.Errorf("p2p: parse multiaddr %q: %w", addr, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return "", fmt.Errorf("p2p: addr info from %q: %w", addr, err)
	}
	if err := h.h.Connect(ctx, *info); err != nil {
		return "", fmt.Errorf("p2p: connect %s: %w", info.ID, err)
	}
	return info.ID, nil
}

// ConnectString is a string-return wrapper around Connect so non-p2p-tagged
// call sites (e.g. the REST handler) stay free of the libp2p peer.ID type and
// the !p2p stub can mirror the signature without importing libp2p.
func (h *Host) ConnectString(ctx context.Context, addr string) (string, error) {
	pid, err := h.Connect(ctx, addr)
	if err != nil {
		return "", err
	}
	return pid.String(), nil
}

// Close shuts down all services and the underlying host.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	var errs []error
	if h.mdns != nil {
		if err := h.mdns.Close(); err != nil {
			errs = append(errs, fmt.Errorf("mdns: %w", err))
		}
	}
	if h.dht != nil {
		if err := h.dht.Close(); err != nil {
			errs = append(errs, fmt.Errorf("dht: %w", err))
		}
	}
	if h.tracker != nil {
		h.tracker.close()
	}
	if err := h.h.Close(); err != nil {
		errs = append(errs, fmt.Errorf("host: %w", err))
	}
	return errors.Join(errs...)
}

// mdnsNotifee bridges mDNS discoveries into peerstore inserts. We don't
// auto-dial; downstream code can decide whether to connect.
type mdnsNotifee struct{ h *Host }

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.h.ID() {
		return
	}
	n.h.logger.Debug("p2p mdns peer found", "peer", pi.ID, "addrs", pi.Addrs)
	n.h.h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstoreTTL)
	if d := n.h.discovery; d != nil {
		go d.onMDNSFound(pi)
	}
}

// Discovery returns the DiscoveryService bound to this host, if any. Nil
// when no service has been attached.
func (h *Host) Discovery() *DiscoveryService {
	if h == nil {
		return nil
	}
	return h.discovery
}

// LastSeenAddrs returns the peerstore-cached multiaddrs for a peer ID
// (rendered as strings). Returns nil for unknown peers. Useful for the
// /api/p2p/peers/{id}/status endpoint.
func (h *Host) LastSeenAddrs(idStr string) []string {
	if h == nil {
		return nil
	}
	pid, err := peer.Decode(idStr)
	if err != nil {
		return nil
	}
	return multiaddrsAsStrings(h.h.Peerstore().Addrs(pid))
}
