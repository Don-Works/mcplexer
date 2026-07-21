#!/usr/bin/env bash
# run.sh — orchestrates the multi-node mcplexer integration harness.
#  1. docker compose up --build -d (3 nodes + echo-llm)
#  2. wait for all healthchecks
#  3. run scenarios.sh
#  4. on EXIT — regardless of pass/fail — tear down (unless TEST_KEEP=1).
#
# Env knobs:
#   TEST_KEEP=1     keep containers + volumes on exit (for debugging)
#   TEST_LOGS_DIR   where to dump container logs on failure (default ./test/integration/_logs)
#   COMPOSE_FILE    override the compose file path
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT/test/integration/docker-compose.yml}"
SCENARIO_SCRIPT="$ROOT/test/integration/scenarios.sh"
LOGS_DIR="${TEST_LOGS_DIR:-$ROOT/test/integration/_logs}"

cleanup() {
    local exit_code=$?
    if [ "${TEST_KEEP:-0}" = "1" ]; then
        printf '\nTEST_KEEP=1, leaving containers up. Tear down with:\n'
        printf '  make test-integration-down\n'
        return $exit_code
    fi
    printf '\n--- tearing down docker harness ---\n'
    if [ "$exit_code" -ne 0 ]; then
        mkdir -p "$LOGS_DIR"
        for svc in node-a node-b node-c node-d node-e echo-llm; do
            docker compose -f "$COMPOSE_FILE" logs --no-color --timestamps "$svc" \
                > "$LOGS_DIR/$svc.log" 2>&1 || true
        done
        printf 'container logs saved to %s\n' "$LOGS_DIR"
    fi
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
    return $exit_code
}
trap cleanup EXIT

wait_healthy() {
    local svc="$1"
    local timeout="${2:-120}"
    local i=0
    while [ $i -lt $timeout ]; do
        local state
        state=$(docker inspect --format '{{ .State.Health.Status }}' \
            "mcplexer-test-$svc" 2>/dev/null || echo "missing")
        if [ "$state" = "healthy" ]; then
            return 0
        fi
        if [ "$state" = "unhealthy" ]; then
            printf '%s is unhealthy — dumping last logs:\n' "$svc" >&2
            docker compose -f "$COMPOSE_FILE" logs --tail=80 "$svc" >&2 || true
            return 1
        fi
        # "missing" / "starting" / "" are all transient — keep polling.
        # Docker desktop occasionally returns "No such container" on a
        # container that's still being created; we treat that as a soft
        # retry the same as "starting".
        sleep 2
        i=$((i + 2))
    done
    printf 'timed out waiting for %s (last state=%s)\n' "$svc" "$state" >&2
    docker compose -f "$COMPOSE_FILE" logs --tail=80 "$svc" >&2 || true
    return 1
}

# wait_listening pokes the HTTP endpoint until it answers. Docker
# reporting a container "healthy" only means the healthcheck command
# returned 0 once — under load the port can briefly stop accepting
# after that ack, especially on docker-desktop bring-ups where the
# proxy + bridge networking are still settling. This belt-and-braces
# check confirms scenario tooling can actually reach the listener.
wait_listening() {
    local url="$1"
    local timeout="${2:-30}"
    local i=0
    while [ $i -lt $timeout ]; do
        if curl -sf -o /dev/null --max-time 2 "$url/api/v1/health" 2>/dev/null; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    printf 'timed out waiting for %s/api/v1/health to respond\n' "$url" >&2
    return 1
}

main() {
    printf 'mcplexer integration harness\n'
    printf '  compose: %s\n' "$COMPOSE_FILE"

    printf '\n--- building + starting containers ---\n'
    # Retry compose up once on transient docker daemon errors.
    # Docker desktop on Apple silicon intermittently reports "No such
    # container: <hash>" when the bridge network is racing the volume
    # creation; a single retry has resolved it in every case we've seen.
    if ! docker compose -f "$COMPOSE_FILE" up --build -d 2>&1; then
        printf '  compose up failed — retrying once after 3s settle\n' >&2
        sleep 3
        docker compose -f "$COMPOSE_FILE" down -v --remove-orphans \
            >/dev/null 2>&1 || true
        docker compose -f "$COMPOSE_FILE" up --build -d
    fi

    printf '\n--- waiting for healthchecks ---\n'
    for svc in echo-llm node-a node-b node-c node-d node-e; do
        # echo-llm doesn't have the mcplexer-test-* container prefix; handle inline.
        if [ "$svc" = "echo-llm" ]; then
            local i=0
            while [ $i -lt 60 ]; do
                local state
                state=$(docker compose -f "$COMPOSE_FILE" ps --format json echo-llm \
                    | jq -r '.Health // .State' 2>/dev/null || echo "")
                if [ "$state" = "healthy" ] || [ "$state" = "running" ]; then
                    break
                fi
                sleep 1
                i=$((i + 1))
            done
            printf '  %s: %s\n' "$svc" "${state:-unknown}"
            continue
        fi
        wait_healthy "$svc" 180
        printf '  %s: healthy\n' "$svc"
    done

    # Post-healthy settle: containers report "healthy" the moment the
    # healthcheck succeeds once, but the listener can briefly stop
    # accepting under load on docker-desktop while the proxy/bridge
    # finalize. Poll each node's /api/v1/health from the host until
    # it answers before handing off to scenarios.sh.
    printf '\n--- confirming host-side reachability ---\n'
    wait_listening "http://localhost:23333" 30 && \
        printf '  node-a: reachable on :23333\n'
    wait_listening "http://localhost:23334" 30 && \
        printf '  node-b: reachable on :23334\n'
    wait_listening "http://localhost:23335" 30 && \
        printf '  node-c: reachable on :23335\n'
    wait_listening "http://localhost:13336" 30 && \
        printf '  node-d: reachable on :13336\n'
    wait_listening "http://localhost:13337" 30 && \
        printf '  node-e: reachable on :13337\n'

    printf '\n--- running scenarios ---\n'
    bash "$SCENARIO_SCRIPT"
}

main "$@"
