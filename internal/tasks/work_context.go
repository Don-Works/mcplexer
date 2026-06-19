// work_context.go — structured "where-the-work-lives" annotations
// layered on top of the existing `meta` frontmatter column. Each
// supported key (worktree, branch, pr, commits, peer, session, linear,
// mesh_thread) gets its own line in meta following the convention
// `key: value`. Non-work-context lines (composes, composed_by, custom
// user fields) are preserved verbatim — Parse + Merge round-trips are
// non-destructive.
//
// Rationale: the hammerspoon coordination thread showed that a task
// should carry first-class pointers to its branch / worktree / PR / peer
// thread so agents + humans can stitch together parallel work without
// mining mesh chatter. Riding on the existing meta column avoids a
// schema migration AND keeps the existing free-form discoverability
// (custom keys still work — they're just ignored by the typed
// renderer).
package tasks

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// WorkContext is the typed view of the structured frontmatter keys
// agents + the dashboard render as chips on a task. JSON tags use
// snake_case to match the on-disk meta key spelling exactly.
type WorkContext struct {
	Worktree   string `json:"worktree,omitempty"`
	Branch     string `json:"branch,omitempty"`
	PR         string `json:"pr,omitempty"`          // URL
	Commits    string `json:"commits,omitempty"`     // sha range "abc..def"
	Peer       string `json:"peer,omitempty"`        // libp2p peer id
	Session    string `json:"session,omitempty"`     // session id (any shape)
	Linear     string `json:"linear,omitempty"`      // ticket id (e.g. ABC-123)
	MeshThread string `json:"mesh_thread,omitempty"` // mesh thread root id
}

// workContextKeys is the canonical key order — used by Merge to write
// keys in deterministic order so the meta column doesn't churn with
// non-semantic diffs across edits.
var workContextKeys = []string{
	"worktree", "branch", "pr", "commits",
	"peer", "session", "linear", "mesh_thread",
}

// commitsRangeRE matches a `<sha>..<sha>` shape, where each sha is at
// least 7 hex chars. Tolerates the optional `...` (triple-dot) form
// git uses for symmetric-difference ranges.
var commitsRangeRE = regexp.MustCompile(`^[0-9a-f]{7,}\.{2,3}[0-9a-f]{7,}$`)

// ParseWorkContext extracts the typed WorkContext from meta. Validates
// PR (must be parseable URL with scheme+host), Commits (sha..sha
// shape), and Peer (libp2p multihash length 46-52 chars) at parse
// time — invalid values surface as a non-nil error so the caller can
// reject the write rather than silently storing garbage.
//
// Unknown keys are ignored (preserved on disk via Merge); empty meta
// returns the zero WorkContext + nil error.
//
// Dual-read: meta is parsed as either JSON (post-072 shape) or
// frontmatter (legacy) per parseMetaToMap.
func ParseWorkContext(meta string) (WorkContext, error) {
	out := WorkContext{}
	if strings.TrimSpace(meta) == "" {
		return out, nil
	}
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return out, err
	}
	for _, key := range workContextKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		value, _ := raw.(string)
		if value == "" {
			// Array-valued work-context fields aren't a thing in the
			// existing schema (every work-context key is documented as a
			// scalar). Collapse to first element if someone wrote an
			// array via a custom client.
			if list := coerceMetaListValue(raw); len(list) > 0 {
				value = list[0]
			}
		}
		if err := assignWorkContextKey(&out, key, value); err != nil {
			return out, err
		}
	}
	return out, nil
}

// assignWorkContextKey writes one parsed key:value pair into the
// WorkContext slot, running the per-key validator first. Unknown keys
// are silently skipped (the merge path preserves them unchanged).
func assignWorkContextKey(out *WorkContext, key, value string) error {
	switch key {
	case "worktree":
		out.Worktree = value
	case "branch":
		out.Branch = value
	case "pr":
		if err := validatePRURL(value); err != nil {
			return fmt.Errorf("pr: %w", err)
		}
		out.PR = value
	case "commits":
		if err := validateCommitsRange(value); err != nil {
			return fmt.Errorf("commits: %w", err)
		}
		out.Commits = value
	case "peer":
		if err := validatePeerID(value); err != nil {
			return fmt.Errorf("peer: %w", err)
		}
		out.Peer = value
	case "session":
		out.Session = value
	case "linear":
		out.Linear = value
	case "mesh_thread":
		out.MeshThread = value
	}
	return nil
}

