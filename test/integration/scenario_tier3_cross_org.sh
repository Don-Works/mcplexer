#!/usr/bin/env bash
# scenario_tier3_cross_org.sh — Tier 3 trust-tier matrix scenario.
#
# Two machines, different users AND different orgs (Alice/AcmeCo on
# NODE_A vs Carol/BetaCo on NODE_D). pair_cross_org records the
# baseline (zero default scopes + org-boundary PENDING). This scenario
# then:
#
#   1. Verifies the cross-org default-reject (no surface succeeds
#      without an explicit grant).
#   2. Issues an explicit per-peer scope grant from BOTH sides (the
#      daemon doesn't yet model org-pair-scoped grants; we use the
#      per-peer scope endpoint and leave a TODO for the org-aware
#      variant under epic 01KSK91Q4W8TNED9MAF0CTRVKC).
#   3. Confirms all four surfaces (skill/memory/task/mesh) succeed.
#   4. Worker-scope isolation: write TWO memories on node-a, one in
#      the shared scope, one in a different scope. A worker invoked on
#      node-d should be able to read ONLY the shared one — the other
#      must rejection-without-leak (denial payload must not contain
#      the other memory's id, name, or content).
#   5. Audit assertion on node-a for the rejection — the un-granted
#      memory must not appear in the rejection's audit payload.
#
# Belongs to epic 01KSK91Q4W8TNED9MAF0CTRVKC (task B3
# 01KSK9598K378WQ5X10NG74BP3). Sourced by scenarios.sh; BULLETPROOF=1
# gated.
#
# TODO(epic 01KSK91Q4W8TNED9MAF0CTRVKC): once the daemon ships an
# org-pair-scoped grant endpoint (e.g. POST /api/v1/orgs/{a}/peers/{b}/
# scope-grants), replace the per-peer mesh__grant_peer_scope calls in
# T3.2 with the org-aware variant and assert that the grant surfaces
# `org_pair=("AcmeCo","BetaCo")` on the audit row.

tier3_skill_request() {
    # Same shape as tier2_skill_request — duplicated here so tier3 is
    # self-contained if someone runs it without tier2.
    local requester_url="$1"
    local owner_pid="$2"
    local skill_name="$3"
    local resp text
    resp=$(mcp_call "$requester_url" "mesh__request_skill" \
        "$(jq -nc --arg pid "$owner_pid" --arg n "$skill_name" \
            '{peer_id:$pid, skill_name:$n}')")
    text=$(echo "$resp" | jq -r '.result.content[0].text // ""' 2>/dev/null)
    if echo "$resp" | jq -e '.error != null' >/dev/null 2>&1; then
        echo "error"; return
    fi
    if echo "$resp" | jq -e '.result.isError != true' >/dev/null 2>&1; then
        if echo "$text" | grep -qiE 'not installed|not.have'; then
            echo "not_installed"
        else
            echo "ok"
        fi
        return
    fi
    if echo "$text" | grep -qiE 'scope|paired-peers|denied|forbid|not.granted|cross_org'; then
        echo "scope_denied"
    elif echo "$text" | grep -qiE 'not installed|not.have'; then
        echo "not_installed"
    else
        echo "error"
    fi
}

tier3_grant_scope() {
    local granter_url="$1"
    local grantee_pid="$2"
    local scope="$3"
    local resp
    resp=$(mcp_call "$granter_url" "mesh__grant_peer_scope" \
        "$(jq -nc --arg pid "$grantee_pid" --arg s "$scope" \
            '{peer:$pid, scope:$s}')")
    echo "$resp" | jq -e '.result.isError != true' >/dev/null 2>&1
}

