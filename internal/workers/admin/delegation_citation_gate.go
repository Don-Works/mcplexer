package admin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/don-works/mcplexer/internal/toolgate"
)

// citationGateScript is the vetted post_execute_script injected when a
// delegation sets verify_citations. It is embedded verbatim from
// citation_gate.js, a byte-for-byte copy of scripts/citation-gate.js kept in
// this package because go:embed cannot reference a path outside the package
// directory. delegation_citation_gate_test.go asserts the embed still equals
// the repo script, so an edit to one that skips the other fails the build's
// tests rather than shipping a stale gate.
//
//go:embed citation_gate.js
var citationGateScript string

// citationGateRequiredTools are the read-only code-index tools the gate calls
// (index.summary / index.symbols inside the code-mode sandbox). They are in
// the delegscope default allowlist, so verify_citations needs no operator
// allowlist — and must NOT force one, because a CLI worker refuses an
// operator-authored scope as unenforceable. This slice is only used to REJECT
// the contradictory combination of verify_citations with an explicit allowlist
// (or capability profile) that scopes these tools out, where the gate would
// otherwise fail closed and reject every run on a dispatch error.
var citationGateRequiredTools = []string{"index__summary", "index__symbols"}

// applyCitationVerification injects the embedded citation gate as the
// delegation's post_execute_script when verify_citations is set. It runs late
// in normalizeDelegationInput, after the tool allowlist default is applied and
// the capability profile is resolved, so it can validate the gate's index
// tools against the effective surface the worker will run with.
//
// Contract:
//   - verify_citations=false (or absent): no-op.
//   - verify_citations=true with a caller-supplied post_execute_script:
//     REJECTED. The gate calls abort() on a contradicted citation, so silently
//     wrapping or overwriting a caller's own gate would change its meaning; the
//     operator must compose the two deliberately if they want both.
//   - verify_citations=true with the effective surface scoping out the index
//     tools: REJECTED. The gate would fail closed and reject every run.
//   - otherwise: PostExecuteScript is set to the embedded gate. The allowlist
//     is left untouched so the delegscope default stays recognisable to the
//     CLI scope guard (mutating it would make a default CLI worker refuse).
func applyCitationVerification(in *DelegationInput) error {
	if in == nil || !in.VerifyCitations {
		return nil
	}
	if strings.TrimSpace(in.PostExecuteScript) != "" {
		return fmt.Errorf(
			"verify_citations cannot be combined with a caller-supplied post_execute_script: " +
				"the citation gate rejects the run on a contradicted citation, so it will not " +
				"silently wrap or replace your script; drop one, or compose them yourself in a " +
				"single post_execute_script")
	}
	if err := citationGateToolsReachable(in); err != nil {
		return err
	}
	in.PostExecuteScript = citationGateScript
	return nil
}

// citationGateToolsReachable checks the effective tool surface (the resolved
// allowlist plus any capability profile) admits every tool the gate needs. A
// missing tool is a hard error, not a warning: verify_citations promises
// model-free verification, and a fail-closed gate would instead reject every
// run — worse than the operator knowing up front their scope contradicts the
// flag.
func citationGateToolsReachable(in *DelegationInput) error {
	allowed, err := parseAllowlistPatterns(in.ToolAllowlistJSON)
	if err != nil {
		return fmt.Errorf("verify_citations: cannot parse tool_allowlist_json: %w", err)
	}
	var profile *toolgate.CapabilityProfile
	if strings.TrimSpace(in.capabilityProfileJSON) != "" {
		profile = &toolgate.CapabilityProfile{}
		if err := json.Unmarshal([]byte(in.capabilityProfileJSON), profile); err != nil {
			return fmt.Errorf("verify_citations: capability profile is malformed: %w", err)
		}
	}
	for _, tool := range citationGateRequiredTools {
		if !allowlistGrantsTool(allowed, tool) {
			return fmt.Errorf(
				"verify_citations needs %s but tool_allowlist_json scopes it out; "+
					"the citation gate would fail closed and reject every run. Leave "+
					"tool_allowlist_json unset to use the default (which includes the index "+
					"tools), or add the index tools to your allowlist", tool)
		}
		if profile != nil {
			if ok, reason := profile.Allows(tool, false); !ok {
				return fmt.Errorf(
					"verify_citations needs %s but the capability profile denies it (%s); "+
						"the citation gate would fail closed and reject every run", tool, reason)
			}
		}
	}
	return nil
}

// parseAllowlistPatterns unmarshals a resolved tool_allowlist_json into its
// glob patterns. Empty / "null" yields no patterns (allowlistGrantsTool then
// reports the tool ungranted, which is the correct verdict for verify_citations
// since a delegated worker with no allowlist reaches nothing).
func parseAllowlistPatterns(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil, nil
	}
	var patterns []string
	if err := json.Unmarshal([]byte(s), &patterns); err != nil {
		return nil, err
	}
	return patterns, nil
}

// allowlistGrantsTool reports whether tool matches at least one allowlist glob,
// using the same path.Match semantics the runtime allowlist gate applies. Tool
// names use "__" (never "/"), so "index__*" and "*" both match "index__summary".
func allowlistGrantsTool(patterns []string, tool string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == tool {
			return true
		}
		if matched, err := path.Match(pattern, tool); err == nil && matched {
			return true
		}
	}
	return false
}
