#!/usr/bin/env bash
# scenario_worker.sh — Step 7 worker-run scenario. Sourced by scenarios.sh.
# Relies on the helpers in lib.sh and the WS_CHARLIE / SCOPE_CHARLIE
# variables populated by scenario_provision.

# build_worker_body composes the minimum-valid CreateInput for an
# openai_compat worker pointed at the in-cluster echo-llm stub.
build_worker_body() {
    local name="$1"
    local ws="$2"
    local scope="$3"
    local enabled="${4:-false}"
    jq -n \
        --arg name "$name" \
        --arg ws "$ws" \
        --arg scope "$scope" \
        --arg endpoint "http://echo-llm:8080" \
        --argjson enabled "$enabled" \
        '{
            name: $name,
            description: "integration test worker",
            model_provider: "openai_compat",
            model_id: "echo",
            model_endpoint_url: $endpoint,
            secret_scope_id: $scope,
            prompt_template: "say ok",
            schedule_spec: "0 9 * * *",
            workspace_id: $ws,
            exec_mode: "autonomous",
            enabled: $enabled
        }'
}

# dispatch_worker_run posts the detached run-now request, verifies its
# acknowledgement, then polls until the new run row is visible. Stdout is
# only the run id so callers can safely use command substitution.
dispatch_worker_run() {
    local node="$1"
    local worker_id="$2"
    local response
    response=$(api POST "$node/api/v1/workers/$worker_id/run-now" '{}') || return 1
    if ! echo "$response" | jq -e \
        --arg wid "$worker_id" \
        '.worker_id == $wid and .status == "dispatched"' >/dev/null 2>&1
    then
        echo "unexpected run-now acknowledgement: $response" >&2
        return 1
    fi

    local run_id=""
    for _ in $(seq 1 30); do
        local runs
        runs=$(api GET "$node/api/v1/workers/$worker_id/runs?limit=5") || return 1
        run_id=$(echo "$runs" | jq -r '.[0].id // empty')
        if [ -n "$run_id" ]; then
            echo "$run_id"
            return 0
        fi
        sleep 1
    done
    echo "detached run did not materialise for worker $worker_id" >&2
    return 1
}

# poll_worker_run waits up to ~30s for the named run to reach a terminal
# status. Echoes the final status to stdout.
poll_worker_run() {
    local node="$1"
    local worker_id="$2"
    local run_id="$3"
    local status="unknown"
    for _ in $(seq 1 30); do
        local runs
        runs=$(api GET "$node/api/v1/workers/$worker_id/runs?limit=5")
        status=$(echo "$runs" \
            | jq -r ".[] | select(.id == \"$run_id\") | .status // \"\"" \
            | head -1)
        case "$status" in
            success|succeeded|completed|failed|error) break ;;
        esac
        sleep 1
    done
    echo "$status"
}

# seed_worker_secret puts an api_key on the auth scope. The echo-llm stub
# doesn't validate the value — the runner just requires a present secret.
seed_worker_secret() {
    local node="$1"
    local scope_id="$2"
    local body
    body=$(jq -n '{key:"api_key",value:"sk-echo-stub"}')
    api_status PUT "$node/api/v1/auth-scopes/$scope_id/secrets" "$body"
}

scenario_worker_run() {
    step 7 "create + run-now an openai_compat worker on node-c"
    local ws_id="${WS_CHARLIE:-}"
    local scope_id="${SCOPE_CHARLIE:-}"
    if [ -z "$ws_id" ] || [ -z "$scope_id" ]; then
        fail "node-c worker prereqs missing" "ws=$ws_id scope=$scope_id"
        return
    fi

    local seed_status
    seed_status=$(seed_worker_secret "$NODE_C" "$scope_id")
    if [ "$seed_status" = "200" ] || [ "$seed_status" = "204" ]; then
        pass "node-c seeded api_key secret on $scope_id"
    else
        fail "node-c could not seed api_key secret" "status=$seed_status"
        return
    fi

    local create_body
    # run-now rejects paused workers; keep this fixture enabled.
    create_body=$(build_worker_body "echo-worker-$$" "$ws_id" "$scope_id" true)
    local cresp
    if ! cresp=$(api POST "$NODE_C/api/v1/workers" "$create_body"); then
        fail "node-c worker create call" "see daemon logs"
        return
    fi
    local worker_id
    worker_id=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$worker_id" ]; then
        fail "node-c worker create" "resp=$cresp"
        return
    fi
    pass "node-c worker created id=$worker_id"

    local run_id
    if ! run_id=$(dispatch_worker_run "$NODE_C" "$worker_id"); then
        fail "node-c worker run-now dispatch" "detached run did not materialise"
        return
    fi
    pass "node-c worker run-now dispatched; run materialised id=$run_id"

    local status
    status=$(poll_worker_run "$NODE_C" "$worker_id" "$run_id")
    case "$status" in
        success|succeeded|completed)
            pass "node-c worker run terminated cleanly status=$status" ;;
        *)
            fail "node-c worker run did not succeed" "final status=$status" ;;
    esac
}
