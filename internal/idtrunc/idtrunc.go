// Package idtrunc provides safe truncation helpers for displaying ids
// (peer ids, session ids, content hashes) without panicking on
// shorter-than-expected inputs.
//
// Background: the gateway formats peer-, session-, and hash-like
// identifiers in a lot of human-facing strings (mesh listings, skill
// registry summaries, queue diagnostics, audit lines). Each caller used
// to inline `s[:N]` or `s[:H] + "…" + s[len(s)-T:]`. When the input
// happened to be empty (e.g. a legacy mesh_agents row with an empty
// session_id, or a not-yet-populated public key column) the slice
// expression panicked with "slice bounds out of range" and took down the
// whole tool-dispatch goroutine.
//
// These helpers centralise the bounds-check so the same fix doesn't have
// to be re-discovered at every call site. Prefer them over inline slices
// for any string that originates outside the package (DB row, mesh
// envelope, RPC arg).
package idtrunc

// Short returns the first n bytes of s, or s verbatim when shorter than
// or equal to n. Safe for all inputs including the empty string.
func Short(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Ellipsis returns the first head bytes + "…" + the last tail bytes of
// s. When s is shorter than head+tail (so the slices would overlap or
// the middle ellipsis would be uninformative), it returns s verbatim.
// Safe for all inputs including the empty string.
//
// Negative head/tail are treated as zero.
func Ellipsis(s string, head, tail int) string {
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	if head+tail == 0 || len(s) < head+tail {
		return s
	}
	return s[:head] + "…" + s[len(s)-tail:]
}
