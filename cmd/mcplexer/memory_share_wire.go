// memory_share_wire.go — glue between the cross-peer /mcplexer/memory/
// 1.0.0 libp2p protocol and the local memory store. Adapters live here
// rather than in internal/p2p to avoid pulling a store dependency into
// the p2p package (which would create a build cycle).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// buildMemoryShareService wires the libp2p stream handler onto host.
// Returns a non-nil service in both build modes — the slim-build stub
// short-circuits all operations to ErrP2PNotBuiltIn, so callers can use
// the service without branching on the build tag.
//
// resolver is the consent.Resolver injected so every audit row carries
// the tier + accepted_by envelope demanded by epic
// 01KSK91Q4W8TNED9MAF0CTRVKC.
func buildMemoryShareService(
	host *p2p.Host,
	s store.Store,
	mem *memory.Service,
	auditor *audit.Logger,
	resolver consent.Resolver,
	selfUser *store.User,
	settingsSvc *config.SettingsService,
) *p2p.MemoryShareService {
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	// Reuse storePairedLookup defined in skill_share.go — same shape.
	lookup := &storePairedLookup{db: s}
	provider := &memoryShareProvider{store: s}
	receiver := &memoryShareReceiver{mem: mem, store: s}
	// recorder keeps `mem` so RecordOffer can fire the SSE NotifyOfferReceived
	// hook (commit 98d21ff). auditor gains resolver+selfUser to stamp
	// consent envelope on the audit row (this commit). Both compose.
	recorder := &memoryShareRecorder{store: s, mem: mem}
	aud := &memoryShareAuditor{
		auditor:  auditor,
		resolver: resolver,
		selfUser: selfUser,
	}
	svc := p2p.NewMemoryShareService(
		host, lookup, provider, receiver, recorder, aud, slog.Default(),
	)
	// Tier-1 silent receive-side replication: when a SameUser peer offers a
	// memory and this host hasn't opted out (mesh.auto_replicate_off), pull
	// it automatically so the user's own machines stay in sync without a
	// manual mesh__request_memory. SetAutoPuller is a no-op in stub mode.
	svc.SetAutoPuller(&memoryAutoPuller{
		store:    s,
		resolver: resolver,
		settings: settingsSvc,
	})
	return svc
}

// memoryAutoPuller decides whether a freshly-received offer should be
// silently pulled. It admits ONLY the Tier-1 (SameUser) tier and only
// when the receiving host hasn't opted out via the
// mesh.auto_replicate_off setting (config: MeshAutoReplicateOff, default
// false → auto-pull ON). It also short-circuits an offer whose memory is
// already imported (accepted_as_id set) so a re-offer never re-pulls.
type memoryAutoPuller struct {
	store    store.Store
	resolver consent.Resolver
	settings *config.SettingsService
}

// ShouldAutoPull gates the silent pull. Order matters: tier check first
// (cheapest, no DB), then opt-out (one settings read), then the
// already-present guard (one offer-list read). Any miss → false → the
// offer stays OFFER-only, the prior behaviour.
func (p *memoryAutoPuller) ShouldAutoPull(
	ctx context.Context, peerID string, offer *p2p.MemoryOffer,
) bool {
	if p == nil || p.resolver == nil || offer == nil {
		return false
	}
	// Tier-1 only. Same-org / cross-org peers are never auto-pulled —
	// they stay manual regardless of the opt-out flag.
	if p.resolver.TierFor(ctx, peerID) != consent.TierSameUser {
		return false
	}
	// Opt-out: when the host disabled silent replication, fall back to
	// OFFER-only. Defaults to auto-pull ON (MeshAutoReplicateOff=false).
	if p.settings != nil && p.settings.Load(ctx).MeshAutoReplicateOff {
		return false
	}
	// Already-present guard: if a prior pull already imported this exact
	// (peer, remote) offer, don't re-pull. Best-effort — a list error
	// errs toward pulling (the receiver's Write is itself idempotent on
	// the offer's identity, so a duplicate is harmless, not corrupting).
	if p.store != nil && p.alreadyImported(ctx, peerID, offer.RemoteID) {
		return false
	}
	return true
}

// alreadyImported reports whether an offer from (peerID, remoteID) has
// already been accepted+imported locally (accepted_as_id stamped). Walks
// the peer's offers including done ones.
func (p *memoryAutoPuller) alreadyImported(ctx context.Context, peerID, remoteID string) bool {
	offers, err := p.store.ListMemoryOffers(ctx, store.MemoryOfferFilter{
		PeerID:      peerID,
		IncludeDone: true,
	})
	if err != nil {
		return false
	}
	for i := range offers {
		if offers[i].RemoteID == remoteID && offers[i].AcceptedAsID != "" {
			return true
		}
	}
	return false
}

