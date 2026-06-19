package compact

// paginationKeys are metadata keys commonly found in paginated API responses
// that add noise without useful content.
var paginationKeys = map[string]bool{
	"next_cursor":     true,
	"has_more":        true,
	"page_info":       true,
	"total_count":     true,
	"next_page_token": true,
	"previous_cursor": true,
	"cursor":          true,
	"next_page":       true,
	"prev_page":       true,
	"total_pages":     true,
	"per_page":        true,
	"page":            true,
}

// PruneObject removes null, empty string (""), empty array ([]), and
// empty object ({}) values from a JSON object recursively. Does NOT
// remove false or 0 (semantically meaningful).
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

// PruneForSandbox applies PruneObject and additionally strips known
// pagination metadata keys. Intended for use before returning tool
// results to the code sandbox.
func PruneForSandbox(obj map[string]any) map[string]any {
	result := make(map[string]any, len(obj))
	for k, v := range obj {
		if isEmpty(v) || paginationKeys[k] {
			continue
		}
		pruned := pruneNestedSandbox(v)
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

func pruneNestedSandbox(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return PruneForSandbox(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			if m, ok := item.(map[string]any); ok {
				out[i] = PruneForSandbox(m)
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
