//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// PairingProtocol is the libp2p protocol ID used for the code-based handshake.
const PairingProtocol protocol.ID = "/mcplexer/pair/1.0.0"

// PairingTTL is how long a generated 6-digit code remains valid.
const PairingTTL = 5 * time.Minute

// pairingHandshakeTimeout caps a single handshake attempt over the libp2p
// stream. Generous so users have time to type and we still avoid hung peers.
const pairingHandshakeTimeout = 30 * time.Second

// identityFrameTimeout caps the responder's wait for the optional M7.1
// identity line (handshake line 2). A modern initiator writes it immediately
// after the code line, so this is generous; a legacy initiator never writes
// it, so we must NOT spend the full handshake budget here — see handleStream.
const identityFrameTimeout = 2 * time.Second

// PairingStore is the storage interface PairingService uses for the
// pending-pair codes. Production wires this to the sqlite-backed store; tests
// can plug in an in-memory fake. See store.P2PPeerStore for the broader peer
// store this is part of.
type PairingStore interface {
	CreatePendingPair(ctx context.Context, code, peerID string, addrs []string, expiresAt time.Time) error
	GetPendingPair(ctx context.Context, code string) (peerID string, addrs []string, expiresAt time.Time, err error)
	DeletePendingPair(ctx context.Context, code string) error
}