// OnAutoPulled stamps the offer row accepted so the dashboard reflects
// the silent import AND the already-present guard recognises a re-offer.
// Best-effort: a failure here only means a future re-offer might trigger
// a redundant (idempotent) pull. We look the row up by (peer, remote)
// because the auto-pull path doesn't carry the offer row id.
func (p *memoryAutoPuller) OnAutoPulled(
	ctx context.Context, peerID, remoteID, localID string,
) {
	if p == nil || p.store == nil || localID == "" {
		return
	}
	offers, err := p.store.ListMemoryOffers(ctx, store.MemoryOfferFilter{
		PeerID:      peerID,
		IncludeDone: true,
	})
	if err != nil {
		return
	}
	for i := range offers {
		if offers[i].RemoteID == remoteID {
			_ = p.store.AcceptMemoryOffer(ctx, offers[i].ID, localID)
			return
		}
	}
}

// memoryShareProvider serves outgoing memory payloads (responding to a
// peer's mesh__request_memory). Looks up by local memory id, with a
// per-peer workspace scope check enforced in SQL.
type memoryShareProvider struct {
	store store.Store
}

// allowedWorkspacesFromScopes extracts the set of workspace_id values
// the peer is granted memory-read access to. The current grant model:
//
//   - "mesh.memory_request"            — boolean coarse grant; admits
//     global memories (workspace_id IS NULL) only.
//   - "mesh.memory_request:<wsid>"     — per-workspace grant; admits
//     memories scoped to that exact workspace_id.
//   - "mesh.memory_request:*"          — wildcard; admits every
//     workspace as well as global. The caller short-circuits to the
//     unscoped GetMemory path when wildcard=true (the SQL helper's
//     IN clause can't represent "every value", and faking it via a
//     synthetic match list would re-introduce subtle leak surface).
//
// Returns (workspaceIDs, allowGlobal, wildcard). The wildcard branch
// short-circuits — when wildcard=true the workspaceIDs slice is nil and
// the caller falls back to GetMemory. When the boolean coarse grant is
// the only one present, returns (nil, true, false) — global memories
// only. When neither the boolean nor any colon-prefix grant is present,
// returns (nil, false, false) so GetMemoryForPeer short-circuits to
// ErrNotFound without a query. This is defensive — the stream-level
// gate should already have rejected the peer in that case.
func allowedWorkspacesFromScopes(peerScopes []string) (ids []string, allowGlobal bool, wildcard bool) {
	const prefix = "mesh.memory_request:"
	for _, s := range peerScopes {
		if s == "mesh.memory_request" {
			allowGlobal = true
			continue
		}
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			suffix := s[len(prefix):]
			if suffix == "*" {
				// Wildcard admits every workspace AND global.
				return nil, true, true
			}
			ids = append(ids, suffix)
		}
	}
	return ids, allowGlobal, false
}

// GetMemoryPayload fetches the memory ONLY when the requesting peer's
// scopes admit its workspace_id, then converts it to the wire shape.
// Returns ErrMemoryNotFound for both genuinely-missing rows AND rows
// the peer isn't scoped for — the two cases collapse to the same
// sentinel so the cross-peer wire response can be byte-identical
// (constant-shape deny envelope, no side-channel leak).
//
// Scope resolution:
//   - bare "mesh.memory_request" → only global (workspace_id IS NULL)
//   - "mesh.memory_request:<ws>" → only that exact workspace
//   - "mesh.memory_request:*"    → every workspace AND global
//
// The scope filter runs IN the SQL WHERE clause via GetMemoryForPeer —
// un-granted rows never load into Go memory. JTAC65 fix.
//
// Entity links are loaded only after the scope check passes; they're
// lower-cased to wire form and filtered through p2p.FilterEntitiesForMesh
// so peer-local kinds (place, event, …) don't cross machine boundaries.
func (p *memoryShareProvider) GetMemoryPayload(
	ctx context.Context, peerID, remoteID string, peerScopes []string,
) (*p2p.MemoryPayload, error) {
	wsIDs, allowGlobal, wildcard := allowedWorkspacesFromScopes(peerScopes)

	// Wildcard: bypass the per-workspace IN clause by falling back to
	// the unscoped GetMemory — the wildcard grant explicitly admits
	// every workspace. We still go through the soft-delete check.
	var (
		e   *store.MemoryEntry
		err error
	)
	if wildcard {
		e, err = p.store.GetMemory(ctx, remoteID)
	} else {
		e, err = p.store.GetMemoryForPeer(ctx, remoteID, wsIDs, allowGlobal)
	}
	if err != nil {
		// Collapse the "not found" + "not scoped" cases into the same
		// sentinel — the cross-peer caller maps ErrMemoryNotFound to
		// the constant-shape deny envelope. Internal errors (SQL
		// failure, etc.) propagate as-is so the local audit row gets
		// the real error string.
		if errors.Is(err, store.ErrNotFound) {
			return nil, p2p.ErrMemoryNotFound
		}
		return nil, err
	}
	links, _ := p.store.ListMemoryEntities(ctx, e.ID)
	wireLinks := make([]p2p.EntityLink, 0, len(links))
	for _, l := range links {
		wireLinks = append(wireLinks, p2p.EntityLink{
			Kind: l.EntityKind, ID: l.EntityID, Role: l.Role,
		})
	}
	wireLinks = p2p.FilterEntitiesForMesh(wireLinks)
	return &p2p.MemoryPayload{
		Type:         "memory",
		RemoteID:     e.ID,
		Name:         e.Name,
		Kind:         e.Kind,
		Content:      e.Content,
		TagsJSON:     json.RawMessage(e.TagsJSON),
		MetadataJSON: json.RawMessage(e.MetadataJSON),
		EmbedModel:   e.EmbedModel,
		EmbedVersion: e.EmbedVersion,
		Entities:     wireLinks,
		// Carry the source workspace so a linked-workspace receiver can land
		// this memory in the bound local workspace (resolved via
		// workspace_peer_bindings) instead of global.
		RemoteWorkspaceID: derefStr(e.WorkspaceID),
	}, nil
}

