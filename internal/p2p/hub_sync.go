// Package p2p — hub sync protocol types and interfaces.
//
// The hub-sync extension allows an always-on mcplexer peer (the "hub") to
// serve its skill-registry index to paired peers. Callers pull the index,
// compare content hashes against their local state, and selectively pull
// entries they don't have.
//
// Wire protocol (extends /mcplexer/skill-registry/1.0.0):
//
//	Request types:
//	  → {"type":"index"}\n
//	  ← JSON array of HubIndexEntry, one line
//
//	  → {"type":"search","q":"deploy to fly","limit":10}\n
//	  ← JSON array of HubSearchHit, one line
//
//	  → {"type":"request","name":"foo","version":0}\n   (existing)
//	  ← framed (body, bundle)                            (existing)
//
//	Versioning and conflict model:
//
//	  Each registry entry is identified by (name, version, content_hash).
//	  Content hash is the SHA-256 of (body + bundle). When a peer pulls an
//	  index entry whose content_hash differs from the local entry with the
//	  same (name, version), that entry is a "conflict candidate". The hub
//	  sync service does NOT auto-resolve conflicts — it surfaces them so
//	  the agent or user can decide.
//
//	  Design principles:
//	    - Immutable content hash: once published, an entry's content hash
//	      never changes. A "re-publish" of different content gets a new
//	      version number (monotonic increment).
//	    - No last-writer-wins: both the local and remote version are
//	      preserved. The sync service marks the pair as a conflict
//	      candidate and stops.
//	    - Stale divergent base: when peer A has v3 and hub has v5, A
//	      pulls v4+v5 incrementally. When A has v3 with different
//	      content_hash from hub's v3, A marks it as a conflict and
//	      does NOT auto-pull v4+v5 until the conflict is resolved.
//
// Follow-up tasks:
//   - Implement periodic background sync from a designated hub peer
//   - Add upstream push and notify frames.
//   - Add conflict-resolution UI in the dashboard.
//   - Extend the index response with incremental diff (since_version).

package p2p

import (
	"context"
)

// HubIndexEntry is one skill in the hub's index response. Contains enough
// information for the caller to decide whether to pull the full entry
// without needing the body or bundle.
type HubIndexEntry struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	BundleSHA   string `json:"bundle_sha,omitempty"`
}

// HubIndexRequest is the wire frame for requesting a full index.
type HubIndexRequest struct {
	Type string `json:"type"` // always "index"
}

// HubIndexResponse is the JSON array of entries sent back by the hub.
type HubIndexResponse struct {
	Entries []HubIndexEntry `json:"entries"`
}

// HubSearchRequest is the wire frame for querying a hub's skill index.
// It returns metadata only; callers must issue a follow-up pull for the
// selected skill body/bundle.
type HubSearchRequest struct {
	Type              string `json:"type"` // always "search"
	Q                 string `json:"q"`
	Limit             int    `json:"limit,omitempty"`
	RemoteWorkspaceID string `json:"remote_workspace_id,omitempty"`
}

// HubSearchHit is one ranked remote match. It intentionally omits the
// skill body and bundle bytes so search stays cheap and inspection-safe.
type HubSearchHit struct {
	Name              string  `json:"name"`
	Version           int     `json:"version"`
	Score             float64 `json:"score"`
	ContentHash       string  `json:"content_hash"`
	Description       string  `json:"description"`
	Author            string  `json:"author,omitempty"`
	BundleSHA         string  `json:"bundle_sha,omitempty"`
	Scope             string  `json:"scope,omitempty"`
	RemoteWorkspaceID string  `json:"remote_workspace_id,omitempty"`
}

// HubSearchResponse is the single-line JSON response for a search query.
type HubSearchResponse struct {
	Hits []HubSearchHit `json:"hits"`
}

// HubSyncResult is returned by the sync operation, summarising what was
// pulled and what conflicts were detected.
type HubSyncResult struct {
	Pulled    []HubIndexEntry `json:"pulled"`
	Skipped   []HubIndexEntry `json:"skipped"`   // already present with same hash
	Conflicts []HubIndexEntry `json:"conflicts"` // different hash for same (name,version)
	Errors    []string        `json:"errors,omitempty"`
}

// HubIndexProvider is the responder-side hook that returns the hub's
// full index of active skill entries. Implementations must respect
// visibility: only entries the remote peer is authorised to see should
// be included. For now, global entries only (workspace-scoped entries
// are not shared over the mesh).
type HubIndexProvider interface {
	ListIndexEntries(ctx context.Context) ([]HubIndexEntry, error)
}

// HubSearchProvider is optionally implemented by the hub's index
// provider to serve ranked natural-language search over visible entries.
type HubSearchProvider interface {
	SearchIndexEntries(ctx context.Context, q string, limit int) ([]HubSearchHit, error)
}

// ConflictDetector compares a remote index against local state and
// partitions entries into (to-pull, already-have, conflict). When nil
// conflict candidates are found the caller is responsible for
// resolution.
type ConflictDetector interface {
	ClassifyEntries(ctx context.Context, remote []HubIndexEntry) (
		toPull []HubIndexEntry,
		skipped []HubIndexEntry,
		conflicts []HubIndexEntry,
		err error,
	)
}

// HubSyncService orchestrates pulling an index from a hub peer and
// selectively fetching entries. It is constructed once per
// RegistryShareService in the gateway wiring.
//
// The full implementation lives in hub_sync_p2p.go (p2p build tag)
// and hub_sync_stub.go (!p2p build tag).
type HubSyncService struct {
	service *RegistryShareService
	detect  ConflictDetector
}

// NewHubSyncService wires the sync helper around an existing
// RegistryShareService. The ConflictDetector is used to partition the
// remote index into pull/skip/conflict sets.
func NewHubSyncService(
	service *RegistryShareService, detect ConflictDetector,
) *HubSyncService {
	return &HubSyncService{service: service, detect: detect}
}

// SyncFromPeer pulls the index from the designated hub peer, classifies
// entries against local state, and pulls each entry that's missing.
// Returns a summary of what happened.
//
// This is the skeleton — the full p2p implementation will dial the peer,
// send an index request, read the response, and then issue individual
// request calls for each entry that needs pulling.
func (s *HubSyncService) SyncFromPeer(
	ctx context.Context, peerID string,
) (*HubSyncResult, error) {
	_ = ctx
	_ = peerID
	return nil, ErrHubSyncNotImplemented
}
