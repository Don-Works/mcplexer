// Package delegscope holds the canonical default tool allowlists that the
// delegation admin layer applies to every delegated worker, plus the predicate
// the CLI scope guard uses to recognise them.
//
// The constants live here — a leaf package importing only the standard library
// — because two packages need a single source of truth for them and cannot
// import each other:
//
//   - internal/workers/admin (delegation.go) WRITES these onto a delegated
//     worker's tool_allowlist_json when the operator supplies no allowlist.
//   - internal/workers/runner (cli_scope_guard.go) must DISTINGUISH this system
//     default from an operator-authored scope: a CLI-provider worker's scope is
//     unenforceable, so the guard refuses a scoped CLI run — but the broad
//     system default is a baseline, not an operator security boundary, and
//     refusing it would break every default CLI delegation.
//
// admin already imports runner, so the constants cannot live in runner without
// runner "owning" a delegation-product decision; and runner cannot import admin
// (that would cycle). A neutral leaf both import keeps one definition.
package delegscope

import (
	"encoding/json"
	"sort"
	"strings"
)

// DefaultToolsJSON is the tool allowlist an execute-mode delegated worker
// receives when the operator supplies no tool_allowlist_json. It is the SYSTEM
// default, not an operator-authored scope.
//
// index__summary / index__symbols are in the baseline deliberately. They are
// read-only, local code-index queries, and their presence is what lets a
// citation-verifying post_execute_script (which re-derives every file:line
// claim from the index) run WITHOUT the operator having to add index tools —
// which on a CLI worker would read as an operator scope and be refused as
// unenforceable, leaving weak/local models (where line-citation drift is
// worst) with no model-free way to catch a wrong-line answer.
const DefaultToolsJSON = `["mcpx__execute_code","mcpx__search_tools","mcpx__skill_search","mcpx__skill_get","mcpx__workspace_read_file","mcpx__workspace_list_directory","mcpx__workspace_write_file","mcpx__workspace_edit_file","index__summary","index__symbols","mesh__send","mesh__receive","mesh__list_peers","mesh__list_agents","memory__save","memory__recall","memory__list","task__create","task__get","task__list","task__update","task__append_note"]`

// DefaultReviewToolsJSON is the hardened default for worker_mode=review. It
// omits mutating operations (task create/update, memory save) so a review
// worker cannot make state changes unless the operator explicitly supplies a
// broader allowlist. Like DefaultToolsJSON it is a system default: the role
// filter narrows the baseline, it is not an operator-authored security scope.
// The read-only index tools stay (a review worker benefits most from checking
// citations against the index).
const DefaultReviewToolsJSON = `["mcpx__execute_code","mcpx__search_tools","mcpx__skill_search","mcpx__skill_get","mcpx__workspace_read_file","mcpx__workspace_list_directory","index__summary","index__symbols","mesh__send","mesh__receive","mesh__list_peers","mesh__list_agents","memory__recall","memory__list","task__get","task__list","task__append_note"]`

// IsDefaultAllowlist reports whether raw is one of the system default
// delegation allowlists (execute or review), compared as an order- and
// whitespace-insensitive set of tool names.
//
// A set comparison — rather than byte equality — makes the check robust to any
// re-marshalling of the stored column and treats an operator who reproduces the
// exact default set (a permutation restricts nothing below the baseline) the
// same as the default. An operator who authors a genuinely different allowlist
// (fewer tools, extra tools) does not match and is correctly seen as scoped.
func IsDefaultAllowlist(raw string) bool {
	got, ok := parseToolList(raw)
	if !ok {
		return false
	}
	for _, def := range []string{DefaultToolsJSON, DefaultReviewToolsJSON} {
		want, ok := parseToolList(def)
		if ok && equalStringSets(got, want) {
			return true
		}
	}
	return false
}

// parseToolList unmarshals a tool_allowlist_json column into its tool names.
// Empty, "null", and any non-array/malformed value are reported absent — those
// are handled as "no allowlist" by the guard's non-empty check, never a
// default.
func parseToolList(raw string) ([]string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil, false
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}

// equalStringSets reports whether a and b hold the same tool names, ignoring
// order. Duplicates are preserved (multiset equality) so a padded list never
// aliases a shorter default.
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
