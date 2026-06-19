package mesh

import "time"

// meshImportSourcePrefix marks store rows (auth scopes, OAuth providers,
// downstream servers, route rules) that were written by an inbound
// mesh.auth_sync import rather than authored locally. The sender peer ID is
// appended so a row's provenance records *which* peer it came from.
const meshImportSourcePrefix = "mesh-import:"

// meshImportSource is the Source value stamped on rows imported from peerID.
func meshImportSource(peerID string) string {
	return meshImportSourcePrefix + peerID
}

// importClobberOK reports whether an existing local row whose Source is
// existingSource may be overwritten by an inbound snapshot from senderPeerID.
//
// Only rows previously imported from the SAME peer may be refreshed. Locally
// authored rows (Source "api", "yaml", "") and rows imported from a DIFFERENT
// peer are preserved — this is what prevents unsafe last-writer-wins where a
// second peer (or a peer racing local edits) silently clobbers credentials or
// downstream server command/args that the user did not intend to replace.
func importClobberOK(existingSource, senderPeerID string) bool {
	if senderPeerID == "" {
		return false
	}
	return existingSource == meshImportSource(senderPeerID)
}

// authSyncAcceptSnapshot is the replay + staleness gate. It returns true when a
// snapshot identified by (peerID, snapshotID, scopeName, exportedAt) should be
// applied, and records it as accepted. It returns false — and records nothing —
// when the snapshot is a replay (snapshotID already seen) or stale (exportedAt
// not strictly newer than the last accepted snapshot for that peer+scope, or
// missing entirely). Callers MUST drop the snapshot on false.
func (m *Manager) authSyncAcceptSnapshot(peerID, scopeName, snapshotID string, exportedAt time.Time) bool {
	if peerID == "" || snapshotID == "" || scopeName == "" {
		return false
	}
	// A snapshot with no export timestamp cannot be reasoned about for
	// freshness; reject rather than let an unstamped (legacy/forged) payload
	// bypass the rollback guard. Our own sender always stamps exported_at.
	if exportedAt.IsZero() {
		return false
	}
	m.authSyncGuardMu.Lock()
	defer m.authSyncGuardMu.Unlock()
	if m.authSyncSeen == nil {
		m.authSyncSeen = map[string]struct{}{}
	}
	if m.authSyncFreshness == nil {
		m.authSyncFreshness = map[string]time.Time{}
	}
	seenKey := peerID + "\x00" + snapshotID
	if _, dup := m.authSyncSeen[seenKey]; dup {
		return false
	}
	freshKey := peerID + "\x00" + scopeName
	if last, ok := m.authSyncFreshness[freshKey]; ok && !exportedAt.After(last) {
		return false
	}
	m.authSyncSeen[seenKey] = struct{}{}
	m.authSyncFreshness[freshKey] = exportedAt
	return true
}
