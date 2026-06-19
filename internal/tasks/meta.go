// meta.go — JSON-shaped meta column helpers (migration 072).
//
// Background: meta started as a frontmatter blob — one `key: a, b, c`
// line per key — that was opaque to the server and parsed only by
// AI consumers. As real workloads piled up the filter-by-meta-key
// surface (give me everything composed_by:<epic>, all rows with
// worktree:<path>, etc.) turned that opacity into a scaling cliff —
// no indexable expression, no AND across keys, no array-contains.
//
// Migration 072 reshapes meta into a JSON object literal — same
// TEXT column, same .Meta field, just stricter content. Two
// invariants make this safe to land in one PR:
//
//  1. Dual-read on every helper here — meta whose first non-space
//     byte is `{` is parsed as JSON; anything else flows through
//     the legacy frontmatter parser. Old rows keep working until
//     the next write rewrites them.
//  2. Single-write — appendMetaListLine + removeMetaListLine +
//     MergeWorkContext + SetWorkContext + Update all funnel
//     through encodeMetaJSON so the on-disk shape after any
//     write is always JSON. Backfill is done row-by-row in a
//     post-migration Go hook in internal/store/sqlite so even
//     tasks no one touches eventually upgrade.
package tasks

import (
	"encoding/json"
	"sort"
	"strings"
)

// MetaIsLegacyFrontmatter reports whether a meta value is in the
// pre-072 frontmatter shape (key: a, b, c lines). The check is
// trivial — JSON objects always start with `{` after trim — but it's
// centralised so every helper makes the same decision.
//
// Empty strings count as JSON (the empty object is the canonical
// "no metadata" representation; the legacy empty-string sentinel is
// preserved on disk only because writers haven't touched the row yet).
func MetaIsLegacyFrontmatter(meta string) bool {
	for _, r := range meta {
		switch r {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return false
		default:
			return true
		}
	}
	return false
}

// MetaToJSON normalises a meta string to canonical JSON-object shape.
// Legacy frontmatter is parsed into a map[string][]string then
// re-serialised as JSON; scalar/list disambiguation follows the same
// "single value → string, multiple → array" rule the frontmatter
// shape implied. JSON input is round-tripped through json.Unmarshal
// + json.Marshal to canonicalise key ordering and whitespace.
//
// An empty input is left empty so the post-migration backfill can
// detect untouched rows (it doesn't have to rewrite the entire table
// on first boot).
func MetaToJSON(meta string) (string, error) {
	if strings.TrimSpace(meta) == "" {
		return "", nil
	}
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return "", err
	}
	return encodeMetaJSON(obj)
}

// encodeMetaJSON marshals a normalised meta map back to JSON with
// stable key ordering. Empty maps round-trip to `""` so the column's
// "no metadata" sentinel stays consistent.
func encodeMetaJSON(obj map[string]any) (string, error) {
	if len(obj) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Re-emit in sorted order so diffs stay stable.
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		kj, _ := json.Marshal(k)
		vj, err := json.Marshal(obj[k])
		if err != nil {
			return "", err
		}
		pairs = append(pairs, string(kj)+":"+string(vj))
	}
	return "{" + strings.Join(pairs, ",") + "}", nil
}

// parseMetaToMap turns either the JSON or the legacy frontmatter
// shape into the canonical in-memory representation. Values are
// either string (single scalar) or []any (list of strings) — the
// frontmatter form's single-element line collapses to a string so
// round-tripping a "key: value" line doesn't gratuitously promote
// it to an array on next write.
func parseMetaToMap(meta string) (map[string]any, error) {
	trim := strings.TrimSpace(meta)
	if trim == "" {
		return map[string]any{}, nil
	}
	if !MetaIsLegacyFrontmatter(meta) {
		var out map[string]any
		if err := json.Unmarshal([]byte(trim), &out); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]any{}
		}
		return out, nil
	}
	out := map[string]any{}
	for _, line := range strings.Split(meta, "\n") {
		key, value, ok := splitFrontmatterLine(line)
		if !ok {
			continue
		}
		parts := splitFrontmatterListValue(value)
		switch len(parts) {
		case 0:
			out[key] = ""
		case 1:
			out[key] = parts[0]
		default:
			// Lift to []any so json.Marshal emits a JSON array (matches
			// MetaListGet's contract for multi-valued keys).
			arr := make([]any, len(parts))
			for i, p := range parts {
				arr[i] = p
			}
			out[key] = arr
		}
	}
	return out, nil
}

// splitFrontmatterListValue splits a `key: a, b, c` value body into
// its comma-separated parts. Whitespace is trimmed, empties dropped.
// A single-element line returns a single-element slice (the caller
// decides whether to collapse to scalar).
func splitFrontmatterListValue(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// MetaListGet returns the values stored under `key` as a string
// slice. Scalars become a 1-element slice; arrays of mixed types
// have their non-string members dropped (matches the original
// frontmatter convention's all-strings-or-nothing assumption). A
// missing key returns nil — distinguishing "no such key" from "key
// with empty value" is the caller's job (use MetaHasKey).
//
// Replaces the package-private appendMetaListLine/ReadMetaList
// frontmatter readers — same external contract, supports both shapes
// while the backfill rolls forward.
func MetaListGet(meta, key string) []string {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return nil
	}
	v, ok := obj[key]
	if !ok {
		return nil
	}
	return coerceMetaListValue(v)
}

