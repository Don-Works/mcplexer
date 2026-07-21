#!/usr/bin/env bash
# run_overnight.sh — repeat the bulletproof e2e suite N times, aggregate
# results, identify flake.
#
# A scenario that PASSes in one run and FAILs/SKIPs in another is a flake
# — exit non-zero. A scenario that FAILs in all N runs is a stable failure
# — exit non-zero. A scenario that SKIPs in all N runs is a pending gap —
# logged, not failed.
#
# Usage:
#   bash test/integration/run_overnight.sh           # N=10 (default)
#   bash test/integration/run_overnight.sh 5         # N=5
#   RUNS_N=20 bash test/integration/run_overnight.sh
#
# Env:
#   RUNS_N=N      number of iterations (overrides positional arg)
#   TEST_KEEP=1   skip teardown on the LAST run so the operator can poke
#                 at the live state (earlier runs always tear down so
#                 each iteration starts fresh).
#
# Hooks (E2):
#   scenario_resource_posture --baseline   runs ONCE before the first
#                                          test-integration invocation.
#   scenario_resource_posture --check      runs ONCE after the final run
#                                          completes.
#
# Design note: we drive the baseline + check from this orchestrator
# rather than from inside scenarios.sh because scenarios.sh tears its
# containers down between iterations. The resource posture guard needs
# the daemon state to persist across the full N runs — see
# scenario_resource_posture.sh for the docker-volume-keep pattern.

set -uo pipefail
# NOTE: -e intentionally off — we want to capture every run's exit code
# and aggregate even when one (or more) of the N invocations fails.

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
LOGS_DIR="$ROOT/test/integration/_logs"
SCENARIO_RESOURCE="$ROOT/test/integration/scenario_resource_posture.sh"

# ----- args + env --------------------------------------------------------
ARG_N="${1:-}"
if [ -n "${RUNS_N:-}" ]; then
    N="$RUNS_N"
elif [ -n "$ARG_N" ]; then
    N="$ARG_N"
else
    N=10
fi
case "$N" in
    *[!0-9]*|"")
        printf 'invalid N=%s — must be a positive integer\n' "$N" >&2
        exit 2
        ;;
esac
if [ "$N" -lt 1 ]; then
    printf 'invalid N=%s — must be >= 1\n' "$N" >&2
    exit 2
fi

TIMESTAMP="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
REPORT="$LOGS_DIR/overnight_report.md"

mkdir -p "$LOGS_DIR"

# Fresh per-overnight workspace: previous run-* trees from earlier
# invocations are pruned so the aggregator only sees this run's data.
find "$LOGS_DIR" -maxdepth 1 -type d -name 'run-*' -exec rm -rf {} + 2>/dev/null || true
rm -f "$REPORT" "$LOGS_DIR/baseline.json" "$LOGS_DIR/final_posture.json"

printf 'mcplexer overnight bulletproof harness\n'
printf '  runs:      %d\n' "$N"
printf '  logs:      %s\n' "$LOGS_DIR"
printf '  timestamp: %s\n' "$TIMESTAMP"
printf '\n'

# ----- E2 hook (baseline) ------------------------------------------------
# scenario_resource_posture.sh is sourced (not exec'd) so the
# scenario_resource_posture function lives in our shell. Source-time errors
# are non-fatal — the baseline is best-effort; a missing baseline means
# the --check step SKIPs rather than blocking the whole overnight.
if [ -f "$SCENARIO_RESOURCE" ]; then
    # shellcheck disable=SC1090
    . "$SCENARIO_RESOURCE" || printf '  warn: failed to source %s\n' "$SCENARIO_RESOURCE" >&2
fi