// PairingResult is what StartPair returns: the user-visible code, the QR
// payload to render, and the expiry timestamp.
type PairingResult struct {
	Code      string    `json:"code"`
	QRPayload string    `json:"qr_payload"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PairingService generates and validates 6-digit pairing codes. It also
// registers the libp2p protocol handler that responds when another peer dials
// us with their code. Construct via NewPairingService and (optionally) attach
// a PeerPersister via SetPeerPersister so the responder side can record the
// paired peer when the handshake succeeds.
type PairingService struct {
	host  *Host
	store PairingStore

	mu          sync.Mutex
	memCode     map[string]pendingPair // mirror of pending pairs for fast lookups
	persister   PeerPersister          // optional; nil means responder skips persist
	userLinker  UserLinker             // optional; M7.1 per-human identity link
	selfUserID  string                 // M7.1 — set via SetSelfIdentity
	selfDisplay string                 // M7.1 — display_name for this human
	logger      *slog.Logger
	rateLim     *pairingRateLimiter
}

type pendingPair struct {
	peerID    string
	addrs     []string
	expiresAt time.Time
}

// NewPairingService wires the protocol handler onto host and returns a
// service. Pass a PairingStore that persists pending codes so a daemon
// restart mid-handshake doesn't strand the user.
func NewPairingService(host *Host, st PairingStore) *PairingService {
	s := &PairingService{
		host:    host,
		store:   st,
		memCode: make(map[string]pendingPair),
		logger:  slog.Default(),
		rateLim: newPairingRateLimiter(),
	}
	host.Inner().SetStreamHandler(PairingProtocol, s.handleStream)
	return s
}

// SetPeerPersister attaches a PeerPersister to the service. When set, the
// responder side of a successful handshake inserts the initiator into the
// peer store. Without this, only the initiator's HTTP handler writes a row
// — leaving the two sides asymmetric.
func (s *PairingService) SetPeerPersister(p PeerPersister) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persister = p
}

// SetLogger overrides the slog.Default() logger used for debug/error lines.
// Useful in tests that want to assert on log output (or silence it).
func (s *PairingService) SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
}

// StartPair generates a fresh 6-digit code, stores it, and returns the QR
// payload (a JSON blob containing the code + this host's peer ID). The code
// is single-use and valid for PairingTTL.
//
// The QR payload deliberately omits multiaddrs — once the DHT is wired (see
// dht_p2p.go) the responder side resolves the peer's current addresses via
// host.FindPeer, which is more reliable than baking-in addrs that may have
// changed across NAT, sleep/wake, or IP-rotations. Smaller payload also
// produces a denser QR that scans more reliably under poor lighting + at
// monitor scaling.
func (s *PairingService) StartPair(ctx context.Context) (*PairingResult, error) {
	code, err := generateSixDigitCode()
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}
	// We still keep the dialable-addrs filter in the persistent pending-pair
	// row (`addrs`) for legacy callers that introspect the store, and so the
	// reconnector has a hint if the DHT is slow. They are NOT serialized into
	// the QR payload.
	addrs := dialableAddrsForPairing(s.host.Addrs())
	expires := time.Now().UTC().Add(PairingTTL)

	if err := s.store.CreatePendingPair(ctx, code, s.host.PeerID(), addrs, expires); err != nil {
		return nil, fmt.Errorf("persist pending pair: %w", err)
	}
	s.mu.Lock()
	s.memCode[code] = pendingPair{peerID: s.host.PeerID(), addrs: addrs, expiresAt: expires}
	uid, dname := s.selfUserID, s.selfDisplay
	s.mu.Unlock()
	body := map[string]any{
		"code":    code,
		"peer_id": s.host.PeerID(),
	}
	// M7.1: include self user identity so the initiator can link the
	// responder's peer to the right human user. Elided when empty so the
	// QR JSON stays clean for legacy + test callers.
	if uid != "" {
		body["user_id"] = uid
	}
	if dname != "" {
		body["display_name"] = dname
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode qr payload: %w", err)
	}
	return &PairingResult{Code: code, QRPayload: string(payload), ExpiresAt: expires}, nil
}

// CompletePair runs the client side of pairing: dial the other peer, open a
// pairing stream, send the code, and wait for an OK. On success the code on
// the remote side is consumed and both peers should add each other to their
// trusted-peer store.
//
// remoteAddrs is optional. When empty (the new QR shape — addrs are no
// longer baked into the payload) we fall back to host.FindPeer, which walks
// the DHT for the peer's current AddrInfo. If the DHT lookup fails too we
// surface a clear error so the UI can prompt the user to retry.
func (s *PairingService) CompletePair(
	ctx context.Context, code, remotePeerID string, remoteAddrs []string,
) error {
	pid, err := peer.Decode(remotePeerID)
	if err != nil {
		return fmt.Errorf("decode peer id: %w", err)
	}
	if err := s.seedPeerAddrs(ctx, pid, remoteAddrs); err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, pairingHandshakeTimeout)
	defer cancel()

	stream, err := s.host.Inner().NewStream(dialCtx, pid, PairingProtocol)
	if err != nil {
		return fmt.Errorf("open pair stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	if _, err := fmt.Fprintln(stream, strings.TrimSpace(code)); err != nil {
		return fmt.Errorf("send code: %w", err)
	}
	// M7.1: send our user identity as a single JSON line. Older binaries
	// only read the code line so trailing data is ignored — backward
	// compatible. Empty values mean "not yet bootstrapped"; we still send
	// the JSON so framing is predictable.
	s.mu.Lock()
	uid, dname := s.selfUserID, s.selfDisplay
	s.mu.Unlock()
	frame, _ := json.Marshal(map[string]string{
		"user_id":      uid,
		"display_name": dname,
	})
	if _, err := fmt.Fprintln(stream, string(frame)); err != nil {
		return fmt.Errorf("send identity: %w", err)
	}

	reply, err := bufio.NewReader(stream).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}
	if strings.TrimSpace(reply) != "ok" {
		return ErrPairingInvalid
	}
	return nil
}

// handleStream is the server side: read a code, look it up, reply ok/no.
// On ok we delete the pending row so the code is single-use AND insert the
// initiator's peer ID into our peer store (when a PeerPersister is attached)
// so both ends of a pairing end up symmetric.
//
// Wire protocol:
//
//	line 1: "<six-digit-code>\n"
//	line 2: '{"user_id":"...","display_name":"..."}\n'  (M7.1; optional —
//	        absent on legacy initiators)
//	reply : "ok\n" | "no\n"
func (s *PairingService) handleStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()
	deadline := time.Now().Add(pairingHandshakeTimeout)
	_ = stream.SetDeadline(deadline)

	remote := stream.Conn().RemotePeer().String()
	if s.rateLim != nil && !s.rateLim.allow(remote, time.Now()) {
		s.logger.Warn("pairing attempt rate-limited",
			"remote_peer", remote)
		_, _ = fmt.Fprintln(stream, "no")
		return
	}

	reader := bufio.NewReader(stream)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	code := strings.TrimSpace(line)
	if !s.consumeCode(code) {
		_, _ = fmt.Fprintln(stream, "no")
		return
	}
	// Read the optional M7.1 identity line under a SHORT independent
	// deadline, NOT the full 30s handshake budget. A genuine legacy
	// initiator sends ONLY the code line and then blocks reading our reply
	// (it never writes line 2 nor closes its write side), so a blind
	// ReadString('\n') here would stall until the 30s stream deadline and
	// pin a goroutine — and a handful of half-open legacy dials could
	// exhaust the global rate-limit window. A timeout/EOF is treated as
	// "legacy peer, no identity" (zero frame). Restore the handshake
	// deadline afterwards so the "ok" write below isn't truncated.
	_ = stream.SetReadDeadline(time.Now().Add(identityFrameTimeout))
	frame := readIdentityFrame(reader)
	_ = stream.SetReadDeadline(deadline)
	s.persistRemotePeer(stream.Conn().RemotePeer())
	s.linkRemoteUser(stream.Conn().RemotePeer().String(), frame)
	_, _ = fmt.Fprintln(stream, "ok")
}

// consumeCode validates a code (in-memory + persistent) and removes it on
// success. Returns true if the code was valid and consumed.
func (s *PairingService) consumeCode(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.memCode[code]
	if !ok || time.Now().After(rec.expiresAt) {
		delete(s.memCode, code)
		_ = s.store.DeletePendingPair(context.Background(), code)
		return false
	}
	delete(s.memCode, code)
	_ = s.store.DeletePendingPair(context.Background(), code)
	return true
}

// LoadPending repopulates the in-memory cache from the persistent store on
// daemon startup. Expired rows are pruned.
func (s *PairingService) LoadPending(ctx context.Context, codes map[string]pendingPair) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for code, p := range codes {
		if now.After(p.expiresAt) {
			_ = s.store.DeletePendingPair(ctx, code)
			continue
		}
		s.memCode[code] = p
	}
}

// generateSixDigitCode returns a uniformly random 6-digit code (000000-999999)
// as a zero-padded string. Uses crypto/rand — never math/rand.
func generateSixDigitCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
