package skillregistry

import (
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// extractManifestExtra walks the parsed frontmatter map and pulls out
// the W4 fields (requires, produces, consumes, phases, refinement, includes) as
// a strongly-typed skills.ManifestExtra. Validated immediately so an
// invalid block fails Publish (rather than silently dropping fields).
//
// Strategy: round-trip through JSON. The YAML decoder produces
// `map[string]any` shapes that yaml.v3 understands, but the canonical
// json struct tags on ManifestExtra do the heavy lifting. Anything not
// recognised stays in `meta` for the freeform metadata blob.
//
// The claimed keys are pulled out of `meta` BEFORE the metadata blob is
// re-marshalled — we don't want them duplicated as both `metadata.requires`
// and the dedicated `manifest_extra` column.
func extractManifestExtra(meta map[string]any) (skills.ManifestExtra, error) {
	if meta == nil {
		return skills.ManifestExtra{}, nil
	}
	picked := map[string]any{}
	for _, key := range manifestExtraKeys {
		if v, ok := meta[key]; ok {
			picked[key] = v
			delete(meta, key)
		}
	}
	if len(picked) == 0 {
		return skills.ManifestExtra{}, nil
	}
	encoded, err := json.Marshal(picked)
	if err != nil {
		return skills.ManifestExtra{}, fmt.Errorf("marshal frontmatter extras: %w", err)
	}
	var e skills.ManifestExtra
	if err := json.Unmarshal(encoded, &e); err != nil {
		return skills.ManifestExtra{}, fmt.Errorf("decode frontmatter extras: %w", err)
	}
	if err := skills.ValidateExtra(e); err != nil {
		return skills.ManifestExtra{}, err
	}
	return e, nil
}

// manifestExtraKeys are the top-level frontmatter keys claimed by the
// W4 typed extras. Anything else stays in the freeform metadata blob.
var manifestExtraKeys = []string{
	"requires", "produces", "consumes", "phases", "refinement", "includes",
}

// stashManifestExtra round-trips the W4 extras INTO the metadata blob
// under ManifestExtraStashKey. Round-tripping (rather than carrying a
// new field on the store entry) is the workaround for this milestone's
// "do not touch internal/store/models.go" constraint — the sqlite
// layer pulls the value out of the blob and into the dedicated
// `manifest_extra` column on insert, and re-injects it on read.
//
// metaJSON nil/empty → a fresh `{key: extra}` blob.
// extras zero → original metaJSON returned untouched.
func stashManifestExtra(metaJSON json.RawMessage, extra skills.ManifestExtra) (json.RawMessage, error) {
	if extra.IsZero() {
		return metaJSON, nil
	}
	out := map[string]any{}
	if len(metaJSON) > 0 {
		if err := json.Unmarshal(metaJSON, &out); err != nil {
			return nil, fmt.Errorf("stash extras: parse metadata: %w", err)
		}
		if out == nil {
			out = map[string]any{}
		}
	}
	encoded, err := skills.MarshalExtra(extra)
	if err != nil {
		return nil, err
	}
	var typed any
	if err := json.Unmarshal(encoded, &typed); err != nil {
		return nil, fmt.Errorf("stash extras: re-decode: %w", err)
	}
	out[ManifestExtraStashKey] = typed
	merged, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("stash extras: marshal: %w", err)
	}
	return merged, nil
}

// ExtraFromEntry extracts the W4 ManifestExtra stashed inside a store
// entry's MetadataJSON. Returns the zero value when the entry doesn't
// carry one (skill predates W4, or didn't declare the fields). Never
// returns an error — a corrupted stash silently degrades to "no extras"
// because the canonical source is the dedicated sqlite column and the
// stash is only there to bridge the store-layer ownership boundary.
func ExtraFromEntry(entry *store.SkillRegistryEntry) skills.ManifestExtra {
	if entry == nil || len(entry.MetadataJSON) == 0 {
		return skills.ManifestExtra{}
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(entry.MetadataJSON, &meta); err != nil {
		return skills.ManifestExtra{}
	}
	raw, ok := meta[ManifestExtraStashKey]
	if !ok || len(raw) == 0 {
		return skills.ManifestExtra{}
	}
	e, err := skills.UnmarshalExtra(raw)
	if err != nil {
		return skills.ManifestExtra{}
	}
	return e
}
