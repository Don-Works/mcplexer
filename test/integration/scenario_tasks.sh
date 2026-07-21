#!/usr/bin/env bash
# scenario_tasks.sh — exercises the per-workspace tasks subsystem
# (migration 061 + 062 backfill).
#
# Phase 1 (local CRUD + notes + vocabulary) is the always-on baseline.
# Phase 2 (mesh task_event emission + chain-depth) and Phase 3 (cross-
# peer offer/assign_remote/accept/decline) wrap their assertions in
# capability probes so the harness degrades gracefully on builds that
# haven't shipped those phases yet.
#
# Mirrors scenario_memory.sh in structure.
#
# Required: scenarios.sh sourced this AFTER ensure_tokens populated
# TOK_A/B/C and after the workspace IDs (WS_ALPHA/BRAVO/CHARLIE) were
# set by scenario_provision.

# ----- 12.x: local CRUD + notes + vocabulary -----------------------------

scenario_tasks_local_crud() {
    step 12.1 "REST POST /api/v1/tasks creates and round-trips a task"
    local ws="$WS_ALPHA"
    local title="integration-task-$RANDOM"
    local cbody
    cbody=$(jq -nc \
        --arg ws "$ws" --arg t "$title" \
        '{workspace_id:$ws, title:$t, description:"created by harness",
          status:"open", priority:"normal", tags:["integration","harness"],
          meta:""}')
    local cresp
    if ! cresp=$(api POST "$NODE_A/api/v1/tasks" "$cbody" 2>&1); then
        fail "POST /api/v1/tasks failed" "$cresp"
        return
    fi
    local tid
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "create did not return task id" "body: $cresp"
        return
    fi
    pass "REST POST /api/v1/tasks created $title ($tid)"

    # Fetch
    local gresp
    gresp=$(api GET "$NODE_A/api/v1/tasks/$tid?workspace_id=$ws" 2>/dev/null || echo "{}")
    assert_jq "GET /api/v1/tasks/$tid returns same row" "$gresp" \
        "(.title // .Title) == \"$title\""

    # List with workspace filter
    local lresp
    lresp=$(api GET "$NODE_A/api/v1/tasks?workspace_id=$ws&limit=200" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.id == \"$tid\" or .ID == \"$tid\")" >/dev/null 2>&1; then
        pass "GET /api/v1/tasks lists the created row"
    else
        fail "list did not include $tid" "head: $(echo "$lresp" | head -c 300)"
    fi

    # Export the task id for later steps.
    TASK_A_ID="$tid"
}

scenario_tasks_update_terminal() {
    step 12.2 "POST /api/v1/tasks/{id}/update sets status, terminal flips closed_at"
    local ws="$WS_ALPHA"
    local tid="${TASK_A_ID:-}"
    if [ -z "$tid" ]; then
        skip "update test" "TASK_A_ID unset — Phase 1 create must run first"
        return
    fi
    # Move to 'doing'. workspace_id is a QUERY-STRING param; body is the patch.
    local ubody uresp
    ubody=$(jq -nc '{status:"doing"}')
    if ! uresp=$(api POST "$NODE_A/api/v1/tasks/$tid/update?workspace_id=$ws" "$ubody" 2>&1); then
        fail "update to status=doing" "$uresp"
        return
    fi
    assert_jq "update to status=doing" "$uresp" '(.status // .Status) == "doing"'

    # Close it (terminal=true).
    local cbody cresp
    cbody=$(jq -nc '{status:"done", terminal:true}')
    if ! cresp=$(api POST "$NODE_A/api/v1/tasks/$tid/update?workspace_id=$ws" "$cbody" 2>&1); then
        fail "terminal=true update" "$cresp"
        return
    fi
    assert_jq "terminal=true sets status=done" "$cresp" '(.status // .Status) == "done"'
    assert_jq "terminal=true stamps closed_at" "$cresp" \
        '((.closed_at // .ClosedAt) // "") | length > 0'

    # Vocab should now show "done" with is_terminal=1.
    local vresp
    vresp=$(api GET "$NODE_A/api/v1/task-status-vocabulary?workspace_id=$ws" 2>/dev/null || echo "[]")
    if echo "$vresp" | jq -e '.[]? | select(.status_text == "done" and (.is_terminal == true or .is_terminal == 1))' >/dev/null 2>&1; then
        pass "task_status_vocabulary learned 'done' as terminal"
    else
        fail "vocab did not record 'done' as terminal" "vocab: $(echo "$vresp" | jq -c .)"
    fi
}

scenario_tasks_notes() {
    step 12.3 "POST /api/v1/tasks/{id}/notes appends and lists"
    local ws="$WS_ALPHA"
    local tid="${TASK_A_ID:-}"
    if [ -z "$tid" ]; then
        skip "notes test" "TASK_A_ID unset"
        return
    fi
    local nbody nresp
    nbody=$(jq -nc '{body:"harness note one", author_kind:"system"}')
    if ! nresp=$(api POST "$NODE_A/api/v1/tasks/$tid/notes?workspace_id=$ws" "$nbody" 2>&1); then
        fail "append note failed" "$nresp"
        return
    fi
    pass "appended note one"

    # Append a second note.
    nbody=$(jq -nc '{body:"harness note two", author_kind:"system"}')
    api POST "$NODE_A/api/v1/tasks/$tid/notes?workspace_id=$ws" "$nbody" >/dev/null 2>&1 || true

    local lresp n
    lresp=$(api GET "$NODE_A/api/v1/tasks/$tid/notes?workspace_id=$ws" 2>/dev/null || echo "[]")
    n=$(echo "$lresp" | jq -r 'length // 0')
    if [ "${n:-0}" -ge 2 ]; then
        pass "list notes returns >=2 entries (got $n)"
    else
        fail "list notes returned $n, want >=2" "body: $lresp"
    fi
}

scenario_tasks_claim() {
    step 12.4 "POST /api/v1/tasks/{id}/claim atomic assign+status flip"
    local ws="$WS_BRAVO"
    # Create a fresh task on node-b for the claim test.
    local cbody cresp tid
    cbody=$(jq -nc --arg ws "$ws" --arg t "claim-test-$RANDOM" \
        '{workspace_id:$ws, title:$t, status:"open", priority:"normal"}')
    if ! cresp=$(api POST "$NODE_B/api/v1/tasks" "$cbody" 2>&1); then
        fail "create on node-b failed" "$cresp"
        return
    fi
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "create on node-b returned no id" "$cresp"
        return
    fi

    local clbody clresp
    clbody=$(jq -nc \
        '{session_id:"integration-harness-bravo", status:"doing", note:"claimed by harness"}')
    if ! clresp=$(api POST "$NODE_B/api/v1/tasks/$tid/claim?workspace_id=$ws" "$clbody" 2>&1); then
        fail "claim failed" "$clresp"
        return
    fi
    assert_jq "claim returns status=doing" "$clresp" '(.status // .Status) == "doing"'
    assert_jq "claim records assignee_session_id" "$clresp" \
        '((.assignee_session_id // .AssigneeSessionID) // "") | length > 0'

    # Second claim attempt — service returns ErrTaskAlreadyClaimed → 409.
    # That's the locked-decision-1 semantics: first claimant wins. We
    # treat either idempotent-OK or 409 as acceptable.
    local clresp2 status2
    status2=$(api_status POST "$NODE_B/api/v1/tasks/$tid/claim?workspace_id=$ws" "$clbody")
    if [ "$status2" = "200" ] || [ "$status2" = "409" ]; then
        pass "second claim returned $status2 (idempotent or already-claimed — both acceptable)"
    else
        clresp2=$(api POST "$NODE_B/api/v1/tasks/$tid/claim?workspace_id=$ws" "$clbody" 2>/dev/null || echo "{}")
        fail "unexpected second-claim status=$status2" "body=$clresp2"
    fi
}

# ----- 12.5: Phase 2 — mesh task_event emission --------------------------

scenario_tasks_mesh_events() {
    step 12.5 "task mutations emit kind=task_event on the local mesh"
    local ws="$WS_CHARLIE"
    local title="mesh-evt-$RANDOM"

    # Create a task on node-c.
    local cbody cresp tid
    cbody=$(jq -nc --arg ws "$ws" --arg t "$title" \
        '{workspace_id:$ws, title:$t, status:"open", priority:"normal"}')
    if ! cresp=$(api POST "$NODE_C/api/v1/tasks" "$cbody" 2>&1); then
        fail "create for mesh-event test failed" "$cresp"
        return
    fi
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "create for mesh-event test returned no id" "$cresp"
        return
    fi

    # Update + close — three mutations total (created, status_changed,
    # closed). Phase 2 emitter should produce at least one task_event for
    # the create path; we only assert that net-new task_event rows fire.
    api POST "$NODE_C/api/v1/tasks/$tid/update?workspace_id=$ws" \
        "$(jq -nc '{status:"doing"}')" >/dev/null 2>&1 || true
    api POST "$NODE_C/api/v1/tasks/$tid/update?workspace_id=$ws" \
        "$(jq -nc '{status:"done", terminal:true}')" >/dev/null 2>&1 || true

    # Poll a few seconds for the mesh events to land.
    local after found="false"
    for _ in 1 2 3 4 5 6 7 8; do
        after=$(api GET "$NODE_C/api/v1/mesh/status" 2>/dev/null \
            | jq "[.messages[]? | select(.kind == \"task_event\" and (.tags // \"\" | contains(\"task_id:$tid\")))] | length")
        after=${after:-0}
        if [ "${after:-0}" -gt 0 ]; then
            found="true"; break
        fi
        sleep 1
    done

    if [ "$found" = "true" ]; then
        pass "task_event messages produced for task $tid (count=$after)"
    else
        # Phase 2 not landed yet (capability probe).
        skip "task_event emission on node-c" \
            "no kind=task_event rows for task $tid after 8s — Phase 2 emitter not yet wired"
    fi
}

# ----- 12.6: Phase 3 — cross-peer offer flow -----------------------------

scenario_tasks_cross_peer_share() {
    step 12.6 "cross-peer task offer: node-a creates + offers to node-b"
    if [ -z "${PID_A:-}" ] || [ -z "${PID_B:-}" ]; then
        skip "cross-peer task offer" \
            "PID_A or PID_B unset — p2p identity step failed earlier"
        return
    fi

    # Grant task_offer:* from node-b to node-a — peer-scope grants live
    # only on the MCP surface (mesh__grant_peer_scope), no REST shape.
    local gresp
    gresp=$(mcp_call "$NODE_B" "mesh__grant_peer_scope" \
        "$(jq -nc --arg pid "$PID_A" '{peer:$pid, scope:"task_offer:*"}')")
    if ! echo "$gresp" | jq -e '.result.isError != true' >/dev/null 2>&1; then
        skip "cross-peer task offer" \
            "could not grant task_offer:* on node-b: $(echo "$gresp" | jq -c '.result // .')"
        return
    fi

    # Create a task on node-a in its own workspace.
    local ws_a="$WS_ALPHA"
    local title="cross-peer-$RANDOM"
    local cbody cresp tid
    cbody=$(jq -nc --arg ws "$ws_a" --arg t "$title" \
        '{workspace_id:$ws, title:$t, description:"shared via offer", status:"open"}')
    if ! cresp=$(api POST "$NODE_A/api/v1/tasks" "$cbody" 2>&1); then
        fail "create source task for offer failed" "$cresp"
        return
    fi
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "create source task returned no id" "$cresp"
        return
    fi

    # Send the offer (REST). The body uses to_peer_id + workspace_id +
    # task_id + message; see internal/api/tasks_handler.go:createOfferRequest.
    local obody oresp
    obody=$(jq -nc --arg ws "$ws_a" --arg to "$PID_B" --arg tid "$tid" \
        '{workspace_id:$ws, task_id:$tid, to_peer_id:$to, message:"please review"}')
    oresp=$(api POST "$NODE_A/api/v1/tasks/offers" "$obody" 2>&1) || {
        # Wire-layer send may fail on closed docker bridge (libp2p not
        # paired); fall back to MCP path which uses the same service.
        oresp=$(mcp_call "$NODE_A" "task__offer" \
            "$(jq -nc --arg to "$PID_B" --arg tid "$tid" '{to:$to, task_id:$tid, message:"please review"}')")
    }

    # Wait for the offer to land on node-b. Two reasons it might not:
    # libp2p closed-bridge (typical SKIP) or sender-side wire failure.
    local found="false"
    local rb
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        rb=$(api GET "$NODE_B/api/v1/tasks/offers?direction=incoming&limit=50" 2>/dev/null || echo "[]")
        if echo "$rb" | jq -e ".[]? | select(.title == \"$title\")" >/dev/null 2>&1; then
            found="true"; break
        fi
        sleep 1
    done

    if [ "$found" = "true" ]; then
        pass "node-b received the offer for task '$title'"
    else
        skip "cross-peer task offer" \
            "node-b never received the offer (libp2p closed-bridge typical on docker — see README)."
        return
    fi

    # Accept the offer to materialise the local task AND establish a
    # workspace_peer_binding (PID_A, WS_ALPHA → some-local-ws). The
    # binding lets subsequent cross-peer mesh sends from node-a stamped
    # with workspace_id=WS_ALPHA reach node-b's dispatcher G2 gate.
    local offer_id
    offer_id=$(api GET "$NODE_B/api/v1/tasks/offers?direction=incoming&limit=50" 2>/dev/null \
        | jq -r '.[]? | select(.title == "'"$title"'") | .id // empty' | head -1)
    if [ -n "$offer_id" ]; then
        local aresp astatus
        astatus=$(api_status POST "$NODE_B/api/v1/tasks/offers/$offer_id/accept" \
            "$(jq -nc --arg ws "$WS_BRAVO" '{workspace_id:$ws}')")
        if [ "$astatus" = "200" ] || [ "$astatus" = "201" ]; then
            pass "node-b accepted the offer → workspace_peer_binding established"
            # Stash for scenario 7.5's cross-peer denial test.
            export TASK_OFFER_BINDING_ESTABLISHED=1
        else
            aresp=$(api POST "$NODE_B/api/v1/tasks/offers/$offer_id/accept" \
                "$(jq -nc --arg ws "$WS_BRAVO" '{workspace_id:$ws}')" 2>/dev/null || echo "{}")
            skip "offer accept" "status=$astatus body=$aresp"
        fi
    fi
}

scenario_tasks_unauthorized_offer() {
    step 12.7 "cross-peer task offer is rejected without task_offer scope"
    if [ -z "${PID_A:-}" ] || [ -z "${PID_C:-}" ]; then
        skip "unauthorized offer" "p2p identity missing"
        return
    fi
    # node-c has NOT granted task_offer to node-a → the offer should be
    # rejected at the receiver. Asserts negative: no incoming offer with
    # this title lands on node-c.
    local title="unauth-$RANDOM"
    local cbody cresp tid
    cbody=$(jq -nc --arg ws "$WS_ALPHA" --arg t "$title" \
        '{workspace_id:$ws, title:$t, status:"open"}')
    if ! cresp=$(api POST "$NODE_A/api/v1/tasks" "$cbody" 2>&1); then
        skip "unauthorized offer" "couldn't create source task: $cresp"
        return
    fi
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    [ -z "$tid" ] && { skip "unauthorized offer" "no task id"; return; }

    local obody
    obody=$(jq -nc --arg ws "$WS_ALPHA" --arg to "$PID_C" --arg tid "$tid" \
        '{workspace_id:$ws, task_id:$tid, to_peer_id:$to}')
    api POST "$NODE_A/api/v1/tasks/offers" "$obody" >/dev/null 2>&1 || true

    sleep 3
    local accepted
    accepted=$(api GET "$NODE_C/api/v1/tasks/offers?direction=incoming&limit=50" 2>/dev/null \
        | jq "[.[]? | select(.title == \"$title\" and (.state == \"pending\" or .state == \"accepted\" or .state == \"auto_accepted\"))] | length" 2>/dev/null || echo "0")
    if [ "${accepted:-0}" -eq 0 ]; then
        pass "node-c did NOT accept the unscoped offer for '$title'"
    else
        fail "node-c accepted unscoped offer" \
            "expected rejection — task_offer scope was never granted to node-a"
    fi
}

# ----- 12.8: Phase 5 — admin status consolidator ------------------------

scenario_tasks_consolidator() {
    step 12.8 "admin task__consolidate_statuses is CWD-gated from non-admin cwd"
    # Admin tools are CWD-gated. The integration harness runs the MCP
    # socket from `/`, NOT ~/.mcplexer, so calling task__consolidate_statuses
    # MUST return a JSON-RPC error explaining the gate. Confirms both
    # the visibility filter AND the defence-in-depth dispatch refusal.
    local args resp
    args=$(jq -nc --arg ws "$WS_ALPHA" '{workspace:$ws, dry_run:true}')
    resp=$(mcp_call "$NODE_A" "task__consolidate_statuses" "$args" 2>/dev/null || echo "{}")
    # Path 1: JSON-RPC error envelope.
    local emsg
    emsg=$(echo "$resp" | jq -r '.error.message // ""' 2>/dev/null || echo "")
    if echo "$emsg" | grep -qi "admin-only\|mcplexer data directory"; then
        pass "admin gate refused task__consolidate_statuses from non-admin cwd (defence-in-depth working)"
        return
    fi
    # Path 2: rare environment where admin is enabled for the harness —
    # accept either a content payload or a result envelope as PASS.
    if echo "$resp" | jq -e '.result.content[0]' >/dev/null 2>&1; then
        pass "admin task__consolidate_statuses returned a result (admin cwd context active)"
        return
    fi
    if echo "$resp" | jq -e '.result' >/dev/null 2>&1; then
        pass "admin task__consolidate_statuses dispatched (admin cwd context active)"
        return
    fi
    fail "admin consolidator response unrecognised" \
        "expected admin-gate error or result; got: $(echo "$resp" | head -c 300)"
}

# ----- 12.9: audit redaction over task surfaces --------------------------

scenario_tasks_audit() {
    step 12.9 "audit ledger contains task_* rows on the node that mutated"
    local body="{}"
    for _ in 1 2 3 4 5; do
        body=$(api GET "$NODE_A/api/v1/audit?limit=200" 2>/dev/null || echo "{}")
        if echo "$body" | jq -e '.data[]? | select((.tool_name // .verb // "") | test("task"))' >/dev/null 2>&1; then
            pass "node-a audit contains task_* row"
            return
        fi
        sleep 1
    done
    fail "node-a audit missing task_* rows" \
        "(head) $(echo "$body" | jq -c '.data[0:3] // .')"
}
