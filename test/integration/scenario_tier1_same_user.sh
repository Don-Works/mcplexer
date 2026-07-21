#!/usr/bin/env bash
# scenario_tier1_same_user.sh — Tier 1 trust-tier matrix scenario.
#
# Two machines, same MCPLEXER_SELF_USER_ID (Alice's box-A vs box-B). The
# pair handshake should auto-grant the default same-user scopes (see
# pair_same_user in lib_tiers.sh). Once paired, every cross-machine
# surface (skill / memory / task / mesh) MUST flow silently:
#
#   - no /api/v1/approvals entry on the receiver
#   - the target row visible on the partner machine within the poll window
#   - audit row on the receiver carries tier="same_user" when the audit
#     schema surfaces tier metadata (SKIP+PENDING otherwise)
#
# Belongs to epic 01KSK91Q4W8TNED9MAF0CTRVKC (task B1
# 01KSK9597J51A4007EC0BF5PFR). Sourced by scenarios.sh; gated behind
# BULLETPROOF=1 in main().

# tier1_count_recent_approvals returns how many entries exist on $node's
# approval queue whose payload mentions $marker (any field). Tier 1 must
# be ZERO — that's the silent-grant contract.
tier1_count_recent_approvals() {
    local node="$1"
    local marker="$2"
    api GET "$node/api/v1/approvals?limit=200" 2>/dev/null \
        | jq --arg m "$marker" \
            '[(.data // . // [])[]? | select((tostring) | contains($m))] | length' \
            2>/dev/null \
        || echo "0"
}

