package sqlite

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// manifestExtraStashKey is the reserved key under which skillregistry
// round-trips W4 ManifestExtra through SkillRegistryEntry.MetadataJSON
// — see internal/skillregistry/registry.go (ManifestExtraStashKey) for
// the canonical declaration. Duplicated here as a string constant to
// keep the sqlite layer free of an upward dependency on skillregistry.
const manifestExtraStashKey = "__manifest_extra"

// splitManifestExtra pops the stashed ManifestExtra JSON out of a
// MetadataJSON blob and returns (metadataWithout, extraJSON). When the
// blob is empty or has no stash key, returns the original blob and
// "{}" — the canonical zero-extras encoding stored in the dedicated
// `manifest_extra` column.
//
// Always returns at least "{}" for the second value so the INSERT can
// rely on a non-null payload (the column is NOT NULL DEFAULT '{}').
func splitManifestExtra(metaJSON json.RawMessage) (json.RawMessage, string) {
	if len(metaJSON) == 0 {
		return metaJSON, "{}"
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaJSON, &meta); err != nil || meta == nil {
		return metaJSON, "{}"
	}
	raw, ok := meta[manifestExtraStashKey]
	if !ok || len(raw) == 0 {
		return metaJSON, "{}"
	}
	delete(meta, manifestExtraStashKey)
	cleaned, err := json.Marshal(meta)
	if err != nil {
		// Drop only the stash key; leave the rest of metadata alone
		// by falling back to the original blob. Worst case the stash
		// is duplicated; correctness is preserved.
		return metaJSON, normalizeStashedExtra(raw)
	}
	return cleaned, normalizeStashedExtra(raw)
}

// normalizeStashedExtra coerces a json.RawMessage into the canonical
// "{}"-as-empty wire shape used by the manifest_extra column.
func normalizeStashedExtra(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

// mergeManifestExtra re-injects the manifest_extra column value back
// into the entry's MetadataJSON blob under manifestExtraStashKey, so
// downstream consumers that read MetadataJSON (API responses, dashboard
// renderers, the mesh skill-share path) see the W4 fields without
// needing to know about the dedicated column.
//
// metaJSON empty or unparseable: returned untouched.
// extra NULL / "{}" / "null": returned untouched (no stash injected).
func mergeManifestExtra(metaJSON json.RawMessage, extra sql.NullString) json.RawMessage {
	if !extra.Valid {
		return metaJSON
	}
	trimmed := strings.TrimSpace(extra.String)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return metaJSON
	}
	base := map[string]json.RawMessage{}
	if len(metaJSON) > 0 {
		if err := json.Unmarshal(metaJSON, &base); err != nil {
			// Original blob unparseable — return it unchanged so we
			// don't silently corrupt freeform metadata for the sake
			// of a (later, separately readable) extras column.
			return metaJSON
		}
	}
	base[manifestExtraStashKey] = json.RawMessage(trimmed)
	out, err := json.Marshal(base)
	if err != nil {
		return metaJSON
	}
	return out
}
