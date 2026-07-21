package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// sha256Hex returns the hex-encoded SHA-256 of b. Used for the SHA256
// field of SkillOffer so the receiver can detect transit corruption
// independently of the signature check.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildSkillShareService wires the M2.7 mesh skill share service. Returns
// nil when p2pHost is nil (feature off) — gateway tooling translates a nil
// service into "p2p not enabled" replies.
//
// resolver is the consent.Resolver injected so every audit row carries
// the tier + accepted_by envelope demanded by epic
// 01KSK91Q4W8TNED9MAF0CTRVKC. nil is tolerated (treated as NopResolver
// → cross_org default) so callers can wire without branching.
func buildSkillShareService(
	host *p2p.Host, db *sqlite.DB, auditor *audit.Logger, skillsDir string,
	resolver consent.Resolver, selfUser *store.User,
) *p2p.SkillShareService {
	if host == nil {
		return nil
	}
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	lookup := &storePairedLookup{db: db}
	provider := &storeSkillProvider{db: db, skillsDir: skillsDir}
	receiver := &storeSkillReceiver{db: db, skillsDir: skillsDir}
	auditAdapter := &skillShareAuditAdapter{
		auditor:  auditor,
		resolver: resolver,
		selfUser: selfUser,
	}
	return p2p.NewSkillShareService(host, lookup, provider, receiver, auditAdapter, slog.Default())
}

// storePairedLookup translates store.P2PPeerStore into the
// p2p.PairedPeerLookup interface the skill share service expects.
type storePairedLookup struct{ db store.P2PPeerStore }

// GetPairedPeer returns the paired-peer record (without revealing the full
// store.P2PPeer model to the p2p package).
func (l *storePairedLookup) GetPairedPeer(
	ctx context.Context, peerID string,
) (p2p.PairedPeer, error) {
	row, err := l.db.GetPeer(ctx, peerID)
	if err != nil {
		return p2p.PairedPeer{}, err
	}
	return p2p.PairedPeer{
		PeerID:  row.PeerID,
		Scopes:  row.Scopes,
		Revoked: row.RevokedAt != nil,
	}, nil
}

// storeSkillProvider serves installed-skill bundles + metadata.
type storeSkillProvider struct {
	db        store.InstalledSkillStore
	skillsDir string
}

// GetInstalledOffer constructs a SkillOffer from the manifest of an
// installed skill. Errors with p2p.ErrSkillNotInstalled when the row is
// absent.
func (p *storeSkillProvider) GetInstalledOffer(
	ctx context.Context, name string,
) (*p2p.SkillOffer, error) {
	row, err := p.db.GetInstalledSkill(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", p2p.ErrSkillNotInstalled, name)
		}
		return nil, err
	}
	bundle, _, err := skills.ReadBundleCache(p.skillsDir, name)
	if err != nil {
		if errors.Is(err, skills.ErrBundleCacheMissing) {
			return nil, err
		}
		return nil, err
	}
	return &p2p.SkillOffer{
		Name:         row.Name,
		Version:      row.Version,
		SignerPubkey: row.SignerPubkey,
		ManifestJSON: row.ManifestJSON,
		SHA256:       sha256Hex(bundle),
		SizeBytes:    int64(len(bundle)),
	}, nil
}

// GetSkillBundle returns the original .mcskill bytes + .minisig bytes for
// the requested skill. Version is matched only when non-empty (the offering
// peer always has exactly one version installed per name).
func (p *storeSkillProvider) GetSkillBundle(
	ctx context.Context, name, version string,
) ([]byte, []byte, error) {
	row, err := p.db.GetInstalledSkill(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("%w: %s", p2p.ErrSkillNotInstalled, name)
		}
		return nil, nil, err
	}
	if version != "" && row.Version != version {
		return nil, nil, fmt.Errorf("%w: %s@%s (installed: %s)",
			p2p.ErrSkillNotInstalled, name, version, row.Version)
	}
	bundle, sig, err := skills.ReadBundleCache(p.skillsDir, name)
	if err != nil {
		return nil, nil, err
	}
	return bundle, sig, nil
}

// storeSkillReceiver runs the standard install pipeline on bytes received
// from a paired peer. Identical security guarantees as a registry install.
type storeSkillReceiver struct {
	db        store.Store
	skillsDir string
}

// HandleIncomingBundle calls skills.InstallFromBytes with the standard
// options. AllowUnsigned is intentionally false — peers must always sign.
// Errors propagate upward so the agent gets a clear failure reason.
func (r *storeSkillReceiver) HandleIncomingBundle(
	ctx context.Context, peerID string, bundle, sig []byte,
) error {
	source := "p2p:" + peerID
	_, _, err := skills.InstallFromBytes(ctx, r.db, bundle, sig, skills.InstallOptions{
		SkillsDir: r.skillsDir,
		Source:    source,
		Force:     false,
	})
	return err
}

// skillShareAuditAdapter writes audit rows for each skill-share event.
// The peerID + skill name go into ParamsRedacted so the existing audit UI
// can surface them. Tier + consent envelope are populated from the
// resolver at record-time per epic 01KSK91Q4W8TNED9MAF0CTRVKC.
type skillShareAuditAdapter struct {
	auditor  *audit.Logger
	resolver consent.Resolver
	selfUser *store.User
}

// RecordSkillShare records a single offer/request/install event. Best
// effort: errors are logged, never returned, since audit failures must
// never block the share path.
//
// Tier is derived from the resolver; the AcceptedBy envelope is
// auto_pair on Tier 1 and human on Tier 2/3 (the local user_id is the
// human who accepted by virtue of operating the daemon — when the
// approval-queue ships per-grant agent ids, those replace selfUser).
// On rejection rows (status="denied") DenialReason is populated from
// the action string + errMsg.
func (a *skillShareAuditAdapter) RecordSkillShare(
	ctx context.Context, action, peerID, skill, status, errMsg string,
) {
	if a.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]string{
		"action":  action,
		"peer_id": peerID,
		"skill":   skill,
		"error":   errMsg,
	})
	env := a.buildEnvelope(ctx, peerID, status, errMsg)
	rec := &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      time.Now().UTC(),
		ToolName:       "mesh__skill_share",
		Subpath:        "/" + strings.ReplaceAll(action, "_", "-"),
		Status:         status,
		ParamsRedacted: params,
		ErrorMessage:   errMsg,
		CreatedAt:      time.Now().UTC(),
		ActorKind:      "mesh",
		ActorID:        peerID,
		Tier:           string(env.Tier),
		AcceptedBy:     env.MarshalAcceptedBy(),
		GrantOrigin:    env.MarshalGrantOrigin(),
		DenialReason:   env.DenialReason,
	}
	if err := a.auditor.Record(ctx, rec); err != nil {
		slog.Warn("audit skill share", "error", err)
	}
}

// buildEnvelope is shared with memory/task adapters via the
// shareEnvelope helper to keep tier+consent semantics consistent. The
// scope used for grant-origin lookup mirrors the on-stream check
// (mesh.skill_request); when ScopeAware future-revs differentiate
// "skill_request" from "skill_offer", populate that here.
func (a *skillShareAuditAdapter) buildEnvelope(
	ctx context.Context, peerID, status, errMsg string,
) consent.Envelope {
	return shareEnvelope(ctx, a.resolver, a.selfUser, peerID,
		"mesh.skill_request", status, errMsg)
}
