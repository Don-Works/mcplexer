#!/usr/bin/env bash
# scenario_worker_adapters.sh — Defines scenarios verifying the
# claude_cli + opencode_cli + grok_cli adapter env opt-ins and the sandbox
# protected-paths posture (CLAUDE.md "Workers — CLI
# providers are opt-in" + "DB lockdown" sections).
#
# Sourced by scenarios.sh. The scenarios it exports:
#   scenario_worker_adapters_claude_cli   — env gate + (optional) stub run
#   scenario_worker_adapters_opencode_cli — env gate + (optional) stub run
#   scenario_worker_adapters_grok_cli     — env gate + (optional) stub run
#   scenario_worker_adapters_sandbox      — protected-paths write block
#
# Behaviour:
#  - Workers with model_provider=claude_cli / opencode_cli / grok_cli pass admin
#    validation ONLY when the respective MCPLEXER_ALLOW_* env is "1" in
#    the daemon's environment (internal/workers/admin/validate.go).
#  - The integration docker-compose.yml does NOT set those envs (and the
#    brief tells us NOT to edit it). So the env-gate rejection IS the
#    primary assertion. The stub-binary-run sub-tests are SKIPPED with a
#    clear PENDING note when the env isn't set.
#
# Owner: D5 in epic 01KSK91Q4W8TNED9MAF0CTRVKC.

# adapter_create_worker_body composes the minimum-valid CreateInput for a
# Worker with the given model_provider. Mirrors the openai_compat shape
# in scenario_worker.sh::build_worker_body so the validator sees a
# well-formed body (everything except the gated provider).
#
# Usage: adapter_create_worker_body <name> <ws> <scope> <provider>
adapter_create_worker_body() {
    local name="$1"
    local ws="$2"
    local scope="$3"
    local provider="$4"
    jq -n \
        --arg name "$name" \
        --arg ws "$ws" \
        --arg scope "$scope" \
        --arg provider "$provider" \
        '{
            name: $name,
            description: "integration test — gated adapter",
            model_provider: $provider,
            model_id: "stub-model",
            secret_scope_id: $scope,
            prompt_template: "say ok",
            schedule_spec: "0 9 * * *",
            workspace_id: $ws,
            exec_mode: "autonomous",
            enabled: false
        }'
}

# adapter_create_returns_status posts the worker body and returns the
# HTTP status code. Used to assert the validator's pre-create gate.
#
# Usage: adapter_create_returns_status <node_url> <body>
adapter_create_returns_status() {
    local url="$1"
    local body="$2"
    local tmp status
    tmp=$(mktemp)
    status=$(curl -s -o "$tmp" -w '%{http_code}' \
        -X POST "$url/api/v1/workers" \
        -H "Authorization: Bearer $(token_for "$url")" \
        -H "Content-Type: application/json" \
        --data "$body")
    ADAPTER_LAST_BODY=$(cat "$tmp")
    rm -f "$tmp"
    echo "$status"
}

# adapter_env_set returns 0 if the named env is "1" inside the worker
# node's container (CONT_C is where the worker-run scenarios live).
#
# Usage: adapter_env_set <container> <env_name>
adapter_env_set() {
    local container="$1"
    local var="$2"
    local v
    v=$(docker exec "$container" sh -c "printenv $var 2>/dev/null" 2>/dev/null | tr -d '[:space:]')
    [ "$v" = "1" ]
}

# adapter_install_stub_binary writes a minimal stub binary into the
# container's PATH that emits a deterministic JSON envelope on stdout
# (matching claude_cli/opencode_cli's expected NDJSON shape). Returns 0
# on success, 1 if no writable bin dir. Cleanup is best-effort via the
# scenario's `trap` (the volume is wiped between full runs anyway).
#
# Usage: adapter_install_stub_binary <container> <binary_name>
adapter_install_stub_binary() {
    local container="$1"
    local name="$2"
    # The script prints a single-line JSON envelope to stdout. The
    # claude_cli adapter parses `{type:"result", result:"<text>"}`;
    # opencode_cli reads NDJSON with `text` or `tokens`. A unified
    # envelope that satisfies both is close enough for an integration
    # stub — and the runner will surface a parse error otherwise so
    # the assertion still works (it just lands as "non-zero exit"
    # rather than "success").
    docker exec "$container" sh -c "cat >/usr/local/bin/$name <<'EOF'
#!/bin/sh
echo '{\"type\":\"result\",\"result\":\"ok-from-stub-$name\",\"text\":\"ok\"}'
exit 0
EOF
chmod +x /usr/local/bin/$name" 2>/dev/null
}

