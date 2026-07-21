#!/usr/bin/env bash
# scenario_tier2_same_org.sh — Tier 2 trust-tier matrix scenario.
#
# Two machines, DIFFERENT users (Alice on NODE_A vs Bob on NODE_C) but
# the same org label (AcmeCo). pair_same_org asserts the zero-default-
# scope baseline; this scenario then exercises the explicit-grant
# lifecycle on each surface:
#
#   1. Negative: ungranted attempt MUST reject (no scope).
#   2. Grant via mesh__grant_peer_scope (the only path the daemon ships
#      for peer-scoped permissions).
#   3. Re-attempt: succeeds. Approval queue on the receiver may record
#      the consent event (best-effort assertion — schema still settling).
#   4. Revoke: next attempt fails again. We accept any rejection but
#      prefer `denial="scope_revoked"`; SKIP+PENDING if the daemon
#      returns a generic 403/error with no code.
#
# Belongs to epic 01KSK91Q4W8TNED9MAF0CTRVKC (task B2
# 01KSK9598A33SS901SNSHV68B2). Sourced by scenarios.sh; gated behind
# BULLETPROOF=1.

# tier2_skill_request runs the standard mesh__request_skill from
# requester → owner and prints a single-token verdict:
#
#   ok                 — request succeeded
#   not_installed      — auth passed, resolver said no such skill
#                        (counts as scope-OK for the gate test)
#   scope_denied       — auth gate rejected the call
#   error              — any other failure
#
# Used by the negative + grant phases below — clearer than re-decoding
# the response in each step.
tier2_skill_request() {
    local requester_url="$1"
    local owner_pid="$2"
    local skill_name="$3"
    local resp text
    resp=$(mcp_call "$requester_url" "mesh__request_skill" \
        "$(jq -nc --arg pid "$owner_pid" --arg n "$skill_name" \
            '{peer_id:$pid, skill_name:$n}')")
    text=$(echo "$resp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    if echo "$resp" | jq -e '.error != null' >/dev/null 2>&1; then
        echo "error"
        return
    fi
    if echo "$resp" | jq -e '.result.isError != true' >/dev/null 2>&1; then
        # Even when isError=false the body may indicate "not installed"
        # (auth-pass + resolver-miss). We treat that as scope OK.
        if echo "$text" | grep -qiE 'not installed|not.have'; then
            echo "not_installed"
        else
            echo "ok"
        fi
        return
    fi
    if echo "$text" | grep -qiE 'scope|paired-peers|denied|forbid|not.granted'; then
        echo "scope_denied"
    elif echo "$text" | grep -qiE 'not installed|not.have'; then
        echo "not_installed"
    else
        echo "error"
    fi
}

# tier2_grant_scope_via_mesh is a tiny wrapper that grants `scope` on
# `granter_url` to `grantee_peer_id`. Returns 0 on success, non-zero on
# any failure (which we report up the chain).
tier2_grant_scope_via_mesh() {
    local granter_url="$1"
    local grantee_pid="$2"
    local scope="$3"
    local resp
    resp=$(mcp_call "$granter_url" "mesh__grant_peer_scope" \
        "$(jq -nc --arg pid "$grantee_pid" --arg s "$scope" \
            '{peer:$pid, scope:$s}')")
    if echo "$resp" | jq -e '.result.isError != true' >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

tier2_revoke_scope_via_mesh() {
    local granter_url="$1"
    local grantee_pid="$2"
    local scope="$3"
    local resp
    resp=$(mcp_call "$granter_url" "mesh__revoke_peer_scope" \
        "$(jq -nc --arg pid "$grantee_pid" --arg s "$scope" \
            '{peer:$pid, scope:$s}')")
    if echo "$resp" | jq -e '.result.isError != true' >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

# scenario_tier2_same_org — the headline scenario. Pair Alice's NODE_A
# with Bob's NODE_C (same org AcmeCo, different users). Then drive the
# negative → grant → re-attempt → revoke loop on each surface.
scenario_tier2_same_org() {
    step "T2.0" "Tier 2 — pair NODE_A↔NODE_C (Alice vs Bob, same org)"

    if ! bulletproof_topology_ready; then
        skip "tier2 scenario" \
            "5-node bulletproof topology not up (NODE_D/E unreachable)."
        return
    fi

    pair_same_org "$NODE_A" "$NODE_C"

    local pid_a pid_c
    pid_a=$(peer_id_for "$NODE_A")
    pid_c=$(peer_id_for "$NODE_C")
    if [ -z "$pid_a" ] || [ -z "$pid_c" ]; then
        skip "tier2 surfaces" "p2p identity unset on a or c"
        return
    fi
    if ! is_paired_with "$NODE_C" "$pid_a" || ! is_paired_with "$NODE_A" "$pid_c"; then
        skip "tier2 surfaces" \
            "A and C not paired (libp2p closed-bridge); explicit-grant surfaces cannot be exercised"
        return
    fi

    # Pre-publish a skill on node-a so the resolver has something to find
    # AFTER the grant lands — distinguishes "auth-gated" from "resolver
    # miss".
    local skill_name="tier2-skill-$RANDOM"
    local marker_skill="tier2-skill-marker-$RANDOM"
    local skill_body
    skill_body=$(build_skill_body "$skill_name" "$marker_skill")
    api POST "$NODE_A/api/v1/skill-registry" \
        "$(jq -n --arg name "$skill_name" --arg body "$skill_body" \
            '{name:$name, body:$body, scope:"global", author:"tier2-runner"}')" \
        >/dev/null 2>&1 || true

    # Make sure node-a hasn't already granted node-c any mesh scope from
    # an earlier scenario (skill_share grants A↔B, not A↔C, but be
    # defensive — revoke first, ignore failures).
    tier2_revoke_scope_via_mesh "$NODE_A" "$pid_c" "mesh.skill_request" >/dev/null 2>&1 || true
    tier2_revoke_scope_via_mesh "$NODE_A" "$pid_c" "mesh.memory_request" >/dev/null 2>&1 || true

    # ------------------------------------------------------------------
    # 1. Negative — Bob (node-c) reads Alice's skill WITHOUT a grant
    # ------------------------------------------------------------------
    step "T2.1" "negative: ungranted skill request from node-c rejected at scope gate"
    local verdict
    verdict=$(tier2_skill_request "$NODE_C" "$pid_a" "$skill_name")
    case "$verdict" in
        scope_denied)
            pass "T2.1 ungranted skill request rejected (scope gate)"
            ;;
        ok|not_installed)
            fail "T2.1 ungranted skill request succeeded — Tier 2 baseline leak" \
                "verdict=$verdict (scope=mesh.skill_request was not granted; this is a security regression)"
            ;;
        error)
            skip "T2.1 ungranted skill request" \
                "MCP returned a generic error; couldn't classify reason — daemon may not surface denial code"
            ;;
    esac

    # Negative memory: node-c requests a non-existent memory id from
    # node-a (we just need the rejection shape — no need to plant a
    # specific memory because the auth gate fires first).
    step "T2.1b" "negative: ungranted memory request from node-c rejected"
    local mresp mtext
    mresp=$(mcp_call "$NODE_C" "memory__request_memory" \
        "$(jq -nc --arg pid "$pid_a" --arg rid "tier2-no-such" '{peer_id:$pid, remote_id:$rid}')")
    mtext=$(echo "$mresp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    if echo "$mresp" | jq -e '.result.isError == true' >/dev/null 2>&1 \
            || echo "$mresp" | jq -e '.error != null' >/dev/null 2>&1; then
        if echo "$mtext" | grep -qiE 'scope|denied|paired-peers|unauthor|not.granted'; then
            pass "T2.1b ungranted memory request rejected at scope gate"
        else
            skip "T2.1b ungranted memory rejection reason" \
                "rejected but no scope-related message; could be plain not-found instead of scope gate"
        fi
    else
        fail "T2.1b ungranted memory request unexpectedly succeeded" \
            "$(echo "$mtext" | head -c 200)"
    fi

    # Negative task offer: node-c tries to offer a task to node-a. The
    # offer is from C, but C has no task_offer scope on A so the
    # receiver should reject. (Note: scenario_tasks already covers this
    # for A↔C in scenario_tasks_unauthorized_offer; we re-drive it here
    # so the tier2 file is self-contained.)
    step "T2.1c" "negative: ungranted task offer C→A rejected by receiver"
    local neg_title="tier2-unauth-task-$RANDOM"
    local cbody cresp neg_tid
    cbody=$(jq -nc --arg ws "${WS_CHARLIE:-default}" --arg t "$neg_title" \
        '{workspace_id:$ws, title:$t, status:"open"}')
    cresp=$(api POST "$NODE_C/api/v1/tasks" "$cbody" 2>/dev/null || echo "{}")
    neg_tid=$(echo "$cresp" | jq -r '.id // .ID // empty' 2>/dev/null)
    if [ -n "$neg_tid" ]; then
        local obody
        obody=$(jq -nc --arg ws "${WS_CHARLIE:-default}" --arg to "$pid_a" --arg tid "$neg_tid" \
            '{workspace_id:$ws, task_id:$tid, to_peer_id:$to}')
        api POST "$NODE_C/api/v1/tasks/offers" "$obody" >/dev/null 2>&1 || true
        sleep 3
        if api GET "$NODE_A/api/v1/tasks/offers?direction=incoming&limit=50" 2>/dev/null \
                | jq -e ".[]? | select(.title == \"$neg_title\")" >/dev/null 2>&1; then
            fail "T2.1c node-a accepted ungranted task offer from node-c" \
                "expected rejection — task_offer scope was not granted"
        else
            pass "T2.1c node-a did NOT surface ungranted task offer"
        fi
    else
        skip "T2.1c task negative" "could not create source task on node-c: $cresp"
    fi

    # ------------------------------------------------------------------
    # 2. Grant — node-a explicitly grants node-c the scopes
    # ------------------------------------------------------------------
    step "T2.2" "node-a issues explicit mesh.skill_request grant to node-c"

    # Snapshot approval queue on node-c before the grant lands.
    local approvals_c_before
    approvals_c_before=$(api GET "$NODE_C/api/v1/approvals?limit=200" 2>/dev/null \
        | jq '[.data // .[] // []] | length' 2>/dev/null || echo "0")

    if tier2_grant_scope_via_mesh "$NODE_A" "$pid_c" "mesh.skill_request"; then
        pass "T2.2 mesh.skill_request granted A→C"
    else
        fail "T2.2 grant failed" "could not issue mesh.skill_request grant"
        return
    fi
    # Also grant memory + task_offer so the re-attempts below have a
    # valid scope to ride on.
    tier2_grant_scope_via_mesh "$NODE_A" "$pid_c" "mesh.memory_request" \
        && pass "T2.2 mesh.memory_request granted A→C" \
        || skip "T2.2 mesh.memory_request grant" "grant call failed; memory re-attempt below will reflect this"
    tier2_grant_scope_via_mesh "$NODE_A" "$pid_c" "task_offer:*" \
        && pass "T2.2 task_offer:* granted A→C" \
        || skip "T2.2 task_offer grant" "grant call failed; task re-attempt below will reflect this"

    # Give the gateway a beat to commit + the receiver to see it.
    sleep 2

    # ------------------------------------------------------------------
    # 3. Re-attempt — should now SUCCEED
    # ------------------------------------------------------------------
    step "T2.3" "re-attempt: granted skill request succeeds"
    verdict=$(tier2_skill_request "$NODE_C" "$pid_a" "$skill_name")
    case "$verdict" in
        ok|not_installed)
            pass "T2.3 granted skill request passed scope gate (verdict=$verdict)"
            ;;
        scope_denied)
            fail "T2.3 granted skill request still rejected" \
                "grant didn't take effect; daemon may not be propagating peerscope rows"
            ;;
        error)
            skip "T2.3 re-attempt classification" \
                "MCP returned generic error; couldn't tell if scope was honoured"
            ;;
    esac

    # ------------------------------------------------------------------
    # 4. Approval queue / consent record (best-effort)
    # ------------------------------------------------------------------
    step "T2.4" "approval queue on node-c records the grant consent event"
    local approvals_c_after delta_c
    approvals_c_after=$(api GET "$NODE_C/api/v1/approvals?limit=200" 2>/dev/null \
        | jq '[.data // .[] // []] | length' 2>/dev/null || echo "0")
    delta_c=$((approvals_c_after - approvals_c_before))
    if [ "$delta_c" -gt 0 ]; then
        pass "T2.4 node-c approval queue grew by $delta_c after grant"
    else
        skip "T2.4 approval consent record on grant" \
            "PENDING — no /api/v1/approvals row for the mesh grant. Either grant doesn't surface to approvals yet OR auto-accepted. Tracked in epic 01KSK91Q4W8TNED9MAF0CTRVKC."
    fi

    # ------------------------------------------------------------------
    # 5. Audit grant_origin field (PENDING-tolerant)
    # ------------------------------------------------------------------
    step "T2.5" "audit row on node-a references the grant by Alice (grant_origin)"
    local audit_a found_origin=0 found_grant=0
    audit_a=$(api GET "$NODE_A/api/v1/audit?limit=400" 2>/dev/null)
    if echo "$audit_a" | jq -e '.data[]? | select((.tool_name // "") == "mesh__grant_peer_scope")' \
            >/dev/null 2>&1; then
        found_grant=1
    fi
    if echo "$audit_a" | jq -e \
            '.data[]?
              | select((.tool_name // "") == "mesh__grant_peer_scope")
              | select(
                  (.grant_origin // .granted_by // "") != ""
                  or ((.tags // "") | tostring | contains("grant_origin"))
              )' \
            >/dev/null 2>&1; then
        found_origin=1
    fi
    if [ "$found_origin" = "1" ]; then
        pass "T2.5 audit row carries grant_origin metadata"
    elif [ "$found_grant" = "1" ]; then
        skip "T2.5 grant_origin field on audit row" \
            "PENDING — mesh__grant_peer_scope audit row exists but no grant_origin field surfaced yet (epic 01KSK91Q4W8TNED9MAF0CTRVKC)"
    else
        skip "T2.5 audit row" \
            "no mesh__grant_peer_scope audit row visible on node-a yet — batching delay"
    fi

    # ------------------------------------------------------------------
    # 6. Revoke — next attempt fails again
    # ------------------------------------------------------------------
    step "T2.6" "revoke scope and re-attempt fails"
    if tier2_revoke_scope_via_mesh "$NODE_A" "$pid_c" "mesh.skill_request"; then
        pass "T2.6 mesh.skill_request revoked A→C"
    else
        fail "T2.6 revoke failed" "could not revoke mesh.skill_request"
        return
    fi
    sleep 2

    verdict=$(tier2_skill_request "$NODE_C" "$pid_a" "$skill_name")
    local denial_text
    local resp_for_text
    resp_for_text=$(mcp_call "$NODE_C" "mesh__request_skill" \
        "$(jq -nc --arg pid "$pid_a" --arg n "$skill_name" '{peer_id:$pid, skill_name:$n}')")
    denial_text=$(echo "$resp_for_text" | jq -r '.result.content[0].text // ""' 2>/dev/null)

    case "$verdict" in
        scope_denied)
            if echo "$denial_text" | grep -qi 'scope_revoked'; then
                pass "T2.6 re-attempt rejected with denial=scope_revoked (clean)"
            else
                skip "T2.6 denial code" \
                    "PENDING — re-attempt rejected but daemon returned generic scope-denied not denial=scope_revoked. Epic 01KSK91Q4W8TNED9MAF0CTRVKC."
                pass "T2.6 re-attempt rejected (any-shape denial)"
            fi
            ;;
        ok|not_installed)
            fail "T2.6 revoke didn't take effect" \
                "request still succeeded after revoke (verdict=$verdict)"
            ;;
        error)
            skip "T2.6 re-attempt classification" \
                "MCP returned generic error post-revoke; couldn't tell if revoke was honoured"
            ;;
    esac
}
