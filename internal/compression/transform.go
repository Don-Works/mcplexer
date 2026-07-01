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
