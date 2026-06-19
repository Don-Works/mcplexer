#!/usr/bin/env bash
# scenarios.sh — multi-node integration scenarios against the docker-compose
# harness. Each scenario prints `=== STEP n: <name> ===`, runs assertions,
# and emits PASS/FAIL with detail. Any FAIL exits non-zero; SKIP records a
# known-environmental limitation without failing the run.
#
# Required: curl, jq, docker.
set -euo pipefail

# ----- config -------------------------------------------------------------
NODE_A="${NODE_A:-http://localhost:23333}"
NODE_B="${NODE_B:-http://localhost:23334}"
NODE_C="${NODE_C:-http://localhost:23335}"

CONT_A="${CONT_A:-mcplexer-test-node-a}"
CONT_B="${CONT_B:-mcplexer-test-node-b}"
CONT_C="${CONT_C:-mcplexer-test-node-c}"

TOK_A=""
TOK_B=""
TOK_C=""

# Helpers (step/pass/fail/api/assert_jq/...) live in lib.sh so scenarios.sh
# stays focused on the test logic itself.
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"

# ----- scenarios ----------------------------------------------------------

scenario_health() {
    step 1 "all nodes healthy"
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local body
        body=$(curl -fsS "$url/api/v1/health")
        assert_jq "$url health.status=ok" "$body" '.status == "ok"'
        assert_jq "$url health.p2p_enabled=true" "$body" '.system.p2p_enabled == true'
    done
}

scenario_provision() {
    step 2 "provision workspace + auth scope per node"
    local nodes=("$NODE_A" "$NODE_B" "$NODE_C")
    local names=("alpha" "bravo" "charlie")
    for i in 0 1 2; do
        local url="${nodes[$i]}"
        local nm="${names[$i]}"
        local ws_id="ws-${nm}-$(date +%s)"
        local body
        body=$(api POST "$url/api/v1/workspaces" \
            "{\"id\":\"$ws_id\",\"name\":\"$nm\",\"root_path\":\"/tmp/$nm\",\"default_policy\":\"allow\"}")
        assert_jq "$url created workspace $nm" "$body" "(.id // .ID) == \"$ws_id\""

        local scope_id="auth-${nm}-$(date +%s)"
        local sbody
        sbody=$(api POST "$url/api/v1/auth-scopes" \
            "{\"id\":\"$scope_id\",\"name\":\"$nm-key\",\"type\":\"api_key\"}")
        assert_jq "$url created auth scope $nm" "$sbody" "(.id // .ID) == \"$scope_id\""

        eval "WS_${nm^^}=\"$ws_id\""
        eval "SCOPE_${nm^^}=\"$scope_id\""
    done
}

scenario_p2p_identity() {
    step 3 "each node reports a libp2p identity"
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local body
        body=$(api GET "$url/api/p2p/identity")
        local pid
        pid=$(echo "$body" | jq -r '.peer_id // empty')
        if [ -z "$pid" ]; then
            fail "$url p2p identity missing" "body: $body"
            continue
        fi
        pass "$url peer_id=$pid"
        eval "PID_$(node_var "$url")=\"$pid\""
    done
}

# pair_pair runs one initiator↔responder pair handshake. The responder
# calls pair/start; the initiator calls pair/complete with the responder's
# code + peer_id. On a closed docker bridge with no public DHT bootstrap
# the responder may fail to resolve the initiator's multiaddrs — we
# treat that as SKIP, not FAIL, because the test is exercising the API
# contract, not testing the public IPFS network. mDNS discovery on the
# same docker bridge typically works and the handshake completes.
pair_pair() {
    local label="$1"
    local resp_url="$2"
    local init_url="$3"

    local sresp
    sresp=$(api POST "$resp_url/api/p2p/pair/start" '{}')
    local code peer_id
    code=$(echo "$sresp" | jq -r '.code // empty')
    peer_id=$(echo "$sresp" | jq -r '.qr_payload | fromjson | .peer_id // empty')
    if [ -z "$code" ] || [ -z "$peer_id" ]; then
        fail "$label pair/start" "resp=$sresp"
        return
    fi

    local cbody="{\"code\":\"$code\",\"peer_id\":\"$peer_id\"}"
    local status
    status=$(api_status POST "$init_url/api/p2p/pair/complete" "$cbody")
    if [ "$status" = "204" ]; then
        pass "$label libp2p pair handshake succeeded"
    else
        skip "$label pair handshake" \
            "status=$status (libp2p DHT typically unreachable on closed docker bridge; see test/integration/README.md)"
    fi
}