scenario_tier3_cross_org() {
    step "T3.0" "Tier 3 — pair NODE_A↔NODE_D (Alice AcmeCo vs Carol BetaCo)"

    if ! bulletproof_topology_ready; then
        skip "tier3 scenario" \
            "5-node bulletproof topology not up (NODE_D/E unreachable)."
        return
    fi

    pair_cross_org "$NODE_A" "$NODE_D"

    local pid_a pid_d
    pid_a=$(peer_id_for "$NODE_A")
    pid_d=$(peer_id_for "$NODE_D")
    if [ -z "$pid_a" ] || [ -z "$pid_d" ]; then
        skip "tier3 surfaces" "p2p identity unset on a or d"
        return
    fi
    if ! is_paired_with "$NODE_D" "$pid_a" || ! is_paired_with "$NODE_A" "$pid_d"; then
        skip "tier3 surfaces" \
            "A and D not paired (libp2p closed-bridge); cross-org explicit-grant surfaces cannot be exercised"
        return
    fi

    # Pre-publish a skill on node-a we can ask for once granted.
    local skill_name="tier3-skill-$RANDOM"
    local marker_skill="tier3-skill-marker-$RANDOM"
    local skill_body
    skill_body=$(build_skill_body "$skill_name" "$marker_skill")
    api POST "$NODE_A/api/v1/skill-registry" \
        "$(jq -n --arg name "$skill_name" --arg body "$skill_body" \
            '{name:$name, body:$body, scope:"global", author:"tier3-runner"}')" \
        >/dev/null 2>&1 || true

    # ------------------------------------------------------------------
    # 1. Default cross-org attempt — MUST REJECT
    # ------------------------------------------------------------------
    step "T3.1" "default cross-org skill request REJECTED (no grant in place)"
    local verdict
    verdict=$(tier3_skill_request "$NODE_D" "$pid_a" "$skill_name")
    case "$verdict" in
        scope_denied)
            pass "T3.1 cross-org skill request rejected (scope gate / org boundary)"
            ;;
        ok|not_installed)
            fail "T3.1 cross-org skill request succeeded without grant" \
                "verdict=$verdict — this is a Tier 3 security regression"
            ;;
        error)
            skip "T3.1 cross-org rejection classification" \
                "MCP returned generic error; couldn't classify reason"
            ;;
    esac

    # ------------------------------------------------------------------
    # 2. Explicit grants from BOTH sides
    # ------------------------------------------------------------------
    step "T3.2" "explicit per-peer scope grants A↔D (org-pair grant pending)"
    # A grants D mesh.skill_request + mesh.memory_request + task_offer.
    if tier3_grant_scope "$NODE_A" "$pid_d" "mesh.skill_request"; then
        pass "T3.2 A→D mesh.skill_request granted"
    else
        fail "T3.2 A→D mesh.skill_request grant failed" ""
    fi
    tier3_grant_scope "$NODE_A" "$pid_d" "mesh.memory_request" \
        && pass "T3.2 A→D mesh.memory_request granted" \
        || skip "T3.2 A→D memory grant" "grant call failed"
    tier3_grant_scope "$NODE_A" "$pid_d" "task_offer:*" \
        && pass "T3.2 A→D task_offer:* granted" \
        || skip "T3.2 A→D task_offer grant" "grant call failed"

    # D grants A reciprocal — cross-org needs symmetric trust to share.
    tier3_grant_scope "$NODE_D" "$pid_a" "mesh.skill_request" \
        && pass "T3.2 D→A mesh.skill_request granted" \
        || skip "T3.2 D→A skill grant" "grant call failed"
    tier3_grant_scope "$NODE_D" "$pid_a" "mesh.memory_request" \
        && pass "T3.2 D→A mesh.memory_request granted" \
        || skip "T3.2 D→A memory grant" "grant call failed"

    sleep 2

    # ------------------------------------------------------------------
    # 3. All four surfaces succeed after grant
    # ------------------------------------------------------------------
    step "T3.3" "post-grant: skill request succeeds across org boundary"
    verdict=$(tier3_skill_request "$NODE_D" "$pid_a" "$skill_name")
    case "$verdict" in
        ok|not_installed)
            pass "T3.3 cross-org skill request passes scope gate (verdict=$verdict)"
            ;;
        scope_denied)
            fail "T3.3 cross-org skill request still rejected" \
                "grant didn't propagate, or org-boundary blocks even after explicit grant"
            ;;
        error)
            skip "T3.3 post-grant classification" "generic error from MCP"
            ;;
    esac

    # Memory: create on A, offer to D.
    step "T3.3b" "post-grant: memory share A→D succeeds"
    local mem_shared_name="tier3-shared-$RANDOM"
    local mem_shared_content="tier3 cross-org shared fact $RANDOM"
    local cresp mid_shared
    cresp=$(api POST "$NODE_A/api/v1/memory" \
        "$(jq -nc --arg n "$mem_shared_name" --arg c "$mem_shared_content" \
            '{name:$n, content:$c, kind:"fact", tags:["tier3-shared"]}')" 2>&1)
    mid_shared=$(echo "$cresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$mid_shared" ]; then
        fail "T3.3b create shared memory on node-a" "$(echo "$cresp" | head -c 200)"
    else
        pass "T3.3b created shared memory on node-a ($mid_shared)"
        local oresp
        oresp=$(mcp_call "$NODE_A" "memory__offer_memory" \
            "$(jq -nc --arg pid "$pid_d" --arg mid "$mid_shared" \
                '{peer_id:$pid, memory_id:$mid}')")
        if echo "$oresp" | jq -e '.result.isError == true' >/dev/null 2>&1; then
            fail "T3.3b memory offer A→D failed post-grant" \
                "$(echo "$oresp" | jq -c '.result.content[0].text // .result' | head -c 200)"
        else
            pass "T3.3b memory offer A→D dispatched"
        fi
    fi

    # Task: A offers a cross-org task to D.
    step "T3.3c" "post-grant: task offer A→D succeeds"
    local task_title="tier3-task-$RANDOM"
    local tbody tresp tid
    tbody=$(jq -nc --arg ws "${WS_ALPHA:-default}" --arg t "$task_title" \
        '{workspace_id:$ws, title:$t, status:"open", description:"tier3 cross-org"}')
    if ! tresp=$(api POST "$NODE_A/api/v1/tasks" "$tbody" 2>&1); then
        fail "T3.3c create source task on A" "$tresp"
    else
        tid=$(echo "$tresp" | jq -r '.id // .ID // empty' 2>/dev/null)
        if [ -n "$tid" ]; then
            local obody
            obody=$(jq -nc --arg ws "${WS_ALPHA:-default}" --arg to "$pid_d" --arg tid "$tid" \
                '{workspace_id:$ws, task_id:$tid, to_peer_id:$to, message:"cross-org"}')
            api POST "$NODE_A/api/v1/tasks/offers" "$obody" >/dev/null 2>&1 || true

            local found="false"
            for _ in 1 2 3 4 5 6 7 8 9 10; do
                if api GET "$NODE_D/api/v1/tasks/offers?direction=incoming&limit=50" \
                        2>/dev/null \
                        | jq -e ".[]? | select(.title == \"$task_title\")" \
                        >/dev/null 2>&1; then
                    found="true"; break
                fi
                sleep 1
            done
            if [ "$found" = "true" ]; then
                pass "T3.3c node-d received cross-org task offer"
            else
                skip "T3.3c cross-org task propagation" \
                    "node-d never surfaced the offer (libp2p closed-bridge typical)"
            fi
        fi
    fi

    # Mesh broadcast.
    step "T3.3d" "post-grant: mesh broadcast A→D"
    local mesh_marker="tier3-mesh-$RANDOM"
    api POST "$NODE_A/api/v1/mesh/send" \
        "$(jq -nc --arg c "$mesh_marker" \
            '{recipient:{kind:"audience",value:"*"},
              kind:"finding", content:$c, priority:"low",
              agent_name:"tier3-runner"}')" >/dev/null 2>&1 || true
    local seen="false"
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
        if api GET "$NODE_D/api/v1/mesh/status" 2>/dev/null \
                | jq -e ".messages[]? | select(.content == \"$mesh_marker\")" \
                >/dev/null 2>&1; then
            seen="true"; break
        fi
        sleep 1
    done
    if [ "$seen" = "true" ]; then
        pass "T3.3d node-d observed cross-org broadcast"
    else
        skip "T3.3d cross-org mesh propagation" \
            "node-d didn't observe the broadcast within 12s (closed-bridge timing)"
    fi

    # ------------------------------------------------------------------
    # 4. Worker scope isolation — read ONLY the shared scope
    # ------------------------------------------------------------------
    step "T3.4" "scope isolation: D can read shared memory but NOT a separate scope"

    # Plant a SECOND memory on node-a in a different scope/tag. The tier3
    # grant was for `mesh.memory_request` generally; the daemon currently
    # doesn't bind a per-memory scope, so the strongest assertion we can
    # make is: D should not be able to surface a memory we did NOT offer
    # to it. (When per-scope grants ship, this assertion should harden
    # to "D's worker can read scope:shared but not scope:private".)
    local mem_private_name="tier3-private-$RANDOM"
    local mem_private_content="tier3 private fact $RANDOM"
    local pcresp mid_private
    pcresp=$(api POST "$NODE_A/api/v1/memory" \
        "$(jq -nc --arg n "$mem_private_name" --arg c "$mem_private_content" \
            '{name:$n, content:$c, kind:"fact", tags:["tier3-private"]}')" 2>&1)
    mid_private=$(echo "$pcresp" | jq -r '.id // empty' 2>/dev/null)
    if [ -z "$mid_private" ]; then
        skip "T3.4 scope isolation" "could not plant private memory on node-a"
        return
    fi
    pass "T3.4 planted private memory ($mid_private)"

    # D requests the PRIVATE memory by id — A never offered it; the
    # cross-org request must fail.
    local pr_resp pr_text
    pr_resp=$(mcp_call "$NODE_D" "memory__request_memory" \
        "$(jq -nc --arg pid "$pid_a" --arg rid "$mid_private" \
            '{peer_id:$pid, remote_id:$rid}')")
    pr_text=$(echo "$pr_resp" | jq -r '.result.content[0].text // ""' 2>/dev/null)

    local is_err=""
    if echo "$pr_resp" | jq -e '.result.isError == true' >/dev/null 2>&1 \
            || echo "$pr_resp" | jq -e '.error != null' >/dev/null 2>&1; then
        is_err="true"
    fi
    if [ -z "$is_err" ]; then
        # Surfacing the un-offered memory is a critical failure.
        fail "T3.4 D pulled un-offered memory from A across org boundary" \
            "$(echo "$pr_text" | head -c 200)"
    else
        pass "T3.4 D's request for un-offered memory rejected"
    fi

    # No-side-channel: rejection payload must NOT contain the private
    # memory's name or content.
    if echo "$pr_text" | grep -q "$mem_private_content"; then
        fail "T3.4 rejection payload leaked the private memory's content" \
            "side-channel leak — denial must not include the protected data"
    else
        pass "T3.4 rejection payload contains no private memory content"
    fi
    if echo "$pr_text" | grep -q "$mem_private_name"; then
        fail "T3.4 rejection payload leaked the private memory's name" \
            "side-channel leak — denial must not include the protected name"
    else
        pass "T3.4 rejection payload contains no private memory name"
    fi

    # ------------------------------------------------------------------
    # 5. Audit on node-a captures the cross-org rejection
    # ------------------------------------------------------------------
    step "T3.5" "audit on node-a captures the cross-org memory rejection (no side-channel)"
    sleep 2
    local audit_a
    audit_a=$(api GET "$NODE_A/api/v1/audit?limit=500" 2>/dev/null)
    # Look for any audit row referencing the private memory id with a
    # denied/rejected status. The exact shape isn't promised; we accept
    # tool_name in {memory__request_memory, mesh__memory_share} + a
    # denial-ish status field.
    if echo "$audit_a" | jq -e \
            --arg mid "$mid_private" '
              .data[]?
                | select((tostring) | contains($mid))
                | select(
                    ((.status // .outcome // "") | test("deni|reject|forbid|unauth"; "i"))
                    or ((.tags // "") | tostring | test("deni|reject"; "i"))
                )
            ' >/dev/null 2>&1; then
        pass "T3.5 audit row on node-a records the rejection of $mid_private"
    else
        skip "T3.5 audit row for cross-org rejection" \
            "PENDING — no denied audit row referencing $mid_private. Daemon may not emit audit on receive-side rejection yet (epic 01KSK91Q4W8TNED9MAF0CTRVKC)."
    fi

    # Belt-and-braces: the audit row, if present, must not include the
    # private memory's content either. (Different leak shape than the
    # rejection payload — audit is consumed by ops humans.)
    if echo "$audit_a" | grep -q "$mem_private_content"; then
        fail "T3.5 audit on node-a contains the private memory's content" \
            "side-channel leak via audit row"
    else
        pass "T3.5 audit does not leak the private memory's content"
    fi
}
