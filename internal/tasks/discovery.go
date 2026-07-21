// discovery.go — pure helpers that shape the discovery envelope
// returned on task__list / task__get responses. Lives in the tasks
// package (not the gateway) so the filter/dedupe rules can be unit
// tested without standing up a full RPC handler. The gateway calls
// these to build the envelope.
//
// Rationale: every task__* response previously carried a ~600-token
// envelope (`known_assignees` with stale sessions, `known_tags` with
// the entire workspace history, `known_statuses` with the full vocab
// regardless of relevance). For typical read/write traffic the agent
// never acts on that metadata — it's pure context burn. These helpers
// trim each field to "things the caller actually might use next":
//
//   - known_assignees: active sessions only (caller passes `since`),
//     deduped by session_id, drop empties, cap at 5 most-recent.
//   - known_tags: tags present in the returned rows ∪ top-N most
//     frequent workspace-wide (provided by caller).
//   - known_statuses: statuses present in result ∪ default vocab.
package tasks

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// MaxKnownAssignees caps the number of assignee entries returned in the
// discovery envelope. Five is enough to disambiguate the people you're
// likely collaborating with right now without ballooning the response.
const MaxKnownAssignees = 5

// DefaultKnownStatuses is the canonical status vocabulary every
// workspace gets even when it has zero rows yet. Matches the existing
// fallback in Service.KnownStatuses.
var DefaultKnownStatuses = []string{
	"open", "doing", "blocked", "review", "done", "cancelled",
}