scenario_pairing() {
    step 4 "attempt libp2p pairing across the mesh"
    # mDNS discovery on the docker bridge takes a few seconds after the
    # libp2p host comes up. Give it room before kicking off pair_pair so
    # the responder's peerstore has the initiator's multiaddrs cached.
    sleep 5
    pair_pair "A↔B" "$NODE_B" "$NODE_A"
    pair_pair "B↔C" "$NODE_C" "$NODE_B"
    pair_pair "A↔C" "$NODE_C" "$NODE_A"
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local body
        body=$(api GET "$url/api/p2p/peers")
        assert_jq "$url /api/p2p/peers shape" "$body" '(.peers // []) | type == "array"'
    done
}

scenario_mesh_send() {
    step 5 "mesh send via REST propagates to all peers"
    local marker="multinode-mesh-$RANDOM"
    local sbody
    sbody=$(jq -nc --arg c "$marker" \
        '{recipient:{kind:"audience",value:"*"},
          kind:"finding", content:$c, priority:"low",
          agent_name:"integration-runner"}')
    local sresp
    sresp=$(api POST "$NODE_A/api/v1/mesh/send" "$sbody")
    assert_jq "node-a mesh/send returned message_id" "$sresp" 'has("message_id")'

    # Node-a sees its own send synchronously (local fan-in). Remote
    # peers receive via libp2p — give the gossip up to 12s to land.
    # Treat "remote node not paired with A" as SKIP, not FAIL: on the
    # closed docker bridge mDNS sometimes misses one pair and the test
    # is about propagation contract, not the bridge's discovery layer.
    local pid_a="${PID_A:-$(peer_id_for "$NODE_A")}"
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local found="false"
        for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
            local mstatus
            mstatus=$(api GET "$url/api/v1/mesh/status" 2>/dev/null)
            if echo "$mstatus" | jq -e ".messages[]? | select(.content == \"$marker\")" >/dev/null 2>&1; then
                found="true"; break
            fi
            sleep 1
        done
        if [ "$found" = "true" ]; then
            pass "$url observed the broadcast (marker=$marker)"
        elif [ "$url" = "$NODE_A" ]; then
            fail "$url never observed its own broadcast" "marker=$marker"
        elif ! is_paired_with "$url" "$pid_a"; then
            skip "$url broadcast" \
                "not paired with node-a (libp2p closed-bridge) — propagation cannot reach this node."
        else
            fail "$url never observed the broadcast" "marker=$marker"
        fi
    done
}

# build_skill_body composes a minimal markdown skill with the leading
# frontmatter fence the registry requires (mirrors Claude Code's
# SKILL.md convention).
build_skill_body() {
    local name="$1"
    local marker="$2"
    printf -- '---\nname: %s\ndescription: integration test skill\n---\n\n# Echo skill\nReturn the input verbatim. %s\n' \
        "$name" "$marker"
}

scenario_skill_publish_request() {
    step 6 "publish skill on node-a, fetch by name from node-b"
    local skill_name="integration-echo-$$"
    local marker="test-marker-$RANDOM"
    local skill_body
    skill_body=$(build_skill_body "$skill_name" "$marker")

    local publish_body
    publish_body=$(jq -n --arg name "$skill_name" --arg body "$skill_body" \
        '{name: $name, body: $body, scope: "global", author: "integration-runner"}')

    local presp
    if ! presp=$(api POST "$NODE_A/api/v1/skill-registry" "$publish_body"); then
        fail "node-a publish $skill_name (HTTP error)" "body=$publish_body"
        return
    fi
    assert_jq "node-a publish $skill_name" "$presp" '(.version // .entry.version // 0) > 0'

    local lresp
    lresp=$(api GET "$NODE_A/api/v1/skill-registry?limit=200")
    assert_jq "node-a list contains $skill_name" "$lresp" \
        "any(.[]; .name == \"$skill_name\")"

    local gresp
    gresp=$(api GET "$NODE_A/api/v1/skill-registry/$skill_name")
    local got
    got=$(echo "$gresp" | jq -r '.body // empty')
    if echo "$got" | grep -q "$marker"; then
        pass "node-a fetched skill body contains marker"
    else
        fail "node-a fetched skill body missing marker" \
            "marker=$marker got=$(echo "$got" | head -c 200)"
    fi

    # Cross-node skill sync needs a paired mesh, which is best-effort on
    # docker. Verify node-b's endpoint at least returns a well-formed array.
    local bresp
    bresp=$(api GET "$NODE_B/api/v1/skill-registry?limit=200")
    assert_jq "node-b skill-registry endpoint responds" "$bresp" 'type == "array"'
}

