package compact

// PruneObject removes null, empty string (""), empty array ([]), and
// empty object ({}) values from a JSON object recursively. Does NOT
// remove false or 0 (semantically meaningful). Pagination/cursor keys are
// deliberately NOT special-cased: cursors are load-bearing metadata, and
// silently stripping them cost agents the ability to page (removed 2026-07).
// Used ONLY by explicit opt-in surfaces (the sandbox compact() helper and
// columnar table rendering) — never on a value or result the caller did not
// ask to have pruned.
func PruneObject(obj map[string]any) map[string]any {
	result := make(map[string]any, len(obj))
	for k, v := range obj {
		if isEmpty(v) {
			continue
		}
		pruned := pruneNested(v)
		if isEmpty(pruned) {
			continue
		}
		result[k] = pruned
	}
	return result
}

func pruneNested(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return PruneObject(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			if m, ok := item.(map[string]any); ok {
				out[i] = PruneObject(m)
			} else {
				out[i] = item
			}
		}
		return out
	default:
		return v
	}
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return val == ""
	case []any:
		return len(val) == 0
	case map[string]any:
		return len(val) == 0
	default:
		return false
	}
}
