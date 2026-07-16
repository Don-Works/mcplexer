#!/usr/bin/env bash
# scenario_mesh_triggers.sh — M4 mesh-triggered worker runs. Sourced by
# scenarios.sh.
#
# A mesh trigger is a per-Worker row that fires a run when a matching
# mesh message arrives. Remote-origin messages are gated by a per-peer
# `trigger_worker:<name>` (or wildcard `trigger_worker:*`) scope on the
# receiving node — without it the dispatcher records "denied" and no run
# fires. These scenarios exercise the full matrix: each filter type,
# throttling, and the unauthorized-peer rejection path.

# ----- helpers -----------------------------------------------------------

# Count terminal runs for a worker on a node. Stdout: the integer count.
# Defensive: on curl/HTTP failure (transient race after a worker create)
# return 0 rather than empty — empty values cascade into
# "[: : integer expression expected" errors in the caller's [ ] tests
# and, under pipefail, can abort the whole scenarios run.
count_runs() {
    local node="$1"
    local worker_id="$2"
    local body
    body=$(api GET "$node/api/v1/workers/$worker_id/runs?limit=200" 2>/dev/null || echo '[]')
    local n
    n=$(echo "$body" | jq -r 'length // 0' 2>/dev/null || echo 0)
    if [ -z "$n" ] || ! [[ "$n" =~ ^[0-9]+$ ]]; then
        echo 0
    else
        echo "$n"
    fi
}

# Wait until `min_runs` (or more) runs exist for the worker, or timeout.
# Stdout: the final count.
wait_for_runs() {
    local node="$1"
    local worker_id="$2"
    local min_runs="$3"
    local timeout="${4:-20}"
    local i=0
    local n=0
    while [ $i -lt "$timeout" ]; do
        n=$(count_runs "$node" "$worker_id")
        if [ "$n" -ge "$min_runs" ]; then
            echo "$n"
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    echo "$n"
}

# Create a Worker on the named node, reusing scenario_provision's
# auth scope. Trigger fixtures are enabled because the dispatcher
# deliberately rejects paused workers. Echoes the worker_id on success.
make_trigger_worker() {
    local node="$1"
    local name="$2"
    local ws="${WS_CHARLIE:-}"
    local scope="${SCOPE_CHARLIE:-}"
    local create_body
    create_body=$(build_worker_body "$name" "$ws" "$scope" true)
    local cresp
    cresp=$(api POST "$node/api/v1/workers" "$create_body" 2>/dev/null) || return 1
    echo "$cresp" | jq -r '.id // .ID // empty'
}

# Create a mesh trigger on a worker. `match_extra` is a JSON object with
# trigger fields (tag_match, kind_match, content_regex, throttle_seconds,
# audience_match, all_messages). Echoes the trigger id.
make_trigger() {
    local node="$1"
    local worker_id="$2"
    local match_extra="$3"
    local trig_body
    trig_body=$(jq -nc \
        --arg wid "$worker_id" \
        --argjson match "$match_extra" \
        '{worker_id:$wid, enabled:true, max_chain_depth:3} + $match')
    local resp
    resp=$(api POST "$node/api/v1/workers/$worker_id/mesh-triggers" "$trig_body" 2>/dev/null) || return 1
    echo "$resp" | jq -r '.id // .ID // empty'
}

# Grant a peer the trigger scope for `worker_name` on `node`. Stdout: the
# HTTP status code so callers can branch.
grant_trigger() {
    local node="$1"
    local peer_id="$2"
    local worker_name="$3"
    api_status POST "$node/api/v1/peers/$peer_id/trigger-grants" \
        "$(jq -nc --arg name "$worker_name" '{worker_name:$name}')"
}

# Send a mesh message from `from_node` with the given kind/tags/content.
# Defaults to recipient = audience:* so every peer sees it. Optional 5th
# arg `workspace_id` lets trigger scenarios stamp the receiver's
# workspace so the dispatcher's G2 isolation gate matches; empty falls
# back to the REST handler's default ("global").
mesh_emit() {
    local from_node="$1"
    local kind="$2"
    local tags="$3"
    local content="$4"
    local workspace_id="${5:-}"
    local body
    body=$(jq -nc \
        --arg kind "$kind" \
        --arg tags "$tags" \
        --arg content "$content" \
        --arg ws "$workspace_id" \
        '{recipient:{kind:"audience",value:"*"},
          kind:$kind, tags:$tags, content:$content,
          priority:"low", agent_name:"trigger-tester"}
         + (if $ws != "" then {workspace_id:$ws} else {} end)')
    api POST "$from_node/api/v1/mesh/send" "$body" 2>/dev/null \
        | jq -r '.message_id // empty'
}

# ----- scenarios ---------------------------------------------------------