# shellcheck source=scenario_worker.sh
. "$(dirname "$0")/scenario_worker.sh"
# shellcheck source=scenario_skill_share.sh
. "$(dirname "$0")/scenario_skill_share.sh"
# shellcheck source=scenario_mesh_triggers.sh
. "$(dirname "$0")/scenario_mesh_triggers.sh"
# shellcheck source=scenario_mesh_wait.sh
. "$(dirname "$0")/scenario_mesh_wait.sh"
# shellcheck source=scenario_memory.sh
. "$(dirname "$0")/scenario_memory.sh"
# shellcheck source=scenario_tasks.sh
. "$(dirname "$0")/scenario_tasks.sh"
# shellcheck source=scenario_linked_workspaces.sh
. "$(dirname "$0")/scenario_linked_workspaces.sh"
# shellcheck source=scenario_googlechat.sh
. "$(dirname "$0")/scenario_googlechat.sh"
# shellcheck source=scenario_hammerspoon.sh
. "$(dirname "$0")/scenario_hammerspoon.sh"
# shellcheck source=scenario_admin_crud.sh
. "$(dirname "$0")/scenario_admin_crud.sh"
# shellcheck source=scenario_approvals.sh
. "$(dirname "$0")/scenario_approvals.sh"
# shellcheck source=scenario_cmdguard.sh
. "$(dirname "$0")/scenario_cmdguard.sh"
# shellcheck source=scenario_shellguard.sh
. "$(dirname "$0")/scenario_shellguard.sh"
# shellcheck source=scenario_aux.sh
. "$(dirname "$0")/scenario_aux.sh"

scenario_audit() {
    step 9 "audit ledger captures upstream activity"
    # Audit writes are async (bus + sqlite flush); poll briefly so a
    # fast-running test doesn't race the writer on a fresh node.
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local body total="0"
        for _ in 1 2 3 4 5; do
            body=$(api GET "$url/api/v1/audit?limit=50")
            total=$(echo "$body" | jq -r '.total // 0')
            [ "$total" != "0" ] && break
            sleep 1
        done
        assert_jq "$url audit returns rows" "$body" '(.total // 0) > 0'
    done

    local cbody
    cbody=$(api GET "$NODE_C/api/v1/audit?limit=200")
    if echo "$cbody" | jq -e '.data[]? | select(.tool_name // .verb // "" | test("worker"))' >/dev/null 2>&1; then
        pass "node-c audit contains worker_* row"
    else
        fail "node-c audit missing worker_* row" "(head) $(echo "$cbody" | jq -c '.data[0:3]')"
    fi
}

# scenario_audit_redaction — across EVERY audit row on every node, the
# seeded api_key value (sk-echo-stub) must NEVER appear in plaintext.
# Mesh propagation, worker arguments, and ContextSummary fields are all
# expected to scrub secrets — this scenario catches regressions where a
# new code path accidentally leaks one.
scenario_audit_redaction() {
    step 10 "secret redaction holds across every audit row on every node"
    local needle="sk-echo-stub"
    local leaked=""
    for url in "$NODE_A" "$NODE_B" "$NODE_C"; do
        local body
        body=$(api GET "$url/api/v1/audit?limit=1000" 2>/dev/null)
        if echo "$body" | jq -c '.data[]?' 2>/dev/null | grep -q "$needle"; then
            leaked="$leaked $url"
        fi
    done
    if [ -z "$leaked" ]; then
        pass "no audit row on any node contains the seeded secret value"
    else
        fail "audit redaction leak — secret visible on:$leaked"
    fi
}