# ----- run loop ----------------------------------------------------------
declare -a EXIT_CODES=()
for i in $(seq 1 "$N"); do
    printf '\n========== RUN %d / %d ==========\n' "$i" "$N"
    RUN_DIR="$LOGS_DIR/run-$i"
    mkdir -p "$RUN_DIR"

    # TEST_KEEP semantics: only the LAST run keeps containers up (per the
    # brief). Earlier runs MUST tear down so each iteration starts fresh
    # — otherwise the bulletproof suite races against stale state.
    if [ "${TEST_KEEP:-0}" = "1" ] && [ "$i" -eq "$N" ]; then
        export TEST_KEEP=1
        printf '  (final run — TEST_KEEP=1, containers will be left up)\n'
    else
        unset TEST_KEEP
    fi

    # E2 baseline: BEFORE the first run's containers exist we can't
    # measure them, so the baseline runs AFTER run-1 brings the
    # containers up but BEFORE we start counting deltas. Easiest is to
    # snapshot right after run-1 — that becomes the floor. Later runs
    # bring containers up + down again, so the absolute counters reset
    # naturally; we re-snapshot every run and only compare the final
    # snapshot vs the baseline.
    #
    # The "delta" semantics: baseline = state after run 1; final = state
    # after run N. If something leaked across N-1 teardown-bringup
    # cycles, the final will be markedly larger.
    #
    # Caveat: each run tears its containers DOWN unless TEST_KEEP=1, so
    # the "baseline" needs the same compose-up state as the "check". We
    # use docker-volume retention (default behaviour) plus the same
    # container names, but the container PIDs reset every run. This is
    # an honest weakness of the harness — flagged in the report.
    BULLETPROOF=1 make -C "$ROOT" test-integration 2>&1 | tee "$RUN_DIR/test.log"
    EC=${PIPESTATUS[0]}
    echo "$EC" > "$RUN_DIR/exit_code"
    EXIT_CODES+=("$EC")

    # Capture the SUMMARY block (last ~120 lines: the === SUMMARY ===
    # banner + PASS/FAIL counters + per-line RESULTS).
    tail -n 120 "$RUN_DIR/test.log" > "$RUN_DIR/summary.txt"

    # E2 baseline: take after run 1 succeeds (or even fails — we want
    # the post-bringup state regardless). Skipped if the function isn't
    # available (scenario_resource_posture.sh wasn't sourceable).
    if [ "$i" -eq 1 ] && command -v scenario_resource_posture >/dev/null 2>&1; then
        printf '\n  --- capturing resource baseline ---\n'
        scenario_resource_posture --baseline "$LOGS_DIR/baseline.json" \
            || printf '  warn: baseline capture failed (continuing)\n' >&2
    fi

    printf '  run %d exit=%d\n' "$i" "$EC"
done

# ----- E2 hook (final check) --------------------------------------------
POSTURE_STATUS="skip"
POSTURE_DETAIL="resource posture not measured"
if command -v scenario_resource_posture >/dev/null 2>&1; then
    if [ -f "$LOGS_DIR/baseline.json" ]; then
        printf '\n========== RESOURCE POSTURE CHECK ==========\n'
        if scenario_resource_posture --check "$LOGS_DIR/baseline.json" \
                "$LOGS_DIR/final_posture.json" \
                > "$LOGS_DIR/posture.log" 2>&1; then
            POSTURE_STATUS="pass"
            POSTURE_DETAIL="all deltas within guard"
        else
            POSTURE_STATUS="fail"
            POSTURE_DETAIL="$(tail -n 20 "$LOGS_DIR/posture.log" | tr '\n' ' ')"
        fi
        cat "$LOGS_DIR/posture.log"
    else
        POSTURE_DETAIL="baseline.json missing — baseline step failed"
    fi
fi

# ----- aggregation -------------------------------------------------------
# Each run's summary.txt contains lines like:
#   PASS: <name>
#   FAIL: <name>
#   SKIP: <name> — <reason>
# Scenarios.sh adds these via lib.sh's pass/fail/skip. We walk every run's
# summary and build a {scenario -> [r1_result, r2_result, ..., rN_result]}
# map, then classify each scenario as stable-pass / stable-fail /
# stable-skip / flake.

printf '\n========== AGGREGATING ==========\n'

# Tab-separated extract: run_index<TAB>status<TAB>scenario_name
EXTRACT="$LOGS_DIR/_extract.tsv"
: > "$EXTRACT"
for i in $(seq 1 "$N"); do
    SUMMARY="$LOGS_DIR/run-$i/summary.txt"
    [ -f "$SUMMARY" ] || continue
    # Match the lib.sh "  PASS: ..." / "  FAIL: ..." / "  SKIP: ..." lines
    # plus the bare-prefix variants (in case future edits drop indents).
    awk -v run="$i" '
        /^[[:space:]]*PASS:[[:space:]]/ {
            sub(/^[[:space:]]*PASS:[[:space:]]*/, "");
            sub(/[[:space:]]+$/, "");
            printf "%d\tPASS\t%s\n", run, $0; next
        }
        /^[[:space:]]*FAIL:[[:space:]]/ {
            sub(/^[[:space:]]*FAIL:[[:space:]]*/, "");
            sub(/[[:space:]]+$/, "");
            printf "%d\tFAIL\t%s\n", run, $0; next
        }
        /^[[:space:]]*SKIP:[[:space:]]/ {
            sub(/^[[:space:]]*SKIP:[[:space:]]*/, "");
            sub(/[[:space:]]+$/, "");
            printf "%d\tSKIP\t%s\n", run, $0; next
        }
    ' "$SUMMARY" >> "$EXTRACT"
