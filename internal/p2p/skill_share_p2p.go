//go:build p2p

package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// SkillShareProtocol is the libp2p protocol ID for the M2.7 skill share mesh.
// JSON over a single libp2p stream — simpler than protobuf and good enough
// for skill bundles which are bounded by the 100 MB cap from M2.2.
const SkillShareProtocol protocol.ID = "/mcplexer/skill/1.0.0"

// MaxSkillBundleBytes mirrors the install-time cap (skills.MaxBundleSize). We
// duplicate the constant here to avoid an import cycle and to defend the
// stream pipeline even if a caller forgets to enforce it upstream.
const MaxSkillBundleBytes int64 = 100 * 1024 * 1024

// skillShareReadDeadline caps how long any single read on a skill stream can
// block. The full transfer of a 100 MB bundle on a slow link can take longer,
// so we re-arm the deadline per chunk inside readBundle.
const skillShareReadDeadline = 30 * time.Second

// skillShareScopeName is the boolean scope on a paired peer that must be
// truthy for that peer's offers/requests to be accepted on the responder.
// This is the scope for the installed .mcskill bundle protocol, distinct
// from mesh.registry_request used by RegistryShareService.
const skillShareScopeName = "mesh.skill_request"

// SkillShareErrors surfaced over the wire are mapped to user-friendly text by
// the gateway handlers; the codes themselves are stable for tests.
var (
	// ErrPeerNotPaired indicates the remote peer is not in the local paired
	// peers list (or has been revoked). Used both server-side (refuse stream)
	// and client-side (don't bother dialing).
	ErrPeerNotPaired = errors.New("p2p: peer not paired")

	// ErrSkillNotInstalled is returned by RequestSkill when the offering peer
	// no longer has the requested skill installed.
	ErrSkillNotInstalled = errors.New("p2p: skill not installed on peer")

	// ErrSkillBundleTooLarge wraps a size cap violation seen on the wire.
	ErrSkillBundleTooLarge = errors.New("p2p: skill bundle exceeds size cap")

	// ErrSkillShareDenied is the generic "we won't fulfil this request" code
	// — the wire never reveals whether the cause is missing pairing or a
	// scope being false, so a peer can't probe our state.
	ErrSkillShareDenied = errors.New("p2p: skill share denied")
)

// SkillOffer is the JSON payload sent from offering peer to receiver
// announcing a skill is available. The receiver inspects this before deciding
// whether to call RequestSkill back.
type SkillOffer struct {
	Type         string `json:"type"`          // always "offer"
	Name         string `json:"name"`          // skill name (machine id)
	Version      string `json:"version"`       // semver
	SignerPubkey string `json:"signer_pubkey"` // 56-char canonical, "" if unsigned
	ManifestJSON []byte `json:"manifest_json"` // raw manifest bytes
	SHA256       string `json:"sha256"`        // hex sha256 of bundle bytes
	SizeBytes    int64  `json:"size_bytes"`    // bundle byte length
}

// SkillRequest is the JSON payload sent by a receiving peer to ask for a
// specific bundle. The reply is the framed bundle (4-byte big-endian length
// + signature + 4-byte big-endian length + bundle bytes).
type SkillRequest struct {
	Type    string `json:"type"` // always "request"
	Name    string `json:"name"`
	Version string `json:"version"`
}

// skillShareError is used as the wire response for a denied request. The
// stream is closed immediately after.
type skillShareError struct {
	Type    string `json:"type"`    // always "error"
	Code    string `json:"code"`    // stable code (e.g. "not_paired")
	Message string `json:"message"` // human-readable
}

// PairedPeerLookup returns the paired-peer record for a given libp2p peer ID.
// Returns ErrPeerNotPaired when the peer is unknown or revoked.
type PairedPeerLookup interface {
	GetPairedPeer(ctx context.Context, peerID string) (PairedPeer, error)
}

// PairedPeer is the slimmed-down view the skill share service needs from
// store.P2PPeer. Defining it here keeps the p2p package free of a store
// import (and a build cycle).
type PairedPeer struct {
	PeerID  string
	Scopes  []string
	Revoked bool
}

// SkillProvider is the offering-side hook: given a name+version, return the
// raw bundle bytes and the .minisig signature bytes. Implementations must
// enforce a size cap (MaxSkillBundleBytes) and return ErrSkillNotInstalled
// when the skill isn't present.
type SkillProvider interface {
	GetSkillBundle(ctx context.Context, name, version string) ([]byte, []byte, error)
	GetInstalledOffer(ctx context.Context, name string) (*SkillOffer, error)
}