# scenario_mesh_trigger_tag — node-c has a tag_match trigger; node-a (a
# granted peer) emits a matching message; the dispatcher fires a run.
scenario_mesh_trigger_tag() {
    step 7.1 "mesh-trigger by tag — local mesh send fires the worker"
    # Same-node trigger: worker + message both live on node-c. Cross-peer
    # tag triggers need a workspace_peer_binding (established via task
    # offer accept) — covered by a separate cross-peer trigger scenario.
    local wname="tag-worker-$$"
    local wid
    wid=$(make_trigger_worker "$NODE_C" "$wname")
    if [ -z "$wid" ]; then fail "tag-trigger worker create"; return; fi

    if [ -z "$(make_trigger "$NODE_C" "$wid" '{"tag_match":"trigger-me","throttle_seconds":1}')" ]; then
        fail "tag-trigger create on $wname"; return
    fi
    pass "tag-trigger: worker + trigger in place"

    local before
    before=$(count_runs "$NODE_C" "$wid")
    mesh_emit "$NODE_C" "finding" "trigger-me" "tag-trigger fixture" "$WS_CHARLIE" >/dev/null
    local after
    after=$(wait_for_runs "$NODE_C" "$wid" "$((before + 1))" 25)
    if [ "$after" -gt "$before" ]; then
        pass "tag-trigger fired a run on node-c (runs $before → $after)"
    else
        fail "tag-trigger did NOT fire" "runs stayed at $before"
    fi
}

# scenario_mesh_trigger_kind — kind_match=question; emit from B (must be
# granted too — using wildcard scope here).
scenario_mesh_trigger_kind() {
    step 7.2 "mesh-trigger by kind — local mesh send fires the worker"
    local wname="kind-worker-$$"
    local wid
    wid=$(make_trigger_worker "$NODE_C" "$wname")
    if [ -z "$wid" ]; then fail "kind-trigger worker create"; return; fi

    if [ -z "$(make_trigger "$NODE_C" "$wid" '{"kind_match":"question","throttle_seconds":1}')" ]; then
        fail "kind-trigger create"; return
    fi
    pass "kind-trigger: worker + trigger in place"

    local before
    before=$(count_runs "$NODE_C" "$wid")
    mesh_emit "$NODE_C" "question" "" "kind-trigger fixture" "$WS_CHARLIE" >/dev/null
    local after
    after=$(wait_for_runs "$NODE_C" "$wid" "$((before + 1))" 25)
    if [ "$after" -gt "$before" ]; then
        pass "kind-trigger fired (runs $before → $after)"
    else
        fail "kind-trigger did NOT fire" "runs stayed at $before"
    fi
}

# scenario_mesh_trigger_regex — content_regex filter. Content NOT
# matching the regex must NOT fire; content matching MUST fire.
scenario_mesh_trigger_regex() {
    step 7.3 "mesh-trigger by content_regex — only matching content fires"
    local wname="regex-worker-$$"
    local wid
    wid=$(make_trigger_worker "$NODE_C" "$wname")
    if [ -z "$wid" ]; then fail "regex-trigger worker create"; return; fi

    if [ -z "$(make_trigger "$NODE_C" "$wid" '{"content_regex":"^run-me:","throttle_seconds":1}')" ]; then
        fail "regex-trigger create"; return
    fi
    pass "regex-trigger: worker + trigger in place"

    local before
    before=$(count_runs "$NODE_C" "$wid")
    # Non-matching content first — must NOT fire.
    mesh_emit "$NODE_C" "finding" "" "do not run this one" "$WS_CHARLIE" >/dev/null
    sleep 3
    local mid
    mid=$(count_runs "$NODE_C" "$wid")
    if [ "$mid" -eq "$before" ]; then
        pass "regex-trigger correctly ignored non-matching content"
    else
        fail "regex-trigger fired on non-matching content" "runs $before → $mid"
    fi
    # Matching content — must fire.
    mesh_emit "$NODE_C" "finding" "" "run-me: regex-trigger fixture" "$WS_CHARLIE" >/dev/null
    local after
    after=$(wait_for_runs "$NODE_C" "$wid" "$((before + 1))" 25)
    if [ "$after" -gt "$before" ]; then
        pass "regex-trigger fired on matching content (runs $before → $after)"
    else
        fail "regex-trigger did NOT fire on matching content"
    fi
}