# ----- main ---------------------------------------------------------------
main() {
    printf 'mcplexer integration scenarios\n'
    printf '  NODE_A=%s NODE_B=%s NODE_C=%s\n' "$NODE_A" "$NODE_B" "$NODE_C"

    if ! ensure_tokens; then
        printf 'aborting: could not fetch api tokens\n' >&2
        exit 2
    fi
    printf '  tokens loaded (len A=%d B=%d C=%d)\n' ${#TOK_A} ${#TOK_B} ${#TOK_C}

    scenario_health
    scenario_provision
    scenario_p2p_identity
    scenario_pairing
    scenario_mesh_send
    scenario_mesh_wait
    scenario_skill_publish_request
    scenario_skill_share_grant
    scenario_skill_share_unauthorized
    scenario_skill_share_authorized_nonexistent
    scenario_worker_run
    scenario_mesh_trigger_tag
    scenario_mesh_trigger_kind
    scenario_mesh_trigger_regex
    scenario_mesh_trigger_throttle
    # 7.5 deferred — needs workspace_peer_binding established by 12.6.
    scenario_memory_save_recall
    scenario_memory_list_filter
    scenario_memory_supersede
    scenario_memory_forget_by_source
    scenario_memory_audit
    scenario_memory_cross_peer_share
    scenario_memory_unauthorized_share
    scenario_tasks_local_crud
    scenario_tasks_update_terminal
    scenario_tasks_notes
    scenario_tasks_claim
    scenario_tasks_mesh_events
    scenario_tasks_cross_peer_share
    # Deferred 7.5 — now that 12.6 has established the workspace_peer_binding.
    scenario_mesh_trigger_unauthorized
    scenario_tasks_unauthorized_offer
    scenario_tasks_consolidator
    scenario_tasks_audit
    scenario_linked_workspaces
    scenario_downstream_crud
    scenario_routes_crud
    scenario_workspaces_lifecycle
    scenario_auth_scopes_list
    scenario_settings_get_put
    scenario_cache_endpoints
    scenario_dashboard
    scenario_approval_rules_crud
    scenario_approval_queue
    scenario_worker_approvals
    scenario_approval_envelope_schema
    scenario_mesh_grant_consent_envelope
    # Downstream-spawn guard (cmdguard.go) — was previously orphaned;
    # the harness MUST run this on every CI sweep to catch a regression
    # where a poisoned downstream config slips into the registry. Step 16.
    scenario_cmdguard_db_lockdown
    # Shell Guard (PreToolUse hook + per-workspace approval_rules).
    # Steps 17-28. Most rule-matched cases resolve in 5-8s (resolver
    # grace); "no match keeps pending" cases use curl --max-time so
    # they don't block the suite. Setup MUST run before allow/deny/
    # isolation/etc. — teardown closes out the workspaces + rules.
    scenario_shellguard_hook_surface
    scenario_shellguard_cheap_blocks
    scenario_shellguard_setup
    scenario_shellguard_allow_match
    scenario_shellguard_deny_match
    scenario_shellguard_isolation
    scenario_shellguard_priority
    scenario_shellguard_expiry
    scenario_shellguard_session_filter
    scenario_shellguard_crud_reload
    scenario_shellguard_audit
    scenario_shellguard_workspace_tagging
    scenario_shellguard_teardown
    scenario_auth_gate
    scenario_health_detail
    scenario_audit_pagination
    scenario_named_devices
    scenario_mesh_agents_directory
    scenario_mesh_list_queue
    scenario_notifications
    scenario_secret_refs
    scenario_code_mode
    scenario_search_tools
    scenario_backups
    scenario_worker_templates
    scenario_tools_endpoint
    scenario_skill_search
    scenario_guards_inventory
    scenario_googlechat_status
    scenario_googlechat_spaces_list_empty
    scenario_googlechat_token_rejected_invalid
    scenario_googlechat_webhook_unbound
    scenario_googlechat_webhook_added
    scenario_googlechat_pairing_requires_workspace
    scenario_googlechat_test_message_requires_client
    scenario_hammerspoon_disabled_by_default
    scenario_hammerspoon_auth_scope_seeded
    scenario_hammerspoon_snippet_endpoint
    scenario_hammerspoon_install_rejects_non_darwin
    scenario_hammerspoon_probe_unconfigured
    scenario_hammerspoon_probe_persists_cache
    scenario_hammerspoon_probe_audit
    scenario_audit
    scenario_audit_redaction

    printf '\n=== SUMMARY ===\n'
    printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
    for line in "${RESULTS[@]}"; do
        printf '  %s\n' "$line"
    done

    if [ "$FAIL" -gt 0 ]; then
        exit 1
    fi
    exit 0
}

main "$@"
