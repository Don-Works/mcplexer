#!/usr/bin/env bash
# citation-bench-questions.sh — the question set and its ground truth.
#
# Sourced by citation-bench.sh; not executable on its own. Split out to keep
# both files inside the repo's 300-line limit, and so the question set can be
# edited without touching the dispatch/scoring engine.
#
# Every expected value is DERIVED FROM THE WORKING TREE at call time via
# grep -n. Nothing here is a literal answer, so the benchmark cannot rot as
# the repo moves.

# ---------------------------------------------------------------------------
# ground truth — every value below is derived from the tree, never literal.
# Each emits: id <TAB> kind <TAB> expected <TAB> cite_file <TAB> cite_line <TAB> prompt
# cite_line empty means "no single authoritative line" (counts, yes/no).
# ---------------------------------------------------------------------------
HOOKS=internal/workers/runner/hooks.go
FACTORY=internal/models/factory.go
ROUTER=internal/api/router.go
MAIN=cmd/mcplexer/main.go

# const_value <file> <name> -> prints the literal on the right of `=`.
# Handles both grouped (`\tNAME = x`) and standalone (`const NAME = x`) decls.
const_value() {
    grep -nE "^[[:space:]]*(const[[:space:]]+)?$2[[:space:]]*=" "$1" | head -1 |
        sed -E 's/^[0-9]+:.*=[[:space:]]*//' | tr -d '"'
}
# decl_line <file> <regex> -> prints the 1-based line of the first match
decl_line() { grep -nE "$2" "$1" | head -1 | cut -d: -f1; }

# NOTE: fields are tab-separated but bash `read` collapses runs of IFS
# whitespace, so an empty column would silently shift every later field. Rows
# emit "-" for "no authoritative line" rather than an empty cell.

questions() {
    local v l
    v=$(const_value "$HOOKS" hookVerdictSentinel); l=$(decl_line "$HOOKS" "^[[:space:]]*hookVerdictSentinel[[:space:]]*=")
    printf 'Q01\tconst_value\t%s\t%s\t%s\tIn %s, what is the exact string value of the constant hookVerdictSentinel? Give the value and cite file:line.\n' "$v" "$HOOKS" "$l" "$HOOKS"

    v=$(const_value "$HOOKS" maxHookReasonLen); l=$(decl_line "$HOOKS" "^[[:space:]]*maxHookReasonLen[[:space:]]*=")
    printf 'Q02\tconst_value\t%s\t%s\t%s\tIn %s, what is the numeric value of maxHookReasonLen? Give the value and cite file:line.\n' "$v" "$HOOKS" "$l" "$HOOKS"

    v=$(const_value "$HOOKS" maxHookOutputPreview); l=$(decl_line "$HOOKS" "^[[:space:]]*maxHookOutputPreview[[:space:]]*=")
    printf 'Q03\tconst_value\t%s\t%s\t%s\tIn %s, what is the numeric value of maxHookOutputPreview? Give the value and cite file:line.\n' "$v" "$HOOKS" "$l" "$HOOKS"

    l=$(decl_line "$HOOKS" "func \(r \*Runner\) runPostExecuteHook")
    printf 'Q04\tline_number\t%s\t%s\t%s\tIn %s, on which line does the function runPostExecuteHook begin? Cite file:line.\n' "$l" "$HOOKS" "$l" "$HOOKS"

    l=$(decl_line "$HOOKS" "func applyDelegationDeliverabilityGate")
    printf 'Q05\tline_number\t%s\t%s\t%s\tIn %s, on which line is applyDelegationDeliverabilityGate defined? Cite file:line.\n' "$l" "$HOOKS" "$l" "$HOOKS"

    l=$(decl_line "$HOOKS" "^[[:space:]]*hookPhasePost[[:space:]]*=")
    printf 'Q06\tline_number\t%s\t%s\t%s\tIn %s, on which line is the constant hookPhasePost declared? Cite file:line.\n' "$l" "$HOOKS" "$l" "$HOOKS"

    v=$(grep -cE '^\tcase "' "$MAIN")
    printf 'Q07\tcount\t%s\t%s\t-\tHow many top-level CLI subcommands (top-level `case "..."` arms) does %s dispatch? Answer with the number.\n' "$v" "$MAIN" "$MAIN"

    v=$(grep -c 'mux.HandleFunc' "$ROUTER")
    printf 'Q08\tcount\t%s\t%s\t-\tHow many mux.HandleFunc registrations are there in %s? Answer with the number.\n' "$v" "$ROUTER" "$ROUTER"

    if grep -q 'MCPLEXER_ALLOW_MIMO_CLI' "$FACTORY"; then v=yes; else v=no; fi
    printf 'Q09\tpresence\t%s\t%s\t-\tDoes the string MCPLEXER_ALLOW_MIMO_CLI appear anywhere in %s? Answer yes or no.\n' "$v" "$FACTORY" "$FACTORY"

    if grep -q 'MCPLEXER_ALLOW_DEEPSEEK_CLI' "$FACTORY"; then v=yes; else v=no; fi
    printf 'Q10\tpresence\t%s\t%s\t-\tDoes the string MCPLEXER_ALLOW_DEEPSEEK_CLI appear anywhere in %s? Answer yes or no.\n' "$v" "$FACTORY" "$FACTORY"

    v=$(const_value "$FACTORY" mimoCLIAllowEnvVar); l=$(decl_line "$FACTORY" "mimoCLIAllowEnvVar[[:space:]]*=")
    printf 'Q11\tconst_value\t%s\t%s\t%s\tIn %s, what string does the constant mimoCLIAllowEnvVar hold? Give the value and cite file:line.\n' "$v" "$FACTORY" "$l" "$FACTORY"

    l=$(decl_line "$HOOKS" "^[[:space:]]*hookVerdictSentinel[[:space:]]*=")
    printf 'Q12\tmulti_file\t%s\t%s\t%s\thookVerdictSentinel is a wire contract asserted in two files. Name the non-test file that declares it AND the _test.go file that mirrors it, citing file:line for the non-test declaration.\n' "hooks_test.go" "$HOOKS" "$l"
}