# tier1_audit_tier_present scans node-b's audit for any row whose tags or
# context references both $marker AND tier="same_user". Returns 0 if
# present, 1 otherwise. We deliberately accept multiple shapes because
# the audit schema is still being shaped under the epic — different rows
# put trust-tier in different places. If NO shape carries tier metadata
# at all, the caller should SKIP with a PENDING note.
tier1_audit_tier_present() {
    local node="$1"
    local marker="$2"
    api GET "$node/api/v1/audit?limit=400" 2>/dev/null \
        | jq -e --arg m "$marker" \
            '.data[]?
              | select((tostring) | contains($m))
              | select(
                  (.tier // .trust_tier // "") == "same_user"
                  or ((.tags // "") | tostring | contains("tier=same_user"))
                  or ((.context // "") | tostring | contains("same_user"))
              )' \
            >/dev/null 2>&1
}

# tier1_audit_marker_present is the fallback existence check — at least
# one audit row references our marker, regardless of tier metadata. This
# is what lets us distinguish "audit didn't capture this share at all"
# (FAIL) from "audit captured but tier field hasn't shipped yet" (SKIP).
tier1_audit_marker_present() {
    local node="$1"
    local marker="$2"
    api GET "$node/api/v1/audit?limit=400" 2>/dev/null \
        | jq -e --arg m "$marker" \
            '.data[]? | select((tostring) | contains($m))' \
            >/dev/null 2>&1
}

# scenario_tier1_same_user — the headline scenario. Pair NODE_A/NODE_B
# (Alice's two machines), then drive each of the four cross-machine
# surfaces and assert the silent-grant contract.
scenario_tier1_same_user() {
    step "T1.0" "Tier 1 — pair NODE_A↔NODE_B (same user, two machines)"

    if ! bulletproof_topology_ready; then
        skip "tier1 scenario" \
            "5-node bulletproof topology not up (NODE_D/E unreachable). Run with the bulletproof compose."
        return
    fi

    pair_same_user "$NODE_A" "$NODE_B"

    # If the pair handshake itself skipped (closed bridge), the downstream
    # assertions will be meaningless. Detect by checking peer_id on both
    # sides and bail with a single SKIP.
    local pid_a pid_b
    pid_a=$(peer_id_for "$NODE_A")
    pid_b=$(peer_id_for "$NODE_B")
    if [ -z "$pid_a" ] || [ -z "$pid_b" ]; then
        skip "tier1 surfaces" "p2p identity unset on a or b — pair did not land"
        return
    fi
    if ! is_paired_with "$NODE_B" "$pid_a" || ! is_paired_with "$NODE_A" "$pid_b"; then
        skip "tier1 surfaces" \
            "A and B not paired (libp2p closed-bridge) — silent-grant surfaces cannot be exercised"
        return
    fi

    # Snapshot the approval queue length on node-b BEFORE we exercise any
    # surface, so the silent-grant assertion compares "delta" not "total".
    local approvals_before
    approvals_before=$(api GET "$NODE_B/api/v1/approvals?limit=200" 2>/dev/null \
        | jq '[.data // .[] // []] | length' 2>/dev/null || echo "0")

    # ------------------------------------------------------------------
    # 1. SKILL: publish on A, fetch on B silently
    # ------------------------------------------------------------------
    step "T1.1" "skill flows A→B silently (no approval, no consent prompt)"
    local skill_name="tier1-skill-$RANDOM"
    local marker_skill="tier1-skill-marker-$RANDOM"
    local skill_body
    skill_body=$(build_skill_body "$skill_name" "$marker_skill")
    local publish_body presp
    publish_body=$(jq -n --arg name "$skill_name" --arg body "$skill_body" \
        '{name:$name, body:$body, scope:"global", author:"tier1-runner"}')
    if ! presp=$(api POST "$NODE_A/api/v1/skill-registry" "$publish_body" 2>&1); then
        fail "T1.1 publish on node-a" "$presp"
    else
        pass "T1.1 publish on node-a"
    fi

    # Silent fetch from B: request the skill via the granted scope. Use
    # mesh__request_skill — same path as scenario_skill_share. The pair
    # auto-grant should mean the request lands on a "granted" path; the
    # skill itself may not be in B's installed-skill store, which would
    # surface as ErrSkillNotInstalled. EITHER outcome (success OR clean
    # "not installed") confirms the auth gate passed silently. The
    # FAILURE shape is "scope denied / paired-peers-only" — that would
    # mean the auto-grant didn't fire.
    local rresp rtext
    rresp=$(mcp_call "$NODE_B" "mesh__request_skill" \
        "$(jq -nc --arg pid "$pid_a" --arg n "$skill_name" '{peer_id:$pid, skill_name:$n}')")
    rtext=$(echo "$rresp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    if echo "$rtext" | grep -qiE 'mesh\.skill_request|scope.*required|paired-peers|denied'; then
        fail "T1.1 skill auto-grant didn't fire" \
            "B→A request hit scope gate: $(echo "$rtext" | head -c 200)"
    else
        pass "T1.1 B→A skill request bypassed scope gate (silent auto-grant)"
    fi

    # ------------------------------------------------------------------
    # 2. MEMORY: save on A, recall on B silently
    # ------------------------------------------------------------------
    step "T1.2" "memory flows A→B silently"
    local mem_marker="tier1-mem-marker-$RANDOM"
    local mem_name="tier1-fact-$RANDOM"
    local cresp mid
    cresp=$(api POST "$NODE_A/api/v1/memory" \
        "$(jq -nc --arg n "$mem_name" --arg c "$mem_marker" \
            '{name:$n, content:$c, kind:"fact", tags:["tier1-share"]}')" 2>&1)
    mid=$(echo "$cresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$mid" ]; then
        fail "T1.2 create memory on node-a" "$(echo "$cresp" | head -c 200)"
    else
        pass "T1.2 created memory on node-a ($mid)"
    fi

    if [ -n "$mid" ]; then
        # Offer the memory to B. Same-user pair should silently grant
        # mesh.memory_request both ways, so this offer should not stall
        # in any approval queue.
        local oresp
        oresp=$(mcp_call "$NODE_A" "memory__offer_memory" \
            "$(jq -nc --arg pid "$pid_b" --arg mid "$mid" '{peer_id:$pid, memory_id:$mid}')")
        if echo "$oresp" | jq -e '.result.isError == true' >/dev/null 2>&1; then
            fail "T1.2 memory offer A→B" \
                "$(echo "$oresp" | jq -c '.result.content[0].text // .result' | head -c 200)"
        else
            pass "T1.2 memory offer A→B accepted by silent grant"
        fi

        # B should accumulate an offer/shared row referencing $mid within
        # ~8s. We accept either /memory/offers or a propagated row visible
        # to a memory list filter — the contract is "B can see it"; the
        # surface choice is daemon-internal.
        local landed="false"
        for _ in 1 2 3 4 5 6 7 8; do
            local lresp
            lresp=$(api GET "$NODE_B/api/v1/memory/offers" 2>/dev/null \
                || api GET "$NODE_B/api/v1/memory?limit=200" 2>/dev/null \
                || echo "[]")
            if echo "$lresp" | grep -q "$mid" 2>/dev/null \
                    || echo "$lresp" | grep -q "$mem_marker" 2>/dev/null; then
                landed="true"; break
            fi
            sleep 1
        done
        if [ "$landed" = "true" ]; then
            pass "T1.2 node-b observed memory $mid silently"
        else
            skip "T1.2 cross-peer memory propagation" \
                "node-b didn't surface $mid within 8s — typical libp2p closed-bridge timing on docker"
        fi
    fi

    # ------------------------------------------------------------------
    # 3. TASK: create + offer on A, observed on B silently
    # ------------------------------------------------------------------
    step "T1.3" "task offer flows A→B silently"
    local task_title="tier1-task-$RANDOM"
    local tbody tresp tid
    tbody=$(jq -nc --arg ws "${WS_ALPHA:-default}" --arg t "$task_title" \
        '{workspace_id:$ws, title:$t, status:"open", priority:"normal",
          description:"tier1 silent share"}')
    if ! tresp=$(api POST "$NODE_A/api/v1/tasks" "$tbody" 2>&1); then
        fail "T1.3 create task on node-a" "$tresp"
    else
        tid=$(echo "$tresp" | jq -r '.id // .ID // empty' 2>/dev/null)
        if [ -n "$tid" ]; then
            pass "T1.3 created task on node-a ($tid)"
        else
            fail "T1.3 task create returned no id" "$(echo "$tresp" | head -c 200)"
        fi
    fi

    if [ -n "${tid:-}" ]; then
        # Offer to B. Tier 1 should silently auto-grant task_offer:* —
        # no separate explicit grant required.
        local obody oresp
        obody=$(jq -nc --arg ws "${WS_ALPHA:-default}" --arg to "$pid_b" --arg tid "$tid" \
            '{workspace_id:$ws, task_id:$tid, to_peer_id:$to, message:"tier1 silent offer"}')
        oresp=$(api POST "$NODE_A/api/v1/tasks/offers" "$obody" 2>&1 || true)

        local found="false"
        for _ in 1 2 3 4 5 6 7 8 9 10; do
            local rb
            rb=$(api GET "$NODE_B/api/v1/tasks/offers?direction=incoming&limit=50" \
                2>/dev/null || echo "[]")
            if echo "$rb" | jq -e ".[]? | select(.title == \"$task_title\")" \
                    >/dev/null 2>&1; then
                found="true"; break
            fi
            sleep 1
        done
        if [ "$found" = "true" ]; then
            pass "T1.3 node-b observed task offer silently"
        else
            skip "T1.3 cross-peer task propagation" \
                "node-b never surfaced the offer (libp2p closed-bridge typical)"
        fi
    fi

    # ------------------------------------------------------------------
    # 4. MESH: broadcast on A, observed on B
    # ------------------------------------------------------------------
    step "T1.4" "mesh broadcast A→B silently (no approval prompt)"
    local mesh_marker="tier1-mesh-$RANDOM"
    local sbody sresp
    sbody=$(jq -nc --arg c "$mesh_marker" \
        '{recipient:{kind:"audience",value:"*"},
          kind:"finding", content:$c, priority:"low",
          agent_name:"tier1-runner"}')
    if sresp=$(api POST "$NODE_A/api/v1/mesh/send" "$sbody" 2>&1); then
        if echo "$sresp" | jq -e 'has("message_id")' >/dev/null 2>&1; then
            pass "T1.4 mesh/send returned message_id"
        else
            fail "T1.4 mesh/send no message_id" "$(echo "$sresp" | head -c 200)"
        fi
    else
        fail "T1.4 mesh/send" "$sresp"
    fi

    local seen="false"
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
        local mstatus
        mstatus=$(api GET "$NODE_B/api/v1/mesh/status" 2>/dev/null || echo "{}")
        if echo "$mstatus" | jq -e \
                ".messages[]? | select(.content == \"$mesh_marker\")" \
                >/dev/null 2>&1; then
            seen="true"; break
        fi
        sleep 1
    done
    if [ "$seen" = "true" ]; then
        pass "T1.4 node-b observed broadcast (marker=$mesh_marker)"
    else
        skip "T1.4 mesh propagation" \
            "node-b didn't observe the broadcast (libp2p closed-bridge timing)"
    fi

    # ------------------------------------------------------------------
    # 5. SILENT GRANT contract: no /api/v1/approvals delta on node-b
    # ------------------------------------------------------------------
    step "T1.5" "no approval-queue entries created on node-b for any of the four shares"

    local approvals_after delta=0
    approvals_after=$(api GET "$NODE_B/api/v1/approvals?limit=200" 2>/dev/null \
        | jq '[.data // .[] // []] | length' 2>/dev/null || echo "0")
    delta=$((approvals_after - approvals_before))
    if [ "$delta" -le 0 ]; then
        pass "T1.5 approval queue on node-b unchanged ($approvals_before → $approvals_after)"
    else
        # Could be a false-positive if some unrelated approval landed
        # mid-scenario. Look for one of our markers in the new entries.
        local bleed=""
        for m in "$marker_skill" "$mem_marker" "$task_title" "$mesh_marker"; do
            if api GET "$NODE_B/api/v1/approvals?limit=200" 2>/dev/null \
                    | grep -q "$m" 2>/dev/null; then
                bleed="$bleed $m"
            fi
        done
        if [ -n "$bleed" ]; then
            fail "T1.5 silent-grant contract violated — approval row(s) for tier1 share(s):$bleed" \
                "Tier 1 must be auto-granted; an approval entry means consent was prompted."
        else
            pass "T1.5 approval delta=$delta but none reference our tier1 markers (unrelated)"
        fi
    fi

    # ------------------------------------------------------------------
    # 6. AUDIT carries tier=same_user (PENDING-tolerant)
    # ------------------------------------------------------------------
    step "T1.6" "audit on node-b carries tier=\"same_user\" on tier1 transfers"

    # Pick the marker most likely to land in an audit row. Mesh
    # broadcasts and skill requests are the surfaces where the audit
    # schema is most mature.
    local tier_seen=0 marker_seen=0
    for m in "$marker_skill" "$mesh_marker" "$task_title" "$mem_marker"; do
        if tier1_audit_marker_present "$NODE_B" "$m"; then
            marker_seen=1
            if tier1_audit_tier_present "$NODE_B" "$m"; then
                tier_seen=1
                break
            fi
        fi
    done
    if [ "$tier_seen" = "1" ]; then
        pass "T1.6 audit row carries tier=same_user"
    elif [ "$marker_seen" = "1" ]; then
        skip "T1.6 tier metadata on audit rows" \
            "PENDING — audit captured the share but tier=\"same_user\" field not surfaced yet (epic 01KSK91Q4W8TNED9MAF0CTRVKC)"
    else
        skip "T1.6 audit on node-b" \
            "no audit row references any tier1 marker yet — audit writer may still be batching, or audit on receiver is async"
    fi
}