done

# Build sorted unique scenario list. Names may contain spaces; sort by
# name only.
SCENARIOS_LIST="$LOGS_DIR/_scenarios.txt"
awk -F'\t' '{print $3}' "$EXTRACT" | sort -u > "$SCENARIOS_LIST"
SCENARIO_COUNT=$(wc -l < "$SCENARIOS_LIST" | tr -d '[:space:]')

STABLE_PASS=0
STABLE_FAIL=0
STABLE_SKIP=0
FLAKE=0
declare -a FLAKE_DETAIL=()
declare -a STABLE_FAIL_DETAIL=()
declare -a PENDING_DETAIL=()

# For each scenario, build per-run status array.
while IFS= read -r scen; do
    [ -z "$scen" ] && continue
    declare -a STATUSES=()
    for i in $(seq 1 "$N"); do
        # Pull status for this scenario in this run (default "-" = missing).
        st=$(awk -F'\t' -v r="$i" -v s="$scen" '
            $1==r && $3==s {print $2; exit}
        ' "$EXTRACT")
        if [ -z "$st" ]; then
            st="-"
        fi
        STATUSES+=("$st")
    done

    saw_pass=0
    saw_fail=0
    saw_skip=0
    saw_missing=0
    for st in "${STATUSES[@]}"; do
        case "$st" in
            PASS) saw_pass=1 ;;
            FAIL) saw_fail=1 ;;
            SKIP) saw_skip=1 ;;
            -) saw_missing=1 ;;
        esac
    done

    # Flake: any scenario that has both PASS and (FAIL or SKIP) across runs.
    # Stable: every observed run agrees (missing rows are treated as
    # neutral — a scenario that wasn't observed in some runs is still
    # stable if all observed runs agree).
    if [ "$saw_pass" -eq 1 ] && [ "$saw_fail" -eq 1 ]; then
        FLAKE=$((FLAKE + 1))
        FLAKE_DETAIL+=("$scen|$(IFS=,; echo "${STATUSES[*]}")")
    elif [ "$saw_pass" -eq 1 ] && [ "$saw_skip" -eq 1 ]; then
        # PASS in some runs, SKIP in others — that's a flake too (a
        # scenario that's sometimes runnable and sometimes not is a
        # signal worth investigating).
        FLAKE=$((FLAKE + 1))
        FLAKE_DETAIL+=("$scen|$(IFS=,; echo "${STATUSES[*]}")")
    elif [ "$saw_fail" -eq 1 ] && [ "$saw_pass" -eq 0 ] && [ "$saw_skip" -eq 0 ]; then
        STABLE_FAIL=$((STABLE_FAIL + 1))
        STABLE_FAIL_DETAIL+=("$scen")
    elif [ "$saw_skip" -eq 1 ] && [ "$saw_pass" -eq 0 ] && [ "$saw_fail" -eq 0 ]; then
        STABLE_SKIP=$((STABLE_SKIP + 1))
        PENDING_DETAIL+=("$scen")
    elif [ "$saw_pass" -eq 1 ] && [ "$saw_fail" -eq 0 ] && [ "$saw_skip" -eq 0 ]; then
        STABLE_PASS=$((STABLE_PASS + 1))
    elif [ "$saw_fail" -eq 1 ] && [ "$saw_skip" -eq 1 ]; then
        # FAIL+SKIP without PASS — still degraded, count as stable fail
        # since the scenario never succeeded.
        STABLE_FAIL=$((STABLE_FAIL + 1))
        STABLE_FAIL_DETAIL+=("$scen (mixed FAIL/SKIP)")
    else
        # No observations at all — shouldn't happen because we got here
        # from the extract, but guard anyway.
        :
    fi

    # Reset for next iteration.
    unset STATUSES
done < "$SCENARIOS_LIST"

# Per-run run-level pass/fail (from the exit code).
RUNS_GREEN=0
RUNS_RED=0
for ec in "${EXIT_CODES[@]}"; do
    if [ "$ec" = "0" ]; then
        RUNS_GREEN=$((RUNS_GREEN + 1))
    else
        RUNS_RED=$((RUNS_RED + 1))
    fi
