#!/usr/bin/env bash
# run_logwatch.sh — focused runner for the log-injection scenarios (42.x).
#
# The full harness brings up five nodes and works through ~40 scenario files.
# The monitoring plumbing needs a much tighter loop than that: it only needs
# node-a and the log host, and it is the layer every detector assertion stands
# on, so it should be cheap to re-run on its own.
#
#   ./run_logwatch.sh              # up, run 42.x, tear down
#   TEST_KEEP=1 ./run_logwatch.sh  # leave the containers up for poking
#
# Env knobs mirror run.sh: TEST_KEEP, TEST_LOGS_DIR, COMPOSE_FILE.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HERE="$ROOT/test/integration"
COMPOSE_FILE="${COMPOSE_FILE:-$HERE/docker-compose.yml}"
LOGS_DIR="${TEST_LOGS_DIR:-$HERE/_logs}"

NODE_A="${NODE_A:-http://localhost:23333}"
NODE_B="${NODE_B:-http://localhost:23334}"
NODE_C="${NODE_C:-http://localhost:23335}"
CONT_A="${CONT_A:-mcplexer-test-node-a}"
CONT_B="${CONT_B:-mcplexer-test-node-b}"
CONT_C="${CONT_C:-mcplexer-test-node-c}"
export NODE_A NODE_B NODE_C CONT_A CONT_B CONT_C

cleanup() {
    local code=$?
    if [ "${TEST_KEEP:-0}" = "1" ]; then
        printf '\nTEST_KEEP=1 — containers left up. Tear down with:\n'
        printf '  docker compose -f %s down -v\n' "$COMPOSE_FILE"
        return $code
    fi
    printf '\n--- tearing down ---\n'
    if [ "$code" -ne 0 ]; then
        mkdir -p "$LOGS_DIR"
        for svc in node-a log-host; do
            docker compose -f "$COMPOSE_FILE" logs --no-color "$svc" \
                > "$LOGS_DIR/logwatch-$svc.log" 2>&1 || true
        done
        printf 'container logs saved to %s\n' "$LOGS_DIR"
    fi
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
    return $code
}
trap cleanup EXIT

printf 'logwatch injection harness\n  compose: %s\n' "$COMPOSE_FILE"

printf '\n--- fixture self-test (no containers needed) ---\n'
"$HERE/_fixtures/logwatch/verify_fixtures.sh" >/dev/null \
    || { printf 'fixture self-test FAILED — run _fixtures/logwatch/verify_fixtures.sh\n' >&2; exit 1; }
printf '  fixtures verified\n'

printf '\n--- building + starting log-host and node-a ---\n'
docker compose -f "$COMPOSE_FILE" up --build -d log-host node-a

printf '\n--- waiting for health ---\n'
i=0
while [ $i -lt 180 ]; do
    state=$(docker inspect --format '{{ .State.Health.Status }}' \
        mcplexer-test-node-a 2>/dev/null || echo missing)
    [ "$state" = "healthy" ] && break
    if [ "$state" = "unhealthy" ]; then
        docker compose -f "$COMPOSE_FILE" logs --tail=80 node-a >&2
        exit 1
    fi
    sleep 2; i=$((i + 2))
done
[ "$state" = "healthy" ] || { printf 'node-a never became healthy (%s)\n' "$state" >&2; exit 1; }
printf '  node-a: healthy\n'
docker inspect --format '  log-host: {{ .State.Status }}' mcplexer-test-log-host

# lib.sh's helpers are shared with the full suite; sourcing them here keeps
# one definition of `api`, `assert_jq` and the counters.
# shellcheck source=lib.sh
. "$HERE/lib.sh"

# ensure_tokens insists on node-a, node-b AND node-c, because the full suite
# needs all three. This runner deliberately starts only node-a and the log
# host, so it bootstraps that ONE token itself rather than pulling four extra
# nodes into a focused loop. TOK_B/TOK_C stay empty and token_for returns ""
# for them, which is correct — nothing here addresses those nodes.
curl -sf -o /dev/null "$NODE_A/api/v1/health" 2>/dev/null || true
TOK_A=""; TOK_B=""; TOK_C=""
for _ in 1 2 3 4 5; do
    TOK_A="$(fetch_token "$CONT_A")"
    [ -n "$TOK_A" ] && break
    sleep 1
done
[ -n "$TOK_A" ] || { printf 'could not read node-a api key\n' >&2; exit 1; }

# shellcheck source=scenario_logwatch_ingest.sh
. "$HERE/scenario_logwatch_ingest.sh"

printf '\n--- running 42.x ---\n'
scenario_logwatch_ingest_setup
scenario_logwatch_ingest_pull_path
scenario_logwatch_ingest_backdating
scenario_logwatch_ingest_incremental
scenario_logwatch_ingest_error_alerting
scenario_logwatch_ingest_shim_fidelity
scenario_logwatch_ingest_refusal_shapes
scenario_logwatch_ingest_cursor_precision
scenario_logwatch_ingest_learner_gap

printf '\n=== logwatch injection: %d passed, %d failed, %d skipped ===\n' \
    "$PASS" "$FAIL" "$SKIP"
for r in "${RESULTS[@]}"; do printf '  %s\n' "$r"; done
[ "$FAIL" -eq 0 ]
