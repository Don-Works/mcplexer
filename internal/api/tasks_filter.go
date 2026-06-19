package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// parseTaskFilter reads the common querystring filter shape into
// store.TaskFilter + an FTS query if `q` was set.
//
// Meta filter args (since migration 072):
//   - meta_match=key:value,key2:value2  — every pair must match.
//   - meta_has_key=key1,key2            — meta object contains each key.
//   - meta_in=key:v1|v2,key2:vA|vB      — value at key is one of |-list.
//
// Assignee filter args (human assignees since migration 105):
//   - assignee_user_id=<id>             — match tasks owned by this human.
//   - assignee_origin_kind=human|local|peer — also accepts "self" / "other"
//     shorthands: "self" resolves to the local self user; "other" matches
//     any task not assigned to self. The MCP surface accepts these too.
//
// (The MCP surface uses richer JSON shapes for these; the REST query-
// string uses the comma-shorthand because URLs can't carry nested
// objects nicely. Same TaskFilter on the other side.)
func parseTaskFilter(r *http.Request) (store.TaskFilter, string) {
	q := r.URL.Query()
	f := store.TaskFilter{
		WorkspaceID:         q.Get("workspace_id"),
		Status:              q.Get("status"),
		AssigneeSessionID:   q.Get("assignee_session_id"),
		AssigneeOriginKind:  q.Get("assignee_origin_kind"),
		AssigneePeerID:      q.Get("assignee_peer_id"),
		AssigneeUserID:      q.Get("assignee_user_id"),
		AssignedBySessionID: q.Get("assigned_by_session_id"),
		OriginPeerID:        q.Get("origin_peer_id"),
	}
	if tag := q.Get("tag"); tag != "" {
		f.Tags = []string{tag}
	}
	switch strings.ToLower(q.Get("state")) {
	case "open":
		b := false
		f.OnlyTerminal = &b
	case "closed":
		b := true
		f.OnlyTerminal = &b
	}
	if limit, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = limit
	}
	if offset, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = offset
	}
	if since := q.Get("updated_after"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			f.UpdatedAfter = &t
		}
	}
	if m, err := parseTaskMetaMatch(q.Get("meta_match")); err == nil && len(m) > 0 {
		f.MetaMatch = m
	}
	if k, err := parseTaskMetaHasKey(q.Get("meta_has_key")); err == nil && len(k) > 0 {
		f.MetaHasKey = k
	}
	if in, err := parseTaskMetaIn(q.Get("meta_in")); err == nil && len(in) > 0 {
		f.MetaIn = in
	}
	return f, q.Get("q")
}

// parseTaskMetaMatch parses the meta_match=key:value,key2:value2
// querystring form. Returns an empty map for empty input.
func parseTaskMetaMatch(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.IndexByte(pair, ':')
		if idx <= 0 {
			return nil, fmt.Errorf("expected key:value, got %q", pair)
		}
		k := strings.TrimSpace(pair[:idx])
		v := strings.TrimSpace(pair[idx+1:])
		if err := validateRESTMetaKey(k); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// parseTaskMetaHasKey parses meta_has_key=key1,key2.
func parseTaskMetaHasKey(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := []string{}
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if err := validateRESTMetaKey(k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// parseTaskMetaIn parses meta_in=key:v1|v2,key2:vA|vB.
func parseTaskMetaIn(s string) (map[string][]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := map[string][]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.IndexByte(pair, ':')
		if idx <= 0 {
			return nil, fmt.Errorf("expected key:v1|v2, got %q", pair)
		}
		k := strings.TrimSpace(pair[:idx])
		if err := validateRESTMetaKey(k); err != nil {
			return nil, err
		}
		vals := []string{}
		for _, v := range strings.Split(pair[idx+1:], "|") {
			v = strings.TrimSpace(v)
			if v != "" {
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			out[k] = vals
		}
	}
	return out, nil
}

// validateRESTMetaKey mirrors the MCP surface's validator — only
// safe characters for json_extract path interpolation.
func validateRESTMetaKey(k string) error {
	if k == "" {
		return fmt.Errorf("meta key is required")
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("meta key %q contains illegal character %q", k, r)
		}
	}
	return nil
}

// decodeJSON helper for typed request bodies. Mirrors the memory
// handler's decodeJSON; small duplication for clarity.
func taskDecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// (unused in this file — relies on the package-level decodeJSON in
// helpers.go. Kept for the test build link if helpers.go evolves.)
var _ = taskDecodeJSON