# scenario_mesh_trigger_throttle — three back-to-back matching messages
# inside one throttle window must yield at most one run.
scenario_mesh_trigger_throttle() {
    step 7.4 "mesh-trigger throttle — duplicates collapsed inside the window"
    local wname="throttle-worker-$$"
    local wid
    wid=$(make_trigger_worker "$NODE_C" "$wname")
    if [ -z "$wid" ]; then fail "throttle-trigger worker create"; return; fi

    if [ -z "$(make_trigger "$NODE_C" "$wid" '{"tag_match":"throttle-me","throttle_seconds":30}')" ]; then
        fail "throttle-trigger create"; return
    fi
    pass "throttle-trigger: worker + trigger in place"

    local before
    before=$(count_runs "$NODE_C" "$wid")
    mesh_emit "$NODE_C" "finding" "throttle-me" "throttle msg 1" "$WS_CHARLIE" >/dev/null
    mesh_emit "$NODE_C" "finding" "throttle-me" "throttle msg 2" "$WS_CHARLIE" >/dev/null
    mesh_emit "$NODE_C" "finding" "throttle-me" "throttle msg 3" "$WS_CHARLIE" >/dev/null
    sleep 8
    local after
    after=$(count_runs "$NODE_C" "$wid")
    local delta=$((after - before))
    if [ "$delta" -eq 1 ]; then
        pass "throttle-trigger collapsed 3 messages into 1 run"
    else
        fail "throttle-trigger fired wrong run count" "delta=$delta (expected 1)"
    fi
}

# scenario_mesh_trigger_unauthorized — trigger configured on node-c but
# NO grant given to the originating peer. Matching messages must NOT
# fire a run; the dispatcher records "denied".
scenario_mesh_trigger_unauthorized() {
    step 7.5 "mesh-trigger denies ungranted peer (cross-peer denial path)"
    # Cross-peer denial needs a workspace_peer_binding so the inbound
    # libp2p message resolves to a real workspace before the dispatcher
    # runs its peer-scope check. Scenario 12.6 establishes that binding
    # by accepting an offer from node-a into node-b's WS_BRAVO. Without
    # the binding, we SKIP.
    if [ "${TASK_OFFER_BINDING_ESTABLISHED:-0}" != "1" ]; then
        skip "unauth-trigger" \
            "no PID_A→WS_BRAVO workspace_peer_binding established (scenario 12.6 didn't accept) — denial path requires it"
        return
    fi
    if [ -z "${PID_A:-}" ]; then
        skip "unauth-trigger" "PID_A unset"
        return
    fi
    if ! is_paired_with "$NODE_B" "$PID_A"; then
        skip "unauth-trigger" "node-b not paired with node-a"
        return
    fi
    # Build a worker + trigger on node-B in its WS_BRAVO workspace. We
    # purposefully do NOT grant node-a `trigger_worker:<name>` on node-b,
    # so the dispatcher's peer-scope gate denies it.
    local wname="locked-worker-$$"
    local create_body wresp wid
    create_body=$(build_worker_body "$wname" "$WS_BRAVO" "$SCOPE_BRAVO" true)
    wresp=$(api POST "$NODE_B/api/v1/workers" "$create_body" 2>/dev/null) || {
        fail "unauth-trigger worker create on node-b"; return
    }
    wid=$(echo "$wresp" | jq -r '.id // .ID // empty')
    if [ -z "$wid" ]; then fail "unauth-trigger no worker id"; return; fi

    if [ -z "$(make_trigger "$NODE_B" "$wid" '{"tag_match":"deny-me","throttle_seconds":1}')" ]; then
        fail "unauth-trigger create"; return
    fi
    pass "unauth-trigger: worker + trigger in place on node-b WS_BRAVO"

    local before
    before=$(count_runs "$NODE_B" "$wid")
    # Send from node-a stamped with WS_ALPHA — node-b resolves through
    # the (PID_A, WS_ALPHA) → WS_BRAVO binding established by scenario
    # 12.6. The dispatcher reaches its peer-scope check and denies.
    mesh_emit "$NODE_A" "finding" "deny-me" "should be denied" "$WS_ALPHA" >/dev/null
    sleep 5
    local after
    after=$(count_runs "$NODE_B" "$wid")
    if [ "$after" -eq "$before" ]; then
        pass "unauth-trigger correctly denied (no run fired on node-b)"
    else
        fail "unauth-trigger leaked" "runs $before → $after"
        return
    fi

    # Audit row check on node-B (the receiver), not node-c.
    local arows
    arows=$(api GET "$NODE_B/api/v1/audit?limit=400" 2>/dev/null)
    if echo "$arows" \
        | jq -e '.data[]?
            | select(.tool_name == "worker_trigger.mesh")
            | select(.status == "blocked")
            | select(.params_redacted.decision == "denied")' \
            >/dev/null 2>&1
    then
        pass "node-b audit records worker_trigger.mesh blocked/denied row"
    else
        fail "node-b audit missing denial record" \
            "head=$(echo "$arows" | jq -c '[.data[]? | select(.tool_name | test("trigger"))][0:3]')"
    fi
}
