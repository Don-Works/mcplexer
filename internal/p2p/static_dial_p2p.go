//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// static-dial.json sits next to the identity key and records multiaddrs
// (each ending in /p2p/<peerid>) that were established via an explicit
// POST /api/p2p/connect. These are direct, DHT-independent addresses — e.g.
// a peer's Tailscale IP that it does not advertise — re-dialed periodically
// so the link survives a restart of either side (the reconnector otherwise
// only resolves peers via the DHT).

func (h *Host) staticDialFile() string {
	return filepath.Join(filepath.Dir(h.cfg.IdentityPath), "static-dial.json")
}

func loadStaticDials(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var addrs []string
	if json.Unmarshal(data, &addrs) != nil {
		return nil
	}
	return addrs
}

// peerIDInAddr returns the /p2p/<id> component of a multiaddr string, or "".
func peerIDInAddr(addr string) string {
	const marker = "/p2p/"
	i := strings.LastIndex(addr, marker)
	if i < 0 {
		return ""
	}
	return addr[i+len(marker):]
}

// PersistStaticDial records a multiaddr (with /p2p/ID) as a known direct
// address to re-dial. Keeps at most one address per peer ID (the latest) so a
// peer that returns on a different port leaves no stale entry. Best-effort;
// safe on a nil host. A persistence failure never fails the live connection.
func (h *Host) PersistStaticDial(addr string) error {
	if h == nil || addr == "" {
		return nil
	}
	// Serialize the read-modify-write: concurrent POST /api/p2p/connect
	// requests would otherwise each load, mutate, and overwrite the file,
	// losing entries from the racing writer.
	h.staticDialMu.Lock()
	defer h.staticDialMu.Unlock()
	path := h.staticDialFile()
	newID := peerIDInAddr(addr)
	var kept []string
	for _, a := range loadStaticDials(path) {
		if a == addr {
			continue
		}
		if newID != "" && peerIDInAddr(a) == newID {
			continue // drop a stale address for the same peer
		}
		kept = append(kept, a)
	}
	kept = append(kept, addr)
	sort.Strings(kept)
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// PruneStaticDial removes every persisted static address for peerID. Called
// when a peer is revoked so the daemon stops re-dialing it every 60s forever
// (its address was otherwise never removed). Best-effort; safe on a nil host.
func (h *Host) PruneStaticDial(peerID string) error {
	if h == nil || peerID == "" {
		return nil
	}
	h.staticDialMu.Lock()
	defer h.staticDialMu.Unlock()
	path := h.staticDialFile()
	existing := loadStaticDials(path)
	var kept []string
	for _, a := range existing {
		if peerIDInAddr(a) == peerID {
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == len(existing) {
		return nil // nothing to prune
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// RedialStatic dials every persisted static address whose peer is not already
// connected and returns the number of new connections established. Skipping
// connected peers keeps steady state quiet, so it is safe to call on a timer
// to recover the link after either side restarts.
func (h *Host) RedialStatic(ctx context.Context) int {
	if h == nil {
		return 0
	}
	n := 0
	for _, addr := range loadStaticDials(h.staticDialFile()) {
		if id := peerIDInAddr(addr); id != "" {
			if pid, err := peer.Decode(id); err == nil && h.IsConnected(pid) {
				continue
			}
		}
		if _, err := h.Connect(ctx, addr); err != nil {
			h.logger.Debug("p2p static redial failed", "addr", addr, "err", err)
			continue
		}
		n++
	}
	return n
}