# ----- D5.1 — claude_cli env-gate ----------------------------------------

scenario_worker_adapters_claude_cli() {
    step 19 "claude_cli adapter requires MCPLEXER_ALLOW_CLAUDE_CLI=1"
    local ws="${WS_CHARLIE:-}"
    local scope="${SCOPE_CHARLIE:-}"
    if [ -z "$ws" ] || [ -z "$scope" ]; then
        fail "19 claude_cli prereqs" "ws=$ws scope=$scope (provision step)"
        return
    fi

    # The validator runs inside the daemon container; the env it reads
    # is the container env, not the harness env. Probe what node-c sees.
    local env_on="false"
    adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_CLAUDE_CLI" && env_on="true"

    local body
    body=$(adapter_create_worker_body \
        "claude-cli-fixture-$RANDOM" "$ws" "$scope" "claude_cli")
    local status
    status=$(adapter_create_returns_status "$NODE_C" "$body")

    if [ "$env_on" = "false" ]; then
        # Expected reject: validator returns 400 + clear policy reason.
        if [ "$status" = "400" ]; then
            if echo "$ADAPTER_LAST_BODY" | grep -qE 'MCPLEXER_ALLOW_CLAUDE_CLI|opt-in|network-host'; then
                pass "19.1 env-gate refused claude_cli create (400 + policy reason)"
            else
                pass "19.1 env-gate refused claude_cli create (400) — message: $(echo "$ADAPTER_LAST_BODY" | head -c 160)"
            fi
        else
            fail "19.1 expected 400 (env gate), got $status" \
                "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        fi
        skip "19.2 claude_cli runtime stub run" \
            "PENDING — MCPLEXER_ALLOW_CLAUDE_CLI is not set on $CONT_C. \
Set it externally (compose env or docker exec -e ...) AND re-run to exercise the run path."
        return
    fi

    # Env IS on — accept-path: worker creates AND a run terminates.
    if [ "$status" != "200" ] && [ "$status" != "201" ]; then
        fail "19.1 claude_cli create with env=1 should succeed; got $status" \
            "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        return
    fi
    local worker_id
    worker_id=$(echo "$ADAPTER_LAST_BODY" | jq -r '.id // .ID // empty')
    if [ -z "$worker_id" ]; then
        fail "19.1 claude_cli worker create returned no id" "$ADAPTER_LAST_BODY"
        return
    fi
    pass "19.1 claude_cli worker created (env opt-in honoured) id=$worker_id"

    # Install the stub binary, fire run-now, observe outcome.
    if ! adapter_install_stub_binary "$CONT_C" "claude"; then
        skip "19.2 claude_cli stub run" "could not write stub into $CONT_C"
        return
    fi
    local rresp run_id
    rresp=$(api POST "$NODE_C/api/v1/workers/$worker_id/run-now" '{}')
    run_id=$(echo "$rresp" | jq -r '.run_id // .id // empty')
    if [ -z "$run_id" ]; then
        fail "19.2 run-now returned no run_id" "$rresp"
        return
    fi
    local final
    final=$(poll_worker_run "$NODE_C" "$worker_id" "$run_id")
    case "$final" in
        success|succeeded|completed)
            pass "19.2 claude_cli run terminated cleanly via stub (status=$final)" ;;
        *)
            fail "19.2 claude_cli run did not succeed via stub" "final status=$final"
            ;;
    esac
}

# ----- D5.2 — opencode_cli env-gate -------------------------------------

