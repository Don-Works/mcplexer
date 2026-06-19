package mesh

import (
	"context"
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// Capability-grant audit emitters. These cover the mutating mesh tools
// that send/receive don't already audit — without these rows, an
// operator who grants the wrong scope on the wrong peer leaves no
// forensics trail. Each helper is nil-safe (nil Manager or nil auditor
// → no-op) and runs the actual store write on a background goroutine
// via auditAsync so mesh activity never blocks on audit I/O.

// Audit event names — one constant per mutating mesh tool so dashboards
// can group by ToolName without typo risk.
const (
	auditEventSkillOffer      = "mesh__offer_skill"
	auditEventRequestSkill    = "mesh__request_skill"
	auditEventGrantPeerScope  = "mesh__grant_peer_scope"
	auditEventRevokePeerScope = "mesh__revoke_peer_scope"
	auditEventSetAgentStatus  = "mesh__set_agent_status"
)

// buildMeshAuditRecord constructs the common record shape used by every
// mesh capability-grant audit row. Callers supply the event-specific
// params map; this helper fills in ID, ClientType, SessionID, Status,
// CreatedAt, etc. ClientType is fixed to "mesh" and SessionID is
// derived from the local libp2p peer (so multi-host forensics can group
// all rows by originating device).
func (m *Manager) buildMeshAuditRecord(
	meta SessionMeta, event string, params map[string]any,
	status, errCode, errMsg string,
) *store.AuditRecord {
	clientType := meta.ClientType
	if clientType == "" {
		clientType = "mesh"
	}
	sessionID := meta.SessionID
	if sessionID == "" && m.selfPeerID != "" {
		sessionID = "mesh:" + m.selfPeerID
	}
	wsID := ""
	if len(meta.WorkspaceIDs) > 0 {
		wsID = meta.WorkspaceIDs[0]
	}
	raw, _ := json.Marshal(params)
	now := time.Now().UTC()
	return &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		SessionID:      sessionID,
		ClientType:     clientType,
		Model:          meta.ModelHint,
		WorkspaceID:    wsID,
		ToolName:       event,
		ParamsRedacted: raw,
		Status:         status,
		ErrorCode:      errCode,
		ErrorMessage:   errMsg,
		CreatedAt:      now,
	}
}

// RecordSkillOffer emits a mesh__offer_skill audit row. Capability-grant
// forensics: a "skill_offer" hands custom code to a peer, so the
// recipient + skill identity must be on the ledger. Nil auditor → no-op.
func (m *Manager) RecordSkillOffer(
	ctx context.Context, meta SessionMeta,
	recipientPeerID, skillName, skillVersion string,
	status, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	params := map[string]any{
		"recipient_peer_id": recipientPeerID,
		"skill_name":        skillName,
	}
	if skillVersion != "" {
		params["skill_version"] = skillVersion
	}
	m.auditAsync(ctx, m.buildMeshAuditRecord(meta, auditEventSkillOffer, params, status, "", errMsg))
}

// RecordRequestSkill emits a mesh__request_skill audit row. The inverse
// direction from skill_offer — we're pulling code FROM a peer, so the
// forensics question is "did we install something we didn't ask for?"
func (m *Manager) RecordRequestSkill(
	ctx context.Context, meta SessionMeta,
	requestedFromPeerID, skillName, skillVersion string,
	status, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	params := map[string]any{
		"requested_from": requestedFromPeerID,
		"skill_name":     skillName,
	}
	if skillVersion != "" {
		params["skill_version"] = skillVersion
	}
	m.auditAsync(ctx, m.buildMeshAuditRecord(meta, auditEventRequestSkill, params, status, "", errMsg))
}

// RecordGrantPeerScope emits a mesh__grant_peer_scope audit row. This is
// the highest-impact capability change on the mesh — a stray grant
// authorizes a peer for skill_request / cross-machine actions. Captures
// the granter (SessionID via meta) implicitly.
func (m *Manager) RecordGrantPeerScope(
	ctx context.Context, meta SessionMeta,
	peerID, scope string, status, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	params := map[string]any{
		"peer_id":    peerID,
		"scope":      scope,
		"granted_by": granterFromMeta(meta, m.selfPeerID),
	}
	m.auditAsync(ctx, m.buildMeshAuditRecord(meta, auditEventGrantPeerScope, params, status, "", errMsg))
}

// RecordRevokePeerScope emits a mesh__revoke_peer_scope audit row. The
// inverse of grant; closing forensics gap on capability removal too
// (so an attacker who flipped a peer cannot quietly cover their tracks).
func (m *Manager) RecordRevokePeerScope(
	ctx context.Context, meta SessionMeta,
	peerID, scope string, status, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	params := map[string]any{
		"peer_id":    peerID,
		"scope":      scope,
		"revoked_by": granterFromMeta(meta, m.selfPeerID),
	}
	m.auditAsync(ctx, m.buildMeshAuditRecord(meta, auditEventRevokePeerScope, params, status, "", errMsg))
}

// RecordSetAgentStatus emits a mesh__set_agent_status audit row.
// Status strings can carry leaked operational detail (e.g. project
// names) — auditing the transitions lets ops reconstruct what an
// agent claimed it was doing at a given time.
func (m *Manager) RecordSetAgentStatus(
	ctx context.Context, meta SessionMeta,
	agentID, oldStatus, newStatus string,
	status, errMsg string,
) {
	if m == nil || m.auditor == nil {
		return
	}
	params := map[string]any{
		"agent_id":   agentID,
		"old_status": oldStatus,
		"new_status": newStatus,
	}
	m.auditAsync(ctx, m.buildMeshAuditRecord(meta, auditEventSetAgentStatus, params, status, "", errMsg))
}

// granterFromMeta picks the most informative identifier for the actor
// performing a capability change. Prefers the session ID (which lets
// joins on mesh_agents recover the human/agent), then falls back to
// the local peer ID. Empty string means "unknown" — forensics will
// see a row with no actor but the timestamp/scope still pin the event.
func granterFromMeta(meta SessionMeta, selfPeerID string) string {
	if meta.SessionID != "" {
		return meta.SessionID
	}
	if selfPeerID != "" {
		return "mesh:" + selfPeerID
	}
	return ""
}
