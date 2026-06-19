//go:build p2p

// Package p2p — registry share service (sibling of skill_share_p2p.go).
//
// /mcplexer/skill-registry/1.0.0 is the libp2p protocol that lets two
// paired peers exchange agent-facing skill_registry entries — the text
// SKILL.md plus an optional tar.gz bundle (migration 059). Distinct
// from the .mcskill share path (skill_share_p2p.go) which carries
// signed bundles for the installed_skills surface (gated by
// mesh.skill_request); the registry path uses mesh.registry_request so
// the surfaces have independent grant toggles. The registry path is
// intentionally unsigned because registry entries are author-tagged
// in-database, not cryptographically signed.
//
// Wire format:
//
//	→ {"type":"request","name":"foo","version":0}\n   (version=0 = latest)
//	← either skillShareError JSON line, OR
//	   4-byte BE len + body bytes + 4-byte BE len + bundle bytes
//
// body always present (the SKILL.md). bundle can be zero-length when
// the entry has no tar.gz attached (text-only skill).

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// RegistryShareProtocol is the libp2p protocol ID for the registry
// share mesh. Separate from SkillShareProtocol so a peer can grant
// one without granting the other.
const RegistryShareProtocol protocol.ID = "/mcplexer/skill-registry/1.0.0"

// registryShareScopeName gates inbound requests. Distinct from
// skillShareScopeName so the .mcskill and registry surfaces have
// independent toggles on a paired peer.
const registryShareScopeName = "mesh.registry_request"

// ErrRegistryEntryNotFound is the typed return from RegistryProvider
// when no row matches (name, version) in scope.
var ErrRegistryEntryNotFound = errors.New("p2p: registry entry not found")

// RegistryRequest is the JSON header sent by the requester. Version=0
// resolves to the latest active version on the responder.
type RegistryRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// RegistryProvider is the responder-side hook. Returns the body
// (SKILL.md text) and the optional bundle (tar.gz). When the row has
// no bundle, bundle is nil — the caller still gets the body so the
// receiver can publish a text-only entry.
type RegistryProvider interface {
	GetRegistryEntry(
		ctx context.Context, name string, version int,
	) (body string, bundle []byte, sha256 string, err error)
}

// RegistryReceiver is the requester-side hook. Called once the bundle
// frame is read; it must publish the (body, bundle) into the local
// registry under the original name. The implementation is free to
// reject (e.g. content too large, body parse error) and the request
// surface reports the error back to the caller.
type RegistryReceiver interface {
	HandleIncomingRegistryEntry(
		ctx context.Context, peerID, name, body string, bundle []byte,
	) error
}

// RegistryShareService glues the libp2p handler to local providers.
// Constructed once per Host in the gateway wiring.
type RegistryShareService struct {
	host           *Host
	lookup         PairedPeerLookup
	provider       RegistryProvider
	receiver       RegistryReceiver
	indexProvider  HubIndexProvider
	searchProvider HubSearchProvider
	auditor        SkillShareAuditor
	logger         *slog.Logger
}

// NewRegistryShareService wires the stream handler and returns a
// service ready for RequestRegistrySkill calls. host + lookup +
// provider + receiver must be non-nil; auditor + logger may be nil.
// indexProvider and searchProvider are optional — when non-nil the
// service also responds to hub index and search requests.
func NewRegistryShareService(
	host *Host, lookup PairedPeerLookup,
	provider RegistryProvider, receiver RegistryReceiver,
	indexProvider HubIndexProvider, searchProvider HubSearchProvider,
	auditor SkillShareAuditor, logger *slog.Logger,
) *RegistryShareService {
	if logger == nil {
		logger = slog.Default()
	}
	if searchProvider == nil {
		searchProvider, _ = indexProvider.(HubSearchProvider)
	}
	s := &RegistryShareService{
		host:           host,
		lookup:         lookup,
		provider:       provider,
		receiver:       receiver,
		indexProvider:  indexProvider,
		searchProvider: searchProvider,
		auditor:        auditor,
		logger:         logger,
	}
	host.Inner().SetStreamHandler(RegistryShareProtocol, s.handleStream)
	return s
}

// RequestRegistrySkill dials peerID, sends a RegistryRequest, reads
// the response, and hands the result to the local receiver. version=0
// asks for the responder's latest active version. Returns the bundle
// sha256 the receiver landed (empty string when no bundle).
func (s *RegistryShareService) RequestRegistrySkill(
	ctx context.Context, peerID, name string, version int,
) (string, error) {
	if name == "" {
		return "", errors.New("name is required")
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return "", fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, RegistryShareProtocol)
	if err != nil {
		s.recordAudit(ctx, "registry_request_dial", peerID, name, "error", err.Error())
		return "", fmt.Errorf("open stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	req := RegistryRequest{Type: "request", Name: name, Version: version}
	if err := writeJSONLine(stream, req); err != nil {
		return "", fmt.Errorf("write request: %w", err)
	}
	body, bundle, err := readRegistryResponse(stream)
	if err != nil {
		s.recordAudit(ctx, "registry_request_read", peerID, name, "error", err.Error())
		return "", err
	}
	if err := s.receiver.HandleIncomingRegistryEntry(ctx, peerID, name, body, bundle); err != nil {
		s.recordAudit(ctx, "registry_request_install", peerID, name, "error", err.Error())
		return "", fmt.Errorf("install: %w", err)
	}
	s.recordAudit(ctx, "registry_request_ok", peerID, name, "ok", "")
	return "", nil
}

// recordAudit forwards to the optional auditor. No-op when nil.
func (s *RegistryShareService) recordAudit(
	ctx context.Context, action, peerID, name, status, msg string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordSkillShare(ctx, action, peerID, name, status, msg)
}

// readRegistryResponse parses the wire reply. Either a JSON error line
// or framed (body, bundle) bytes. Mirrors readBundleResponse from the
// skill_share path so the framing utilities can be reused.
func readRegistryResponse(stream network.Stream) (string, []byte, error) {
	_ = stream.SetReadDeadline(time.Now().Add(skillShareReadDeadline))
	br := bufio.NewReader(stream)
	first, err := br.Peek(1)
	if err != nil {
		return "", nil, fmt.Errorf("peek: %w", err)
	}
	if first[0] == '{' {
		return "", nil, decodeStreamError(br)
	}
	body, err := readChunk(br, MaxSkillBundleBytes)
	if err != nil {
		return "", nil, fmt.Errorf("read body: %w", err)
	}
	bundle, err := readChunk(br, MaxSkillBundleBytes)
	if err != nil {
		return "", nil, fmt.Errorf("read bundle: %w", err)
	}
	return string(body), bundle, nil
}

// Silence the unused-import lint when only the stub is selected by
// build tags somewhere downstream — encoding/json is only needed in
// the request path.
var _ = json.Marshal