scenario_worker_adapters_opencode_cli() {
    step 20 "opencode_cli adapter requires MCPLEXER_ALLOW_OPENCODE_CLI=1"
    local ws="${WS_CHARLIE:-}"
    local scope="${SCOPE_CHARLIE:-}"
    if [ -z "$ws" ] || [ -z "$scope" ]; then
        fail "20 opencode_cli prereqs" "ws=$ws scope=$scope"
        return
    fi

    local env_on="false"
    adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_OPENCODE_CLI" && env_on="true"

    local body
    body=$(adapter_create_worker_body \
        "opencode-cli-fixture-$RANDOM" "$ws" "$scope" "opencode_cli")
    local status
    status=$(adapter_create_returns_status "$NODE_C" "$body")

    if [ "$env_on" = "false" ]; then
        if [ "$status" = "400" ]; then
            if echo "$ADAPTER_LAST_BODY" | grep -qE 'MCPLEXER_ALLOW_OPENCODE_CLI|opt-in|network-host'; then
                pass "20.1 env-gate refused opencode_cli create (400 + policy reason)"
            else
                pass "20.1 env-gate refused opencode_cli create (400) — message: $(echo "$ADAPTER_LAST_BODY" | head -c 160)"
            fi
        else
            fail "20.1 expected 400 (env gate), got $status" \
                "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        fi
        skip "20.2 opencode_cli runtime stub run" \
            "PENDING — MCPLEXER_ALLOW_OPENCODE_CLI is not set on $CONT_C."
        return
    fi

    if [ "$status" != "200" ] && [ "$status" != "201" ]; then
        fail "20.1 opencode_cli create with env=1 should succeed; got $status" \
            "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        return
    fi
    local worker_id
    worker_id=$(echo "$ADAPTER_LAST_BODY" | jq -r '.id // .ID // empty')
    if [ -z "$worker_id" ]; then
        fail "20.1 opencode_cli worker create returned no id" "$ADAPTER_LAST_BODY"
        return
    fi
    pass "20.1 opencode_cli worker created (env opt-in honoured) id=$worker_id"

    if ! adapter_install_stub_binary "$CONT_C" "opencode"; then
        skip "20.2 opencode_cli stub run" "could not write stub into $CONT_C"
        return
    fi
    local rresp run_id
    rresp=$(api POST "$NODE_C/api/v1/workers/$worker_id/run-now" '{}')
    run_id=$(echo "$rresp" | jq -r '.run_id // .id // empty')
    if [ -z "$run_id" ]; then
        fail "20.2 run-now returned no run_id" "$rresp"
        return
    fi
    local final
    final=$(poll_worker_run "$NODE_C" "$worker_id" "$run_id")
    case "$final" in
        success|succeeded|completed)
            pass "20.2 opencode_cli run terminated cleanly via stub (status=$final)" ;;
        *)
            fail "20.2 opencode_cli run did not succeed via stub" "final status=$final"
            ;;
    esac
}

# ----- D5.3 — grok_cli env-gate -----------------------------------------

scenario_worker_adapters_grok_cli() {
    step 21 "grok_cli adapter requires MCPLEXER_ALLOW_GROK_CLI=1"
    local ws="${WS_CHARLIE:-}"
    local scope="${SCOPE_CHARLIE:-}"
    if [ -z "$ws" ] || [ -z "$scope" ]; then
        fail "21 grok_cli prereqs" "ws=$ws scope=$scope"
        return
    fi

    local env_on="false"
    adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_GROK_CLI" && env_on="true"

    local body
    body=$(adapter_create_worker_body \
        "grok-cli-fixture-$RANDOM" "$ws" "$scope" "grok_cli")
    local status
    status=$(adapter_create_returns_status "$NODE_C" "$body")

    if [ "$env_on" = "false" ]; then
        if [ "$status" = "400" ]; then
            if echo "$ADAPTER_LAST_BODY" | grep -qE 'MCPLEXER_ALLOW_GROK_CLI|opt-in|network-host'; then
                pass "21.1 env-gate refused grok_cli create (400 + policy reason)"
            else
                pass "21.1 env-gate refused grok_cli create (400) — message: $(echo "$ADAPTER_LAST_BODY" | head -c 160)"
            fi
        else
            fail "21.1 expected 400 (env gate), got $status" \
                "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        fi
        skip "21.2 grok_cli runtime stub run" \
            "PENDING — MCPLEXER_ALLOW_GROK_CLI is not set on $CONT_C."
        return
    fi

    if [ "$status" != "200" ] && [ "$status" != "201" ]; then
        fail "21.1 grok_cli create with env=1 should succeed; got $status" \
            "body head: $(echo "$ADAPTER_LAST_BODY" | head -c 200)"
        return
    fi
    local worker_id
    worker_id=$(echo "$ADAPTER_LAST_BODY" | jq -r '.id // .ID // empty')
    if [ -z "$worker_id" ]; then
        fail "21.1 grok_cli worker create returned no id" "$ADAPTER_LAST_BODY"
        return
    fi
    pass "21.1 grok_cli worker created (env opt-in honoured) id=$worker_id"

    if ! adapter_install_stub_binary "$CONT_C" "grok"; then
        skip "21.2 grok_cli stub run" "could not write stub into $CONT_C"
        return
    fi
    local rresp run_id
    rresp=$(api POST "$NODE_C/api/v1/workers/$worker_id/run-now" '{}')
    run_id=$(echo "$rresp" | jq -r '.run_id // .id // empty')
    if [ -z "$run_id" ]; then
        fail "21.2 run-now returned no run_id" "$rresp"
        return
    fi
    local final
    final=$(poll_worker_run "$NODE_C" "$worker_id" "$run_id")
    case "$final" in
        success|succeeded|completed)
            pass "21.2 grok_cli run terminated cleanly via stub (status=$final)" ;;
        *)
            fail "21.2 grok_cli run did not succeed via stub" "final status=$final"
            ;;
    esac
}