done

# ----- report ------------------------------------------------------------
{
    printf '# Bulletproof overnight report\n\n'
    printf '- Run timestamp: %s\n' "$TIMESTAMP"
    printf '- N (iterations): %d\n' "$N"
    printf '- Scenarios observed (unique names): %d\n' "$SCENARIO_COUNT"
    printf '\n## Run-level\n\n'
    printf '- Runs green (exit=0): %d\n' "$RUNS_GREEN"
    printf '- Runs red (exit!=0): %d\n' "$RUNS_RED"
    printf '- Per-run exit codes: %s\n' "$(IFS=,; echo "${EXIT_CODES[*]}")"
    printf '\n## Aggregate scenario tallies\n\n'
    printf '- Stable PASS: %d\n' "$STABLE_PASS"
    printf '- Stable FAIL: %d\n' "$STABLE_FAIL"
    printf '- Stable SKIP (pending): %d\n' "$STABLE_SKIP"
    printf '- FLAKE: %d\n' "$FLAKE"
    printf '\n## Resource posture (E2)\n\n'
    printf '- Status: %s\n' "$POSTURE_STATUS"
    printf '- Detail: %s\n' "$POSTURE_DETAIL"

    if [ "$FLAKE" -gt 0 ]; then
        printf '\n## Flake detail\n\n'
        # Header row
        printf '| scenario |'
        for i in $(seq 1 "$N"); do printf ' r%d |' "$i"; done
        printf '\n|---|'
        for _ in $(seq 1 "$N"); do printf '---|'; done
        printf '\n'
        for entry in "${FLAKE_DETAIL[@]}"; do
            name="${entry%%|*}"
            seq_csv="${entry#*|}"
            printf '| %s |' "$name"
            IFS=',' read -r -a parts <<< "$seq_csv"
            for s in "${parts[@]}"; do
                # P/F/S/- one-letter glyph for compact rendering.
                case "$s" in
                    PASS) glyph="P" ;;
                    FAIL) glyph="F" ;;
                    SKIP) glyph="S" ;;
                    *)    glyph="-" ;;
                esac
                printf ' %s |' "$glyph"
            done
            printf '\n'
        done
    fi

    if [ "$STABLE_FAIL" -gt 0 ]; then
        printf '\n## Stable failures\n\n'
        for s in "${STABLE_FAIL_DETAIL[@]}"; do
            printf '- %s\n' "$s"
        done
    fi

    if [ "$STABLE_SKIP" -gt 0 ]; then
        printf '\n## Pending (stable SKIP)\n\n'
        for s in "${PENDING_DETAIL[@]}"; do
            printf '- %s\n' "$s"
        done
    fi

    printf '\n## Per-run summaries\n\n'
    for i in $(seq 1 "$N"); do
        SUMMARY="$LOGS_DIR/run-$i/summary.txt"
        [ -f "$SUMMARY" ] || continue
        # Extract just the "PASS=X FAIL=Y SKIP=Z" line.
        line=$(grep -E '^PASS=' "$SUMMARY" | head -1 || true)
        if [ -z "$line" ]; then
            line="(no SUMMARY line — see _logs/run-$i/test.log)"
        fi
        printf '- run %d (exit=%s): %s\n' \
            "$i" "${EXIT_CODES[$((i - 1))]}" "$line"
    done
} > "$REPORT"

printf '\nreport written: %s\n' "$REPORT"

# Echo a compact summary banner so the operator sees the totals without
# opening the file.
printf '\n========== OVERNIGHT SUMMARY ==========\n'
printf '  runs: %d green=%d red=%d\n' "$N" "$RUNS_GREEN" "$RUNS_RED"
printf '  scenarios: PASS=%d FAIL=%d SKIP=%d FLAKE=%d\n' \
    "$STABLE_PASS" "$STABLE_FAIL" "$STABLE_SKIP" "$FLAKE"
printf '  posture: %s\n' "$POSTURE_STATUS"

# Exit non-zero if any flake OR any stable fail OR posture failed.
# Stable skips are OK (they're known gaps, tracked separately).
if [ "$FLAKE" -gt 0 ] || [ "$STABLE_FAIL" -gt 0 ] || [ "$POSTURE_STATUS" = "fail" ]; then
    exit 1
fi
exit 0
