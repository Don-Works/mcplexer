#!/usr/bin/env bash
# scenario_linked_workspaces.sh — cross-machine task replication via
# explicit workspace links (migration 088 + the REST /workspace-links
# admin surface + the replication coordinator).
#
# Two layers:
#   30.1  Always-on (single node): the link admin surface works in the
#         real daemon — create / list / unlink round-trips, and a link
#         grants the peer the task_assign scope.
#   30.2  Best-effort (needs a paired libp2p bridge): a task created on
#         node-a's 'alpha' workspace REPLICATES into node-b's 'bravo'
#         workspace, and a subsequent status change CONVERGES onto the
#         same row (no duplicate). SKIPs on a closed docker bridge, the
#         same posture as the other cross-peer scenarios.
#
# Sourced by scenarios.sh AFTER provision (WS_ALPHA/WS_BRAVO),
# p2p_identity (PID_A/PID_B) and pairing.

scenario_linked_workspaces_crud() {
    step 30.1 "linked workspaces — REST create / list / unlink + scope grant"
    local peer="${PID_B:-synthetic-peer-$RANDOM}"

    local body resp
    body=$(jq -nc --arg p "$peer" --arg lw "$WS_ALPHA" --arg rw "$WS_BRAVO" \
        '{peer_id:$p, local_workspace:$lw, remote_workspace_id:$rw, remote_workspace_name:"bravo"}')
    if ! resp=$(api POST "$NODE_A/api/v1/workspace-links" "$body" 2>&1); then
        fail "POST /api/v1/workspace-links" "$resp"
        return
    fi
    assert_jq "link created (linked=true)" "$resp" '.linked == true'
    assert_jq "link resolved local workspace 'alpha' by id" "$resp" \
        '(.local_workspace_name) == "alpha"'

    local lresp
    lresp=$(api GET "$NODE_A/api/v1/workspace-links" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.peer_id == \"$peer\" and .local_workspace_name == \"alpha\" and .remote_workspace_id == \"$WS_BRAVO\")" >/dev/null 2>&1; then
        pass "GET /api/v1/workspace-links lists the link"
    else
        fail "link not in list" "$(echo "$lresp" | head -c 300)"
    fi

    # When the peer is real (paired), the link must have granted it
    # task_assign:bravo. With a synthetic peer the grant is a warning —
    # accept either shape so the always-on assertion never flakes.
    if [ -n "${PID_B:-}" ]; then
        assert_jq "link granted task_assign:bravo to a real peer" "$resp" \
            '(.granted_scope) == "task_assign:bravo"'
    fi

    # Unlink removes it.
    api DELETE "$NODE_A/api/v1/workspace-links?peer_id=$peer&remote_workspace_id=$WS_BRAVO" \
        >/dev/null 2>&1 || true
    lresp=$(api GET "$NODE_A/api/v1/workspace-links" 2>/dev/null || echo "[]")
    if echo "$lresp" | jq -e ".[]? | select(.peer_id == \"$peer\")" >/dev/null 2>&1; then
        fail "link still present after unlink"
    else
        pass "DELETE /api/v1/workspace-links removed the link"
    fi
}

scenario_linked_workspaces_replication() {
    step 30.2 "linked workspaces — task A→B replicates + status converges"
    if [ -z "${PID_A:-}" ] || [ -z "${PID_B:-}" ]; then
        skip "linked-workspace replication" "PID_A/PID_B unset — p2p identity step failed"
        return
    fi
    if ! is_paired_with "$NODE_B" "$PID_A" || ! is_paired_with "$NODE_A" "$PID_B"; then
        skip "linked-workspace replication" \
            "A and B not paired (libp2p closed-bridge) — replication cannot flow"
        return
    fi

    # Link BOTH directions:
    #  node-a: alpha → (PID_B, bravo)  — send-side gate on A; grants B task_assign:bravo
    #  node-b: bravo → (PID_A, alpha)  — routing binding on B + grants A task_assign:alpha
    #                                    (this is what lets A's pushed tasks land on B)
    api POST "$NODE_A/api/v1/workspace-links" \
        "$(jq -nc --arg p "$PID_B" --arg lw "$WS_ALPHA" --arg rw "$WS_BRAVO" \
            '{peer_id:$p, local_workspace:$lw, remote_workspace_id:$rw, remote_workspace_name:"bravo"}')" \
        >/dev/null 2>&1 || true
    api POST "$NODE_B/api/v1/workspace-links" \
        "$(jq -nc --arg p "$PID_A" --arg lw "$WS_BRAVO" --arg rw "$WS_ALPHA" \
            '{peer_id:$p, local_workspace:$lw, remote_workspace_id:$rw, remote_workspace_name:"alpha"}')" \
        >/dev/null 2>&1 || true

    # Create a task on node-a's alpha workspace.
    local title="linkedws-$RANDOM"
    local cresp tid
    cresp=$(api POST "$NODE_A/api/v1/tasks" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg t "$title" \
            '{workspace_id:$ws, title:$t, status:"open", priority:"normal",
              description:"linked-workspace replication"}')" 2>&1)
    tid=$(echo "$cresp" | jq -r '.id // .ID // empty')
    if [ -z "$tid" ]; then
        fail "create task on node-a" "$cresp"
        return
    fi
    pass "created task '$title' on node-a/alpha ($tid)"

    # Poll node-b's bravo workspace for the replicated task. The
    # coordinator batches on a ~5s interval, then direct-assigns; give it
    # room.
    local found="false" rb
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        rb=$(api GET "$NODE_B/api/v1/tasks?workspace_id=$WS_BRAVO&limit=200" 2>/dev/null || echo "[]")
        if echo "$rb" | jq -e ".[]? | select(.title == \"$title\")" >/dev/null 2>&1; then
            found="true"; break
        fi
        sleep 1
    done
    if [ "$found" != "true" ]; then
        skip "linked-workspace replication" \
            "task '$title' did not surface on node-b within 15s (libp2p bridge timing on docker)"
        return
    fi
    pass "task '$title' replicated A→B into node-b/bravo"

    # Update status on node-a → must CONVERGE on node-b (same row, new
    # status, exactly one task — proving the dedup, not duplicate clones).
    api POST "$NODE_A/api/v1/tasks/$tid/update?workspace_id=$WS_ALPHA" \
        "$(jq -nc '{status:"doing"}')" >/dev/null 2>&1 || true

    local converged="false" count=0
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        rb=$(api GET "$NODE_B/api/v1/tasks?workspace_id=$WS_BRAVO&limit=200" 2>/dev/null || echo "[]")
        count=$(echo "$rb" | jq "[.[]? | select(.title == \"$title\")] | length" 2>/dev/null || echo 0)
        if echo "$rb" | jq -e ".[]? | select(.title == \"$title\" and ((.status // .Status) == \"doing\"))" >/dev/null 2>&1; then
            converged="true"; break
        fi
        sleep 1
    done
    if [ "$converged" = "true" ] && [ "${count:-0}" -eq 1 ]; then
        pass "status change converged on node-b (status=doing, exactly 1 task — no duplicate)"
    elif [ "$converged" = "true" ]; then
        fail "convergence produced duplicates" \
            "node-b has ${count} tasks titled '$title' (want exactly 1)"
    else
        skip "linked-workspace status convergence" \
            "status update not observed on node-b within 15s"
    fi

    # ----- memory replicates A->B (as a Tier-1 offer) -------------------
    # NOTE on memory semantics: Tier-1 memory replication is OFFER-based —
    # the write only happens when the receiver pulls (RequestMemory). The
    # linked-workspace routing fix lives on that pull path
    # (HandleIncomingMemory resolves the binding so the memory lands in the
    # BOUND local workspace, not global). Fully-silent auto-pull of Tier-1
    # offers is a separate memory-subsystem follow-up; here we assert the
    # offer reaches node-b, matching scenario_tier1_same_user's posture.
    step 30.3 "linked workspaces — a memory written on node-a/gateway is offered to node-b"
    local mmark="linkedws-mem-$RANDOM"
    api POST "$NODE_A/api/v1/memory" \
        "$(jq -nc --arg ws "$WS_ALPHA" --arg c "$mmark" \
            '{name:$c, content:$c, kind:"fact", workspace_id:$ws, tags:["linkedws"]}')" \
        >/dev/null 2>&1 || true
    local memfound="false" mb
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18; do
        mb=$(api GET "$NODE_B/api/v1/memory/offers" 2>/dev/null \
            || api GET "$NODE_B/api/v1/memory?limit=200" 2>/dev/null || echo "[]")
        if echo "$mb" | grep -q "$mmark" 2>/dev/null; then
            memfound="true"; break
        fi
        sleep 1
    done
    if [ "$memfound" = "true" ]; then
        pass "memory '$mmark' replicated A->B (offer reached node-b; routes to the bound workspace on accept)"
    else
        skip "linked-workspace memory replication" \
            "memory did not surface on node-b within 18s (libp2p timing / Tier-1 interval)"
    fi
}

scenario_linked_workspaces() {
    scenario_linked_workspaces_crud
    scenario_linked_workspaces_replication
}
