#!/bin/sh
# check-deadcode.sh — guard against re-introduction of removed dead packages
# and dead exports.
#
# Checks tracked source files only. This repo often has nested historical
# worktrees under .claude/worktrees, so filesystem-wide grep/find would report
# false positives for code that is not part of the current tree.

set -eu

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

errors=0

check_pkg_absent() {
	pkg="$1"
	if tracked_pkg_files="$(git ls-files "$pkg" "$pkg/*" | grep '\.go$' || true)"; [ -n "$tracked_pkg_files" ]; then
		echo "ERROR: dead package '$pkg' is present in tracked files:"
		printf '%s\n' "$tracked_pkg_files" | sed 's/^/  /'
		errors=$((errors + 1))
	fi
}

check_sym_absent() {
	sym="$1"
	if refs="$(git grep -n "$sym" -- '*.go' || true)"; [ -n "$refs" ]; then
		echo "ERROR: tracked Go references to '$sym' found:"
		printf '%s\n' "$refs" | sed 's/^/  /'
		errors=$((errors + 1))
	fi
}

check_file_absent() {
	f="$1"
	if [ -f "$f" ]; then
		echo "ERROR: dead file '$f' still exists"
		errors=$((errors + 1))
	fi
}

# 1. internal/sudohelper — zero importers; helper daemon never built.
check_pkg_absent "internal/sudohelper"
check_sym_absent "sudohelper"

# 2. internal/concierge/ab.go — AggregateArmStats/PickWinner/etc only called
#    from tests; REST handler reimplements aggregation independently.
#    MemoryScopeKeyLessons is retained (used by lessons.go).
check_file_absent "internal/concierge/ab_test.go"
check_sym_absent "AggregateArmStats"
check_sym_absent "PickWinner"
check_sym_absent "DefaultStickyHash"

# 3. MeshPeerApprover — never wired into approval pipeline; PeerApprover
#    interface retained for policy.go.
check_sym_absent "MeshPeerApprover"
check_sym_absent "NewMeshPeerApprover"

# 4. Dispatcher dead exports — superseded by the two-tool surface.
check_sym_absent "parseAllowlistJSONForWorker"
check_sym_absent "workerAllowlistPatterns"

if [ "$errors" -gt 0 ]; then
	printf '\nFound %s error(s). Dead code guard failed.\n' "$errors"
	exit 1
fi

echo "OK: dead-code guard passed."