// derefStr returns the pointed-to string, or "" when nil.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// memoryShareReceiver imports an incoming memory payload into the local
// store with provenance set. Returns the new local memory id so the
// caller can stamp accepted_as_id on the offer row.
type memoryShareReceiver struct {
	mem   *memory.Service
	store store.Store
}

// originRemoteIDMetaKey is the reserved metadata key stamped on a memory
// imported from a peer, holding "<peerID>|<remoteID>". It makes the
// cross-peer import idempotent: a re-pull of the same source memory finds
// the existing row instead of writing a duplicate. Underscore-prefixed so
// it doesn't collide with user-authored metadata.
const originRemoteIDMetaKey = "_origin_remote_id"

// findImportedMemory returns the local id of a memory already imported
// from (peerID, remoteID) — identified by the originRemoteIDMetaKey
// marker — or "" if none. Scoped IncludeAll so it finds the row wherever
// linked-workspace routing landed it (global or a bound workspace).
func (r *memoryShareReceiver) findImportedMemory(ctx context.Context, peerID, originMarker string) string {
	rows, err := r.store.ListMemories(ctx, store.MemoryFilter{
		OriginPeerID: peerID,
		Scope:        store.SkillScope{IncludeAll: true},
	})
	if err != nil {
		return ""
	}
	for i := range rows {
		if len(rows[i].MetadataJSON) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(rows[i].MetadataJSON), &m) != nil {
			continue
		}
		if v, _ := m[originRemoteIDMetaKey].(string); v == originMarker {
			return rows[i].ID
		}
	}
	return ""
}

// HandleIncomingMemory persists the payload. SourceKind=peer is set
// automatically; OriginPeerID is the libp2p peer id of the sender so
// the dashboard can render "received from peer X". Entity links from
// the payload are re-applied locally via WriteOptions.Entities — the
// sender already stripped peer-local kinds, so the receiver writes
// whatever arrives verbatim. The defensive second filter pass guards
// against a peer that didn't update yet (forward-compat).
func (r *memoryShareReceiver) HandleIncomingMemory(
	ctx context.Context, peerID string, payload *p2p.MemoryPayload,
) (string, error) {
	if r == nil || r.mem == nil {
		return "", errors.New("memory share receiver not initialised")
	}
	// Path-agnostic idempotency: a re-pull of the same (peer, remote_id)
	// memory — whether triggered by silent auto-pull or a manual
	// mesh__request_memory — must NOT create a duplicate local row. The
	// offer-row accepted_as_id stamp only covers the auto-pull path (and
	// is best-effort), so we make the IMPORT itself idempotent here: stamp
	// a reserved provenance marker into metadata and short-circuit if a
	// memory carrying that marker already exists from this peer.
	originMarker := peerID + "|" + payload.RemoteID
	if r.store != nil {
		if existingID := r.findImportedMemory(ctx, peerID, originMarker); existingID != "" {
			return existingID, nil
		}
	}
	var meta map[string]any
	if len(payload.MetadataJSON) > 0 {
		_ = json.Unmarshal(payload.MetadataJSON, &meta)
	}
	if meta == nil {
		meta = make(map[string]any)
	}
	meta[originRemoteIDMetaKey] = originMarker
	var tags []string
	if len(payload.TagsJSON) > 0 {
		_ = json.Unmarshal(payload.TagsJSON, &tags)
	}
	filtered := p2p.FilterEntitiesForMesh(payload.Entities)
	entities := make([]store.EntityRef, 0, len(filtered))
	for _, l := range filtered {
		entities = append(entities, store.EntityRef{
			Kind: l.Kind, ID: l.ID, Role: l.Role,
		})
	}
	// Linked-workspace routing: if the sender named a source workspace and
	// we have a binding for (peer, remote workspace), land the memory in
	// the bound LOCAL workspace instead of global. No binding (or no
	// remote workspace) → nil → global, the prior behaviour. Mirrors the
	// task offer-accept path (resolveAcceptWorkspace).
	var localWS *string
	if r.store != nil && payload.RemoteWorkspaceID != "" {
		if b, err := r.store.GetWorkspacePeerBinding(ctx, peerID, payload.RemoteWorkspaceID); err == nil && b != nil && b.LocalWorkspaceID != "" {
			ws := b.LocalWorkspaceID
			localWS = &ws
		}
	}
	return r.mem.Write(ctx, memory.WriteOptions{
		Name:         payload.Name,
		Kind:         payload.Kind,
		Content:      payload.Content,
		Tags:         tags,
		Metadata:     meta,
		WorkspaceID:  localWS,
		SourceKind:   store.MemorySourcePeer,
		SourcePeerID: peerID,
		OriginPeerID: peerID,
		Entities:     entities,
	})
}

