// Package writeclass provides a single canonical heuristic that
// classifies a namespaced MCP tool name as "write-class" (side-effecting)
// or "read-class" (safe / informational).
//
// The heuristic is the SECURITY contract for propose-mode: a worker in
// propose mode MUST short-circuit on any write-class tool BEFORE the
// tool is dispatched. Two divergent copies of this rule (one in the
// dispatcher, one in the UI handler) is a classic drift surface — so
// we keep one implementation here and reuse it from both call sites.
//
// Coverage:
//   - snake_case prefixes  e.g. github__create_issue, linear__update_status
//   - camelCase prefixes   e.g. github__createIssue, linear__updateUser
//   - dangerous substrings e.g. info_purge_logs, drop_table_*, force_push
//
// When in doubt the helper FAILS CLOSED — flags ambiguous names as
// write-class so propose-mode opts for an approval prompt rather than
// silently executing.
package writeclass

import "strings"

// writeVerbs is the canonical list of verbs that imply side effects.
// Used in three matching modes (see IsWriteClass).
//
// The second block (archive..move) was added to close the read-only
// capability-gate leak: downstream mutators that don't use the classic
// create/update/delete verbs (notion__archive_page, jira__assign_issue,
// github__add_collaborator, …) were slipping past the researcher
// (read-only) gate. Each is boundary-matched as a snake_case word or
// camelCase prefix, so benign names (address, assets, additional_*) do
// NOT trip — see verbAppearsAsWord / hasCamelCaseVerbPrefix.
var writeVerbs = []string{
	"create", "update", "delete", "remove", "post", "send",
	"write", "publish", "merge", "execute", "approve", "reject",
	"set", "patch", "upsert", "edit", "drop", "truncate",
	"overwrite", "purge",
	"archive", "cancel", "revoke", "rename", "assign", "disable",
	"enable", "restore", "add", "append", "import", "ingest", "move",
	"harvest",
}

// dangerSubstrings are verbs / phrases so destructive that finding them
// ANYWHERE in the local tool name flips it to write-class regardless of
// position. Substring match, case-insensitive. Each entry should encode
// a verb + qualifier so a benign "purchase_order" doesn't trip on the
// bare "purge" — qualified strings like "purge_" stay safe.
var dangerSubstrings = []string{
	"purge_", "_purge", "drop_table", "delete_all", "force_push",
	"truncate_", "_truncate",
}

// IsWriteClass returns true when the namespaced tool name looks
// side-effecting. The classification is intentionally over-permissive
// on the write side — a false positive costs the operator one approval
// prompt, a false negative bypasses the propose-mode gate entirely.
func IsWriteClass(name string) bool {
	lower := strings.ToLower(name)
	// Strip the namespace prefix so we classify on the local tool name.
	// LastIndex (not Index) covers nested namespaces if they ever appear.
	local := lower
	if i := strings.LastIndex(lower, "__"); i >= 0 {
		local = lower[i+2:]
	}
	if hasDangerSubstring(local) {
		return true
	}
	if hasSnakeCaseVerbWord(local) {
		return true
	}
	// Camel-case prefix check needs the ORIGINAL (un-lowered) local name
	// so we can spot the capitalised second word: createIssue → "create"
	// + "Issue". Re-derive the un-lowered local slice.
	originalLocal := name
	if i := strings.LastIndex(name, "__"); i >= 0 {
		originalLocal = name[i+2:]
	}
	return hasCamelCaseVerbPrefix(originalLocal)
}

// hasDangerSubstring scans the local tool name for any of the
// dangerSubstrings. Already lowered by the caller.
func hasDangerSubstring(local string) bool {
	for _, s := range dangerSubstrings {
		if strings.Contains(local, s) {
			return true
		}
	}
	return false
}

// hasSnakeCaseVerbWord matches `<verb>` as a snake_case word — either
// at the very start, or following an underscore. The trailing boundary
// must be end-of-string or another underscore so `set` matches in
// `set_value` / `info_set_flag` but not in `setup_thing` / `assets`.
func hasSnakeCaseVerbWord(local string) bool {
	for _, v := range writeVerbs {
		if verbAppearsAsWord(local, v) {
			return true
		}
	}
	return false
}

// verbAppearsAsWord checks whether `verb` appears in `local` as a
// snake_case word: preceded by start-of-string or `_`, followed by
// end-of-string or `_`. Lets us catch `info_send_logs` (a write tool by
// virtue of the embedded send_ verb) while still rejecting `assets`.
func verbAppearsAsWord(local, verb string) bool {
	for i := 0; i+len(verb) <= len(local); i++ {
		if local[i:i+len(verb)] != verb {
			continue
		}
		// Left boundary: start of string or preceding underscore.
		if i != 0 && local[i-1] != '_' {
			continue
		}
		// Right boundary: end of string or trailing underscore.
		end := i + len(verb)
		if end != len(local) && local[end] != '_' {
			continue
		}
		return true
	}
	return false
}

// hasCamelCaseVerbPrefix matches `<verb><Capital>…` — the second
// character after the verb stem MUST be an ASCII uppercase letter so
// `setup` doesn't get classified as `set` + `up`. `local` here is the
// case-preserved local tool name (not lowercased).
func hasCamelCaseVerbPrefix(local string) bool {
	for _, v := range writeVerbs {
		if len(local) <= len(v) {
			continue
		}
		if !strings.EqualFold(local[:len(v)], v) {
			continue
		}
		next := local[len(v)]
		if next >= 'A' && next <= 'Z' {
			return true
		}
	}
	return false
}