# ----- D5.4 — sandbox protected-paths write block -----------------------

scenario_worker_adapters_sandbox() {
    step 22 "sandbox profile blocks writes to ~/.claude/ + ~/.mcplexer/"
    # This is the runtime check that mirrors the gateway-side cmdguard
    # (scenario_cmdguard_db_lockdown). The sandbox profile lives in
    # internal/sandbox/wrap_*.go and is activated when a worker dispatches
    # to claude_cli / opencode_cli / grok_cli / mimo_cli. Without the
    # MCPLEXER_ALLOW_*_CLI env
    # set we can't drive a real run, so this scenario lands as PENDING
    # in that case — the env-gate scenarios (19 / 20 / 21) already cover the
    # static refusal posture.
    if ! adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_CLAUDE_CLI" \
	   && ! adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_OPENCODE_CLI" \
	   && ! adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_GROK_CLI" \
	   && ! adapter_env_set "$CONT_C" "MCPLEXER_ALLOW_MIMO_CLI"; then
        skip "22 sandbox protected-paths runtime check" \
            "PENDING — none of MCPLEXER_ALLOW_CLAUDE_CLI, MCPLEXER_ALLOW_OPENCODE_CLI, \
MCPLEXER_ALLOW_GROK_CLI, or MCPLEXER_ALLOW_MIMO_CLI \
is set on $CONT_C. Sandbox runtime can't be exercised without a runnable adapter. \
Set one of these envs, install a stub binary that attempts the writes \
(touch ~/.claude/x ; touch ~/.mcplexer/y), then run-now. \
Expected: the stub's exit code surfaces 'permission denied' in stderr; \
the worker run lands status=failed with the denied write in the run record."
        return
    fi

    # Best-effort full path (when an operator has set the env). Install a
    # stub binary that attempts the protected writes and emits an envelope
    # carrying the resulting exit/error. The sandbox should block both,
    # so the run lands non-zero AND the run record / audit row carries a
    # "permission denied" marker.
    local probe_name="sandbox-probe"
    docker exec "$CONT_C" sh -c "cat >/usr/local/bin/$probe_name <<'EOF'
#!/bin/sh
err=\"\"
touch \"\$HOME/.claude/sandbox-probe-marker\" 2>/dev/null || err=\"\${err} home_claude_denied\"
touch \"\$HOME/.mcplexer/sandbox-probe-marker\" 2>/dev/null || err=\"\${err} home_mcplexer_denied\"
echo '{\"type\":\"result\",\"result\":\"probe:'\"\$err\"'\",\"text\":\"probe:'\"\$err\"'\"}'
exit 0
EOF
chmod +x /usr/local/bin/$probe_name" 2>/dev/null

    skip "22 sandbox protected-paths runtime check" \
        "PENDING — stub installed at /usr/local/bin/$probe_name in $CONT_C. \
To drive the real sandboxed path, restart the daemon with one of \
MCPLEXER_TEST_CLAUDE_CLI_BIN, MCPLEXER_TEST_OPENCODE_CLI_BIN, or \
MCPLEXER_TEST_GROK_CLI_BIN pointing at the probe binary, plus the matching \
MCPLEXER_ALLOW_*_CLI=1 opt-in."
}
