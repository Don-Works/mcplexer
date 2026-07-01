package compression

import "encoding/json"

// Transform is a single content-aware compression step over an MCP
// tool-result envelope. Implementations MUST be pure and deterministic: the
// same input always yields the same output, with no external state.
type Transform interface {
	// Name is a stable identifier used as the key in savings stats and as the
	// per-transform toggle id in the settings UI.
	Name() string

	// Lossless reports whether applying this transform loses NO information the
	// consumer needs — either byte-identical or value-identical (e.g. the same
	// JSON value with insignificant whitespace removed). Only lossless
	// transforms may run in On mode without a CCR backing (reversible-lossy
	// transforms land later, gated by that store).
	Lossless() bool

	// Apply returns the transformed payload and whether anything changed. It
	// MUST return the input unchanged (changed=false) on any structural
	// surprise rather than erroring — a compressor must never break a result.
	Apply(result json.RawMessage) (out json.RawMessage, changed bool)
}

// StashingTransform is the optional interface a transform implements when it
// drops information that must remain recoverable via CCR. The pipeline
// persists each returned original (keyed by CCRKey) so mcpx__retrieve can
// return the exact bytes on demand — making an otherwise-lossy transform
// effectively lossless from the model's perspective. A StashingTransform that
// changes a payload MUST return the dropped original(s) in stash, or the
// gimmick gate rejects it as irreversibly lossy.
type StashingTransform interface {
	Transform
	ApplyWithStash(result json.RawMessage) (out json.RawMessage, changed bool, stash [][]byte)
}

// Verifier is an optional interface a Lossless transform implements to let the
// pipeline re-check, at apply time, that it actually preserved the payload's
// model-visible meaning — a runtime backstop against a buggy lossless transform
// shipping value corruption to the model, beyond the CI gimmick gate.
type Verifier interface {
	// Verify reports whether `after` preserves the model-visible information of
	// `before`. Called only for Lossless transforms about to be applied.
	Verify(before, after json.RawMessage) bool
}