// MergeWorkContext applies the patch on top of the existing meta and
// returns the rewritten meta string in canonical JSON shape.
// Behaviour:
//
//   - Keys whose value is NOT a work-context key are preserved verbatim
//     (composes, composed_by, custom keys).
//   - For each work-context key with a non-empty value in patch, the
//     value is written (overwriting any prior value or array shape).
//   - Empty-string values in patch are NO-OPS (the struct shape can't
//     distinguish "absent" from "cleared"; explicit clears go through
//     SetWorkContext's clears slice).
//   - Output is always the canonical JSON-object meta shape — meta in
//     legacy frontmatter form is normalised on this write.
//
// Validates the merged values before returning — invalid patch values
// produce a (meta, error) where meta is the pre-merge string.
func MergeWorkContext(meta string, patch WorkContext) (string, error) {
	if err := patch.validate(); err != nil {
		return meta, err
	}
	out := meta
	for _, kv := range patchPairs(patch) {
		if kv.val == "" {
			continue
		}
		next, err := MetaSetScalar(out, kv.key, kv.val)
		if err != nil {
			return meta, err
		}
		out = next
	}
	// If the patch was empty but meta is legacy frontmatter, still
	// normalise to JSON so the dual-read state machine converges.
	if MetaIsLegacyFrontmatter(out) {
		normalised, err := MetaToJSON(out)
		if err != nil {
			return meta, err
		}
		out = normalised
	}
	return out, nil
}

// patchPairs renders the WorkContext patch as an ordered (key, value)
// slice — the canonical workContextKeys order so writes stay
// deterministic.
func patchPairs(p WorkContext) []struct{ key, val string } {
	return []struct{ key, val string }{
		{"worktree", p.Worktree},
		{"branch", p.Branch},
		{"pr", p.PR},
		{"commits", p.Commits},
		{"peer", p.Peer},
		{"session", p.Session},
		{"linear", p.Linear},
		{"mesh_thread", p.MeshThread},
	}
}

// validate runs each non-empty field through its type-specific
// validator. Called by MergeWorkContext before writing so bad patches
// don't corrupt the meta column.
func (w WorkContext) validate() error {
	if w.PR != "" {
		if err := validatePRURL(w.PR); err != nil {
			return fmt.Errorf("pr: %w", err)
		}
	}
	if w.Commits != "" {
		if err := validateCommitsRange(w.Commits); err != nil {
			return fmt.Errorf("commits: %w", err)
		}
	}
	if w.Peer != "" {
		if err := validatePeerID(w.Peer); err != nil {
			return fmt.Errorf("peer: %w", err)
		}
	}
	return nil
}

// validatePRURL requires the PR field be a valid http/https URL with a
// non-empty host. Anything else (e.g. "PR-123", a bare path) is
// rejected so the dashboard's link renderer doesn't have to handle
// malformed input.
func validatePRURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must be http(s) URL, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL is missing host")
	}
	return nil
}

// validateCommitsRange enforces the `sha..sha` shape with at least
// 7 hex chars per sha. Tolerates `...` (symmetric difference).
func validateCommitsRange(s string) error {
	if !commitsRangeRE.MatchString(s) {
		return fmt.Errorf("expected `<sha>..<sha>` with ≥7 hex chars each, got %q", s)
	}
	return nil
}

// validatePeerID is a coarse length check — libp2p peer ids are
// multibase-encoded multihashes, typically 46-52 base58 chars (CIDv0
// QmHash format). A real multihash parser is overkill here; the length
// guard rejects accidental garbage (workspace ids, sha hashes, "me").
func validatePeerID(s string) error {
	n := len(s)
	if n < 46 || n > 52 {
		return fmt.Errorf("expected libp2p peer id (46-52 chars), got %d chars", n)
	}
	return nil
}

// splitFrontmatterLine parses a `key: value` line into its components.
// Lines without a colon, or whose key is empty, return ok=false.
// Whitespace around key + value is trimmed; the value is returned
// untrimmed-internally so embedded colons in URLs survive.
func splitFrontmatterLine(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	if key == "" {
		return "", "", false
	}
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

// isWorkContextKey reports whether key is one of the canonical
// work-context slots — used by the merger to decide whether to touch
// a line or leave it alone.
func isWorkContextKey(key string) bool {
	switch key {
	case "worktree", "branch", "pr", "commits",
		"peer", "session", "linear", "mesh_thread":
		return true
	}
	return false
}
