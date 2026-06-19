package gateway

import (
	"encoding/json"
	"time"
)

// extractAndRemoveCacheBust checks for a _cache_bust boolean in the
// tool call arguments and removes it before forwarding downstream.
func extractAndRemoveCacheBust(args *json.RawMessage) bool {
	if args == nil || len(*args) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(*args, &m); err != nil {
		return false
	}
	raw, ok := m["_cache_bust"]
	if !ok {
		return false
	}
	var bust bool
	if err := json.Unmarshal(raw, &bust); err != nil || !bust {
		return false
	}
	delete(m, "_cache_bust")
	cleaned, err := json.Marshal(m)
	if err != nil {
		return false
	}
	*args = cleaned
	return true
}

// injectCacheMeta adds a _cache field to the MCP tool result _meta object
// so the AI can see whether the response was served from cache and how old it is.
func injectCacheMeta(result json.RawMessage, cacheHit bool, cacheAge time.Duration) json.RawMessage {
	if len(result) == 0 {
		return result
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}

	cacheMeta := map[string]any{
		"cached": cacheHit,
	}
	if cacheHit {
		cacheMeta["age_seconds"] = int(cacheAge.Seconds())
	}

	// Merge into existing _meta or create it.
	meta := make(map[string]json.RawMessage)
	if raw, ok := envelope["_meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	cacheJSON, _ := json.Marshal(cacheMeta)
	meta["cache"] = cacheJSON

	metaJSON, _ := json.Marshal(meta)
	envelope["_meta"] = metaJSON

	out, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return out
}

func injectFuzzyCorrectionMeta(result json.RawMessage, original, corrected string) json.RawMessage {
	if len(result) == 0 || original == "" || corrected == "" || original == corrected {
		return result
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil {
		return result
	}

	meta := make(map[string]json.RawMessage)
	if raw, ok := envelope["_meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	correctionJSON, _ := json.Marshal(map[string]string{
		"original":  original,
		"corrected": corrected,
	})
	meta["fuzzy_correction"] = correctionJSON

	metaJSON, _ := json.Marshal(meta)
	envelope["_meta"] = metaJSON

	out, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return out
}
