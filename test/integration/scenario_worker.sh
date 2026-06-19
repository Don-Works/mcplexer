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
    jq -n \
        --arg name "$name" \
        --arg ws "$ws" \
        --arg scope "$scope" \
        --arg endpoint "http://echo-llm:8080" \
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
            enabled: false
        }'
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
    create_body=$(build_worker_body "echo-worker-$$" "$ws_id" "$scope_id")
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

    local rresp
    rresp=$(api POST "$NODE_C/api/v1/workers/$worker_id/run-now" '{}')
    local run_id
    run_id=$(echo "$rresp" | jq -r '.run_id // .id // empty')
    if [ -z "$run_id" ]; then
        fail "node-c worker run-now returned no run_id" "resp=$rresp"
        return
    fi
    pass "node-c worker run-now accepted run_id=$run_id"

    local status
    status=$(poll_worker_run "$NODE_C" "$worker_id" "$run_id")
    case "$status" in
        success|succeeded|completed)
            pass "node-c worker run terminated cleanly status=$status" ;;
        *)
            fail "node-c worker run did not succeed" "final status=$status" ;;
    esac
}