// memoryShareRecorder persists incoming offer descriptors into the
// memory_offers table so the dashboard / agent can see + decide. The
// memory.Service handle is optional and only used to fan an
// offer_received Event over the dashboard's notify bus — recording
// continues to work in the nil case (e.g. compile-mode tests).
type memoryShareRecorder struct {
	store store.Store
	mem   *memory.Service
}

// RecordOffer upserts the offer. (peer_id, remote_id) uniqueness in the
// store table makes this idempotent — a re-offer is a no-op. After a
// successful upsert we fire an offer_received event on the memory
// service's notify hook so the dashboard's /memory page lights up the
// instant a peer announces a new memory.
func (r *memoryShareRecorder) RecordOffer(
	ctx context.Context, peerID, peerName string, offer *p2p.MemoryOffer,
) error {
	if r == nil || r.store == nil {
		return nil
	}
	row := &store.MemoryOffer{
		PeerID:       peerID,
		PeerName:     peerName,
		RemoteID:     offer.RemoteID,
		Name:         offer.Name,
		Kind:         offer.Kind,
		Description:  offer.Description,
		Preview:      offer.Preview,
		TagsJSON:     json.RawMessage(offer.TagsJSON),
		MetadataJSON: json.RawMessage(offer.MetadataJSON),
		EmbedModel:   offer.EmbedModel,
	}
	if err := r.store.UpsertMemoryOffer(ctx, row); err != nil {
		return err
	}
	if r.mem != nil {
		r.mem.NotifyOfferReceived(ctx, row.ID, peerID, peerName, row.Name)
	}
	return nil
}

// memoryShareAuditor writes a tool-name=mesh__memory_share audit row for
// every offer/request/install transition so the dashboard audit page can
// surface peer memory activity.
type memoryShareAuditor struct {
	auditor  *audit.Logger
	resolver consent.Resolver
	selfUser *store.User
}

// RecordMemoryShare emits one audit row per transition. Best-effort —
// failures are logged inside auditor.Record. Tier + consent envelope
// land on the row per epic 01KSK91Q4W8TNED9MAF0CTRVKC; DenialReason is
// populated on rejection rows (status="error" or "denied").
func (a *memoryShareAuditor) RecordMemoryShare(
	ctx context.Context, action, peerID, remoteID, status, errMsg string,
) {
	if a == nil || a.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{
		"action":    action,
		"peer_id":   peerID,
		"remote_id": remoteID,
		"status":    status,
		"error":     errMsg,
	})
	env := shareEnvelope(ctx, a.resolver, a.selfUser, peerID,
		"mesh.memory_request", status, errMsg)
	now := time.Now().UTC()
	_ = a.auditor.Record(ctx, &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "mesh__memory_share",
		ParamsRedacted: params,
		Status:         status,
		ErrorMessage:   errMsg,
		ActorKind:      "mesh",
		ActorID:        peerID,
		CreatedAt:      now,
		Tier:           string(env.Tier),
		AcceptedBy:     env.MarshalAcceptedBy(),
		GrantOrigin:    env.MarshalGrantOrigin(),
		DenialReason:   env.DenialReason,
	})
}