// SkillReceiver is the receiving-side hook: invoked when an offer arrives
// and the agent calls mesh__request_skill. The implementation runs the
// standard install pipeline (signature verify + capability review +
// 100 MB cap) — same as the registry install path.
type SkillReceiver interface {
	HandleIncomingBundle(
		ctx context.Context, peerID string, bundle, sig []byte,
	) error
}

// SkillShareAuditor is invoked once per offer/request transition so the
// audit trail captures every paired-peer skill exchange.
type SkillShareAuditor interface {
	RecordSkillShare(
		ctx context.Context, action, peerID, skill, status, errMsg string,
	)
}

// SkillShareService glues the libp2p stream handler to the gateway tooling.
// One instance per Host.
type SkillShareService struct {
	host     *Host
	lookup   PairedPeerLookup
	provider SkillProvider
	receiver SkillReceiver
	auditor  SkillShareAuditor
	logger   *slog.Logger

	mu     sync.Mutex
	offers map[string]SkillOffer // peerID|name -> last offer received
}

// NewSkillShareService wires the libp2p stream handler onto host and returns
// a service ready for OfferSkill / RequestSkill calls. lookup, provider, and
// receiver must be non-nil; auditor + logger may be nil.
func NewSkillShareService(
	host *Host, lookup PairedPeerLookup, provider SkillProvider,
	receiver SkillReceiver, auditor SkillShareAuditor, logger *slog.Logger,
) *SkillShareService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SkillShareService{
		host:     host,
		lookup:   lookup,
		provider: provider,
		receiver: receiver,
		auditor:  auditor,
		logger:   logger,
		offers:   make(map[string]SkillOffer),
	}
	host.Inner().SetStreamHandler(SkillShareProtocol, s.handleStream)
	return s
}

// OfferSkill dials peerID, opens a skill-share stream, and sends a single
// SkillOffer JSON line. Returns nil on successful send; the receiver decides
// asynchronously whether to call back with a SkillRequest.
func (s *SkillShareService) OfferSkill(
	ctx context.Context, peerID, skillName string,
) error {
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("decode peer id: %w", err)
	}
	offer, err := s.provider.GetInstalledOffer(ctx, skillName)
	if err != nil {
		return fmt.Errorf("get installed skill: %w", err)
	}
	offer.Type = "offer"

	stream, err := s.host.Inner().NewStream(ctx, pid, SkillShareProtocol)
	if err != nil {
		return fmt.Errorf("open skill stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetWriteDeadline(time.Now().Add(skillShareReadDeadline))
	if err := writeJSONLine(stream, offer); err != nil {
		s.recordAudit(ctx, "offer", peerID, skillName, "error", err.Error())
		return err
	}
	s.recordAudit(ctx, "offer", peerID, skillName, "ok", "")
	return nil
}

// RequestSkill dials peerID and asks for the named skill bundle. On success
// the bundle bytes are passed to the configured SkillReceiver, which runs
// the standard install path (signature verify + capability review + cap).
// Returns the manifest bytes from the offer for the caller to surface.
func (s *SkillShareService) RequestSkill(
	ctx context.Context, peerID, skillName, version string,
) (*SkillOffer, error) {
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return nil, err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("decode peer id: %w", err)
	}

	stream, err := s.host.Inner().NewStream(ctx, pid, SkillShareProtocol)
	if err != nil {
		return nil, fmt.Errorf("open skill stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(skillShareReadDeadline))
	req := SkillRequest{Type: "request", Name: skillName, Version: version}
	if err := writeJSONLine(stream, req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	offer, bundle, sig, err := readBundleResponse(stream)
	if err != nil {
		s.recordAudit(ctx, "request", peerID, skillName, "error", err.Error())
		return nil, err
	}
	if err := s.receiver.HandleIncomingBundle(ctx, peerID, bundle, sig); err != nil {
		s.recordAudit(ctx, "install", peerID, skillName, "error", err.Error())
		return offer, fmt.Errorf("install: %w", err)
	}
	s.recordAudit(ctx, "install", peerID, skillName, "ok", "")
	return offer, nil
}

// LastOfferFor returns the most recent offer received from peerID for
// skillName, or (zero, false) if none. Used by the UI to populate the review
// modal without having to re-fetch over the wire.
func (s *SkillShareService) LastOfferFor(peerID, skillName string) (SkillOffer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers[peerID+"|"+skillName]
	return o, ok
}

// assertPeerPaired returns nil if peerID is in the paired list and active;
// otherwise ErrPeerNotPaired. Wrapped so we can test the rejection path.
func (s *SkillShareService) assertPeerPaired(ctx context.Context, peerID string) error {
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	return nil
}

// recordAudit is a nil-safe wrapper around the auditor hook.
func (s *SkillShareService) recordAudit(
	ctx context.Context, action, peerID, skill, status, errMsg string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordSkillShare(ctx, action, peerID, skill, status, errMsg)
}