// coerceMetaListValue accepts the canonical map value shapes
// (string, []any, []string, nil) and returns the flattened list of
// string values. Non-string elements are dropped — meta is documented
// as a string-only key-value store.
func coerceMetaListValue(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// MetaHasKey reports whether `key` is present in meta — regardless
// of value (covers the `meta_has_key` filter). A missing key returns
// false; a key with empty string / empty array still returns true.
func MetaHasKey(meta, key string) bool {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// MetaGetScalar returns the string value at `key` when meta stores a
// single-string scalar there. Arrays collapse to their first element
// for query convenience (matches the meta_composed_by generated
// column's `[0]` index in the SQL surface).
func MetaGetScalar(meta, key string) (string, bool) {
	list := MetaListGet(meta, key)
	if len(list) == 0 {
		return "", false
	}
	return list[0], true
}

// MetaKeys returns the distinct meta keys present in the value, in
// stable (sorted) order. Used to populate the `known_meta_keys`
// discovery envelope on task__list.
func MetaKeys(meta string) []string {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MetaListAppend adds value to the list stored under `key` and
// returns the rewritten meta (always JSON shape). Idempotent — if
// value already appears in the existing list, returns meta
// unchanged. If `key` doesn't exist, the new entry is stored as a
// scalar string (matches the frontmatter convention's single-line
// case so a freshly-created key looks like `"key": "value"`, not
// `"key": ["value"]`). Adding a second value promotes the slot to
// an array.
//
// Replaces appendMetaListLine.
func MetaListAppend(meta, key, value string) (string, error) {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return meta, err
	}
	existing := coerceMetaListValue(obj[key])
	for _, e := range existing {
		if e == value {
			// Even on no-op, return the canonical JSON form so the
			// dual-read state machine converges every row over time.
			return encodeMetaJSON(obj)
		}
	}
	if _, present := obj[key]; !present {
		obj[key] = value
	} else {
		next := append(existing, value)
		arr := make([]any, len(next))
		for i, s := range next {
			arr[i] = s
		}
		obj[key] = arr
	}
	return encodeMetaJSON(obj)
}

// MetaListRemove drops value from the list stored under `key`. If
// the list ends up empty (or never had value to begin with), the
// key is removed entirely. Idempotent.
//
// Replaces removeMetaListLine (which never existed as a function —
// the only existing remove path was the SetWorkContext clears
// slice — but is provided here for symmetry + filter testing).
func MetaListRemove(meta, key, value string) (string, error) {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return meta, err
	}
	existing := coerceMetaListValue(obj[key])
	if len(existing) == 0 {
		return encodeMetaJSON(obj)
	}
	next := make([]string, 0, len(existing))
	for _, e := range existing {
		if e != value {
			next = append(next, e)
		}
	}
	switch len(next) {
	case 0:
		delete(obj, key)
	case 1:
		obj[key] = next[0]
	default:
		arr := make([]any, len(next))
		for i, s := range next {
			arr[i] = s
		}
		obj[key] = arr
	}
	return encodeMetaJSON(obj)
}

// MetaSetScalar sets a scalar string value under `key`, replacing
// any existing value (scalar or array). Used by MergeWorkContext to
// implement the typed work-context overlay on top of the JSON store.
// Empty value clears the key.
func MetaSetScalar(meta, key, value string) (string, error) {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return meta, err
	}
	if value == "" {
		delete(obj, key)
	} else {
		obj[key] = value
	}
	return encodeMetaJSON(obj)
}

// MetaClearKey removes the key entirely, regardless of its value
// shape. Used by SetWorkContext's clears slice.
func MetaClearKey(meta, key string) (string, error) {
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return meta, err
	}
	delete(obj, key)
	return encodeMetaJSON(obj)
}

// MetaMatch reports whether the meta object contains every key in
// `want` with a matching value (scalar exact match OR array contains
// match). Used as the in-Go evaluator for the meta_match filter when
// the backing store can't push the predicate down to SQL.
func MetaMatch(meta string, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return false
	}
	for k, v := range want {
		got, ok := obj[k]
		if !ok {
			return false
		}
		if !metaValueContains(got, v) {
			return false
		}
	}
	return true
}

// metaValueContains is the per-value "did the filter match" check.
// Scalars compare with == ; arrays match if any element equals the
// wanted value.
func metaValueContains(got any, want string) bool {
	switch x := got.(type) {
	case string:
		return x == want
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range x {
			if s == want {
				return true
			}
		}
	}
	return false
}

// MetaIn reports whether the meta object's value at `key` is one of
// the listed values. Backs the meta_in filter — useful for
// "give me everything whose status_kind is one of [working, blocked]"
// shapes without spamming three meta_match calls.
func MetaIn(meta string, want map[string][]string) bool {
	if len(want) == 0 {
		return true
	}
	obj, err := parseMetaToMap(meta)
	if err != nil {
		return false
	}
	for k, options := range want {
		got, ok := obj[k]
		if !ok {
			return false
		}
		matched := false
		for _, opt := range options {
			if metaValueContains(got, opt) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// ReadMetaList is the legacy export retained for the gateway
// handler + tests. New code should call MetaListGet directly. Same
// dual-read semantics, just a thinner shim.
func ReadMetaList(meta, key string) []string {
	return MetaListGet(meta, key)
}

// appendMetaListLine retains its old name + signature so callers in
// service.go (composeAppend) don't have to change. Internally it
// just delegates to MetaListAppend and ignores the error — meta
// parsing only fails for malformed JSON, which is impossible if the
// dual-read invariant holds.
func appendMetaListLine(meta, key, value string) string {
	out, err := MetaListAppend(meta, key, value)
	if err != nil {
		return meta
	}
	return out
}

// removeMetaListLine retains its old name + signature so callers in
// service.go (Decompose) don't have to change. Delegates to
// MetaListRemove and ignores errors (same rationale as appendMetaListLine).
func removeMetaListLine(meta, key, value string) string {
	out, err := MetaListRemove(meta, key, value)
	if err != nil {
		return meta
	}
	return out
}
