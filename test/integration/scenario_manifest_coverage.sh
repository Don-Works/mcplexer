#!/usr/bin/env bash
# scenario_manifest_coverage.sh — gate the run on feature_manifest.yaml
# coverage. For every feature in the manifest, at least one of its
# `scenario_functions` MUST be defined somewhere in the sourced scenarios.
# Missing scenarios are a hard FAIL — the manifest is the contract that
# every shipped feature has an e2e check.
#
# Sourced by scenarios.sh; exposes scenario_manifest_coverage().
#
# Implementation notes:
#   - We avoid yq so the harness stays low-dep. Manifest parser is awk-only.
#   - "Defined somewhere" is detected by: bash declares the function name
#     after all scenario_*.sh have been sourced. `declare -F <name>` is
#     the cheapest reliable check.
#   - Aspirational features (those owned by an in-flight epic) are still
#     enforced — that's intentional. The whole point is "feature without
#     a scenario row breaks the build".

scenario_manifest_coverage() {
    step 99 "feature_manifest.yaml coverage gate"

    local manifest
    manifest="$(dirname "${BASH_SOURCE[0]}")/feature_manifest.yaml"
    if [ ! -f "$manifest" ]; then
        fail "manifest_coverage" "feature_manifest.yaml not found at $manifest"
        return
    fi

    # Parse the manifest into a flat list of "id<TAB>fn1,fn2,..." records.
    # awk state machine: when we hit a "- id:" line, flush the previous
    # record and start a new one; when we hit "scenario_functions: [a,b]"
    # capture the array; emit on next "- id:" or EOF.
    local records
    records=$(awk '
        function flush() {
            if (cur_id != "") {
                printf "%s\t%s\n", cur_id, cur_fns
                cur_id=""; cur_fns=""
            }
        }
        /^[[:space:]]*-[[:space:]]+id:[[:space:]]+/ {
            flush()
            sub(/^[[:space:]]*-[[:space:]]+id:[[:space:]]+/, "")
            gsub(/["'\'']/, "")
            cur_id=$0
            next
        }
        /^[[:space:]]+scenario_functions:[[:space:]]*\[/ {
            line=$0
            sub(/^[^\[]*\[/, "", line)
            sub(/\].*$/, "", line)
            gsub(/[[:space:]"'\'']/, "", line)
            cur_fns=line
            next
        }
        END { flush() }
    ' "$manifest")

    if [ -z "$records" ]; then
        fail "manifest_coverage" "no features parsed from $manifest"
        return
    fi

    local total=0 missing=0 missing_list=""
    while IFS=$'\t' read -r fid fns; do
        [ -z "$fid" ] && continue
        total=$((total + 1))
        local found=""
        local IFS_orig=$IFS
        IFS=','
        for fn in $fns; do
            [ -z "$fn" ] && continue
            if declare -F "$fn" >/dev/null 2>&1; then
                found="$fn"
                break
            fi
        done
        IFS=$IFS_orig
        if [ -z "$found" ]; then
            missing=$((missing + 1))
            missing_list="$missing_list $fid"
        fi
    done <<< "$records"

    local covered=$((total - missing))
    if [ "$missing" -eq 0 ]; then
        pass "manifest coverage: $covered/$total features have a scenario"
    else
        fail "manifest coverage: $missing/$total features missing a scenario" \
            "missing IDs:$missing_list"
    fi
}