// KnownAssignee is the envelope-shaped form of a mesh-directory entry.
// Mirrors the previous handler-side anonymous map shape so existing
// callers don't see a behavioural break: name, session_id, last_seen,
// and (when present) peer_id.
type KnownAssignee struct {
	Name      string    `json:"name,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	LastSeen  time.Time `json:"last_seen"`
	PeerID    string    `json:"peer_id,omitempty"`
}

// FilterKnownAssignees applies the four rules from the LLM-ergonomics
// envelope spec:
//
//  1. drop entries where both `name` and `session_id` are blank — they
//     can't be referenced from any assignee tool;
//  2. dedupe by session_id, keeping the row with the most-recent
//     `last_seen` (defensive — the SQL key is session_id so dupes are
//     unlikely, but a future schema change shouldn't silently bloat
//     the envelope);
//  3. sort by last_seen DESC;
//  4. cap at MaxKnownAssignees.
//
// The 24-hour activity window is enforced by the caller via
// store.ListActiveMeshAgents's `since` argument — this helper is a
// pure filter over whatever rows it's given.
func FilterKnownAssignees(agents []store.MeshAgent) []KnownAssignee {
	if len(agents) == 0 {
		return nil
	}
	dedup := make(map[string]store.MeshAgent, len(agents))
	for _, a := range agents {
		name := strings.TrimSpace(a.Name)
		session := strings.TrimSpace(a.SessionID)
		if name == "" && session == "" {
			continue
		}
		// Anonymous-but-named rows (no session_id) are kept under a
		// synthetic key so we don't collapse two distinct agents that
		// merely lack session ids onto each other.
		key := session
		if key == "" {
			key = "name:" + name
		}
		prev, ok := dedup[key]
		if !ok || a.LastSeenAt.After(prev.LastSeenAt) {
			dedup[key] = a
		}
	}
	out := make([]KnownAssignee, 0, len(dedup))
	for _, a := range dedup {
		entry := KnownAssignee{
			Name:      strings.TrimSpace(a.Name),
			SessionID: strings.TrimSpace(a.SessionID),
			LastSeen:  a.LastSeenAt,
		}
		if strings.HasPrefix(a.Origin, "peer:") {
			entry.PeerID = strings.TrimPrefix(a.Origin, "peer:")
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		// Tie-break deterministically so the response is stable across
		// requests (otherwise map iteration order leaks into the wire).
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxKnownAssignees {
		out = out[:MaxKnownAssignees]
	}
	return out
}

// FilterKnownTags returns the union of tags present in the returned
// rows and the topN most-frequent tags across the workspace. The
// workspace-level counts are computed cheaply at the call site (one
// scan over the full workspace's tags would dwarf the savings — the
// caller is expected to pass the same `rows` it already has plus a
// pre-computed top-N from a dedicated store query if one exists).
//
// Today the gateway derives both from the same `rows` (one query),
// which is exactly what we want for a list response: the top-N
// surfaces the workspace's most-used vocabulary, and the rows-present
// set guarantees every tag actually visible in the result is callable.
//
// Sort order: alphabetical (stable across requests, matches the prior
// handler's deterministic sort).
func FilterKnownTags(rows []store.Task, workspaceTopN []string) []string {
	seen := make(map[string]struct{}, len(workspaceTopN))
	for _, tg := range workspaceTopN {
		t := strings.TrimSpace(tg)
		if t == "" {
			continue
		}
		seen[t] = struct{}{}
	}
	for _, r := range rows {
		for _, tg := range decodeTags(r.TagsJSON) {
			t := strings.TrimSpace(tg)
			if t == "" {
				continue
			}
			seen[t] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// FilterKnownStatuses returns the union of statuses present in the
// returned rows and the default canonical vocab plus any
// workspace-declared vocab. Drops empties + dedupes. Order: default
// vocab first (preserves the suggested ordering for new workspaces),
// then anything else alphabetically.
func FilterKnownStatuses(rows []store.Task, workspaceVocab []string) []string {
	seen := make(map[string]struct{}, len(rows)+len(workspaceVocab))
	for _, r := range rows {
		s := strings.TrimSpace(r.Status)
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	for _, s := range workspaceVocab {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	// Emit the default vocab first in its canonical order.
	for _, s := range DefaultKnownStatuses {
		if _, ok := seen[s]; ok {
			out = append(out, s)
			delete(seen, s)
		}
	}
	// Then anything else, alphabetised.
	extras := make([]string, 0, len(seen))
	for s := range seen {
		extras = append(extras, s)
	}
	sort.Strings(extras)
	out = append(out, extras...)
	return out
}

// TopMetaKeys scans the provided rows' meta values and returns the
// topN most-frequently-present meta keys (descending by count, then
// alphabetical for stability). Both JSON and legacy frontmatter
// shapes are recognised — the dual-read MetaKeys helper handles
// both. Returns an empty slice when no meta is set on any row.
//
// Used by the discovery envelope on task__list so agents can see
// which meta keys are queryable via meta_match / meta_has_key /
// meta_in without a separate query.
func TopMetaKeys(rows []store.Task, topN int) []string {
	if topN <= 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, r := range rows {
		for _, k := range MetaKeys(r.Meta) {
			counts[k]++
		}
	}
	type kv struct {
		key string
		n   int
	}
	ranked := make([]kv, 0, len(counts))
	for k, v := range counts {
		ranked = append(ranked, kv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return ranked[i].key < ranked[j].key
	})
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	out := make([]string, len(ranked))
	for i, kv := range ranked {
		out[i] = kv.key
	}
	return out
}

// TopWorkspaceTags scans the provided rows and returns the topN most
// frequent tags, sorted by frequency DESC then alphabetically.
// Cheap-per-list-call: a future store-side DistinctTaskTags helper
// would be even cheaper but the interface surface is already large.
func TopWorkspaceTags(rows []store.Task, topN int) []string {
	if topN <= 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, r := range rows {
		for _, tg := range decodeTags(r.TagsJSON) {
			tg = strings.TrimSpace(tg)
			if tg == "" {
				continue
			}
			counts[tg]++
		}
	}
	type kv struct {
		tag string
		n   int
	}
	ranked := make([]kv, 0, len(counts))
	for k, v := range counts {
		ranked = append(ranked, kv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return ranked[i].tag < ranked[j].tag
	})
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	out := make([]string, len(ranked))
	for i, kv := range ranked {
		out[i] = kv.tag
	}
	return out
}

// decodeTags is a tolerant tags-column reader: returns nil on malformed
// JSON rather than failing the whole envelope build. Same forgiveness
// shape as the previous gateway-local helper.
func decodeTags(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
